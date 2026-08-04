#!/usr/bin/env bash
#
# Print the content-addressed toolchain image tag.
#
# This is the single implementation of the tag algorithm, and it needs to be: the
# guarantee in ADR 0005 is that a developer and a CI job which agree on the tag are
# provably running the same tools. Two copies of the hash computation — one in
# scripts/dev.sh, one in the workflow — could drift, and the drift would present as
# CI and dev *appearing* to agree while running different images. That is a worse
# failure than no guarantee at all, because it would be believed (§0.5A).
#
# Deliberately has no dependency on Docker, unlike scripts/dev.sh, so it can run
# inside the toolchain container to verify the container's own identity.
#
# Usage: toolchain-tag.sh [--help]

set -euo pipefail

case "${1:-}" in
    -h | --help)
        sed -n '2,15p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
        exit 0
        ;;
    '') ;;
    *)
        echo "toolchain-tag.sh: unknown flag '$1' (see --help)" >&2
        exit 2
        ;;
esac

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

TOOLCHAIN_DIR="containers/toolchain"

if [ ! -d "$TOOLCHAIN_DIR" ]; then
    echo "toolchain-tag.sh: $TOOLCHAIN_DIR is missing" >&2
    exit 1
fi

# Hash file contents *and* paths, sorted, so the digest is stable across machines
# and changes if a file is renamed or removed as well as edited. LC_ALL=C fixes the
# sort order independent of the caller's locale — without it, a machine with a
# different collation would compute a different tag from identical content, which
# would look exactly like a toolchain change.
find "$TOOLCHAIN_DIR" -type f -print0 \
    | LC_ALL=C sort -z \
    | xargs -0 sha256sum \
    | sha256sum \
    | cut -c1-16
