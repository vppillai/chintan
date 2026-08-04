#!/usr/bin/env bash
#
# Recompute the pinned sha256 for every tool in versions.env, for both
# architectures, and rewrite the file in place.
#
# Run after bumping a version, and commit the version and the hashes together.
# A pin whose checksum was not refreshed fails the image build (see
# install-pinned.sh) rather than installing an unverified binary — so the worst
# outcome of forgetting is a red build, not a silent supply-chain change.
#
# This downloads every artifact for every architecture, so it is slow and
# bandwidth-heavy. That is the point: the hashes are only meaningful if they
# came from the bytes upstream is actually serving.
#
# Usage: containers/toolchain/refresh-checksums.sh [--check]
#   --check  verify the recorded hashes match upstream and exit non-zero if
#            not, without rewriting the file. Suitable for a scheduled job that
#            detects an upstream artifact being re-cut under the same tag.

set -euo pipefail

CHECK_ONLY=0
[ "${1:-}" = "--check" ] && CHECK_ONLY=1

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
VERSIONS="${HERE}/versions.env"

# shellcheck source=/dev/null
source "$VERSIONS"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# Resolve the download URL for a tool and architecture. Kept in step with the
# case statement in install-pinned.sh — the two must agree, so if a naming
# scheme changes upstream, both change together.
url_for() {
    local tool="$1" arch="$2"
    case "$tool" in
        yq) echo "https://github.com/mikefarah/yq/releases/download/v${YQ_VERSION}/yq_linux_${arch}" ;;
        shfmt) echo "https://github.com/mvdan/sh/releases/download/v${SHFMT_VERSION}/shfmt_v${SHFMT_VERSION}_linux_${arch}" ;;
        shellcheck)
            local sc_arch
            [ "$arch" = arm64 ] && sc_arch=aarch64 || sc_arch=x86_64
            echo "https://github.com/koalaman/shellcheck/releases/download/v${SHELLCHECK_VERSION}/shellcheck-v${SHELLCHECK_VERSION}.linux.${sc_arch}.tar.xz"
            ;;
        bun)
            local bun_arch
            [ "$arch" = arm64 ] && bun_arch=aarch64 || bun_arch=x64
            echo "https://github.com/oven-sh/bun/releases/download/bun-v${BUN_VERSION}/bun-linux-${bun_arch}.zip"
            ;;
        awscli)
            local aws_arch
            [ "$arch" = arm64 ] && aws_arch=aarch64 || aws_arch=x86_64
            echo "https://awscli.amazonaws.com/awscli-exe-linux-${aws_arch}-${AWSCLI_VERSION}.zip"
            ;;
        *)
            echo "refresh-checksums: unknown tool '$tool'" >&2
            return 1
            ;;
    esac
}

# versions.env key prefix per tool, so the sed target is derivable.
key_prefix() {
    case "$1" in
        yq) echo YQ ;;
        shfmt) echo SHFMT ;;
        shellcheck) echo SHELLCHECK ;;
        bun) echo BUN ;;
        awscli) echo AWSCLI ;;
    esac
}

FAILED=0

for tool in awscli yq shellcheck shfmt bun; do
    prefix="$(key_prefix "$tool")"
    for arch in arm64 amd64; do
        url="$(url_for "$tool" "$arch")"
        out="${WORK}/${tool}-${arch}"

        printf '%-12s %-6s ' "$tool" "$arch"
        if ! curl --fail --silent --show-error --location --retry 3 --output "$out" "$url"; then
            echo "FETCH FAILED  $url"
            FAILED=1
            continue
        fi

        sum="$(sha256sum "$out" | cut -d' ' -f1)"
        key="${prefix}_SHA256_${arch}"
        recorded="$(grep "^${key}=" "$VERSIONS" | cut -d= -f2)"

        if [ "$CHECK_ONLY" = 1 ]; then
            if [ "$sum" = "$recorded" ]; then
                echo "ok"
            else
                echo "MISMATCH recorded=$recorded upstream=$sum"
                FAILED=1
            fi
        else
            # Delimit with | so a hash never collides with the separator.
            sed -i "s|^${key}=.*|${key}=${sum}|" "$VERSIONS"
            if [ "$sum" = "$recorded" ]; then
                echo "unchanged"
            else
                echo "updated -> $sum"
            fi
        fi

        rm -f "$out"
    done
done

if [ "$FAILED" != 0 ]; then
    echo ""
    if [ "$CHECK_ONLY" = 1 ]; then
        echo "One or more artifacts no longer match their recorded hash." >&2
        echo "An upstream tag re-cut under the same version is a supply-chain event:" >&2
        echo "investigate before refreshing the pins." >&2
    fi
    exit 1
fi

if [ "$CHECK_ONLY" = 0 ]; then
    echo ""
    echo "versions.env updated. Commit the version bump and the hashes together."
fi
