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
    for name in api_endpoint client_id user_pool_id; do
        [ -n "${!name}" ] && [ "${!name}" != "null" ] ||
            die "$stack has no usable ${name} output"
    done

    info "building $site_path from $stack"
    (
        cd frontend
        VITE_BASE="${PAGES_BASE}/${site_path}/" \
            VITE_API_URL="$api_endpoint" \
            VITE_USER_POOL_ID="$user_pool_id" \
            VITE_CLIENT_ID="$client_id" \
            VITE_COGNITO_DOMAIN="$cognito_domain" \
            VITE_INSTANCE="$instance" \
            bun run build
    )

    mkdir -p "${OUT}/${site_path}"
    cp -a frontend/dist/. "${OUT}/${site_path}/"

    # Client-side routing on Pages: an unknown path returns 404.html, so serving
    # the app from it makes a deep link work on a cold load.
    cp "${OUT}/${site_path}/index.html" "${OUT}/${site_path}/404.html"

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

ok "built: ${built[*]}"
