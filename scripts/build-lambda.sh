#!/usr/bin/env bash
#
# Build the Lambda deployment package.
#
# Each package builds to one arm64 binary named `bootstrap`, which is what the
# provided.al2023 runtime executes, zipped at the archive root.
#
# Two binaries, not one. cmd/api serves HTTP; cmd/worker drains the capture
# queue. They are separate main packages and must be separate artifacts: the
# worker running the API handler starts cleanly and then fails every SQS event,
# which looks like a broken pipeline rather than a broken deploy.
#
# Usage:
#   scripts/build-lambda.sh [--output PATH] [--worker-output PATH]

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUTPUT="$REPO_ROOT/backend/lambda-function.zip"
WORKER_OUTPUT=""

while [ $# -gt 0 ]; do
    case "$1" in
        --output)
            OUTPUT="${2:?--output needs a value}"
            shift
            ;;
        --worker-output)
            WORKER_OUTPUT="${2:?--worker-output needs a value}"
            shift
            ;;
        -h | --help)
            awk 'NR == 1 { next } /^#/ { sub(/^# ?/, ""); print; next } { exit }' "${BASH_SOURCE[0]}"
            exit 0
            ;;
        *)
            echo "unknown flag '$1' (see --help)" >&2
            exit 1
            ;;
    esac
    shift
done

# Go is not on PATH in the development container; adding it here rather than in
# every caller means the build works the same from a shell, from bootstrap.sh and
# from CI.
if ! command -v go >/dev/null 2>&1 && [ -x /usr/local/go/bin/go ]; then
    PATH="/usr/local/go/bin:$PATH"
    export PATH
fi
command -v go >/dev/null 2>&1 || {
    echo "go is not installed or not on PATH" >&2
    exit 1
}

# Resolve outputs against the caller's directory BEFORE cd'ing. A relative
# --output would otherwise land in backend/ while the caller looks for it where
# it was standing.
abspath() {
    case "$1" in
        /*) printf '%s\n' "$1" ;;
        *) printf '%s/%s\n' "$PWD" "$1" ;;
    esac
}
OUTPUT="$(abspath "$OUTPUT")"
[ -n "$WORKER_OUTPUT" ] && WORKER_OUTPUT="$(abspath "$WORKER_OUTPUT")"

cd "$REPO_ROOT/backend"

# -X so a rebuild replaces the archive rather than adding a second member, and -j
# so the zip has `bootstrap` at its root, which is where the runtime looks.
package() {
    local pkg="$1" out="$2"
    echo "Building bootstrap from $pkg (linux/arm64)..."
    GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -tags lambda.norpc -trimpath -o bootstrap "$pkg"
    echo "Packaging $out..."
    rm -f "$out"
    zip -j -X -q "$out" bootstrap
    rm -f bootstrap
    echo "Built $out"
}

package ./cmd/api "$OUTPUT"

if [ -n "$WORKER_OUTPUT" ]; then
    package ./cmd/worker "$WORKER_OUTPUT"
fi
