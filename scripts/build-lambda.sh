#!/usr/bin/env bash
#
# Build the reproducible arm64 Lambda artifacts.
#
# arm64 is mandatory (§4, §10.1): ~20% cheaper per GB-second, and Go's cold start
# and memory footprint let a small allocation suffice where Node would need
# several times more.
#
# Two functions, one per execution profile (§4) — never one per endpoint, which
# multiplies cold starts and duplicated init for no benefit:
#   api     sync, 256MB, 10s, behind API Gateway, handles every HTTP route
#   worker  async, S3-event and schedule invoked, higher memory and timeout, not
#           externally reachable
#
# Version is resolved HERE, at build time, from git describe — never from a
# checked-in file, which drifts (G-037). And it must be resolved before the
# deploy: CI resolves git describe during the build, so a tag pushed afterwards
# does not retroactively change the artifact (G-036). Tag before deploying.
#
# Reproducible: -trimpath strips local paths, CGO is off, and the version values
# are the only build-time inputs. The same commit built twice produces the same
# bytes, which is what makes the artifact a function of the source rather than of
# the machine — and is the reason this runs inside the toolchain container.
# Usage: build-lambda.sh [--help]
# shellcheck source-path=SCRIPTDIR source=lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"

case "${1:-}" in
    -h | --help)
        sed -n '2,25p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
        exit 0
        ;;
    '') ;;
    *) die "unknown flag '$1' (see --help)" ;;
esac

cd "$REPO_ROOT" || exit 1

OUT="${CHINTAN_BUILD_DIR:-build}"
mkdir -p "$OUT"

# --tags is deliberate: an untagged repo (a fresh fork) yields "unstamped" rather
# than failing the build, and version.Stamped() then surfaces that in /v1/health
# instead of reporting a plausible-looking wrong version.
TAG="$(git describe --tags --abbrev=0 2>/dev/null || echo unstamped)"
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
# The build timestamp is passed in rather than read from a clock inside the
# program, so nothing at runtime depends on the build host's time.
BUILD_TIME="${CHINTAN_BUILD_TIME:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"

info "building $TAG ($COMMIT) for linux/arm64"
dim "  cache token: ${TAG}-${COMMIT}  (this is what sw.js must use — G-035)"

V=github.com/vppillai/chintan/backend/internal/version
LDFLAGS="-s -w -X ${V}.Tag=${TAG} -X ${V}.Commit=${COMMIT} -X ${V}.BuildTime=${BUILD_TIME}"

for fn in api worker; do
    if [ ! -d "backend/cmd/$fn" ]; then
        dim "  backend/cmd/$fn does not exist yet — skipping"
        continue
    fi
    info "building $fn"
    (
        cd backend || exit 1
        # provided.al2023 expects the handler binary to be named `bootstrap`.
        GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
            go build -trimpath -tags lambda.norpc -ldflags "$LDFLAGS" \
            -o "../$OUT/$fn/bootstrap" "./cmd/$fn"
    )
    # -X strips extended attributes and -D zeroes timestamps, so the zip is
    # byte-identical for identical input. Without -D the archive embeds the file
    # mtime and two builds of one commit differ.
    (cd "$OUT/$fn" && zip -q -X -D "../$fn.zip" bootstrap)
    size="$(du -h "$OUT/$fn.zip" | cut -f1)"
    ok "$fn.zip ($size)"
done

# Artifact size matters more than it looks: Phase 2 links the ONNX Runtime C
# library into the worker, and the Lambda limits are what the §Phase 2 gate
# measures against (§4). Recording the size now gives that gate a baseline.
info "artifact sizes recorded for the Phase 2 ONNX sizing gate (§Phase 2)"
du -b "$OUT"/*.zip 2>/dev/null | sed 's/^/  /' >&2 || true
