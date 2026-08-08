#!/usr/bin/env bash
#
# Build the Lambda deployment package.
#
# One arm64 binary named `bootstrap`, which is what the provided.al2023 runtime
# executes. Both the API function and the worker function in
# infrastructure/template.yaml point at this same object: WorkerCodeKey defaults
# to empty, which the template reads as "share LambdaCodeKey", and there is only
# one main package (backend/cmd/api). When the worker gets its own main package,
# build it here and set WorkerCodeKey.
#
# Usage:
#   scripts/build-lambda.sh [--output PATH]

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUTPUT="$REPO_ROOT/backend/lambda-function.zip"

while [ $# -gt 0 ]; do
    case "$1" in
        --output)
            OUTPUT="${2:?--output needs a value}"
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

cd "$REPO_ROOT/backend"

echo "Building bootstrap (linux/arm64)..."
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -tags lambda.norpc -trimpath -o bootstrap ./cmd/api

echo "Packaging $OUTPUT..."
# -X so a rebuild replaces the archive rather than adding a second member, and -j
# so the zip has `bootstrap` at its root, which is where the runtime looks.
rm -f "$OUTPUT"
zip -j -X -q "$OUTPUT" bootstrap

echo "Built $OUTPUT"
