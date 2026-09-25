#!/usr/bin/env bash
#
# Assert that no provider adapter, pipeline stage or service puts a response
# body — or anything else derived from user speech — into a log line or an
# error string.
#
# The three adapters get this right today, each with a comment saying so:
#
#   groq_stt.go       "Do not include response body — may echo transcript fragments."
#   openai_cleanup.go "Do not include response body — may contain transcript content."
#   openai_router.go  "Counts only: note text does not belong in logs."
#
# A comment is not a control. This is the control. The invariant it defends is
# that no transcript or audio content reaches any log line, ever — and the place
# it is most likely to be broken is a debugging session that adds the body to an
# error message to find out why a provider is returning 400.
#
# HOW IT DECIDES
#
# For each logging or error-formatting call in the adapters it strips the string
# literals (so the word "transcript" in a message is not a hit), strips comments,
# and strips len(...) wrappers (a word COUNT is exactly what these adapters log
# instead of the text). Whatever content-bearing identifier survives that is a
# real reference to the value, not to a description of it.
#
# Usage:
#   scripts/check-log-hygiene.sh [--json] [--self-test] [PATH ...]
#
# PATH defaults to backend/internal/provider, backend/internal/pipeline,
# backend/internal/service and backend/internal/handler — the adapters, the
# two packages whose slog lines carry the most context about a capture and so
# are the likeliest places for a transcript to be added to a message (review
# 2026-09-05, S18), and the handler, which holds a device key exactly as the
# caller presented it (`raw` in inbox.go) between the header and Authenticate
# — the one place a key could be logged whole (review 2026-09-24, R4-37).

# shellcheck source-path=SCRIPTDIR source=lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"

AS_JSON=""
SELF_TEST=0
PATHS=()

while [ $# -gt 0 ]; do
    case "$1" in
        --json) AS_JSON="--json" ;;
        --self-test) SELF_TEST=1 ;;
        -h | --help)
            usage_from_header "${BASH_SOURCE[0]}"
            exit 0
            ;;
        -*) die "unknown flag '$1' (see --help)" ;;
        *) PATHS+=("$1") ;;
    esac
    shift
done

[ "${#PATHS[@]}" -gt 0 ] || PATHS=("backend/internal/provider" "backend/internal/pipeline" "backend/internal/service" "backend/internal/handler")

# Calls whose argument list can end up in CloudWatch or in an HTTP response.
EMITTERS='(log\.(Printf|Println|Print|Fatalf|Fatal|Panicf|Panic)|slog\.[A-Za-z]+|fmt\.(Errorf|Sprintf|Printf|Println|Print)|panic\()'

# Identifiers that hold user speech, the audio itself, the raw provider
# response, or a device key. `.Text` and `.Content` are the decoded fields of
# exactly those bodies; `raw`, `rawKey`, `KeyHash` and `presented` are the
# names the handler and the device service give a key on its way to being
# hashed, and a request body before it is decoded.
FORBIDDEN='(respBody|rawBody|bodyBytes|resp\.Body|\btranscript\b|\bTranscript\b|\baudio\b|\bAudio\b|\.Content\b|\.Text\b|cleanText|CleanText|\bprompt\b|\bPrompt\b|apiKey|APIKey|\brawKey\b|\braw\b|KeyHash|presented)'

scan() {
    local root="$1"
    local file line lineno stripped

    while IFS= read -r file; do
        [ -n "$file" ] || continue
        case "$file" in
            *_test.go) continue ;;
        esac
        lineno=0
        while IFS= read -r line; do
            lineno=$((lineno + 1))
            printf '%s' "$line" | grep -qE "$EMITTERS" || continue

            # Order matters: strings first (a message may contain //), then
            # comments, then len(...) counts.
            stripped="$(printf '%s' "$line" |
                sed -e 's/"[^"]*"//g' -e 's|//.*||' -e 's/len([^)]*)//g')"

            if printf '%s' "$stripped" | grep -qE "$FORBIDDEN"; then
                violation "${file}:${lineno}: user content must not be logged or formatted — $(printf '%s' "$line" | sed 's/^[[:space:]]*//')"
            fi
        done <"$REPO_ROOT/$file"
    done < <(cd "$REPO_ROOT" && find "$root" -name '*.go' -type f 2>/dev/null | LC_ALL=C sort)
}

# ---------------------------------------------------------------------------
# Self-test: prove the check can fail
# ---------------------------------------------------------------------------
#
# A check nobody has watched fail is a check nobody should believe.

if [ "$SELF_TEST" = "1" ]; then
    info "self-test: asserting this check detects a logged response body"
    tmp="$(mktemp -d)"
    trap 'rm -rf "$tmp"' EXIT
    mkdir -p "$tmp/provider"

    cat >"$tmp/provider/clean.go" <<'GO'
package provider

import "log"

func clean(words int) {
    log.Printf("provider: dropped router content (%d words)", words)
}
GO
    if ! CHINTAN_REPO_ROOT="$tmp" "${BASH_SOURCE[0]}" provider >/dev/null 2>&1; then
        die "self-test inconclusive: the check fails on a clean file, so a failure below would prove nothing"
    fi
    ok "control: a counts-only log line passes"

    cat >"$tmp/provider/dirty.go" <<'GO'
package provider

import "log"

func dirty(respBody []byte) {
    log.Printf("provider: response was %s", string(respBody))
}
GO
    if CHINTAN_REPO_ROOT="$tmp" "${BASH_SOURCE[0]}" provider >/dev/null 2>&1; then
        die "self-test FAILED: the check passed on a file that logs a response body"
    fi
    ok "self-test: the check fails when a response body is logged"

    # The handler case: a device key logged under the name the handler gives
    # it. The dirty provider file is deleted first so this fails on its own.
    rm -f "$tmp/provider/dirty.go"
    mkdir -p "$tmp/handler"
    cat >"$tmp/handler/inbox.go" <<'GO'
package handler

import "log/slog"

func authenticate(raw string) {
    slog.Info("inbox: refused key", "key", raw)
}
GO
    if CHINTAN_REPO_ROOT="$tmp" "${BASH_SOURCE[0]}" handler >/dev/null 2>&1; then
        die "self-test FAILED: the check passed on a file that logs a presented device key"
    fi
    ok "self-test: the check fails when a device key is logged"
    exit 0
fi

for p in "${PATHS[@]}"; do
    if [ ! -d "$REPO_ROOT/$p" ]; then
        no_subject="$p does not exist"
        warn "$no_subject — nothing to scan"
        continue
    fi
    info "scanning $p for logged user content"
    scan "$p"
done

finish_check "provider log hygiene" "$AS_JSON"
