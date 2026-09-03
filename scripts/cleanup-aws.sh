#!/usr/bin/env bash
#
# Delete one Chintan instance stack and the resources it retains.
#
# Everything this script touches is discovered from the stack itself, through
# describe-stack-resources. Nothing is found by matching a name prefix across the
# account. That distinction is the difference between deleting the DynamoDB table
# belonging to chintan-dev-staging and deleting every table in the account whose
# name happens to start with "chintan-".
#
# Two v1 defects are fixed here beyond the naming:
#
#   * v1 built the stack name as chintan-<instance>, found no such stack because
#     CI deploys chintan-<instance>-prod, warned, returned — and then went on to
#     delete /chintan/<instance>/groq_api_key and /chintan/<instance>/llm_api_key
#     unconditionally. It broke the running application and cleaned up nothing.
#     Secrets are now deleted only after the stack is actually gone, and only when
#     no other stack for that instance still needs them.
#
#   * v1 prompted on stdin with no way to answer. teardown.sh exported
#     SKIP_CONFIRMATION=true, which this script never read, so an automated
#     teardown blocked on an invisible prompt. --yes is now a real flag, and
#     ASSUME_YES is honoured from the environment so a nested run inherits it.
#
# Usage:
#   scripts/cleanup-aws.sh --instance dev [--environment prod] [--region R]
#                          [--yes] [--apply]
#
# Options:
#   --instance NAME     instance name                       (required)
#   --environment ENV   prod | staging | dev                (default: prod)
#   --region REGION     AWS region                          (default: $AWS_REGION)
#   --yes               skip the typed confirmation
#   --apply             execute; without it nothing is deleted

# shellcheck source-path=SCRIPTDIR source=lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"

INSTANCE=""
ENVIRONMENT="prod"

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
            AWS_REGION="${2:?--region needs a value}"
            export AWS_REGION
            shift
            ;;
        --yes) ASSUME_YES=1 ;;
        --apply) APPLY=1 ;;
        --dry-run) APPLY=0 ;;
        -h | --help)
            usage_from_header "${BASH_SOURCE[0]}"
            exit 0
            ;;
        *) die "unknown flag '$1' (see --help)" ;;
    esac
    shift
done

[ -n "$INSTANCE" ] || die "--instance is required (see --help)"
validate_instance_name "$INSTANCE"
validate_environment "$ENVIRONMENT"

require_aws
require_cmd jq

STACK="$(stack_name "$INSTANCE" "$ENVIRONMENT")"

info "instance:    $INSTANCE"
info "environment: $ENVIRONMENT"
info "stack:       $STACK"

if ! stack_exists "$STACK"; then
    warn "stack $STACK does not exist — nothing to clean up"
    warn "SSM parameters under /chintan/${INSTANCE}/ are left alone: they may belong"
    warn "to another environment of this instance, and deleting them would break it"
    exit 0
fi

# ---------------------------------------------------------------------------
# Inventory, taken while the stack still exists
# ---------------------------------------------------------------------------

BUCKETS="$(stack_resources_of_type "$STACK" 'AWS::S3::Bucket')"
TABLES="$(stack_resources_of_type "$STACK" 'AWS::DynamoDB::Table')"
LOG_GROUPS="$(stack_resources_of_type "$STACK" 'AWS::Logs::LogGroup')"
USER_POOLS="$(stack_resources_of_type "$STACK" 'AWS::Cognito::UserPool')"
KMS_KEYS="$(stack_resources_of_type "$STACK" 'AWS::KMS::Key')"
QUEUES="$(stack_resources_of_type "$STACK" 'AWS::SQS::Queue')"

log ""
info "resources belonging to $STACK"
for b in $BUCKETS; do dim "  s3 bucket     $b (Retain)"; done
for t in $TABLES; do dim "  dynamodb      $t (Retain)"; done
for p in $USER_POOLS; do dim "  user pool     $p (Retain, DeletionProtection ACTIVE)"; done
for k in $KMS_KEYS; do dim "  kms key       $k (Retain)"; done
for q in $QUEUES; do dim "  sqs queue     $q (deleted with the stack)"; done
for g in $LOG_GROUPS; do dim "  log group     $g"; done
[ -n "$BUCKETS$TABLES$LOG_GROUPS$USER_POOLS$KMS_KEYS$QUEUES" ] || dim "  (no resources found)"

# Every one of these carries DeletionPolicy: Retain in infrastructure/template.yaml,
# and correctly so — deleting the stack must not destroy the notes, or the user
# pool whose subjects every note is keyed by. The consequence is that "delete
# the stack" and "delete the data" are two
# separate acts, and this script performs the second one explicitly, after the
# first, on resources it identified while the stack still listed them.

# Whether the provider secrets may go depends on what else still uses them. They
# live at /chintan/<instance>/, one level above the environment, so a staging
# teardown must not take prod's keys with it.
# Fail CLOSED. This guard decides whether prod's provider API keys are deleted,
# so "the enumeration failed" and "there are no siblings" must not look alike.
# An empty answer from a failed list-stacks call previously read as "nothing
# else uses these secrets, delete them".
if ! ALL_STACKS="$(list_chintan_stacks)"; then
    die "could not enumerate stacks, so it is not known whether /chintan/${INSTANCE}/* is still in use — refusing to delete the provider secrets"
fi
if [ -z "$ALL_STACKS" ]; then
    # Not even $STACK came back, and this script only runs against a stack that
    # existed a moment ago. Something is wrong with the enumeration, not with
    # the account.
    die "stack enumeration returned nothing at all, not even ${STACK} — refusing to act on that"
fi
SIBLINGS="$(printf '%s\n' "$ALL_STACKS" | grep -E "^${CHINTAN_PREFIX}${INSTANCE}-" | grep -v "^${STACK}\$" || true)"
if [ -n "$SIBLINGS" ]; then
    log ""
    warn "other stacks still use /chintan/${INSTANCE}/*, so the provider secrets stay:"
    for s in $SIBLINGS; do dim "  $s"; done
fi

log ""
if ! confirm_apply "$APPLY" "delete $STACK, its retained resources, and all of its data"; then
    exit 0
fi

confirm_destructive "DELETE ${STACK}" \
    "This permanently deletes every note, transcript and audio recording in ${STACK}." \
    "It also deletes the Cognito user pool, so every identity and passkey goes with it." \
    "DynamoDB point-in-time recovery does not survive table deletion."

# ---------------------------------------------------------------------------
# Empty buckets before the stack delete
# ---------------------------------------------------------------------------
#
# CloudFormation cannot delete a non-empty bucket, and the failure aborts the
# whole stack delete part-way through.

for bucket in $BUCKETS; do
    empty_s3_bucket "$bucket"
done

# ---------------------------------------------------------------------------
# Cognito deletion protection
# ---------------------------------------------------------------------------
#
# DeletionProtection: ACTIVE is what stops an accidental `delete-user-pool` from
# taking every enrolled identity with it. Turning it off is therefore the point at
# which this run stops being reversible, and it is done here — deliberately, named
# in the log — rather than discovered as an API error later.

for pool in $USER_POOLS; do
    if aws_cli cognito-idp describe-user-pool --user-pool-id "$pool" >/dev/null 2>&1; then
        info "disabling deletion protection on user pool $pool"
        # update-user-pool resets any property not restated in the call. That is
        # normally a trap; here the pool is deleted moments later, so the only
        # exposure is a stack delete that fails in between. If that happens,
        # redeploy the stack before doing anything else — CloudFormation will put
        # the pool's configuration back.
        aws_cli cognito-idp update-user-pool --user-pool-id "$pool" \
            --deletion-protection INACTIVE >/dev/null
    fi
done

# ---------------------------------------------------------------------------
# Delete the stack
# ---------------------------------------------------------------------------

info "deleting stack $STACK"
aws_cli cloudformation delete-stack --stack-name "$STACK"
aws_cli cloudformation wait stack-delete-complete --stack-name "$STACK"
ok "stack deleted"

# ---------------------------------------------------------------------------
# Retained resources
# ---------------------------------------------------------------------------

for table in $TABLES; do
    if aws_cli dynamodb describe-table --table-name "$table" >/dev/null 2>&1; then
        info "deleting retained table $table"
        aws_cli dynamodb delete-table --table-name "$table" >/dev/null
        ok "table $table deleted"
    fi
done

for bucket in $BUCKETS; do
    delete_s3_bucket "$bucket"
done

for pool in $USER_POOLS; do
    if aws_cli cognito-idp describe-user-pool --user-pool-id "$pool" >/dev/null 2>&1; then
        info "deleting retained user pool $pool"
        aws_cli cognito-idp delete-user-pool --user-pool-id "$pool" >/dev/null
        ok "user pool $pool deleted"
    fi
done

# A CMK is SCHEDULED for deletion, never deleted: AWS gives no other option, and
# the 30-day window is chosen rather than the 7-day minimum on purpose, so a
# teardown that turns out to have been a mistake is still recoverable inside it.
# The template declares no KMS key today; this handles one an older stack left.
for key in $KMS_KEYS; do
    state="$(aws_cli kms describe-key --key-id "$key" --query 'KeyMetadata.KeyState' --output text 2>/dev/null || echo NONE)"
    case "$state" in
        Enabled | Disabled)
            info "scheduling deletion of KMS key $key (30-day window)"
            aws_cli kms schedule-key-deletion --key-id "$key" --pending-window-in-days 30 >/dev/null
            warn "to cancel within 30 days: aws kms cancel-key-deletion --key-id $key"
            ;;
        PendingDeletion) dim "  KMS key $key is already pending deletion" ;;
        NONE) dim "  KMS key $key not found" ;;
        *) dim "  KMS key $key is in state $state; leaving it alone" ;;
    esac
done

for group in $LOG_GROUPS; do
    if aws_cli logs describe-log-groups --log-group-name-prefix "$group" \
        --query 'logGroups[0].logGroupName' --output text 2>/dev/null | grep -qx "$group"; then
        info "deleting log group $group"
        aws_cli logs delete-log-group --log-group-name "$group" >/dev/null
    fi
done

# ---------------------------------------------------------------------------
# Provider secrets
# ---------------------------------------------------------------------------

if [ -n "$SIBLINGS" ]; then
    warn "leaving /chintan/${INSTANCE}/* in place: still used by $(printf '%s' "$SIBLINGS" | tr '\n' ' ')"
else
    info "removing SSM parameters under /chintan/${INSTANCE}/"
    while IFS= read -r param; do
        [ -n "$param" ] || continue
        info "deleting $param"
        aws_cli ssm delete-parameter --name "$param" >/dev/null
    done < <(aws_cli ssm get-parameters-by-path --path "/chintan/${INSTANCE}/" --recursive \
        --query 'Parameters[].Name' --output text 2>/dev/null | tr '\t' '\n' | grep -v '^$' || true)
fi

log ""
ok "cleanup of $STACK complete"
