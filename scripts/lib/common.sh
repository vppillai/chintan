#!/usr/bin/env bash
# Shared helpers for every script in scripts/.
#
# Sourced, never executed. §11.3 mandates a set of conventions for every script
# without exception — set -euo pipefail, --help, --region, --dry-run as the
# default for anything destructive, --json, meaningful exit codes — and the way
# conventions actually hold is when they are one implementation rather than a
# habit repeated twenty times (§11.2's lesson from passbook's admin.sh, which had
# drifted ~300 duplicated lines before it was consolidated).
#
# shellcheck shell=bash

# Guard against double-sourcing, which would reset counters mid-run.
if [ -n "${CHINTAN_COMMON_SOURCED:-}" ]; then
    return 0
fi
CHINTAN_COMMON_SOURCED=1

set -euo pipefail

# ---------------------------------------------------------------------------
# Paths
# ---------------------------------------------------------------------------

# REPO_ROOT is resolved from this file's location, so a script works regardless
# of the directory it was invoked from. Every path in every script is built from
# this rather than from $PWD.
CHINTAN_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [ -n "${CHINTAN_REPO_ROOT:-}" ]; then
    # Explicit override, for tests that need to run a check against a doctored
    # tree — guardrails-check.sh --self-test proves it fails when a guardrail is
    # removed, which requires pointing it at a tree that is missing one. Not for
    # ordinary use: every script otherwise resolves the root from its own
    # location, so it works regardless of the invoking directory.
    REPO_ROOT="$(cd "$CHINTAN_REPO_ROOT" && pwd)"
else
    REPO_ROOT="$(cd "${CHINTAN_LIB_DIR}/../.." && pwd)"
fi
export REPO_ROOT

# The frozen system identifier (§7.3). Mirrors backend/internal/systemid; both
# are asserted equal by check-resource-prefix.sh so they cannot drift.
SYSTEM_ID="voicenotes"
export SYSTEM_ID

# ---------------------------------------------------------------------------
# Output
# ---------------------------------------------------------------------------

# Colour only when attached to a terminal. CI logs are not a terminal, and
# escape codes in a CI log make a failure harder to read, not easier.
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
    C_RED=$'\033[31m'
    C_GREEN=$'\033[32m'
    C_YELLOW=$'\033[33m'
    C_DIM=$'\033[2m'
    C_BOLD=$'\033[1m'
    C_OFF=$'\033[0m'
else
    C_RED=''
    C_GREEN=''
    C_YELLOW=''
    C_DIM=''
    C_BOLD=''
    C_OFF=''
fi

# Diagnostics go to stderr so that --json output on stdout stays parseable. A
# script whose machine-readable output is polluted by progress lines forces the
# caller to scrape, which is the thing --json exists to avoid (§11.3).
log() { printf '%s\n' "$*" >&2; }
info() { printf '%s==>%s %s\n' "$C_BOLD" "$C_OFF" "$*" >&2; }
warn() { printf '%swarning:%s %s\n' "$C_YELLOW" "$C_OFF" "$*" >&2; }
err() { printf '%serror:%s %s\n' "$C_RED" "$C_OFF" "$*" >&2; }
ok() { printf '%s  ok%s %s\n' "$C_GREEN" "$C_OFF" "$*" >&2; }
dim() { printf '%s%s%s\n' "$C_DIM" "$*" "$C_OFF" >&2; }

die() {
    err "$*"
    exit 1
}

# ---------------------------------------------------------------------------
# Violation accumulation
# ---------------------------------------------------------------------------
#
# Checks report every violation rather than stopping at the first. An operator
# fixing a config or a template should see the whole list, not discover the next
# problem after each push — the same reasoning as the config validator's
# collected errors.

VIOLATION_COUNT=0
VIOLATIONS=()

violation() {
    VIOLATION_COUNT=$((VIOLATION_COUNT + 1))
    VIOLATIONS+=("$1")
    printf '%s  FAIL%s %s\n' "$C_RED" "$C_OFF" "$1" >&2
}

# finish_check prints a summary and exits with 0 or 1.
#
# Takes the check's name and, optionally, "--json" to emit a machine-readable
# result instead of prose.
finish_check() {
    local name="$1" as_json="${2:-}"

    if [ "$as_json" = "--json" ]; then
        # Build the array with jq rather than string concatenation so a
        # violation containing a quote cannot produce invalid JSON.
        local arr='[]'
        if [ "$VIOLATION_COUNT" -gt 0 ]; then
            arr="$(printf '%s\n' "${VIOLATIONS[@]}" | jq -R . | jq -sc .)"
        fi
        jq -nc \
            --arg check "$name" \
            --argjson ok "$([ "$VIOLATION_COUNT" -eq 0 ] && echo true || echo false)" \
            --argjson violations "$arr" \
            '{check: $check, ok: $ok, violation_count: ($violations|length), violations: $violations}'
    fi

    if [ "$VIOLATION_COUNT" -eq 0 ]; then
        ok "$name"
        return 0
    fi
    err "$name: $VIOLATION_COUNT violation(s)"
    exit 1
}

# ---------------------------------------------------------------------------
# "No subject yet" checks
# ---------------------------------------------------------------------------
#
# §0.5A: "Every check that will ever gate this build is wired in Phase 0, even
# where it has nothing to inspect yet. A check whose subject does not exist
# passes trivially and is never skipped or commented out — so the day the subject
# appears, the check is already running."
#
# The failure mode this guards against is a check that keeps passing trivially
# after its subject arrives, because nobody went back to implement it. So a
# dormant check states what it is waiting for and, crucially, tests for the
# subject's existence rather than being hardcoded to succeed.
no_subject_yet() {
    local check="$1" phase="$2" subject="$3"
    dim "  ${check}: no subject yet (active in Phase ${phase}: ${subject})"
    dim "  passing trivially — this check runs for real the moment the subject exists"
}

# ---------------------------------------------------------------------------
# File selection
# ---------------------------------------------------------------------------

# tracked_files lists files git knows about, so generated artifacts, vendored
# code, and node_modules never reach a check. Falls back to find when git is
# unavailable (a source tarball, or a container without the repo's .git).
tracked_files() {
    local out=""
    if git -C "$REPO_ROOT" rev-parse --git-dir >/dev/null 2>&1; then
        out="$(git -C "$REPO_ROOT" ls-files "$@" 2>/dev/null || true)"
    fi

    # Fall back to the filesystem when git has nothing to report. This is not
    # only for a source tarball: `git ls-files` is also empty on a repository
    # with no commits yet, and a check that iterates over an empty list PASSES
    # VACUOUSLY. §0.5A is explicit that an untested check is worse than no check
    # because it is believed, and a check silently inspecting zero files is that
    # failure arriving quietly.
    if [ -z "$out" ]; then
        local patterns=("$@")
        if [ "${#patterns[@]}" -eq 0 ]; then
            out="$(cd "$REPO_ROOT" && find . -type f \
                -not -path './.git/*' -not -path './build/*' \
                -not -path './frontend/node_modules/*' |
                sed 's|^\./||')"
        else
            local pat found=""
            for pat in "${patterns[@]}"; do
                # Translate a git pathspec glob into a find -path pattern.
                found="$(cd "$REPO_ROOT" && find . -type f -path "./${pat//\*\*\//}" \
                    -not -path './.git/*' 2>/dev/null | sed 's|^\./||' || true)"
                [ -n "$found" ] && out="${out}${found}"$'\n'
            done
            # A '**' pattern needs a recursive match as well as the literal one.
            for pat in "${patterns[@]}"; do
                case "$pat" in
                    *'**'*)
                        local suffix="${pat##*/}"
                        local prefix="${pat%%/**}"
                        found="$(cd "$REPO_ROOT" && find "./$prefix" -type f -name "$suffix" \
                            2>/dev/null | sed 's|^\./||' || true)"
                        [ -n "$found" ] && out="${out}${found}"$'\n'
                        ;;
                esac
            done
        fi
    fi

    # Dedupe and sort: the fallback runs several overlapping patterns, and a file
    # listed twice makes a check report the same violation twice.
    printf '%s' "$out" | grep -v '^$' | LC_ALL=C sort -u || true
}

# shell_files lists every shell script, which is what shellcheck and shfmt run
# over. Selected by path and extension rather than by shebang scan, so a new
# script in scripts/ is covered the moment it is added.
shell_files() {
    tracked_files 'scripts/**/*.sh' 'scripts/*.sh' 'containers/**/*.sh' 2>/dev/null |
        grep -v '^scripts/test/fake-aws/' || true
}

# ---------------------------------------------------------------------------
# Argument conventions (§11.3)
# ---------------------------------------------------------------------------

# require_tenant enforces the §11.3 rule that no data operation runs untenanted
# (I11). Lifecycle scripts act on infrastructure, which has no tenant, and must
# not call this — requiring the flag there would mean inventing a meaningless
# value, and a meaningless required argument is how a real one gets ignored.
require_tenant() {
    local tenant="${1:-}"
    if [ -z "$tenant" ]; then
        err "--tenant is required: no data operation runs untenanted (I11, §11.3)"
        exit 2
    fi
}

# confirm_apply implements the convention that matters most for agent safety:
# --dry-run is the DEFAULT for anything destructive or costly, and --apply
# executes. A mistaken invocation prints a plan instead of causing damage.
#
# Usage: confirm_apply "$APPLY" "delete 412 objects"
confirm_apply() {
    local apply="$1" description="$2"
    if [ "$apply" != "1" ]; then
        log ""
        info "DRY RUN — nothing was changed."
        dim "  Would: ${description}"
        dim "  Re-run with --apply to execute."
        return 1
    fi
    return 0
}

# aws_cli wraps the AWS CLI so that region handling and the "no ad-hoc calls"
# rule have one place to live.
#
# I16 forbids out-of-band mutation of backend state: "No ad-hoc AWS CLI or SDK
# calls to inspect or change data — if an operation is needed and no script
# exists, write the script first." That applies to the implementing agent as
# strictly as to a human operator. Routing every call through here does not
# enforce that by itself, but it does mean every call is inside a script that has
# --help, --dry-run, and a test.
aws_cli() {
    local region="${AWS_REGION:-${CHINTAN_REGION:-}}"
    if [ -n "$region" ]; then
        command aws --region "$region" "$@"
    else
        command aws "$@"
    fi
}
