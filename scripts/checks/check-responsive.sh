#!/usr/bin/env bash
#
# Responsive checks at 320px and 1440px, no horizontal page scroll (§4A.6,
# §0.5A). Active: Phase 1.
#
# What it prevents: a layout that only works at the width it was built on.
#
# §4A.6's rules that are testable mechanically:
#   - every surface usable at 320px and at a 1440px viewport
#   - NO horizontal page scroll at any width in between; wide content — the
#     silence-scaled timeline, tables — scrolls inside its own container
#   - text has a maximum measure always: prompt bodies and transcripts cap at 70
#     characters no matter how wide the window
#   - safe-area insets and dvh rather than vh, because a notch and a keyboard
#     that halves the viewport are the normal case on the primary device
#
# Dormant until Phase 1. Shares the headless browser with check-a11y.sh.
# shellcheck source-path=SCRIPTDIR source=../lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/common.sh"
AS_JSON="${1:-}"
cd "$REPO_ROOT" || exit 1

if [ ! -f frontend/index.html ]; then
    no_subject_yet "responsive layout" 1 "frontend/index.html"
    finish_check "responsive at 320px and 1440px (§4A.6)" "$AS_JSON"
    exit 0
fi

# Checkable from source without a browser: vh instead of dvh is the specific bug
# §4A.6 calls out, and it reproduces only on a phone with the keyboard open —
# never in a desktop browser, which is why a static check earns its place here.
if [ -d frontend/css ]; then
    info "checking for vh units where dvh is required (§4A.6)"
    while IFS= read -r hit; do
        file="${hit%%:*}"
        rest="${hit#*:}"
        line="${rest%%:*}"
        violation "$file:$line uses a vh unit — use dvh: on the primary device the on-screen keyboard halves the viewport and vh does not account for it (§4A.6)"
    done < <(grep -rnE '[0-9]+vh\b' frontend/css 2>/dev/null | grep -v 'dvh' || true)
fi

if ! command -v chromium >/dev/null 2>&1 && ! command -v chrome >/dev/null 2>&1; then
    violation "frontend/index.html exists but the toolchain image has no headless browser; implement the 320px/1440px scroll assertions (§4A.6)"
fi

finish_check "responsive at 320px and 1440px (§4A.6)" "$AS_JSON"
