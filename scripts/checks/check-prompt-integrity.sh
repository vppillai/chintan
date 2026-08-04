#!/usr/bin/env bash
#
# Prompt integrity (§3A.3, A4, §0.5A). Active: Phase 3.
#
# What it prevents: "the destructive failure with the worst blast radius" — and
# that phrasing is the spec's, not an embellishment.
#
# The rule: every `prompt` item's body matches its transcript span apart from
# recorded STT corrections. No shortening, no reordering, no restructuring. §12
# calls this "a hard test", and §11A.4 distinguishes it from every other
# extraction metric: those are proxies, this is not — any failure is a defect.
#
# Why it matters this much: a `prompt` exists to be pasted into a coding agent
# months later. Its value IS the full text. Summarising it destroys the artifact
# while appearing to succeed, and the loss is not noticed for months — at which
# point it is unrecoverable if the original was not retained (G-040). The
# transcript retention in §3A.1 is what makes recovery possible at all, and this
# check is what makes the damage visible before it accumulates.
#
# Two layers, and both are required: this build-time check over fixtures, and the
# same assertion inside verify.sh over the live corpus on the weekly sweep
# (§11.6). A build-time check alone would not catch content already stored.
# shellcheck source-path=SCRIPTDIR source=../lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/common.sh"
AS_JSON="${1:-}"
cd "$REPO_ROOT" || exit 1

# The subject is the extraction pipeline, so this is dormant until the code that
# could damage a prompt exists.
if [ ! -d backend/internal/extraction ]; then
    no_subject_yet "prompt integrity" 3 "backend/internal/extraction"
    finish_check "prompt integrity (§3A.3, A4)" "$AS_JSON"
    exit 0
fi

# Guard against the pipeline gaining a summarisation path that can reach a prompt
# body. Enforced in code rather than by prompt instruction alone, which §Phase 3
# requires explicitly: "Per-type processing per the §3A.3 table, enforced in code
# rather than by prompt instruction alone."
if grep -rn 'summar' backend/internal/extraction --include='*.go' 2>/dev/null |
    grep -iv 'test\|// \|prompt.*never\|never.*summar' |
    grep -i 'prompt' >/dev/null; then
    violation "a summarisation path in backend/internal/extraction references prompt bodies — a prompt body is never summarised (§3A.3)"
fi

if ! (cd backend && go test -run 'TestPromptIntegrity|TestPromptVerbatim' ./internal/extraction/... 2>&1); then
    violation "prompt integrity tests failed (§3A.3) — a prompt body diverged from its transcript span"
fi

finish_check "prompt integrity (§3A.3, A4)" "$AS_JSON"
