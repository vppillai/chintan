#!/usr/bin/env bash
#
# Static check: no hardcoded user-visible strings (§7.3, §0.5A).
#
# What it prevents: a rebrand becoming a code change.
#
# §7.3 states the rule precisely, and the precision matters: "No user-visible
# string is hardcoded anywhere in the frontend or backend. The app name appears in
# the manifest, the document title, the version footer, Telegram bot replies, and
# export frontmatter — all of it resolved from `branding`."
#
# So the subject is **strings that can reach a user**, not every occurrence of the
# word. Three categories are deliberately not violations, because none is
# user-visible and treating them as such would make the check unusable noise:
#
#   - Comments and documentation. Prose explaining the product names it.
#   - The Go module path and the repository name (github.com/vppillai/chintan).
#     The repository is called chintan; that is not a string shipped to a user, and
#     it cannot be resolved from config because it is fixed at compile time.
#   - Build tooling identity — the toolchain image name, Makefile headers. These
#     are developer-facing and never rendered in the app.
#
# What IS checked: string literals in backend and frontend source, and any text or
# attribute value in the frontend's HTML, CSS, and JSON. Those are the surfaces
# §7.3 enumerates.
#
# Note the asymmetry, which is intentional. `voicenotes` SHOULD appear throughout
# the infrastructure — it is the resource namespace. It should appear in no
# user-visible surface. Both directions are checked, because conflating the two is
# what turns a marketing decision into an infrastructure migration (G-056).
#
# Limitation, stated rather than hidden: this is a line-oriented scan, so a brand
# name inside a multi-line raw string could be missed. The precise version is an
# AST walk over string literals, which is real logic and belongs in the admin
# binary (§11.2) rather than in bash. Phase 1 moves it there, when the frontend
# gives this check a substantial surface to cover.
#
# Usage: check-brand-strings.sh [--json]

# shellcheck source-path=SCRIPTDIR source=../lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/common.sh"

AS_JSON="${1:-}"
cd "$REPO_ROOT" || exit 1

# The brand name is read from config rather than hardcoded here — a check that
# hardcodes the value it forbids would need editing on the rebrand it exists to
# make cheap.
BRAND="$(yq -r '.branding.name' config/instances/dev.yaml 2>/dev/null || echo '')"
if [ -z "$BRAND" ] || [ "$BRAND" = "null" ]; then
    die "cannot read branding.name from config/instances/dev.yaml; this check derives the forbidden literal from config so that a rebrand does not require editing the check"
fi

# strip_noise removes what cannot reach a user: comment bodies, and module paths.
# Applied before the search so the search itself stays a simple grep.
strip_noise() {
    sed -E \
        -e 's@//.*$@@' \
        -e 's@/\*.*\*/@@' \
        -e 's@^[[:space:]]*#.*$@@' \
        -e 's@github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+@@g'
}

info "searching for the brand name '$BRAND' in user-reachable strings"

# Source files whose string literals can reach a user.
while IFS= read -r file; do
    [ -f "$file" ] || continue
    case "$file" in
        config/instances/*) continue ;;          # the definition itself
        frontend/dist/*) continue ;;             # generated output
        *_test.go | frontend/test/*) continue ;; # tests assert on the resolved value
    esac

    # Only lines that both mention the brand and carry a quoted string or markup,
    # with comments and module paths already removed.
    while IFS= read -r hit; do
        line_no="${hit%%:*}"
        violation "$file:$line_no hardcodes '$BRAND' in a user-reachable string — resolve it from branding.name (§7.3). A rename must be a config edit, not a code change."
    done < <(
        strip_noise <"$file" | grep -nwi -- "$BRAND" | grep -E '["'"'"'>]' || true
    )
done < <(tracked_files 'backend/**/*.go' 'frontend/**/*.ts' 'frontend/**/*.js' 2>/dev/null)

# Frontend markup, styles, and manifests: every value here is user-visible by
# definition, so no string-literal heuristic is needed.
while IFS= read -r file; do
    [ -f "$file" ] || continue
    case "$file" in
        frontend/dist/*) continue ;;
    esac
    while IFS= read -r hit; do
        line_no="${hit%%:*}"
        violation "$file:$line_no hardcodes '$BRAND' — the manifest, document title, and every rendered string resolve from branding.name (§7.3)"
    done < <(grep -nwi -- "$BRAND" "$file" 2>/dev/null || true)
done < <(tracked_files 'frontend/*.html' 'frontend/**/*.html' 'frontend/**/*.css' 'frontend/*.json' 'frontend/**/*.json' 2>/dev/null)

# The mirror-image defect: the frozen system id leaking into a surface a user
# sees. It is what the infrastructure is called and users never see it (§7.3).
info "checking the frozen system id '$SYSTEM_ID' reaches no user-visible surface"
while IFS= read -r file; do
    [ -f "$file" ] || continue
    case "$file" in
        frontend/dist/*) continue ;;
    esac
    while IFS= read -r hit; do
        line_no="${hit%%:*}"
        violation "$file:$line_no exposes the frozen system id '$SYSTEM_ID' in a user-visible surface (§7.3)"
    done < <(strip_noise <"$file" | grep -nwi -- "$SYSTEM_ID" || true)
done < <(tracked_files 'frontend/*.html' 'frontend/**/*.html' 'frontend/**/*.css' 'frontend/**/*.ts' 2>/dev/null)

finish_check "no hardcoded user-visible strings (§7.3)" "$AS_JSON"
