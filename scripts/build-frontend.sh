#!/usr/bin/env bash
#
# Build the frontend static assets for GitHub Pages.
#
# Dormant until Phase 1 creates the frontend. Present from Phase 0 because the
# pipeline is built first (§0.5A) and the release job needs a build step to call.
#
# What it will do, and why each part is not optional:
#   - substitute the {tag}-{short-sha} cache token into sw.js, and ONLY into
#     sw.js. The displayed version stays the clean tag (§0.6). Getting this wrong
#     ships new markup against old JavaScript in installed PWAs, with no update
#     toast and no way for the user to clear it (G-035).
#   - generate manifest.webmanifest from branding, so no user-visible string is
#     hardcoded (§7.3)
#   - generate the 192/512/maskable icons from branding.icon_source
#   - subset and self-host the fonts, INCLUDING the Devanagari and Malayalam
#     subsets. The primary user code-switches, so transcripts contain those
#     scripts inline, and a transcript that changes texture and baseline where
#     the language switches reads as corruption (§4A.3). A CDN reference is both
#     an offline failure for an installable app and an external request from a
#     page that is otherwise self-contained.
# Usage: build-frontend.sh [--help]
# shellcheck source-path=SCRIPTDIR source=lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"

case "${1:-}" in
    -h | --help)
        sed -n '2,28p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
        exit 0
        ;;
    '') ;;
    *) die "unknown flag '$1' (see --help)" ;;
esac

cd "$REPO_ROOT" || exit 1

if [ ! -f frontend/index.html ]; then
    no_subject_yet "frontend build" 1 "frontend/index.html"
    exit 0
fi

TAG="$(git describe --tags --abbrev=0 2>/dev/null || echo unstamped)"
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
CACHE_TOKEN="${TAG}-${COMMIT}"

info "building frontend $TAG ($COMMIT)"
dim "  service worker cache token: $CACHE_TOKEN (G-035)"

die "frontend build is not implemented yet — Phase 1 owns it, and this script must fail loudly rather than produce an empty dist/ that deploys as a blank app"
