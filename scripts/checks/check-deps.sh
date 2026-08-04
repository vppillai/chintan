#!/usr/bin/env bash
#
# Dependency scan, failing on high severity (§0.5A, §9).
#
# Two ecosystems, both scanned:
#   Go        `govulncheck` against the Go vulnerability database. Reports only
#             vulnerabilities actually reachable from this code, so the signal is
#             usable rather than a list of CVEs in unused code paths.
#   frontend  `bun audit` over the lockfile, once there is one.
#
# This is also a supply-chain control on the agent's own inputs: G-050 notes that
# an agent holding cloud credentials is a more valuable target than the product
# pipeline, and a malicious dependency in the build is one route to it. §0.5A
# limits the blast radius further by keeping AWS credentials out of every job
# below the deploy job.
# shellcheck source-path=SCRIPTDIR source=../lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/common.sh"
AS_JSON="${1:-}"
cd "$REPO_ROOT" || exit 1

info "Go module vulnerability scan"
# Pinned so the scanner itself is reproducible; an unpinned `@latest` makes the
# build's result depend on the day it ran, which is the drift the container image
# exists to remove.
#
# Run from inside the module: govulncheck resolves packages through the module
# graph, so invoking it from the repository root fails with "no go.mod file" —
# which is a tool error, not a clean scan, and must not be reported as either a
# pass or a vulnerability.
#
# Exit codes are distinguished deliberately. govulncheck exits 3 when it finds
# vulnerabilities and non-zero-non-3 when it could not run. Collapsing those into
# one message is how a broken scanner gets read as a security finding — or worse,
# how a scanner that never ran gets read as a clean bill of health.
set +e
(cd backend && GOFLAGS=-mod=mod go run golang.org/x/vuln/cmd/govulncheck@v1.1.4 ./...)
scan_status=$?
set -e
case "$scan_status" in
    0) ok "no reachable vulnerabilities in Go dependencies" ;;
    3) violation "govulncheck found reachable vulnerabilities in Go dependencies (§0.5A: fail on high severity)" ;;
    *) violation "govulncheck could not complete (exit $scan_status) — a scan that did not run must not be reported as clean" ;;
esac

if [ -f frontend/bun.lock ] || [ -f frontend/package.json ]; then
    info "frontend dependency audit"
    set +e
    (cd frontend && bun audit --audit-level=high)
    audit_status=$?
    set -e
    if [ "$audit_status" -ne 0 ]; then
        violation "bun audit reported high-severity vulnerabilities in frontend dependencies (exit $audit_status)"
    fi
else
    dim "  no frontend lockfile yet — active from Phase 1"
fi

finish_check "dependency scan (fail on high severity)" "$AS_JSON"
