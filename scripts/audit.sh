#!/usr/bin/env bash
#
# Query the audit log by actor, action, resource, or time range (§11.4, Phase 0).
#
# The audit log is the answer to "show me the access log", which §2A.1 notes is where every
# future SOC 2 or enterprise conversation begins — and "a gap in it is not repairable". This
# is the only sanctioned way to read it: I16 requires the operational scripts to be the sole
# route to backend state, because an `aws dynamodb query` typed at 2am is untested,
# unaudited, unrepeatable, and has no --dry-run.
#
# READ-ONLY: no --apply, and none needed (§11.3). It writes exactly one thing, and that one
# thing is recursive: §11.3 requires every data script invocation to write an audit record
# (I13), so reading the audit log appends to the audit log. That is handled deliberately
# rather than accidentally — the record is written BEFORE the read (audit.Record's contract:
# a crash between the access and the record leaves an unrepairable gap), then that single
# record is excluded from the results and its id is printed. Without the exclusion,
# `--limit 1` would answer with nothing but itself and each run would inflate the next run's
# answer. Records written by EARLIER invocations are not excluded: reads of the log are
# accesses worth seeing, and `--action audit.query` lists them.
#
# --tenant is REQUIRED: no data operation runs untenanted (I11).
#
# ALL THE LOGIC IS IN chintanctl (§11.2) — the query, the filters, the bound validation and
# the self-exclusion are tested Go. This script owns argument parsing, region defaulting and
# output plumbing. NO BUSINESS LOGIC IN BASH.
#
# Filters: --actor, --action, --resource, --result allowed|denied, --since (inclusive),
# --until (EXCLUSIVE, so consecutive windows tile without double-counting), --limit (0 for
# unlimited, default 100), --oldest to flip the default newest-first ordering.
#
# Usage:
#   scripts/audit.sh --tenant <id> --instance prod
#   scripts/audit.sh --tenant <id> --instance prod --action capture.read --limit 50
#   scripts/audit.sh --tenant <id> --instance prod --since 2026-08-01 --until 2026-09-01 --json
#   scripts/audit.sh --tenant <id> --fixtures scripts/test/fixtures/observability/records.json

# shellcheck source-path=SCRIPTDIR source=lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"

TENANT=""
INSTANCE=""
REGION=""
FIXTURES=""
AS_JSON=""
ACTOR=""
F_ACTOR=""
F_ACTION=""
F_RESOURCE=""
F_RESULT=""
SINCE=""
UNTIL=""
LIMIT=""
OLDEST=""

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
        --actor)
            F_ACTOR="${2:?--actor needs a value}"
            shift
            ;;
        --action)
            F_ACTION="${2:?--action needs a value}"
            shift
            ;;
        --resource)
            F_RESOURCE="${2:?--resource needs a value}"
            shift
            ;;
        --result)
            F_RESULT="${2:?--result needs allowed or denied}"
            shift
            ;;
        --since)
            SINCE="${2:?--since needs a date or RFC3339 UTC timestamp}"
            shift
            ;;
        --until)
            UNTIL="${2:?--until needs a date or RFC3339 UTC timestamp}"
            shift
            ;;
        --limit)
            LIMIT="${2:?--limit needs a number}"
            shift
            ;;
        --oldest) OLDEST="--oldest" ;;
        --as)
            # Who is running this, recorded in the invocation's own audit record (I13). A
            # user id, never an email: the audit package refuses an '@' because PII in the
            # longest-retained store in the system is not repairable (§9.2).
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

# I11 via §11.3. An untenanted audit query would be a cross-tenant read of the one log that
# exists to prove cross-tenant reads do not happen.
require_tenant "$TENANT"

ARGS=(audit --tenant "$TENANT")

if [ -n "$FIXTURES" ]; then
    # CI mode (§11.5): records from a JSON fixture and an in-memory repository, so tests run
    # with no AWS credentials. The output labels itself fixture-sourced.
    [ -f "$FIXTURES" ] || die "no fixture file at '$FIXTURES'"
    ARGS+=(--fixtures "$(cd "$(dirname "$FIXTURES")" && pwd)/$(basename "$FIXTURES")")
else
    # Required rather than defaulted, for the same reason usage.sh requires it: the log lives
    # in the instance's own table, and a default would answer a compliance question from the
    # wrong environment.
    [ -n "$INSTANCE" ] || die "--instance is required (or --fixtures for the CI mode): the audit log lives in the instance's own table (§6.3)"
    CONFIG="config/instances/${INSTANCE}.yaml"
    [ -f "$CONFIG" ] || die "no config for instance '$INSTANCE' at $CONFIG"

    # Region from config, not the ambient environment (see usage.sh; deploy.sh does the same).
    # Reading the wrong region's table would report an empty log, which is the worst possible
    # wrong answer to "who accessed this".
    if [ -z "$REGION" ]; then
        REGION="$(yq -r '.region' "$CONFIG")"
    fi
    [ -n "$REGION" ] && [ "$REGION" != "null" ] || die "no region in $CONFIG"
    export AWS_REGION="$REGION" AWS_PAGER=""
    ARGS+=(--config "$REPO_ROOT/$CONFIG")
fi

[ -n "$F_ACTOR" ] && ARGS+=(--actor "$F_ACTOR")
[ -n "$F_ACTION" ] && ARGS+=(--action "$F_ACTION")
[ -n "$F_RESOURCE" ] && ARGS+=(--resource "$F_RESOURCE")
[ -n "$F_RESULT" ] && ARGS+=(--result "$F_RESULT")
[ -n "$SINCE" ] && ARGS+=(--since "$SINCE")
[ -n "$UNTIL" ] && ARGS+=(--until "$UNTIL")
[ -n "$LIMIT" ] && ARGS+=(--limit "$LIMIT")
[ -n "$OLDEST" ] && ARGS+=("$OLDEST")
[ -n "$ACTOR" ] && ARGS+=(--as "$ACTOR")
[ -n "$AS_JSON" ] && ARGS+=("$AS_JSON")

# Built and executed rather than `go run`, for the reason usage.sh states at length: go run
# exits 1 whatever its child returned, so a caller cannot tell an invocation error (2) from
# a failure (1). CHINTANCTL points at a prebuilt binary for CI.
CTL="${CHINTANCTL:-}"
if [ -z "$CTL" ]; then
    CTL="$REPO_ROOT/build/chintanctl"
    (cd backend && go build -o "$CTL" ./cmd/chintanctl) || die "building chintanctl failed"
fi

set +e
"$CTL" "${ARGS[@]}"
status=$?
set -e

case "$status" in
    0) ;;
    2) err "invocation error (see above)" ;;
    *) err "audit query failed (exit $status)" ;;
esac
exit "$status"
