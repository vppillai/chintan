#!/usr/bin/env bash
#
# Accessibility and contrast, both capture faces (§4A.7, §0.5A). Active: Phase 1.
#
# What it prevents: an interface that fails in the car or at night.
#
# Both faces, because §4A.2 requires the capture face to invert after dark
# automatically — a `paper`-bright screen in a car at night is a hazard, not a
# styling preference — and a contrast suite that only tests the light theme would
# pass while the dark one failed. The checks §4A.7 makes non-optional:
#
#   - WCAG AA or better for every text and state pair, in BOTH themes
#   - visible keyboard focus throughout the triage face, which is keyboard-first
#   - no hover-only affordance anywhere; the primary device has no hover
#   - prefers-reduced-motion respected, the recording indicator excepted because
#     it carries state
#   - 44px minimum targets in the triage face
#   - usable at 200% text zoom
#
# Dormant until there is a rendered surface. Phase 1 adds a headless browser to
# containers/toolchain and turns this on; the image deliberately omits Chromium
# until then, because ~400MB on every CI job is cost without coverage.
# shellcheck source-path=SCRIPTDIR source=../lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/common.sh"
AS_JSON="${1:-}"
cd "$REPO_ROOT" || exit 1

if [ ! -f frontend/index.html ]; then
    no_subject_yet "accessibility and contrast" 1 "frontend/index.html"
    finish_check "accessibility and contrast, both capture faces (§4A.7)" "$AS_JSON"
    exit 0
fi

# The one part that is checkable without a browser: the seven colour tokens in
# §4A.2 are fixed design tokens, and `live` is reserved for recording and nothing
# else. A glance at a screen at 100km/h must answer "is it recording?" from
# colour alone, and every other use of that hue erodes the signal — so a `live`
# token on a delete button or an error state is a violation of the one rule in
# §4A.2 that matters more than the values.
if [ -f frontend/css/tokens.css ]; then
    info "checking the 'live' colour token is reserved for recording (§4A.2)"
    while IFS= read -r hit; do
        file="${hit%%:*}"
        rest="${hit#*:}"
        line="${rest%%:*}"
        # Permitted: the token definition, and rules whose selector names
        # recording state.
        if ! printf '%s' "$rest" | grep -qiE 'record|--color-live:|capture-live'; then
            violation "$file:$line uses the 'live' token outside a recording context — it must never be a decorative accent, a delete button, or an error (§4A.2)"
        fi
    done < <(grep -rn 'var(--color-live)' frontend/css 2>/dev/null || true)
fi

if ! command -v chromium >/dev/null 2>&1 && ! command -v chrome >/dev/null 2>&1; then
    violation "frontend/index.html exists but the toolchain image has no headless browser; add one to containers/toolchain and implement the axe/contrast run (§4A.7). This check must not pass trivially once there is a surface to test."
fi

finish_check "accessibility and contrast, both capture faces (§4A.7)" "$AS_JSON"
