#!/usr/bin/env bash
#
# Per-tenant cost report from Usage records (§11.4, Phase 0).
#
# "Per-tenant cost report from Usage records, by month and provider; reconciles metered
# totals against actual AWS and provider bills."
#
# This is the report §10.7 exists to be checked against: a modelled ~$0.35–1.05/month, and
# "if any phase's design pushes recurring cost above $5/month, stop and flag it before
# implementing." It is also the metered half of §Phase 0's acceptance criterion — summed
# cost_micros within 5% of the provider's reported cost. The billed half is supplied from
# the invoice with --actual/--actual-total, because no billing API is reachable from this
# tooling and Cost Explorer charges $0.01 a request against a $1 budget.
#
# READ-ONLY: no --apply, and none needed (§11.3). It writes exactly one thing — the audit
# record for its own invocation, which §11.3 requires of every data script (I13). It spends
# no provider money and needs no cost estimate: the read is a bounded prefix of one DynamoDB
# partition per month (§6.3), which is inside the ~$0.00 DynamoDB row of §10.7.
#
# --tenant is REQUIRED: no data operation runs untenanted (I11).
#
# ALL THE LOGIC IS IN chintanctl (§11.2). This script owns argument parsing, region
# defaulting and exit-code reporting; the aggregation, the integer-micro money arithmetic
# and the reconciliation are tested Go, so a future admin UI calls the same module rather
# than reimplementing it. NO BUSINESS LOGIC IN BASH.
#
# Exit codes: 0 report produced, 3 a reconciliation line is outside tolerance, 4 a month is
# over the §10.7 ceiling, 1 failure, 2 invocation error.
#
# Usage:
#   scripts/usage.sh --tenant <id> --instance <name> [--month yyyy-mm]
#   scripts/usage.sh --tenant <id> --instance prod --since 2026-06 --until 2026-08 --json
#   scripts/usage.sh --tenant <id> --instance prod --month 2026-07 \
#       --actual groq_whisper_turbo=0.38 --actual-total 1.02
#   scripts/usage.sh --tenant <id> --fixtures scripts/test/fixtures/observability/records.json

# shellcheck source-path=SCRIPTDIR source=lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"

TENANT=""
INSTANCE=""
REGION=""
FIXTURES=""
AS_JSON=""
ACTOR=""
MONTH=""
SINCE=""
UNTIL=""
TOLERANCE=""
ACTUAL_TOTAL=""
ACTUALS=()

while [ $# -gt 0 ]; do
    case "$1" in
        --tenant)
            TENANT="${2:?--tenant needs a value}"
            shift
            ;;
        --instance)
            INSTANCE="${2:?--instance needs a value}"
            shift
            ;;
        --region)
            REGION="${2:?--region needs a value}"
            shift
            ;;
        --fixtures)
            FIXTURES="${2:?--fixtures needs a path}"
            shift
            ;;
        --month)
            MONTH="${2:?--month needs yyyy-mm}"
            shift
            ;;
        --since)
            SINCE="${2:?--since needs yyyy-mm}"
            shift
            ;;
        --until)
            UNTIL="${2:?--until needs yyyy-mm}"
            shift
            ;;
        --tolerance-bp)
            TOLERANCE="${2:?--tolerance-bp needs a value}"
            shift
            ;;
        --actual)
            ACTUALS+=("${2:?--actual needs <provider>=<usd>}")
            shift
            ;;
        --actual-total)
            ACTUAL_TOTAL="${2:?--actual-total needs a USD figure}"
            shift
            ;;
        --as)
            ACTOR="${2:?--as needs an actor id}"
            shift
            ;;
        --json) AS_JSON="--json" ;;
        -h | --help)
            sed -n '2,35p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
            exit 0
            ;;
        *) die "unknown flag '$1' (see --help)" ;;
    esac
    shift
done
cd "$REPO_ROOT" || exit 1

# I11 via §11.3, and enforced here as well as in the binary: the failure this prevents is a
# report attributed to the wrong tenant because a flag was forgotten, and refusing at the
# outermost layer means the operator sees the reason rather than a Go error.
require_tenant "$TENANT"

ARGS=(usage --tenant "$TENANT")

if [ -n "$FIXTURES" ]; then
    # CI mode (§11.5): records come from a JSON fixture and an in-memory repository, so the
    # tests run with no AWS credentials and cannot reach an account. The report labels
    # itself as fixture-sourced, because a fixture report that looked like a real tenant's
    # spend would be worse than no report.
    [ -f "$FIXTURES" ] || die "no fixture file at '$FIXTURES'"
    ARGS+=(--fixtures "$(cd "$(dirname "$FIXTURES")" && pwd)/$(basename "$FIXTURES")")
else
    # An instance is required rather than defaulted. Usage records live in that instance's
    # table (§6.3), so a default would silently report dev's spend as prod's — the same
    # class of mistake deploy.sh refuses by taking the region from config rather than the
    # environment.
    [ -n "$INSTANCE" ] || die "--instance is required (or --fixtures for the CI mode): usage records live in the instance's own table (§6.3)"
    CONFIG="config/instances/${INSTANCE}.yaml"
    [ -f "$CONFIG" ] || die "no config for instance '$INSTANCE' at $CONFIG"

    # Region from config, not from the ambient environment: a developer machine's default
    # region is whatever they last worked on, and reading another region's table would
    # report an empty month as zero spend (deploy.sh defaults it the same way).
    if [ -z "$REGION" ]; then
        REGION="$(yq -r '.region' "$CONFIG")"
    fi
    [ -n "$REGION" ] && [ "$REGION" != "null" ] || die "no region in $CONFIG"
    # Exported rather than passed as a flag: awsclient is the only package that touches the
    # AWS SDK, and it builds its client from the default credential chain — so AWS_REGION is
    # the seam the region travels through.
    export AWS_REGION="$REGION" AWS_PAGER=""
    ARGS+=(--config "$REPO_ROOT/$CONFIG")
fi

[ -n "$MONTH" ] && ARGS+=(--month "$MONTH")
[ -n "$SINCE" ] && ARGS+=(--since "$SINCE")
[ -n "$UNTIL" ] && ARGS+=(--until "$UNTIL")
[ -n "$TOLERANCE" ] && ARGS+=(--tolerance-bp "$TOLERANCE")
[ -n "$ACTUAL_TOTAL" ] && ARGS+=(--actual-total "$ACTUAL_TOTAL")
[ -n "$ACTOR" ] && ARGS+=(--as "$ACTOR")
[ -n "$AS_JSON" ] && ARGS+=("$AS_JSON")
for a in ${ACTUALS[@]+"${ACTUALS[@]}"}; do
    ARGS+=(--actual "$a")
done

# Built and executed, NOT `go run`. go run does not propagate its child's exit code: it
# exits 1 and prints "exit status 3" to stderr, which would flatten every finding into a
# generic failure and make exit codes 3 and 4 unreachable for any caller. Verified, not
# assumed — the first version of this script reported EXIT=1 for a report that had
# correctly decided 3. The binary lands in build/, which is gitignored; CHINTANCTL points
# at a prebuilt one so a test or a CI job need not rebuild per invocation.
CTL="${CHINTANCTL:-}"
if [ -z "$CTL" ]; then
    CTL="$REPO_ROOT/build/chintanctl"
    (cd backend && go build -o "$CTL" ./cmd/chintanctl) || die "building chintanctl failed"
fi

# `set +e` around the call: the exit code is the result here, not a failure to be aborted
# on. 3 and 4 are findings — a report was produced and it says something — and collapsing
# them into the shell's default abort would throw away the distinction §11.3 asks for.
set +e
"$CTL" "${ARGS[@]}"
status=$?
set -e

case "$status" in
    0) ;;
    3) warn "metered totals disagree with the supplied billed figures beyond the tolerance — see the reconciliation table above (§Phase 0 acceptance: within 5%)" ;;
    4) err "a month is over the §10.7 ceiling of \$5 — that is a design error, not a budget overrun: stop and flag it before implementing further" ;;
    2) err "invocation error (see above)" ;;
    *) err "usage report failed (exit $status)" ;;
esac
exit "$status"
