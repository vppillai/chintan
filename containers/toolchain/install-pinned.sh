#!/usr/bin/env bash
#
# Install one pinned tool into the toolchain image, verifying its sha256 before
# the bytes become executable.
#
# Used only during `docker build` (see Dockerfile). Not part of the runtime
# surface and not a §11.4 operational script, so it has no --dry-run: it runs
# in a throwaway build layer and mutates nothing outside the image.
#
# Usage: install-pinned.sh <tool> <version> <sha256-arm64> <sha256-amd64>
#
# Each tool publishes its artifacts under a different naming scheme, which is
# the whole reason this is a script rather than a run of identical RUN lines.

set -euo pipefail

TOOL="${1:?tool name required}"
VERSION="${2:?version required}"
SHA_ARM64="${3:?arm64 sha256 required}"
SHA_AMD64="${4:?amd64 sha256 required}"

# dpkg's architecture name is the one thing reliably available in a Debian base
# image; uname -m would give aarch64/x86_64 and every upstream spells those
# differently anyway, so normalise once here.
case "$(dpkg --print-architecture)" in
    arm64)
        GOARCH=arm64
        SHA="$SHA_ARM64"
        ;;
    amd64)
        GOARCH=amd64
        SHA="$SHA_AMD64"
        ;;
    *)
        echo "install-pinned: unsupported architecture $(dpkg --print-architecture)" >&2
        exit 1
        ;;
esac

if [ "$SHA" = "UNSET" ]; then
    echo "install-pinned: $TOOL has no sha256 pinned for $GOARCH." >&2
    echo "  A version bumped without its checksum must fail the build rather than" >&2
    echo "  install an unverified binary. Run 'make toolchain-checksums' and commit" >&2
    echo "  the version and the hashes together." >&2
    exit 1
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
cd "$WORK"

# Resolve the upstream URL and the local artifact name per tool.
case "$TOOL" in
    yq)
        URL="https://github.com/mikefarah/yq/releases/download/v${VERSION}/yq_linux_${GOARCH}"
        ARTIFACT=yq
        ;;
    shfmt)
        URL="https://github.com/mvdan/sh/releases/download/v${VERSION}/shfmt_v${VERSION}_linux_${GOARCH}"
        ARTIFACT=shfmt
        ;;
    shellcheck)
        # Upstream ships uname-style arch names here, not dpkg ones.
        case "$GOARCH" in
            arm64) SC_ARCH=aarch64 ;;
            amd64) SC_ARCH=x86_64 ;;
        esac
        URL="https://github.com/koalaman/shellcheck/releases/download/v${VERSION}/shellcheck-v${VERSION}.linux.${SC_ARCH}.tar.xz"
        ARTIFACT=shellcheck.tar.xz
        ;;
    bun)
        # bun spells amd64 as "x64" and arm64 as "aarch64".
        case "$GOARCH" in
            arm64) BUN_ARCH=aarch64 ;;
            amd64) BUN_ARCH=x64 ;;
        esac
        URL="https://github.com/oven-sh/bun/releases/download/bun-v${VERSION}/bun-linux-${BUN_ARCH}.zip"
        ARTIFACT=bun.zip
        ;;
    awscli)
        case "$GOARCH" in
            arm64) AWS_ARCH=aarch64 ;;
            amd64) AWS_ARCH=x86_64 ;;
        esac
        URL="https://awscli.amazonaws.com/awscli-exe-linux-${AWS_ARCH}-${VERSION}.zip"
        ARTIFACT=awscli.zip
        ;;
    *)
        echo "install-pinned: unknown tool '$TOOL'" >&2
        exit 1
        ;;
esac

echo "install-pinned: fetching $TOOL $VERSION ($GOARCH)"
curl --fail --silent --show-error --location --retry 3 --retry-delay 2 \
    --output "$ARTIFACT" "$URL"

echo "${SHA}  ${ARTIFACT}" | sha256sum --check --strict --quiet -

# Unpack and install. Every tool lands in /usr/local/bin so PATH needs no
# per-tool entry.
case "$TOOL" in
    yq | shfmt)
        install -m 0755 "$ARTIFACT" "/usr/local/bin/$TOOL"
        ;;
    shellcheck)
        tar -xJf "$ARTIFACT"
        install -m 0755 "shellcheck-v${VERSION}/shellcheck" /usr/local/bin/shellcheck
        ;;
    bun)
        unzip -q "$ARTIFACT"
        # The zip's directory name tracks the arch, so glob rather than guess.
        install -m 0755 bun-linux-*/bun /usr/local/bin/bun
        ;;
    awscli)
        unzip -q "$ARTIFACT"
        ./aws/install --update
        ;;
esac

echo "install-pinned: installed $TOOL $VERSION"
