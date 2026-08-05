#!/usr/bin/env bash
#
# Corpus integrity check (§11.6). First exists in Phase 2; its check set grows
# with the corpus.
#
# "Slow corruption is the failure mode that goes unnoticed for months. This
# script is the detector." It runs on the schedules.verify cadence (§7.4), on
# demand, and in CI against seeded fixtures (§12).
#
# The full check set from §11.6, each becoming active in the phase that
# introduces what it checks. A check whose subject does not yet exist passes
# trivially and is NOT skipped — so the day the entity appears, the check is
# already running:
#
#   Phase 2  every alignment.json entry points to an S3 audio object that exists
#            no orphaned S3 audio and no dangling session records
#            L0 immutability proof — hashes match a stored manifest, FOR EVERY
#              RUN, not only the active one (I1, §6.1). A mismatch is a critical
#              failure, not a warning.
#            every capture's active_l0_run names a run that exists, and every L0
#              run directory is reachable from some capture
#            no DynamoDB keys lacking a tenant prefix (I11)
#   Phase 3  every item's source_blocks resolve to blocks that exist
#            no Item record with kind: 'noise' (§3A.4)
#            every item with text_key has the S3 object it points at, and no
#              text_key object is orphaned
#            every prompt item's body still matches its transcript span apart
#              from recorded STT corrections (§3A.3)
#   Phase 4  consent state present wherever corpus records exist (I14)
#
# Read-only: no --apply, and none needed (§11.3). It reports corruption; it never
# repairs it, because an automatic repair of a corpus this sensitive is a worse
# risk than the corruption.
#
# Usage: verify.sh [--json] [--tenant <id>] [--fixtures]
# shellcheck source-path=SCRIPTDIR source=lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"

AS_JSON=""
TENANT=""
FIXTURES=0
while [ $# -gt 0 ]; do
    case "$1" in
        --json) AS_JSON="--json" ;;
        --tenant)
            TENANT="${2:-}"
            shift
            ;;
        --fixtures) FIXTURES=1 ;;
        -h | --help)
            sed -n '2,35p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
            exit 0
            ;;
        *) die "unknown flag '$1' (see --help)" ;;
    esac
    shift
done
cd "$REPO_ROOT" || exit 1

# --fixtures is the CI mode: run against seeded fixtures with no AWS access
# (§11.6, §12). Against a live corpus, --tenant is required — no data operation
# runs untenanted (I11, §11.3).
if [ "$FIXTURES" = "0" ]; then
    require_tenant "$TENANT"
fi

# The subject is a CORPUS — captures, segments, and alignment to validate — not the
# storage seam. §11.6: "It first exists in Phase 2, when there are captures, segments,
# and alignment to validate." Keying the guard on the repository package was wrong: that
# package is the seam, and its existence says nothing about whether anything has been
# captured. The segmentation package is what Phase 2 adds and what produces the artifacts
# these checks inspect.
if ! compgen -G "backend/internal/segment/*.go" >/dev/null 2>&1; then
    no_subject_yet "corpus integrity" 2 "backend/internal/segment (captures, segments, alignment)"
    finish_check "corpus integrity (§11.6)" "$AS_JSON"
    exit 0
fi

# Closes the loophole a "no subject yet" guard otherwise leaves open: once the subject
# exists the check must be implemented, and a missing implementation is a failure rather
# than a skip. Without this, the guard above could be satisfied while the checks it gates
# were never written — a check reporting success having inspected nothing, which is the
# §0.5A failure mode.
if ! (cd backend && go run ./cmd/chintanctl verify --help >/dev/null 2>&1); then
    violation "backend/internal/segment exists, so a corpus exists, but chintanctl has no verify subcommand — the §11.6 checks are unimplemented and this check would otherwise pass having inspected nothing"
    finish_check "corpus integrity (§11.6)" "$AS_JSON"
    exit 1
fi

# The checks themselves live in the admin binary, not here: they are real logic —
# hash comparison, reachability over S3 prefixes, prompt-span diffing — and that
# belongs in tested application code rather than bash (§11.2). This script owns
# argument parsing and output formatting only.
args=(verify)
[ "$FIXTURES" = "1" ] && args+=(--fixtures)
[ -n "$TENANT" ] && args+=(--tenant "$TENANT")
[ -n "$AS_JSON" ] && args+=(--json)

if ! (cd backend && go run ./cmd/chintanctl "${args[@]}"); then
    violation "corpus integrity violations found — see the enumerated output above"
fi

finish_check "corpus integrity (§11.6)" "$AS_JSON"
