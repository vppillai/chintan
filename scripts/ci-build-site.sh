#!/usr/bin/env bash
#
# Build the GitHub Pages tree: one Vite bundle per instance, under its own path.
#
# Each instance gets its own bundle rather than one bundle plus a runtime config
# file, because Vite bakes `base` in at build time — a shared bundle served from
# /chintan/dev-staging/ would request its assets from /chintan/dev/assets/. The
# per-instance configuration is passed as VITE_* variables and compiled in.
#
# The v1 workflow copied static files and wrote js/config.js, which the service
# worker then cached first-and-forever: recreating a stack changed the API
# endpoint and installed clients were pinned to the dead one. A content-hashed
# build has no such file.
#
# Usage:
#   scripts/ci-build-site.sh --out site
#
# Environment:
#   PAGES_BASE   URL path prefix for the site, e.g. /chintan   (required)
#
# Requires AWS credentials: the API endpoint, user pool and client ID are read
# from each instance's stack outputs, so the built bundle cannot disagree with
# what is deployed.

# shellcheck source-path=SCRIPTDIR source=lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"

OUT="site"

while [ $# -gt 0 ]; do
    case "$1" in
        --out)
            OUT="${2:?--out needs a value}"
            shift
            ;;
        -h | --help)
            usage_from_header "${BASH_SOURCE[0]}"
            exit 0
            ;;
        *) die "unknown flag '$1' (see --help)" ;;
    esac
    shift
done

[ -n "${PAGES_BASE:-}" ] || die "PAGES_BASE is required (e.g. /chintan)"
require_cmd bun jq aws

cd "$REPO_ROOT"
rm -rf "$OUT"
mkdir -p "$OUT"

built=()
default_path=""

while IFS= read -r entry; do
    stack="$(printf '%s' "$entry" | jq -r .stack)"
    site_path="$(printf '%s' "$entry" | jq -r .site_path)"
    environment="$(printf '%s' "$entry" | jq -r .environment)"
    instance="$(printf '%s' "$entry" | jq -r .instance)"
    # display_name is deliberately not exported. Nothing in the bundle reads it —
    # the app name is in the PWA manifest in vite.config.ts — and a VITE_ variable
    # nobody consumes advertises a contract that does not exist.

    if ! stack_exists "$stack"; then
        warn "skipping $site_path: stack $stack does not exist yet"
        continue
    fi

    outputs="$(aws_cli cloudformation describe-stacks --stack-name "$stack" \
        --query 'Stacks[0].Outputs' --output json)"
    value() { printf '%s' "$outputs" | jq -r --arg k "$1" '.[] | select(.OutputKey==$k) | .OutputValue'; }

    api_endpoint="$(value ApiEndpoint)"
    user_pool_id="$(value UserPoolId)"
    client_id="$(value UserPoolClientId)"
    cognito_domain="$(value UserPoolDomainName)"

    # The CloudFormation output is UserPoolClientId; the variable the bundle
    # reads is VITE_CLIENT_ID. They are deliberately not the same name and the
    # mismatch is the whole risk here: frontend/src/config/env.ts treats a
    # missing variable as an empty string and only warns in dev, so exporting the
    # wrong name produces a bundle that builds green, deploys green, and cannot
    # sign in. The authority is frontend/src/config/env.ts — read it before
    # changing anything below.

    # A missing output means the stack is half-deployed. Building against it would
    # ship a bundle that cannot sign in, and the failure would only appear on a
    # phone.
    for name in api_endpoint client_id user_pool_id cognito_domain; do
        [ -n "${!name}" ] && [ "${!name}" != "null" ] ||
            die "$stack has no usable ${name} output"
    done

    # The stack outputs the Cognito domain PREFIX ("chintan-dev-prod-1234"), not
    # a URL. The bundle builds its authorize and logout endpoints with
    # `new URL(`${domain}/oauth2/authorize`)`, which throws TypeError on a bare
    # prefix — and the sign-in path catches everything and reports "This browser
    # could not start a secure sign-in", blaming the browser for a config shape.
    # Exporting the prefix verbatim shipped a bundle where sign-in could never
    # work, and no test caught it: the frontend's fixtures use a full URL, and
    # check-vite-env.sh compares variable NAMES, not their shapes.
    #
    # Prefix domains resolve to a fixed host. A custom domain would already be a
    # hostname, so anything that looks like one is passed through.
    case "$cognito_domain" in
        https://*) ;;
        *.*) cognito_domain="https://${cognito_domain}" ;;
        *) cognito_domain="https://${cognito_domain}.auth.${AWS_REGION}.amazoncognito.com" ;;
    esac
    case "$cognito_domain" in
        https://*.*) ;;
        *) die "cognito domain did not resolve to a usable https origin: $cognito_domain" ;;
    esac

    # The running build's identity, for the footnote on the You screen and for
    # whatever a bug report has to name. The SHA is the only honest answer;
    # `--short` because it is read off a phone screen. A shallow CI checkout
    # still has HEAD, and outside a work tree the frontend falls back to
    # LOCAL_VERSION rather than rendering an empty line.
    #
    # Not `local`: this block is a loop body at top level, not a function.
    version="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"

    info "building $site_path from $stack"
    (
        cd frontend
        VITE_BASE="${PAGES_BASE}/${site_path}/" \
            VITE_API_URL="$api_endpoint" \
            VITE_USER_POOL_ID="$user_pool_id" \
            VITE_CLIENT_ID="$client_id" \
            VITE_COGNITO_DOMAIN="$cognito_domain" \
            VITE_INSTANCE="$instance" \
            VITE_VERSION="$version" \
            bun run build
    )

    mkdir -p "${OUT}/${site_path}"
    cp -a frontend/dist/. "${OUT}/${site_path}/"

    built+=("$site_path")
    [ -n "$default_path" ] || [ "$environment" != "prod" ] || default_path="$site_path"
done < <(scripts/list-instances.sh | jq -c '.[]')

[ "${#built[@]}" -gt 0 ] || die "no instance stacks were available to build"
[ -n "$default_path" ] || default_path="${built[0]}"

cat >"${OUT}/index.html" <<HTML
<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Chintan</title>
<meta http-equiv="refresh" content="0; url=./${default_path}/">
</head>
<body>
<p>Open <a href="./${default_path}/">Chintan</a>.</p>
</body>
</html>
HTML

# GitHub Pages serves a custom 404 only from the site's actual root — the one
# this job's own upload-pages-artifact step publishes, which is $OUT, not any
# instance's subdirectory. A 404.html placed at "${OUT}/${site_path}/404.html"
# (this script's previous approach: a copy of that instance's index.html) is
# never invoked by Pages for an unmatched path; it is just an ordinary file
# reachable only by its own exact URL. A hard refresh, a bookmark, or the PWA
# manifest's own "Record a thought" shortcut therefore still hit Pages' bare
# "File not found" page instead of any instance's app.
#
# One root 404.html serves every instance without knowing in advance which one
# a broken deep link belongs to: it is the "spa-github-pages" trick (rafgraph,
# MIT License, https://github.com/rafgraph/spa-github-pages), redirecting to
# the site root with the real path encoded as a query string. It reads the
# first two path segments — the repo name, then whichever instance's
# site_path the browser actually requested — off the URL itself and preserves
# them verbatim, so /chintan/dev-staging/notes/x round-trips to
# dev-staging's own index.html exactly as /chintan/dev/notes/x round-trips to
# dev's. frontend/index.html's inline script (see its head) decodes the
# redirect back into the real path before the SPA router ever reads it.
cat >"${OUT}/404.html" <<'HTML'
<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <title>Chintan</title>
    <script type="text/javascript">
      var pathSegmentsToKeep = 2;

      var l = window.location;
      l.replace(
        l.protocol +
          '//' +
          l.hostname +
          (l.port ? ':' + l.port : '') +
          l.pathname.split('/').slice(0, 1 + pathSegmentsToKeep).join('/') +
          '/?/' +
          l.pathname
            .slice(1)
            .split('/')
            .slice(pathSegmentsToKeep)
            .join('/')
            .replace(/&/g, '~and~') +
          (l.search ? '&' + l.search.slice(1).replace(/&/g, '~and~') : '') +
          l.hash,
      );
    </script>
  </head>
  <body></body>
</html>
HTML

ok "built: ${built[*]}"
