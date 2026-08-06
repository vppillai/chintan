#!/usr/bin/env bash
#
# Frontend tests: the build script, the Pages workflow, and the two static checks
# that own §4A rules.
#
# Why this file exists rather than a `make test-frontend` target: the §0.5A inventory
# is fixed in Phase 0 and has no frontend-unit-test row, so these run under the
# existing admin-script gate (scripts/test/harness.sh calls this). A row of its own is
# the durable answer and is proposed rather than added, because adding one is a change
# to the inventory and to the Makefile.
#
# Three properties every case here has, because the alternative is a test suite that
# is believed rather than true (§0.5A):
#
#   - **Hermetic.** Each build case runs against a fixture tree in a temp directory
#     with its own git history and its own config/instances/dev.yaml, reached through
#     CHINTAN_REPO_ROOT. Nothing writes into the worktree, so a crashed run cannot
#     leave a stray instance config behind — which would then be discovered by the
#     instance matrix in deploy.yaml.
#   - **Asserted on the artifact.** A build that exits 0 proves nothing; every case
#     reads the file it produced.
#   - **Both directions.** A refusal case asserts the refusal AND that the thing it
#     refused to destroy is still there.
#
# The subject matter is the set of defects a review demonstrated: branding values
# substituted into HTML and SVG with no escaping, `&` in a value replacing a
# placeholder with itself under bash 5.2's patsub_replacement, a trailing slash on
# --out breaking the G-035 leak assertion, `rm -rf` on an unvalidated --out, and a
# Pages workflow whose build job held no `pages` permission and therefore could never
# publish anything.
#
# Usage: frontend.sh [--help]
# shellcheck source-path=SCRIPTDIR source=../lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/common.sh"

case "${1:-}" in
    -h | --help)
        sed -n '2,31p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
        exit 0
        ;;
    "") ;;
    *) die "unknown flag '$1' (see --help)" ;;
esac

cd "$REPO_ROOT" || exit 1

PASS=0
FAIL=0
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

pass() {
    printf '%s  pass%s %s\n' "$C_GREEN" "$C_OFF" "$1" >&2
    PASS=$((PASS + 1))
}

fail() {
    printf '%s  FAIL%s %s\n' "$C_RED" "$C_OFF" "$1" >&2
    FAIL=$((FAIL + 1))
}

assert() {
    local name="$1"
    shift
    if "$@"; then pass "$name"; else fail "$name"; fi
}

refute() {
    local name="$1"
    shift
    if "$@"; then fail "$name"; else pass "$name"; fi
}

# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------

# A minimal repository the build script will accept: the frontend sources, one
# instance config, and a git history with a tag. The tag and the commit matter —
# build-frontend.sh refuses to build without a resolvable commit, because a constant
# cache token IS G-035.
make_tree() {
    local dest="$1"
    mkdir -p "$dest/config/instances"
    cp -R "$REPO_ROOT/frontend" "$dest/frontend"
    rm -rf "$dest/frontend/dist" "$dest/frontend/test"
    cp "$REPO_ROOT/config/instances/dev.yaml" "$dest/config/instances/dev.yaml"
    git -C "$dest" init -q
    git -C "$dest" add -A
    git -C "$dest" -c user.email=t@example.invalid -c user.name=fixture commit -qm fixture
    git -C "$dest" tag v9.9.9
}

# Branding edits go through yq with strenv, never string interpolation: the values
# under test contain quotes, angle brackets and ampersands, which is the entire point.
set_branding() {
    local tree="$1" key="$2" value="$3"
    VALUE="$value" yq -i ".branding.${key} = strenv(VALUE)" "$tree/config/instances/dev.yaml"
}

# Returns 0 on a successful build. Output is captured so a failing case can print it
# and a passing case stays quiet.
build_in() {
    local tree="$1"
    shift
    (
        cd "$tree" || exit 1
        CHINTAN_REPO_ROOT="$tree" CHINTAN_API_BASE_URL="https://api.example.invalid" \
            "$REPO_ROOT/scripts/build-frontend.sh" "$@"
    ) >"$tree/build.log" 2>&1
}

contains() { grep -qF -- "$2" "$1"; }

info "frontend tests"

# ---------------------------------------------------------------------------
# 1. Baseline, and the G-035 split on the real artifact
# ---------------------------------------------------------------------------

BASE="$WORK/base"
make_tree "$BASE"
if build_in "$BASE" --out "$BASE/build/dist"; then
    pass "a clean tree builds"
    assert "the cache token reaches sw.js (G-035)" contains "$BASE/build/dist/sw.js" "v9.9.9-"
    refute "the cache token stays out of index.html (§0.6)" contains "$BASE/build/dist/index.html" "v9.9.9-"
    refute "the cache token stays out of app.js (§0.6)" contains "$BASE/build/dist/js/app.js" "v9.9.9-"
    assert "the bare tag is rendered as text (§0.6)" contains "$BASE/build/dist/index.html" ">v9.9.9<"
    assert "the worker namespaces its cache by scope" contains "$BASE/build/dist/sw.js" "registration.scope"
else
    fail "a clean tree builds"
    cat "$BASE/build.log" >&2
fi

# ---------------------------------------------------------------------------
# 2. Escaping. Each of these was demonstrated to ship broken markup with rc=0.
# ---------------------------------------------------------------------------

XSS="$WORK/xss"
make_tree "$XSS"
set_branding "$XSS" name 'Chintan <script>alert(1)</script>'
if build_in "$XSS" --out "$XSS/build/dist"; then
    pass "a branding name containing markup still builds"
    refute "an angle bracket in branding.name cannot emit a tag into the page" \
        contains "$XSS/build/dist/index.html" "<script>alert(1)</script>"
    assert "it is entity-escaped instead" \
        contains "$XSS/build/dist/index.html" "&lt;script&gt;alert(1)&lt;/script&gt;"
else
    fail "a branding name containing markup still builds"
    cat "$XSS/build.log" >&2
fi

QUOTED="$WORK/quoted"
make_tree "$QUOTED"
set_branding "$QUOTED" description 'Say it "aloud"; find it later'
if build_in "$QUOTED" --out "$QUOTED/build/dist"; then
    pass "a description containing a quote still builds"
    # The failure this replaces: content="Say it "aloud"; find it later" terminated
    # the attribute early and produced three junk attributes.
    refute "a quote cannot terminate the description meta attribute" \
        contains "$QUOTED/build/dist/index.html" 'content="Say it "aloud"'
    assert "it is entity-escaped instead" \
        contains "$QUOTED/build/dist/index.html" "Say it &quot;aloud&quot;"
else
    fail "a description containing a quote still builds"
    cat "$QUOTED/build.log" >&2
fi

AMP="$WORK/amp"
make_tree "$AMP"
set_branding "$AMP" description 'Capture & find your thinking.'
if build_in "$AMP" --out "$AMP/build/dist"; then
    pass "a description containing '&' still builds (patsub_replacement)"
    # Under bash 5.2's default, `&` in the replacement expanded to the matched text,
    # so the placeholder replaced itself and the build died naming index.html — a
    # diagnostic pointing at the template for a fault in the config.
    refute "the placeholder is not substituted with itself" \
        contains "$AMP/build/dist/index.html" "{{APP_DESCRIPTION}}"
    assert "the ampersand is entity-escaped" \
        contains "$AMP/build/dist/index.html" "Capture &amp; find"
else
    fail "a description containing '&' still builds (patsub_replacement)"
    cat "$AMP/build.log" >&2
fi

SVGCOL="$WORK/svgcol"
make_tree "$SVGCOL"
set_branding "$SVGCOL" theme_color 'red; } html { display: none }'
refute "a colour that is not a hex triple is refused before it rewrites the stylesheet" \
    build_in "$SVGCOL" --out "$SVGCOL/build/dist"

NEWLINE="$WORK/newline"
make_tree "$NEWLINE"
set_branding "$NEWLINE" name 'Chintan
second line'
refute "a newline in a branding value is refused, not escaped" \
    build_in "$NEWLINE" --out "$NEWLINE/build/dist"

# ---------------------------------------------------------------------------
# 3. --out: the trailing slash, and the paths this script must refuse to delete
# ---------------------------------------------------------------------------

SLASH="$WORK/slash"
make_tree "$SLASH"
if build_in "$SLASH" --out "$SLASH/build/dist/"; then
    pass "a trailing slash on --out builds (the G-035 leak assertion still matches sw.js)"
else
    fail "a trailing slash on --out builds (the G-035 leak assertion still matches sw.js)"
    cat "$SLASH/build.log" >&2
fi

SRCOUT="$WORK/srcout"
make_tree "$SRCOUT"
refute "--out frontend is refused" build_in "$SRCOUT" --out frontend
assert "and the source tree it would have deleted is intact" \
    test -f "$SRCOUT/frontend/index.html"
assert "including the stylesheets" test -f "$SRCOUT/frontend/css/tokens.css"

TEMPLATED="$WORK/templated"
make_tree "$TEMPLATED"
mkdir -p "$TEMPLATED/staging"
cp -R "$TEMPLATED/frontend" "$TEMPLATED/staging/dist"
refute "a path ending in /dist that holds template sources is refused" \
    build_in "$TEMPLATED" --out "$TEMPLATED/staging/dist"
assert "and those sources are intact" test -f "$TEMPLATED/staging/dist/index.html"

DRY="$WORK/dry"
make_tree "$DRY"
build_in "$DRY" --out "$DRY/build/dist" || true
if [ -f "$DRY/build/dist/index.html" ]; then
    printf 'sentinel\n' >"$DRY/build/dist/sentinel"
    if build_in "$DRY" --out "$DRY/build/dist" --dry-run; then
        assert "--dry-run deletes nothing (§11.3)" test -f "$DRY/build/dist/sentinel"
    else
        fail "--dry-run exits 0"
    fi
else
    fail "--dry-run precondition: the first build produced a dist"
fi

# ---------------------------------------------------------------------------
# 4. The Pages workflow. The critical defect was invisible from the file's own text.
# ---------------------------------------------------------------------------

PAGES=".github/workflows/pages.yaml"
if [ -f "$PAGES" ]; then
    # Without a `pages` scope on the BUILD job, configure-pages cannot even read the
    # Pages site: the metadata GET 403s and the job dies before anything is uploaded,
    # so `deploy` never runs and nothing is ever published. Job-level permissions
    # REPLACE the workflow-level block rather than merging with it, which is what made
    # `pages: write` on the deploy job look sufficient.
    assert "the Pages build job holds a pages permission" \
        bash -c "yq -e '.jobs.build.permissions.pages' '$PAGES' >/dev/null"
    assert "the Pages build job still holds contents and packages after overriding" \
        bash -c "yq -e '.jobs.build.permissions.contents and .jobs.build.permissions.packages' '$PAGES' >/dev/null"
    assert "the deploy job holds pages: write and id-token: write" \
        bash -c "yq -e '.jobs.deploy.permissions.pages == \"write\" and .jobs.deploy.permissions[\"id-token\"] == \"write\"' '$PAGES' >/dev/null"
    # enablement:true requires administration:write, which is not a grantable
    # GITHUB_TOKEN scope at all — the action's own action.yml says so. It can only
    # 403, and its presence made a false claim about first-deploy setup.
    #
    # Asserted on the parsed step inputs, not by grepping the file: the comment that
    # records why it was removed necessarily contains the word.
    refute "no step asks configure-pages for enablement, which GITHUB_TOKEN cannot do" \
        bash -c "yq -e '[.jobs[].steps[]? | (.with // {}) | has(\"enablement\")] | any' '$PAGES' >/dev/null 2>&1"
    # Every action pinned to a commit SHA: an action is code running in a job and a
    # tag is mutable (the G-048 reasoning applied to third-party code).
    while IFS= read -r ref; do
        case "$ref" in
            ./*) continue ;; # a local reusable workflow, not a third-party action
        esac
        if printf '%s' "$ref" | grep -qE '@[0-9a-f]{40}$'; then
            pass "pinned to a commit SHA: $ref"
        else
            fail "not pinned to a commit SHA: $ref"
        fi
    done < <(yq -r '.. | select(has("uses")) | .uses' "$PAGES" 2>/dev/null | grep -v '^null$' || true)
else
    fail "$PAGES exists"
fi

# ---------------------------------------------------------------------------
# 5. The static halves of check-a11y and check-responsive, proved able to fail
# ---------------------------------------------------------------------------
#
# Both gates are red today on the missing headless browser, and a red gate catches no
# regression. So the static rules they DO implement are exercised here against
# doctored trees, and asserted by matching the specific violation rather than by exit
# status — the browser violation is present in every run and would otherwise make
# these pass for the wrong reason.

doctored_tree() {
    local dest="$1"
    mkdir -p "$dest/frontend/css"
    printf '<!doctype html><html><body></body></html>\n' >"$dest/frontend/index.html"
    printf ':root { --color-live: #C0391B; --palette-live: #C0391B; }\n' >"$dest/frontend/css/tokens.css"
}

violation_matching() {
    local script="$1" tree="$2" pattern="$3" out
    out="$(CHINTAN_REPO_ROOT="$tree" bash "$REPO_ROOT/scripts/checks/$script" --json 2>/dev/null || true)"
    printf '%s' "$out" | jq -e --arg p "$pattern" 'any(.violations[]; test($p))' >/dev/null
}

LIVE="$WORK/live"
doctored_tree "$LIVE"
printf '.delete-button { color: var(--color-live); }\n' >"$LIVE/frontend/css/app.css"
assert "check-a11y rejects the 'live' token on a non-recording rule (§4A.2)" \
    violation_matching check-a11y.sh "$LIVE" "live' token outside a recording context"

PALETTE="$WORK/palette"
doctored_tree "$PALETTE"
printf '.badge { color: var(--palette-live); }\n' >"$PALETTE/frontend/css/app.css"
assert "check-a11y rejects a direct --palette-live reference outside tokens.css" \
    violation_matching check-a11y.sh "$PALETTE" "palette-live"

VH="$WORK/vh"
doctored_tree "$VH"
# On ONE line with dvh, which is the case the previous `grep -v dvh` line filter let
# through: the check dropped any line mentioning dvh anywhere.
printf '.pane { height: 100vh; min-height: 100dvh; }\n' >"$VH/frontend/css/app.css"
assert "check-responsive rejects a vh unit on a line that also uses dvh (§4A.6)" \
    violation_matching check-responsive.sh "$VH" "uses a vh unit"

VHJS="$WORK/vhjs"
doctored_tree "$VHJS"
printf '/* clean */\n' >"$VHJS/frontend/css/app.css"
mkdir -p "$VHJS/frontend/js"
printf 'el.style.height = "100vh";\n' >"$VHJS/frontend/js/app.ts"
assert "check-responsive reads the modules too, not only the stylesheets" \
    violation_matching check-responsive.sh "$VHJS" "uses a vh unit"

# ---------------------------------------------------------------------------
# 6. The TypeScript unit tests
# ---------------------------------------------------------------------------

if [ -d frontend/test ]; then
    info "frontend unit tests (bun test)"
    if bun test frontend/test >&2; then
        pass "bun test frontend/test"
    else
        fail "bun test frontend/test"
    fi
fi

# ---------------------------------------------------------------------------

log ""
if [ "$FAIL" -gt 0 ]; then
    err "$FAIL frontend test(s) failed, $PASS passed"
    exit 1
fi
ok "$PASS frontend test(s) passed"
