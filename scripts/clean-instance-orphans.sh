#!/usr/bin/env bash
#
# Delete the resources a failed instance create left behind.
#
# Resources in infrastructure/template.yaml carry DeletionPolicy: Retain so that
# deleting a stack cannot destroy notes, audio, or the user pool the tenant ids
# come from. When a create SUCCEEDS that is exactly right. When a create FAILS,
# the same policy strands resources from a stack that never worked — and because
# bucket names and Cognito domains are globally or account unique, the next
# create collides with its own wreckage and fails validation before it starts.
#
# WHY THIS IS A SEPARATE, HUMAN-RUN SCRIPT
#
# The permissions boundary denies irreversible deletes to any principal not
# acting through CloudFormation (DenyIrreversibleDeletesOutsideCloudFormation).
# The CI role is therefore explicitly denied s3:DeleteBucket,
# dynamodb:DeleteTable and cognito-idp:DeleteUserPool — verified, not assumed.
# That guardrail is correct and this script does not work around it: it is run by
# a human with elevated credentials, like scripts/bootstrap-agent.sh.
#
# An earlier attempt put this cleanup inside scripts/deploy.sh. Every call was
# denied, every denial was swallowed by `|| true`, and the deploy reported that
# it had cleaned up. Silent failure was worse than no attempt.
#
# Usage:
#   scripts/clean-instance-orphans.sh --instance dev --environment staging
#   scripts/clean-instance-orphans.sh --instance dev --environment staging --apply
#
# Dry run is the default. Production requires --i-understand-this-deletes-data
# in addition to --apply, because there the retained resources are usually real.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=lib/common.sh
. "$REPO_ROOT/scripts/lib/common.sh"

INSTANCE=""
ENVIRONMENT=""
REGION="${AWS_REGION:-us-west-2}"
APPLY=0
PROD_ACK=0

while [ $# -gt 0 ]; do
    case "$1" in
        --instance)
            INSTANCE="${2:?--instance needs a value}"
            shift
            ;;
        --environment)
            ENVIRONMENT="${2:?--environment needs a value}"
            shift
            ;;
        --region)
            REGION="${2:?--region needs a value}"
            shift
            ;;
        --apply) APPLY=1 ;;
        --i-understand-this-deletes-data) PROD_ACK=1 ;;
        -h | --help)
            awk 'NR == 1 { next } /^#/ { sub(/^# ?/, ""); print; next } { exit }' "${BASH_SOURCE[0]}"
            exit 0
            ;;
        *) die "unknown flag '$1' (see --help)" ;;
    esac
    shift
done

[ -n "$INSTANCE" ] || die "--instance is required"
[ -n "$ENVIRONMENT" ] || die "--environment is required"
case "$ENVIRONMENT" in
    prod | staging | dev) ;;
    *) die "--environment must be one of prod, staging, dev" ;;
esac
export AWS_REGION="$REGION"

STACK="chintan-${INSTANCE}-${ENVIRONMENT}"

# Refuse to touch anything while the stack that owns it still exists. If the
# stack is alive these are not orphans, they are its resources.
if aws cloudformation describe-stacks --stack-name "$STACK" >/dev/null 2>&1; then
    die "$STACK still exists — these are its resources, not orphans. Delete the stack first."
fi

ACCOUNT_ID="$(aws sts get-caller-identity --query Account --output text)" ||
    die "could not resolve the account id"

BUCKET="chintan-content-${INSTANCE}-${ENVIRONMENT}-${ACCOUNT_ID}"
TABLE="chintan-${INSTANCE}-${ENVIRONMENT}"
POOL_NAME="chintan-${INSTANCE}-${ENVIRONMENT}"

info "account:     $ACCOUNT_ID"
info "region:      $REGION"
info "stack:       $STACK (absent)"

if [ "$ENVIRONMENT" = "prod" ] && [ "$APPLY" = 1 ] && [ "$PROD_ACK" != 1 ]; then
    die "refusing to delete production data without --i-understand-this-deletes-data"
fi

found=0
blind=0

# Every probe answers present / absent / unknown (see scripts/lib/common.sh).
# The three-way answer is the point: `aws s3api head-bucket >/dev/null 2>&1`
# reports failure both for a bucket that is not there and for one this caller
# may not look at — head-bucket answers 403, not 404, when access is denied —
# and collapsing those two into "absent" makes this script print "no orphans"
# and exit 0 over resources that are about to collide with the next create.
# This script has only ever been run in dry-run against a clean instance, where
# a denied probe and a genuinely clean account are indistinguishable.
BUCKET_PROBE="$(probe_bucket "$BUCKET")"
TABLE_PROBE="$(probe_table "$TABLE")"
POOL_PROBE="$(probe_user_pool "$POOL_NAME")"

note_probe() {
    local probe="$1" label="$2"
    case "$(probe_state "$probe")" in
        present)
            warn "${label} $(probe_detail "$probe")"
            found=1
            ;;
        unknown)
            err "${label} could not be checked: $(probe_detail "$probe")"
            blind=1
            ;;
    esac
}

if [ "$(probe_state "$BUCKET_PROBE")" = present ]; then
    # Not silenced and not defaulted to 0: "(0 objects)" on a bucket whose
    # listing was denied invites deleting it as empty debris.
    if n="$(aws s3 ls "s3://${BUCKET}" --recursive --summarize 2>&1 | awk '/Total Objects/ {print $3}')" && [ -n "$n" ]; then
        warn "bucket   s3://${BUCKET} (${n} objects)"
    else
        warn "bucket   s3://${BUCKET} (object count unavailable — listing denied or failed)"
    fi
    found=1
elif [ "$(probe_state "$BUCKET_PROBE")" = unknown ]; then
    err "bucket   could not be checked: $(probe_detail "$BUCKET_PROBE")"
    blind=1
fi

note_probe "$TABLE_PROBE" "table   "
note_probe "$POOL_PROBE" "userpool"

POOL=""
[ "$(probe_state "$POOL_PROBE")" = present ] && POOL="$(probe_detail "$POOL_PROBE")"

if [ "$blind" = 1 ]; then
    printf '\n'
    err "one or more probes could not be answered, so this is not a complete picture."
    die "re-run with credentials that can read the resources above"
fi

if [ "$found" = 0 ]; then
    ok "no orphans for ${STACK}"
    exit 0
fi

if [ "$APPLY" != 1 ]; then
    printf '\n'
    warn "DRY RUN — nothing was changed. Re-run with --apply to delete the above."
    exit 0
fi

confirm_destructive "delete the resources listed above for ${STACK}" || exit 2

if [ "$(probe_state "$BUCKET_PROBE")" = present ]; then
    info "emptying and removing s3://${BUCKET}"
    aws s3 rb "s3://${BUCKET}" --force
fi

if [ "$(probe_state "$TABLE_PROBE")" = present ]; then
    info "deleting table ${TABLE}"
    aws dynamodb delete-table --table-name "$TABLE" >/dev/null
    # delete-table returns immediately; the name stays taken until it is gone,
    # which is long enough for a following create to collide with it.
    info "waiting for ${TABLE} to disappear"
    aws dynamodb wait table-not-exists --table-name "$TABLE"
fi

if [ -n "$POOL" ]; then
    info "deleting user pool ${POOL}"
    # Deletion protection is on by design and must come off first. UpdateUserPool
    # replaces the whole configuration rather than patching it, so the
    # verification attributes have to be restated or the call is rejected with
    # "All attributes in AttributesRequireVerificationBeforeUpdate must exist in
    # AutoVerifiedAttributes".
    aws cognito-idp update-user-pool --user-pool-id "$POOL" \
        --deletion-protection INACTIVE \
        --auto-verified-attributes email \
        --user-attribute-update-settings AttributesRequireVerificationBeforeUpdate=email >/dev/null
    aws cognito-idp delete-user-pool --user-pool-id "$POOL" >/dev/null
fi

ok "orphans cleared for ${STACK}"
