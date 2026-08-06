#!/usr/bin/env bash
#
# Tenant erasure (§9.3, §11.4, Phase 0). **The most destructive operation in this system.**
#
# It deletes every record in the tenant's DynamoDB partition and every object VERSION under
# its S3 prefix, and it is the only code path permitted to delete an L0 transcript.
#
# WHY THAT CARVE-OUT EXISTS, because it looks like a contradiction. I1 says a raw transcript
# "is never deleted by any application code path" — the raw↔edited pair is the training
# signal for the correction system and losing it to a bug is unrecoverable. §9.3 requires
# right-to-erasure. G-038 is explicit that the two conflict and "the conflict must be
# resolved deliberately, not discovered when the first erasure request arrives": immutability
# is scoped to "never deleted by application code", and one separately-permissioned erasure
# path is carved out, writing an audit record BEFORE it executes. This script is the entry
# point to that path and there is no other.
#
# WHAT ERASURE DOES NOT ACHIEVE — read the report, not this header, before telling anyone
# their data is gone. §9.3: crypto-shredding is the primary mechanism ONLY once a
# customer-managed key exists. "During the personal phase there is no customer-managed key
# (I8), so crypto-shredding is unavailable. Erasure falls back to object deletion plus
# waiting out the PITR retention window." G-021's symptom is a "completed" erasure that left
# recoverable data, "discovered during audit, not during testing" — so the dry run enumerates
# what SURVIVES as prominently as what would go: the DynamoDB PITR window, on-demand
# backups, in-flight multipart uploads, the Telegram link record no tenant-scoped query can
# reach, Cognito identities, and the operation's own audit record.
#
# SEPARATELY PERMISSIONED, in three places, because one is not enough:
#   1. --apply requires --confirm <tenant-id>, the tenant id retyped. Enforced in the
#      binary, not here, so calling chintanctl directly does not walk past it.
#   2. This script refuses to --apply as the account root user or as the agent principal.
#      I17: the implementing agent "never holds root credentials"; §9.3 makes erasure
#      separately permissioned, and an agent that could erase a tenant on a misread
#      instruction is the failure §9.4 is written to make impossible. A dry run IS allowed
#      as the agent — producing a plan for a human to execute is the useful division.
#   3. The IAM deny on Protected=true resources is NOT the control here. `aws:ResourceTag`
#      is unsupported for authorization on DynamoDB and does not reach S3 object ARNs from a
#      bucket tag, so that deny is decorative for exactly the two calls this operation makes
#      (G-067, the same finding cleanup-aws.sh rests on). The refusals above are implemented
#      in the open, and they are tested.
#
# --tenant is REQUIRED (I11). Every invocation writes an audit record (I13) — a dry run too,
# because computing the plan reads the tenant's inventory — and the record is written BEFORE
# any deletion. If it cannot be written, nothing is deleted.
#
# IDEMPOTENT, and it converges rather than emptying: each run writes its own audit and
# metering records after taking its inventory, so those survive it. A converged partition
# holds exactly the last run's attestation. Re-run until the plan lists nothing else.
#
# ALL THE LOGIC IS IN chintanctl (§11.2): the inventory, the version-by-version deletion, the
# survival report and the plan --apply executes exactly. This script owns argument parsing,
# the principal check, the confirmation prompt and output plumbing. NO BUSINESS LOGIC IN
# BASH.
#
# Usage:
#   scripts/erase-tenant.sh --tenant <id> --instance prod            # dry run — read this
#   scripts/erase-tenant.sh --tenant <id> --instance prod --json
#   scripts/erase-tenant.sh --tenant <id> --instance prod --apply --confirm <id>
#   scripts/erase-tenant.sh --tenant <id> --fixtures scripts/test/fixtures/tenant-data
#
# Prerequisites: the toolchain container (make, or scripts/dev.sh); for --apply, credentials
# for a principal that is neither root nor the agent, permitted to delete from the instance's
# table and bucket; a config for the instance under config/instances/. Take an export first
# (scripts/export-tenant.sh) if this is a portability request as well as a deletion request —
# afterwards there is nothing left to export.
#
# Exit codes: 0 planned or erased, 1 the erasure was incomplete — some deletions failed and a
#             re-run is required, 2 bad arguments, 3 refused: an unconfirmed --apply, or a
#             principal that must not erase. Refused is distinct from failed because
#             "nothing happened" and "something half-happened" call for different next
#             actions.

# shellcheck source-path=SCRIPTDIR source=lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"

TENANT=""
INSTANCE=""
REGION=""
ACCOUNT=""
CONFIRM=""
FIXTURES=""
ACTOR="script:erase-tenant"
AS_JSON=""
APPLY=0
YES=0

while [ $# -gt 0 ]; do
    case "$1" in
        --tenant)
            TENANT="${2:?--tenant needs a value}"
            shift
            ;;
        --confirm)
            CONFIRM="${2:?--confirm needs the tenant id retyped}"
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
        --account)
            ACCOUNT="${2:?--account needs a value}"
            shift
            ;;
        --fixtures)
            # Harness mode (§11.5): a fixture set instead of live AWS, so both --dry-run and
            # --apply are tested with no credentials.
            FIXTURES="${2:?--fixtures needs a path}"
            shift
            ;;
        --as)
            # Recorded in the invocation's audit record (I13). A user id, never an email —
            # the audit package refuses an '@' (§9.2).
            ACTOR="${2:?--as needs an actor id}"
            shift
            ;;
        --json) AS_JSON="--json" ;;
        --apply) APPLY=1 ;;
        --dry-run) APPLY=0 ;;
        --yes) YES=1 ;;
        -h | --help)
            # Ends at the exit-code paragraph, not at the source line below it: a range
            # that ran past the header printed the shellcheck directive and the source
            # statement as though they were help text.
            sed -n '2,70p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
            exit 0
            ;;
        *) die "unknown flag '$1' (see --help)" ;;
    esac
    shift
done

# Resolved against the invoking directory, before the cd below.
abs_path() {
    case "$1" in
        /*) printf '%s\n' "$1" ;;
        *) printf '%s/%s\n' "$PWD" "$1" ;;
    esac
}
[ -n "$FIXTURES" ] && FIXTURES="$(abs_path "$FIXTURES")"

cd "$REPO_ROOT" || exit 1

# I11 via §11.3: no data operation runs untenanted. **This is the refusal that matters most
# in this repository**: an erasure with no tenant would be either nothing or the whole table,
# and §11.5 requires it to be tested as a refusal for exactly that reason.
require_tenant "$TENANT"

ARGS=(erase --tenant "$TENANT" --as "$ACTOR")

if [ -n "$FIXTURES" ]; then
    [ -e "$FIXTURES" ] || die "no fixture set at '$FIXTURES'"
    ARGS+=(--fixtures "$FIXTURES")
else
    [ -n "$INSTANCE" ] || die "--instance is required (or --fixtures for the CI mode): the corpus lives in the instance's own table and bucket (§6.3)"
    CONFIG="config/instances/${INSTANCE}.yaml"
    [ -f "$CONFIG" ] || die "no config for instance '$INSTANCE' at $CONFIG"
    if [ -z "$REGION" ]; then
        REGION="$(yq -r '.region' "$CONFIG")"
    fi
    [ -n "$REGION" ] && [ "$REGION" != "null" ] || die "no region in $CONFIG"
    export AWS_REGION="$REGION" AWS_PAGER=""

    CALLER="$(aws_cli sts get-caller-identity --output json 2>/dev/null | jq -r '.Arn // empty')"
    [ -n "$CALLER" ] || die "no AWS credentials; erasure cannot be planned without reading the tenant's inventory"
    if [ -z "$ACCOUNT" ]; then
        ACCOUNT="$(aws_cli sts get-caller-identity --output json 2>/dev/null | jq -r '.Account // empty')"
        [ -n "$ACCOUNT" ] || die "could not determine the AWS account id; pass --account"
    fi

    if [ "$APPLY" = "1" ]; then
        # The principal gate. It applies only to --apply: a dry run as the agent is useful
        # and harmless, and refusing it would mean no plan could be produced for a human to
        # act on.
        #
        # Checked against the caller's own identity rather than trusted from a flag, and
        # refused rather than warned about. §9.4: "make harmful actions impossible, not
        # discouraged" — and since IAM cannot express this condition for DynamoDB or for S3
        # objects (G-067), a check here is what there is.
        case "$CALLER" in
            *:root)
                err "refusing to erase as the account root user."
                err "Root bypasses every deny in the agent boundary (§9.4), so the guardrails this operation"
                err "assumes are not in force. Re-run as a principal permitted to delete from"
                err "${SYSTEM_ID}-${INSTANCE} and its bucket, and nothing else."
                exit 3
                ;;
            *"role/${SYSTEM_ID}-agent" | *"user/${SYSTEM_ID}-agent")
                err "refusing to erase as the agent principal (${CALLER})."
                err "§9.3 makes erasure separately permissioned, and I17 keeps the implementing agent"
                err "outside it: an agent that could erase a tenant on a misread instruction is the"
                err "failure §9.4 exists to make impossible."
                err ""
                err "A DRY RUN as this principal is allowed and is the useful thing to do here:"
                err "  scripts/erase-tenant.sh --tenant ${TENANT} --instance ${INSTANCE} --json"
                err "then have an operator apply the plan it prints."
                exit 3
                ;;
        esac
    fi
    ARGS+=(--instance "$INSTANCE" --region "$REGION" --account "$ACCOUNT" --config "../$CONFIG")
fi

[ -n "$AS_JSON" ] && ARGS+=("$AS_JSON")

if [ "$APPLY" = "1" ]; then
    # Interactive confirmation on top of --confirm, for the case the flag was pasted from a
    # runbook. Interactive only: the harness and CI have no terminal, and a prompt that
    # blocked there would turn a test into a hang. --yes skips it for a scripted run.
    if [ "$YES" = "0" ] && [ -t 0 ]; then
        warn "About to ERASE tenant '$TENANT'."
        warn "This deletes every record and every object version — including L0 transcripts (I1),"
        warn "which no other code path in this system may delete. Nothing here can restore them."
        warn "Run without --apply first and read the WHAT SURVIVES section before continuing."
        printf 'Type the tenant id to continue: ' >&2
        read -r reply
        [ "$reply" = "$TENANT" ] || die "not confirmed; nothing was deleted"
    fi
    ARGS+=(--apply)
    # Passed through even when empty: the binary is the authority on the confirmation, and
    # omitting the flag here would make the wrapper the place the gate lives (§11.2 gives
    # bash the argument parsing, never the safety property).
    ARGS+=(--confirm "$CONFIRM")
fi

# The plan, the survival report and the refusals all come from chintanctl, which prints them
# to stdout so --json stays parseable.
set +e
(cd backend && go run ./cmd/chintanctl "${ARGS[@]}")
status=$?
set -e
exit "$status"
