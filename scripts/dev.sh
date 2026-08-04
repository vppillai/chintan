#!/usr/bin/env bash
#
# Run a command inside the Chintan toolchain container.
#
# This is the seam that makes "it passes on my machine" a meaningful claim
# (§0.5A): the image is content-addressed on the contents of
# containers/toolchain/, so a developer and a CI job that agree on the tag are
# provably running the same tools. `make` invokes this automatically, so the
# normal way to use it is not to — run `make check`, not `./scripts/dev.sh`.
#
# Usage:
#   scripts/dev.sh [command...]      # default: an interactive bash shell
#   scripts/dev.sh make check
#
# Environment:
#   CHINTAN_IMAGE      override the image reference entirely (CI sets this to
#                      the GHCR digest so no local build happens)
#   CHINTAN_REGISTRY   registry to try pulling from before building locally
#   CHINTAN_NO_PULL=1  skip the pull attempt and build locally
#   DOCKER             container CLI to use (default: docker)
#
# This is a developer convenience wrapper, not an operational script: it mutates
# no backend state, so the §11.3 --dry-run/--apply convention does not apply.
# It does forward AWS credentials when they are present in the environment,
# because the deploy job runs `deploy.sh` inside this same image.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

DOCKER="${DOCKER:-docker}"
IMAGE_NAME="chintan-toolchain"
TOOLCHAIN_DIR="containers/toolchain"

if ! command -v "$DOCKER" >/dev/null 2>&1; then
    cat >&2 <<-EOF
	error: '$DOCKER' not found.

	Every build, test, and deploy for this project runs inside the toolchain
	container so that dev, CI, and deploy share one environment (§0.5A). Install
	Docker (or set DOCKER=podman), or set CHINTAN_IN_CONTAINER=1 to run make
	against host tools — which is unsupported and will not match CI.
	EOF
    exit 1
fi

# The image tag is the hash of everything that defines the image. Any change to
# the Dockerfile, the pinned versions, or the installer changes the tag, so a
# stale image can never be silently reused after a toolchain bump.
#
# Computed by scripts/toolchain-tag.sh, which is the single implementation. A
# second copy here could drift from the one CI uses, and the drift would present
# as CI and dev appearing to agree on a tag while running different images.
TAG="$("${REPO_ROOT}/scripts/toolchain-tag.sh")"
LOCAL_REF="${IMAGE_NAME}:${TAG}"
IMAGE="${CHINTAN_IMAGE:-$LOCAL_REF}"

image_present() {
    "$DOCKER" image inspect "$1" >/dev/null 2>&1
}

ensure_image() {
    if image_present "$IMAGE"; then
        return 0
    fi

    # CI passes CHINTAN_IMAGE as a digest-pinned GHCR reference and runs jobs in
    # it directly, so reaching this path there means the reference is wrong —
    # building a look-alike locally would hide that. Fail instead.
    if [ -n "${CHINTAN_IMAGE:-}" ]; then
        echo "error: CHINTAN_IMAGE=$CHINTAN_IMAGE is not present locally and will not be built." >&2
        echo "       Pull it first, or unset CHINTAN_IMAGE to build from $TOOLCHAIN_DIR." >&2
        exit 1
    fi

    if [ -n "${CHINTAN_REGISTRY:-}" ] && [ "${CHINTAN_NO_PULL:-0}" != "1" ]; then
        local remote="${CHINTAN_REGISTRY}/${IMAGE_NAME}:${TAG}"
        echo "==> pulling $remote" >&2
        if "$DOCKER" pull --quiet "$remote" >/dev/null 2>&1; then
            "$DOCKER" tag "$remote" "$LOCAL_REF"
            return 0
        fi
        echo "==> not in registry; building locally" >&2
    fi

    echo "==> building $LOCAL_REF (toolchain changed or first run)" >&2
    build_image "$LOCAL_REF"
}

build_image() {
    local ref="$1"
    local -a build_args=()

    # Every KEY=VALUE in versions.env becomes a --build-arg, so the Dockerfile
    # never carries a version literal and bumping a tool is a one-line diff.
    while IFS='=' read -r key value; do
        case "$key" in
            '' | \#*) continue ;;
        esac
        build_args+=(--build-arg "${key}=${value}")
    done <"${TOOLCHAIN_DIR}/versions.env"

    "$DOCKER" build \
        --file "${TOOLCHAIN_DIR}/Dockerfile" \
        --tag "$ref" \
        "${build_args[@]}" \
        "$TOOLCHAIN_DIR"
}

# `toolchain-tag` and `toolchain-build` are handled here rather than in the
# Makefile because they are the two things that cannot run inside the image.
case "${1:-}" in
    --tag)
        echo "$TAG"
        exit 0
        ;;
    --build)
        build_image "$LOCAL_REF"
        echo "$LOCAL_REF"
        exit 0
        ;;
esac

ensure_image

# Caches persist in a named volume so a container run is not a cold Go build
# every time. Keyed on the toolchain tag: a Go version bump gets a fresh cache
# rather than reusing artifacts built by a different compiler.
CACHE_VOLUME="chintan-cache-${TAG}"

DOCKER_ARGS=(
    --rm
    --workdir /workspace
    --volume "${REPO_ROOT}:/workspace"
    --volume "${CACHE_VOLUME}:/go"
    # Run as the invoking user so files written into the worktree are not
    # root-owned. The image makes /go world-writable for exactly this reason.
    --user "$(id -u):$(id -g)"
    --env HOME=/tmp
    --env CHINTAN_IN_CONTAINER=1
)

# Interactive only when there is a TTY, so CI logs stay clean and a piped
# invocation does not fail on "the input device is not a TTY".
if [ -t 0 ] && [ -t 1 ]; then
    DOCKER_ARGS+=(--interactive --tty)
fi

# Forward the variables the deploy path needs, and nothing else. Provider API
# keys are deliberately absent: they live in SSM and are read by the Lambda
# execution role, never by the build environment (§9.4).
for var in \
    AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN \
    AWS_REGION AWS_DEFAULT_REGION AWS_PROFILE AWS_ROLE_ARN \
    AWS_WEB_IDENTITY_TOKEN_FILE AWS_CONTAINER_CREDENTIALS_FULL_URI \
    AWS_CONTAINER_AUTHORIZATION_TOKEN \
    CHINTAN_INSTANCE CHINTAN_TENANT \
    CI GITHUB_ACTIONS GITHUB_SHA GITHUB_REF GITHUB_REF_NAME GITHUB_RUN_ID \
    GITHUB_STEP_SUMMARY GITHUB_OUTPUT; do
    if [ -n "${!var:-}" ]; then
        DOCKER_ARGS+=(--env "${var}=${!var}")
    fi
done

# A shell when given nothing, so `scripts/dev.sh` is a usable way in.
if [ "$#" -eq 0 ]; then
    set -- bash
fi

exec "$DOCKER" run "${DOCKER_ARGS[@]}" "$IMAGE" "$@"
