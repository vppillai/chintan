#!/usr/bin/env bash
#
# Build the frontend static assets for GitHub Pages.
#
# Output is frontend/dist/, which .github/workflows/pages.yaml uploads verbatim.
# Nothing in dist/ is committed: it is a function of the source plus the tag, and a
# committed build output is a second copy that drifts (the same reasoning that
# forbids a checked-in VERSION file — G-037).
#
# Four things here are not optional, each because of a specific failure:
#
#   1. The service-worker cache token is {tag}-{short-sha}, and it is substituted
#      into sw.js AND NOTHING ELSE. Caches must rotate on every DEPLOY, not every
#      TAG: a deploy without a fresh tag produces a byte-identical sw.js, the
#      browser detects no worker update, install/activate never runs, and an
#      installed PWA serves previously-cached assets indefinitely — while
#      index.html, served stale-while-revalidate, DOES pick up the new markup. New
#      HTML against old JavaScript, in installed PWAs only, with no update toast
#      for the user to act on and no reproduction in a normal browser tab (G-035).
#      The DISPLAYED version stays the clean tag (§0.6). Both halves are asserted
#      below rather than trusted, because the failure is invisible from the build
#      log.
#   2. No user-visible string is hardcoded (§7.3). The app name, description, and
#      the two configurable colours come from the branding block of
#      config/instances/{instance}.yaml, and the manifest is generated rather than
#      committed. A rename must be a config edit, not a code change, and
#      scripts/checks/check-brand-strings.sh fails the build on a literal.
#   3. The version is resolved from `git describe` HERE, at build time. CI resolves
#      it during the build, so a tag pushed afterwards does not reach the artifact
#      — tag before deploying (G-036).
#   4. No secret, key, or credential may reach the bundle. The Pages site is
#      world-readable regardless of repository visibility (G-057, §10.6), and the
#      manifest is generated FROM the config file that carries SSM parameter paths
#      — one broadened yq expression is the whole distance between those two facts,
#      so the output is scanned before it ships.
#
# The API base URL is a build input because it differs per instance and must not be
# a literal in source (I5). There is no `api_base_url` key in §7.4 yet, so it is
# supplied by flag or environment and read from config if the key ever exists; see
# the resolution block below for why that ordering.
#
# A fifth thing, learned the hard way: **every substituted value is escaped for the
# syntax it lands in.** The branding block is operator-supplied config, and §7.3
# exists to make a rename a config edit — so a tagline containing an apostrophe, a
# description containing `&`, or a name containing `<` must produce a correct bundle
# or a loud failure, never a world-readable page with broken markup in it. The
# escaping is per destination (HTML, JS, CSS), because escaping is a property of the
# destination syntax and not of the value. See the substitution block.
#
# Usage:
#   build-frontend.sh [--instance NAME] [--api-base-url URL] [--out DIR]
#                     [--dry-run] [--help]
#
#   --out DIR must be a build output path: it must end in /dist or live under the
#   build directory. This script deletes DIR before writing it, and `--out frontend`
#   is a plausible typo that would otherwise delete the source tree.
#
#   --dry-run prints what would be built and deleted, and changes nothing. It is
#   NOT the default, unlike the destructive data scripts §11.3 governs: a build
#   whose default is to do nothing is a build that silently ships the previous
#   artifact, and this script's only external effect is its own output directory,
#   which the --out guard confines to a build path.
#
# Environment:
#   CHINTAN_INSTANCE        default for --instance (dev)
#   CHINTAN_API_BASE_URL    default for --api-base-url
#   CHINTAN_BUILD_TIME      RFC3339 UTC build stamp, shared with build-lambda.sh
#   CHINTAN_BUILD_DIR       scratch directory for the staged copy (build)
# shellcheck source-path=SCRIPTDIR source=lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"

# Bash 5.2 enables patsub_replacement by default, which makes an unquoted `&` in a
# ${var//pat/rep} REPLACEMENT expand to the text that matched the pattern. Every
# substitution below is a ${//} — so with it on, a branding value containing `&`
# ("Capture & find your thinking.") replaces `{{APP_DESCRIPTION}}` with
# `{{APP_DESCRIPTION}}`, and the build then dies on the unsubstituted-placeholder
# assertion naming index.html: a diagnostic pointing at the template for a problem
# in dev.yaml. The HTML escaper below is also written in terms of `&` and would
# corrupt itself.
#
# Verified rather than assumed, because `shopt -u` on an option this bash does not
# have is itself an error, and a silent failure here is a wrong bundle.
shopt -u patsub_replacement 2>/dev/null || true
_patsub_probe="x"
_patsub_probe="${_patsub_probe//x/&y}"
if [ "$_patsub_probe" != "&y" ]; then
    die "this shell expands '&' in a substitution replacement (patsub_replacement) and it could not be disabled: every branding value containing '&' would be silently corrupted"
fi
unset _patsub_probe

INSTANCE="${CHINTAN_INSTANCE:-dev}"
API_BASE_URL="${CHINTAN_API_BASE_URL:-}"
DIST=""
DRY_RUN=0

need_value() {
    [ -n "${2:-}" ] || die "$1 requires a value (see --help)"
}

while [ "$#" -gt 0 ]; do
    case "$1" in
        -h | --help)
            sed -n '2,68p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
            exit 0
            ;;
        --dry-run)
            DRY_RUN=1
            shift
            ;;
        --instance)
            need_value "$1" "${2:-}"
            INSTANCE="$2"
            shift 2
            ;;
        --api-base-url)
            need_value "$1" "${2:-}"
            API_BASE_URL="$2"
            shift 2
            ;;
        --out)
            need_value "$1" "${2:-}"
            DIST="$2"
            shift 2
            ;;
        *) die "unknown flag '$1' (see --help)" ;;
    esac
done

cd "$REPO_ROOT" || exit 1

SRC="frontend"
DIST="${DIST:-frontend/dist}"
BUILD_DIR="${CHINTAN_BUILD_DIR:-build}"
STAGE="${BUILD_DIR}/frontend-stage"

# ---------------------------------------------------------------------------
# --out is a path this script DELETES, so it is normalised and then constrained
# ---------------------------------------------------------------------------

# Trailing slashes are stripped before DIST is used for anything. Tab completion
# supplies one, so `--out build/x/` is the likely first thing an operator types —
# and every later `case "$file" in "$DIST/sw.js")` comparison against a `find`
# result would then compare `build/x//sw.js` against `build/x/sw.js` and not match.
# The specific consequence was the G-035 leak assertion accusing the build of the
# violation it had just implemented correctly, because the exclusion for the worker
# stopped matching the worker.
while [ "$DIST" != "${DIST%/}" ]; do
    DIST="${DIST%/}"
done

# `rm -rf "$DIST"` runs below on an operator-supplied path. `--out frontend` is a
# plausible typo — the default is `frontend/dist` — and it would delete
# frontend/index.html, frontend/css/, frontend/js/ and frontend/assets/ before doing
# anything else. So the path is constrained to something that is plainly a build
# output rather than trusted, which is the same reasoning §11.3 applies to
# destructive data scripts, expressed as a guard instead of a dry-run default
# (a build whose default is to build nothing is a worse failure).
case "$DIST" in
    "") die "--out must not be empty" ;;
    dist | */dist) ;;
    "$BUILD_DIR" | "$BUILD_DIR"/*) ;;
    *) die "refusing --out '$DIST': it is not a build output path. It must end in /dist or live under '${BUILD_DIR}/'. This script deletes the path before writing it, and '--out ${SRC}' would delete the source tree." ;;
esac

# Second guard, on content rather than on shape: a path may satisfy the rule above
# and still be a source tree. Built output never contains a placeholder — that is
# assertion 1 below — so an index.html with one is the source, not the artifact.
if [ -f "$DIST/index.html" ] && grep -q '{{' "$DIST/index.html"; then
    die "refusing to delete '$DIST': its index.html still contains {{...}} placeholders, which makes it a template source tree and not a build output"
fi

[ -f "$SRC/index.html" ] || die "no $SRC/index.html — there is nothing to build"

CONFIG="config/instances/${INSTANCE}.yaml"
[ -f "$CONFIG" ] || die "no $CONFIG: the instance name is the filename, and a typo would otherwise produce a bundle pointed at nothing (§7.4)"

# ---------------------------------------------------------------------------
# Branding (§7.3)
# ---------------------------------------------------------------------------

# Required keys fail rather than defaulting. §Phase 0 requires that a missing
# config value fails the build rather than silently falling back — and the failure
# a default would produce here is a deployed app displaying someone else's name.
branding_required() {
    local key="$1" value
    value="$(yq -r ".branding.${key} // \"\"" "$CONFIG")"
    if [ -z "$value" ]; then
        die "branding.${key} is missing or null in ${CONFIG}: every user-visible string resolves from branding, and there is no default to fall back to (§7.3)"
    fi
    branding_printable "branding.${key}" "$value"
    printf '%s' "$value"
}

# A newline, carriage return, or tab inside a branding value is rejected here rather
# than escaped. Every one of them breaks a destination this value reaches — a JS
# string literal is terminated by a newline, and the assertion messages below become
# unreadable — and none of them is a legitimate app name, tagline, or description.
# NOT a general printable-character test: the primary user thinks in English with
# Malayalam and Hindi (§1.2), so a Devanagari or Malayalam brand name must pass.
branding_printable() {
    local what="$1" value="$2"
    case "$value" in
        *$'\n'* | *$'\r'* | *$'\t'*)
            die "${what} in ${CONFIG} contains a newline or tab. It is substituted into HTML, a JavaScript string literal, and a JSON manifest; none of those survives it, and no name or tagline needs one."
            ;;
    esac
}

# Colours are VALIDATED rather than escaped, because they reach three syntaxes with
# three different dangerous characters — a CSS custom property value (where `;` and
# `}` end the declaration and the rule), an SVG attribute, and JSON — and a hex
# triple is safe in all of them. `theme_color: "red; } html { display: none"` is
# otherwise a stylesheet rewrite from a config file.
branding_colour() {
    local what="$1" value="$2"
    if ! printf '%s' "$value" | grep -qE '^#([0-9A-Fa-f]{3,4}|[0-9A-Fa-f]{6}|[0-9A-Fa-f]{8})$'; then
        die "${what} in ${CONFIG} is '${value}', which is not a hex colour (#rgb, #rgba, #rrggbb, #rrggbbaa). It is substituted into a CSS custom property, an SVG fill attribute, and the manifest, so it is restricted to a form that cannot terminate any of them (§4A.2, §7.3)."
    fi
}

APP_NAME="$(branding_required name)"
APP_SHORT_NAME="$(branding_required short_name)"
APP_DESCRIPTION="$(branding_required description)"
THEME_COLOR="$(branding_required theme_color)"
BACKGROUND_COLOR="$(branding_required background_color)"
ICON_SOURCE="$(branding_required icon_source)"

branding_colour "branding.theme_color" "$THEME_COLOR"
branding_colour "branding.background_color" "$BACKGROUND_COLOR"

# `tagline` is explicitly nullable in config, so it is the one branding key that
# may be absent. Rendering an empty element when it is null would leave a hole in
# the layout, so the description stands in — one placeholder, both keys honoured.
APP_TAGLINE="$(yq -r '.branding.tagline // ""' "$CONFIG")"
branding_printable "branding.tagline" "$APP_TAGLINE"
APP_SUBTITLE="${APP_TAGLINE:-$APP_DESCRIPTION}"

[ -f "$SRC/$ICON_SOURCE" ] || die "branding.icon_source points at $SRC/$ICON_SOURCE, which does not exist"

# ---------------------------------------------------------------------------
# Version (§0.6, G-035, G-036)
# ---------------------------------------------------------------------------

# --tags is deliberate: an untagged repository (a fresh fork) yields "unstamped"
# rather than failing, and the app then SAYS it is unstamped instead of reporting a
# plausible-looking wrong version.
TAG="$(git describe --tags --abbrev=0 2>/dev/null || echo unstamped)"
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
if [ "$COMMIT" = "unknown" ]; then
    # This one is fatal where an absent tag is not, because the commit is half the
    # cache token. A constant token is precisely G-035: every deploy would name its
    # cache the same thing, the worker would be byte-identical, and installed apps
    # would keep serving the first bundle they ever cached.
    #
    # In CI this usually means git refused the repository — either a shallow clone
    # (use fetch-depth: 0) or a "dubious ownership" refusal when the checkout is
    # owned by a different uid than the container user.
    die "cannot resolve the commit with 'git rev-parse HEAD'. Refusing to build a service worker whose cache token would be identical on every deploy (G-035, G-036)."
fi
CACHE_TOKEN="${TAG}-${COMMIT}"
BUILD_TIME="${CHINTAN_BUILD_TIME:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"

# A git refname may legally contain `$`, a backtick, `<` and `"`. The tag reaches a
# JavaScript string literal (js/build.ts) and a template literal (js/sw.ts), so those
# characters are escaped like any other value — but bun's minifier is free to re-emit
# `<` as `<`, and then the two G-035 assertions at the bottom of this file, which
# search dist/ for the literal token, would be searching for a form that no longer
# exists. Refused with a message rather than shipped against an assertion that may
# not hold: the assertions are the only thing standing between this build and an
# installed PWA serving old JavaScript forever.
case "$TAG" in
    *'`'* | *'$'* | *\\* | *'<'* | *'"'*)
        die "the resolved git tag '$TAG' contains a character that needs escaping in a JavaScript string ( \` \$ \\ < \" ). Rename or delete the tag: the cache-token assertions (G-035) search dist/ for the literal token and cannot survive a re-escaped form."
        ;;
esac

# ---------------------------------------------------------------------------
# API base URL
# ---------------------------------------------------------------------------
#
# Resolution order, and the reasoning for it:
#
#   1. --api-base-url / CHINTAN_API_BASE_URL. What CI passes today.
#   2. `api_base_url` in the instance config. §7.4 has no such key yet — §7.4 says
#      a value the spec calls "configured" with no key is a spec bug to flag rather
#      than hardcode — so this reads it if it appears, and the day it does, CI
#      needs no change. That is the durable answer: a committed config value is
#      discovered by the same instance matrix as everything else, needs no
#      credentials to read, and cannot decay the way a manually-set repository
#      variable can (G-041).
#
# There is no third step. A bundle built without an endpoint cannot perform the one
# check §Phase 0 acceptance asks of it, and it would deploy looking perfectly
# healthy — so this fails at build time, where the message can say what to do.
if [ -z "$API_BASE_URL" ]; then
    API_BASE_URL="$(yq -r '.api_base_url // ""' "$CONFIG")"
fi
if [ -z "$API_BASE_URL" ]; then
    die "no API base URL. Pass --api-base-url, set CHINTAN_API_BASE_URL, or add an api_base_url key to ${CONFIG}. The deployed endpoint is the ApiEndpoint output of the voicenotes-${INSTANCE} stack, which scripts/deploy.sh already reads. Without it the frontend cannot compare its version against the API's, which is the whole of §0.6."
fi
case "$API_BASE_URL" in
    https://*) ;;
    # A page served over HTTPS cannot call http://: the browser blocks it as mixed
    # content, and the failure surfaces as an opaque fetch error that looks like the
    # API being down (§10.6's remark about CORS mismatches wasting time applies
    # identically here).
    *) die "API base URL must be https:// — got '$API_BASE_URL'" ;;
esac

# ---------------------------------------------------------------------------
# Stage and substitute
# ---------------------------------------------------------------------------

info "building frontend $TAG ($COMMIT) for instance $INSTANCE"
dim "  service worker cache token: $CACHE_TOKEN (G-035 — sw.js only)"
dim "  api: $API_BASE_URL"
dim "  output: $DIST (deleted and rewritten)"

if [ "$DRY_RUN" = "1" ]; then
    log ""
    info "DRY RUN — nothing was changed."
    dim "  Would delete: $STAGE and $DIST"
    dim "  Would write:  $DIST/{index.html,sw.js,js/app.js,css/,assets/,manifest.webmanifest,.nojekyll}"
    dim "  Re-run without --dry-run to build."
    exit 0
fi

rm -rf "$STAGE" "$DIST"
mkdir -p "$STAGE" "$DIST/js"

# Substitution happens on a COPY, never in the worktree. Editing the sources in
# place would leave a tree whose files are indistinguishable from hand-written ones
# and whose next build substitutes into already-substituted text.
cp "$SRC/index.html" "$STAGE/"
cp -R "$SRC/css" "$SRC/js" "$SRC/assets" "$STAGE/"

# ---------------------------------------------------------------------------
# Escaping: a property of the DESTINATION, not of the value
# ---------------------------------------------------------------------------
#
# The manifest is generated with jq specifically so that "a name containing a quote
# cannot produce invalid JSON". The same values also land in `<title>`, an `<h1>`,
# the footer, the `content="…"` of the description meta, and the `fill="…"` of
# assets/icon.svg — and those were byte-level string replacement with no escaping at
# all. Two demonstrated consequences, both from an ordinary §7.3 config edit:
#
#   - `name: 'Chintan <script>alert(1)</script>'` built with rc=0 and put a live
#     script tag into `<title>`, the masthead, and the footer of a world-readable
#     page (G-057: the Pages site is public whatever the repository's visibility).
#   - `description: 'Say it "aloud"; find it later'` built with rc=0 and terminated
#     the meta attribute early, yielding three junk attributes.
#
# So each value is escaped for the syntax of the file it is going into, and the
# tables below are what make that structural rather than remembered:
#
#   HTML/SVG  entity-escape & < > " '   — text nodes and attribute values
#   TS        escape \ " ` $ and <      — build.ts holds a double-quoted literal,
#                                         sw.ts a template literal
#   CSS       nothing is escaped, because NOTHING BUT THE TWO VALIDATED COLOURS IS
#             IN THE TABLE. A CSS value cannot be safely escaped for a `;`/`}`
#             injection, so the restriction is on the input instead
#             (branding_colour above), and the table enforces it by omission.
#
# A value needing HTML escaping is normal. A value needing TS escaping is not, and
# both are handled anyway: the alternative is deciding which config edits are
# allowed, which is the opposite of what §7.3 is for.

html_escape() {
    local s="$1"
    # Ampersand FIRST: escaping it after `<` would re-escape the `&` in `&lt;`.
    s="${s//&/&amp;}"
    s="${s//</&lt;}"
    s="${s//>/&gt;}"
    s="${s//\"/&quot;}"
    s="${s//\'/&#39;}"
    printf '%s' "$s"
}

js_escape() {
    local s="$1"
    # Backslash first, for the same reason ampersand goes first above.
    s="${s//\\/\\\\}"
    s="${s//\"/\\\"}"
    # Backtick and $ because sw.ts interpolates the cache token into a template
    # literal; without these, a value containing `${` is an expression, not text.
    s="${s//\`/\\\`}"
    s="${s//$/\\$}"
    # Not strictly required while the bundle is loaded with <script src>, but a `<`
    # inside a JS string literal is one inline-script away from closing the tag.
    s="${s//</\\u003c}"
    printf '%s' "$s"
}

declare -A SUBS=(
    [APP_NAME]="$APP_NAME"
    [APP_SHORT_NAME]="$APP_SHORT_NAME"
    [APP_DESCRIPTION]="$APP_DESCRIPTION"
    [APP_SUBTITLE]="$APP_SUBTITLE"
    [THEME_COLOR]="$THEME_COLOR"
    [BACKGROUND_COLOR]="$BACKGROUND_COLOR"
    [VERSION]="$TAG"
    [COMMIT]="$COMMIT"
    [BUILD_TIME]="$BUILD_TIME"
    [INSTANCE]="$INSTANCE"
    [API_BASE_URL]="$API_BASE_URL"
)

# Escaped once per destination rather than per file, so the cost does not scale with
# the number of templates, and a reader can see the three tables side by side.
#
# CSS_KEYS is what makes "only validated colours reach a stylesheet" structural: the
# CSS pass iterates this list, not the full table, so no future key can arrive in a
# stylesheet by being added to SUBS.
declare -A SUBS_HTML=() SUBS_JS=()
for _key in "${!SUBS[@]}"; do
    SUBS_HTML["$_key"]="$(html_escape "${SUBS[$_key]}")"
    SUBS_JS["$_key"]="$(js_escape "${SUBS[$_key]}")"
done
unset _key
CSS_KEYS=(THEME_COLOR BACKGROUND_COLOR)

# CACHE_TOKEN is deliberately NOT in the tables above. It is applied to one file, by
# name, so that "substituted into sw.js and nothing else" is a property of the code
# rather than of everyone remembering it (G-035).
substitute() {
    local file="$1" mode="$2" key content
    content="$(<"$file")"
    case "$mode" in
        html)
            for key in "${!SUBS_HTML[@]}"; do
                content="${content//"{{${key}}}"/${SUBS_HTML[$key]}}"
            done
            ;;
        js)
            for key in "${!SUBS_JS[@]}"; do
                content="${content//"{{${key}}}"/${SUBS_JS[$key]}}"
            done
            ;;
        css)
            for key in "${CSS_KEYS[@]}"; do
                content="${content//"{{${key}}}"/${SUBS[$key]}}"
            done
            ;;
        *) die "internal error: unknown substitution mode '$mode' for $file" ;;
    esac
    printf '%s\n' "$content" >"$file"
}

while IFS= read -r -d '' file; do
    case "$file" in
        # SVG is XML: the same five entities, the same attribute-termination risk.
        *.html | *.svg) substitute "$file" html ;;
        *.ts) substitute "$file" js ;;
        *.css) substitute "$file" css ;;
        *) die "internal error: no substitution mode for $file — add one rather than letting it ship unsubstituted" ;;
    esac
done < <(find "$STAGE" -type f \( -name '*.html' -o -name '*.css' -o -name '*.ts' -o -name '*.svg' \) -print0)

sw_source="$STAGE/js/sw.ts"
[ -f "$sw_source" ] || die "no $sw_source: the service worker is where the cache token lives, and a build without one silently drops G-035's fix"
sw_content="$(<"$sw_source")"
# js-escaped like every other value that lands in TypeScript: the token sits inside a
# template literal in sw.ts (the cache name is namespaced by the worker's scope), so
# an unescaped `${` in it would be an expression rather than text. The tag charset is
# also restricted above, which is what lets the assertions below grep for the literal.
sw_token="$(js_escape "$CACHE_TOKEN")"
printf '%s\n' "${sw_content//'{{CACHE_TOKEN}}'/$sw_token}" >"$sw_source"

# ---------------------------------------------------------------------------
# Bundle
# ---------------------------------------------------------------------------
#
# bun, per docs/decisions/0004: one binary, no dependency tree of its own, which is
# what keeps the toolchain image small enough to pull on every CI job — and there is
# no framework to bundle, so a transpiler and a bundler is the whole requirement.
#
# No source maps. The sources are readable in the repository at the same commit (the
# repository is public — G-057), so a map buys nothing here and every byte on this
# path is a byte on the path with the 2-second budget (§4A.1, §11A.5).
info "bundling TypeScript"
bun build "$STAGE/js/app.ts" --outfile "$DIST/js/app.js" --target browser --format esm --minify
# iife, not esm: this is registered as a CLASSIC service worker, and a module worker
# needs {type: "module"} at registration plus browser support this app does not
# require. An esm bundle here would fail to install with a syntax error.
bun build "$sw_source" --outfile "$DIST/sw.js" --target browser --format iife --minify

cp "$STAGE/index.html" "$DIST/"
cp -R "$STAGE/css" "$STAGE/assets" "$DIST/"

# ---------------------------------------------------------------------------
# Generated artifacts
# ---------------------------------------------------------------------------

# The manifest is generated, not committed, because every string in it is a
# branding value (§7.3). jq rather than a heredoc so a name containing a quote
# cannot produce invalid JSON — and an invalid manifest does not fail loudly, it
# just silently stops the app being installable.
#
# start_url and scope are relative: a Pages project site serves under /{repo}/, and
# "/" would claim the user site's root (§10.6, G-007).
#
# Icons: the SVG only. §7.3 calls for 192/512/maskable PNGs generated from
# icon_source, which needs a rasteriser that containers/toolchain does not pin —
# Phase 1 adds it with installability, where the WebAPK actually depends on it.
jq -n \
    --arg name "$APP_NAME" \
    --arg short_name "$APP_SHORT_NAME" \
    --arg description "$APP_DESCRIPTION" \
    --arg theme_color "$THEME_COLOR" \
    --arg background_color "$BACKGROUND_COLOR" \
    --arg icon "$ICON_SOURCE" \
    '{
        name: $name,
        short_name: $short_name,
        description: $description,
        start_url: ".",
        scope: "./",
        display: "standalone",
        orientation: "any",
        theme_color: $theme_color,
        background_color: $background_color,
        icons: [{src: $icon, sizes: "any", type: "image/svg+xml", purpose: "any"}]
    }' >"$DIST/manifest.webmanifest"

# Pages built by Actions is served as-is, so Jekyll never runs — but the file costs
# nothing and the Pages source is a repository setting a human can change. Without
# it, a switch back to branch-based publishing silently drops every path beginning
# with an underscore (G-041: anything requiring someone to remember will not
# happen).
touch "$DIST/.nojekyll"

# ---------------------------------------------------------------------------
# Assertions — the failures above are all invisible in a build log
# ---------------------------------------------------------------------------

assert_fail() {
    die "frontend build assertion failed: $1"
}

# 1. No placeholder survives. An unsubstituted {{TOKEN}} in CSS silently produces an
#    invalid custom property, and the colour it defines falls back to nothing —
#    which on the paper/ink pair means invisible text.
#
#    The message names both possible causes, because the file it can name is the
#    wrong place to look for one of them: a template referencing a key that is not in
#    the substitution tables is a bug in that file, but a REPLACEMENT that re-inserts
#    the placeholder is a bug in the config (that was patsub_replacement, disabled at
#    the top of this script — the diagnostic stays because the class of failure is
#    "the value did it", not "the template did it").
if leftover="$(grep -rl '{{' "$DIST" 2>/dev/null)"; then
    assert_fail "unsubstituted placeholders remain in: ${leftover//$'\n'/, }. Either the template names a key that is not in the substitution tables, or a branding value in ${CONFIG} substituted the placeholder back into itself — check the config value before the template."
fi

# 2 and 3. The G-035 split, both directions: the token identifies the worker's
#    cache, and it appears nowhere else. If it leaked into index.html or app.js it
#    would be one careless template expression away from becoming the displayed
#    version, which §0.6 requires to be the bare tag.
grep -qF "$CACHE_TOKEN" "$DIST/sw.js" ||
    assert_fail "sw.js does not contain the cache token '$CACHE_TOKEN' — the worker would not rotate its cache on a deploy without a new tag (G-035)"

while IFS= read -r -d '' file; do
    # Compared by BASENAME, not by full path. `find` prints the path as it walks it,
    # so any difference in how DIST was spelled — a trailing slash, `./` — made the
    # exclusion for the worker stop matching the worker, and this assertion then
    # accused the build of the exact violation it had just implemented correctly.
    # DIST is normalised above; this is the second line of defence, because a false
    # positive here is indistinguishable from the real thing in a CI log.
    case "${file##*/}" in
        sw.js) continue ;;
    esac
    if grep -qF "$CACHE_TOKEN" "$file" 2>/dev/null; then
        assert_fail "the cache token '$CACHE_TOKEN' leaked into ${file}: it belongs in sw.js alone, because the displayed version is the bare tag (§0.6, G-035)"
    fi
done < <(find "$DIST" -type f -print0)

# 4. The displayed version is present and is the clean tag. Searched in its escaped
#    form, because that is what the page contains — assertions run against the
#    artifact, not against the intent.
grep -qF ">$(html_escape "$TAG")<" "$DIST/index.html" ||
    assert_fail "index.html does not render the version '$TAG' as text — the running version must be visible in the app (§0.6)"

# 5. Nothing that looks like a credential. The bundle is world-readable whatever the
#    repository's visibility (G-057), and the manifest is generated from a config
#    file containing SSM parameter paths (§9.4).
if secrets="$(grep -rlEi 'secret_ref|api[_-]?key|AKIA[0-9A-Z]{16}|aws_(secret|session)' "$DIST" 2>/dev/null)"; then
    assert_fail "possible credential material in: ${secrets//$'\n'/, } — the Pages site is world-readable regardless of repository visibility (G-057, §10.6)"
fi

# 6. The escaping actually happened. Asserted on the ARTIFACT rather than trusted to
#    the tables above, because the failure it catches — a branding value reaching a
#    world-readable page as markup — is invisible in a build log and rc=0 without it.
#
#    A no-op on a config whose values need no escaping, which is every normal config;
#    it fires only for the values that make it matter. scripts/test/frontend.sh
#    exercises it against a doctored config so that it is a check rather than a hope.
for _pair in "name:$APP_NAME" "description:$APP_DESCRIPTION" "subtitle:$APP_SUBTITLE"; do
    _key="${_pair%%:*}"
    _raw="${_pair#*:}"
    if [ "$(html_escape "$_raw")" = "$_raw" ]; then
        continue
    fi
    if grep -qF -- "$_raw" "$DIST/index.html"; then
        assert_fail "branding.${_key} reached ${DIST}/index.html unescaped: '${_raw}'. A config value must never be able to emit markup into the published page (§7.3, §9.7, G-057)."
    fi
done
unset _pair _key _raw

# ---------------------------------------------------------------------------

ok "frontend built to $DIST"
dim "  displayed version: $TAG   commit: $COMMIT   built: $BUILD_TIME"
du -b "$DIST/js/app.js" "$DIST/sw.js" | sed 's/^/  /' >&2

# Stated every build rather than tracked in a document, because a gap nobody is
# reminded of is a gap that ships (G-041).
warn "not yet shipped, both owned by Phase 1: self-hosted font subsets including Devanagari and Malayalam (§4A.3), and the 192/512/maskable PNG icons (§7.3). Until the fonts land, a code-switched transcript changes texture where the language switches."
