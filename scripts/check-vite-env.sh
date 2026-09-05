#!/usr/bin/env bash
#
# Assert that the VITE_* variables the bundle reads are exactly the ones the
# deploy pipeline exports.
#
# WHY THIS EXISTS
#
# The deploy script exported VITE_USER_POOL_CLIENT_ID, named after the
# CloudFormation output UserPoolClientId. frontend/src/config/env.ts reads
# VITE_CLIENT_ID. Nothing connected the two, and nothing could fail:
# `required()` returns an empty string for a missing variable and only warns
# under import.meta.env.DEV, so the bundle compiled, the deploy went green, and
# the only symptom would have been a user unable to sign in on a phone.
#
# Both directions are checked. A variable read but not exported is the bug above.
# A variable exported but not read is a contract nobody honours — it was
# VITE_ENVIRONMENT and VITE_DISPLAY_NAME, invented by the pipeline and consumed
# by nothing.
#
# A "read" is any of the three places a VITE_* value can be consumed at build
# time: `import.meta.env.VITE_X` in the bundle's source, `%VITE_X%` in
# index.html (Vite substitutes it into the document), and `VITE_X` in
# vite.config.ts (which sets `base` from VITE_BASE, emits preconnect hints and
# feeds the PWA manifest). The app's name and description are consumed by the
# second and third of those as much as by the first, so counting only the
# source would report them as exported-but-unread.
#
# Usage:
#   scripts/check-vite-env.sh [--json] [--self-test]

# shellcheck source-path=SCRIPTDIR source=lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"

AS_JSON=""
SELF_TEST=0

while [ $# -gt 0 ]; do
    case "$1" in
        --json) AS_JSON="--json" ;;
        --self-test) SELF_TEST=1 ;;
        -h | --help)
            usage_from_header "${BASH_SOURCE[0]}"
            exit 0
            ;;
        *) die "unknown flag '$1' (see --help)" ;;
    esac
    shift
done

SRC_DIR="${CHINTAN_FRONTEND_SRC:-frontend/src}"
BUILD_SCRIPT="${CHINTAN_BUILD_SCRIPT:-scripts/ci-build-site.sh}"
# The two build-time consumers outside src/. The self-test points SRC_DIR at a
# doctored tree that has neither, and a missing file is simply no reads.
INDEX_HTML="${CHINTAN_FRONTEND_INDEX:-frontend/index.html}"
VITE_CONFIG="${CHINTAN_FRONTEND_VITE_CONFIG:-frontend/vite.config.ts}"

read_vars() {
    {
        grep -rhoE 'import\.meta\.env\.(VITE_[A-Z0-9_]+)' "$REPO_ROOT/$SRC_DIR" 2>/dev/null |
            sed 's/^import\.meta\.env\.//' || true
        grep -oE '%VITE_[A-Z0-9_]+%' "$REPO_ROOT/$INDEX_HTML" 2>/dev/null | tr -d '%' || true
        # Ends in a letter or digit, so prose such as "VITE_APP_*" in a comment
        # is not mistaken for a variable.
        grep -oE '\bVITE_[A-Z0-9_]*[A-Z0-9]\b' "$REPO_ROOT/$VITE_CONFIG" 2>/dev/null || true
    } | LC_ALL=C sort -u
}

exported_vars() {
    grep -oE '\bVITE_[A-Z0-9_]+=' "$REPO_ROOT/$BUILD_SCRIPT" 2>/dev/null |
        tr -d '=' | LC_ALL=C sort -u || true
}

run_check() {
    local reads exports v
    reads="$(read_vars)"
    exports="$(exported_vars)"

    if [ -z "$reads" ] && [ -z "$exports" ]; then
        violation "found no VITE_* variables in either $SRC_DIR or $BUILD_SCRIPT — this check is inspecting nothing"
        return
    fi

    info "read by $SRC_DIR, $INDEX_HTML and $VITE_CONFIG: $(printf '%s' "$reads" | tr '\n' ' ')"
    info "exported by $BUILD_SCRIPT: $(printf '%s' "$exports" | tr '\n' ' ')"

    for v in $reads; do
        printf '%s\n' "$exports" | grep -qxF "$v" ||
            violation "$v is read by the bundle but never exported by $BUILD_SCRIPT — a missing variable becomes an empty string at build time, so this ships as a sign-in failure, not a build failure"
    done

    for v in $exports; do
        printf '%s\n' "$reads" | grep -qxF "$v" ||
            violation "$v is exported by $BUILD_SCRIPT but read by nothing in $SRC_DIR, $INDEX_HTML or $VITE_CONFIG — remove it or wire it up"
    done
}

# ---------------------------------------------------------------------------
# Self-test: reproduce the exact bug this check was written for
# ---------------------------------------------------------------------------

if [ "$SELF_TEST" = "1" ]; then
    info "self-test: asserting this check detects the name mismatch it exists for"
    tmp="$(mktemp -d)"
    trap 'rm -rf "$tmp"' EXIT
    mkdir -p "$tmp/src" "$tmp/scripts"

    printf 'export const clientId = import.meta.env.VITE_CLIENT_ID;\n' >"$tmp/src/env.ts"
    printf 'VITE_CLIENT_ID="$client_id" bun run build\n' >"$tmp/scripts/build.sh"

    if ! CHINTAN_REPO_ROOT="$tmp" CHINTAN_FRONTEND_SRC=src CHINTAN_BUILD_SCRIPT=scripts/build.sh \
        "${BASH_SOURCE[0]}" >/dev/null 2>&1; then
        die "self-test inconclusive: the check fails when the names already agree"
    fi
    ok "control: matching names pass"

    # The real bug: exported under the CloudFormation output's name instead.
    printf 'VITE_USER_POOL_CLIENT_ID="$client_id" bun run build\n' >"$tmp/scripts/build.sh"
    if CHINTAN_REPO_ROOT="$tmp" CHINTAN_FRONTEND_SRC=src CHINTAN_BUILD_SCRIPT=scripts/build.sh \
        "${BASH_SOURCE[0]}" >/dev/null 2>&1; then
        die "self-test FAILED: the check passed while the bundle read VITE_CLIENT_ID and the pipeline exported VITE_USER_POOL_CLIENT_ID"
    fi
    ok "self-test: the check fails on the exact mismatch that shipped"
    exit 0
fi

info "the bundle's VITE_* reads match the deploy pipeline's exports"
run_check
finish_check "vite env contract" "$AS_JSON"
