#!/usr/bin/env bash
#
# Delete the resources a failed instance create left behind.
#
# Resources in infrastructure/template.yaml carry DeletionPolicy: Retain so that
# deleting a stack cannot destroy notes, audio, or the KMS key every enrolled
# biometric credential depends on. When a create SUCCEEDS that is exactly right.
# When a create FAILS, the same policy strands resources from a stack that never
# worked — and because bucket names, Cognito domains and KMS aliases are globally
# or account unique, the next create collides with its own wreckage and fails
# validation before it starts.
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
KEY_ALIAS="alias/chintan-${INSTANCE}-${ENVIRONMENT}/token-vault"

info "account:     $ACCOUNT_ID"
info "region:      $REGION"
info "stack:       $STACK (absent)"

if [ "$ENVIRONMENT" = "prod" ] && [ "$APPLY" = 1 ] && [ "$PROD_ACK" != 1 ]; then
    die "refusing to delete production data without --i-understand-this-deletes-data"
fi

found=0

bucket_exists() { aws s3api head-bucket --bucket "$BUCKET" >/dev/null 2>&1; }
table_exists() { aws dynamodb describe-table --table-name "$TABLE" >/dev/null 2>&1; }
pool_id() {
    aws cognito-idp list-user-pools --max-results 60 \
        --query "UserPools[?Name=='${POOL_NAME}'].Id | [0]" --output text 2>/dev/null || echo None
}
alias_exists() {
    [ -n "$(aws kms list-aliases --query "Aliases[?AliasName=='${KEY_ALIAS}'].AliasName" --output text 2>/dev/null)" ]
}

if bucket_exists; then
    n="$(aws s3 ls "s3://${BUCKET}" --recursive --summarize 2>/dev/null | awk '/Total Objects/ {print $3}')"
    warn "bucket   s3://${BUCKET} (${n:-0} objects)"
    found=1
fi
if table_exists; then
    warn "table    ${TABLE}"
    found=1
fi
POOL="$(pool_id)"
if [ -n "$POOL" ] && [ "$POOL" != "None" ]; then
    warn "userpool ${POOL} (${POOL_NAME})"
    found=1
fi
if alias_exists; then
    warn "kmsalias ${KEY_ALIAS}"
    found=1
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

if bucket_exists; then
    info "emptying and removing s3://${BUCKET}"
    aws s3 rb "s3://${BUCKET}" --force
fi

if table_exists; then
    info "deleting table ${TABLE}"
    aws dynamodb delete-table --table-name "$TABLE" >/dev/null
    # delete-table returns immediately; the name stays taken until it is gone,
    # which is long enough for a following create to collide with it.
    info "waiting for ${TABLE} to disappear"
    aws dynamodb wait table-not-exists --table-name "$TABLE"
fi

POOL="$(pool_id)"
if [ -n "$POOL" ] && [ "$POOL" != "None" ]; then
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

if alias_exists; then
    info "deleting kms alias ${KEY_ALIAS}"
    aws kms delete-alias --alias-name "$KEY_ALIAS"
fi

# The CMK itself is left alone on purpose. It is the one retained resource that
# might still hold something irreplaceable, it does not block a create (keys have
# no unique name), and deletion is reversible only inside its pending window.
# Schedule it deliberately if you want it gone:
#   aws kms schedule-key-deletion --key-id <id> --pending-window-in-days 7
warn "the KMS CMK was left in place; it does not block a create. Schedule its deletion separately if you want it gone."

ok "orphans cleared for ${STACK}"
