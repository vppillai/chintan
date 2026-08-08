#!/usr/bin/env bash
#
# Remove every Chintan instance stack, then the bootstrap stack.
#
# WHAT THIS DOES NOT TOUCH, AND WHY
#
# The agent principal, its permissions boundary, the deny policies, the CloudTrail
# trail and the CloudTrail bucket are created by scripts/bootstrap-agent.sh and
# are deliberately out of scope here. They are the record of what was done to the
# account, including by this script. v1 swept for buckets whose name began with
# "chintan-" and emptied every match, which included
# chintan-cloudtrail-<account>-<region>: the first thing teardown did was destroy
# the audit trail that would have shown what teardown did. Removing the agent
# principal is a deliberate, separate, human act.
#
# HOW RESOURCES ARE FOUND
#
# Stacks are enumerated by name prefix — that is safe, because a stack is the unit
# of ownership. Resources are then found inside each stack. No resource is ever
# deleted because its name looked like ours.
#
# A chintan-* stack whose name does not parse as chintan-<instance>-<environment>
# is reported and skipped rather than guessed at. v1 stripped only "chintan-" from
# "chintan-dev-prod", derived the instance name "dev-prod", and deleted SSM
# parameters under /chintan/dev-prod/ — a path that had never existed — leaving the
# live keys at /chintan/dev/ untouched while reporting success.
#
# Usage:
#   scripts/teardown.sh [--region R] [--yes] [--apply]
#
# Options:
#   --region REGION   AWS region                        (default: $AWS_REGION)
#   --yes             skip the typed confirmations
#   --apply           execute; without it nothing is deleted

# shellcheck source-path=SCRIPTDIR source=lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"

while [ $# -gt 0 ]; do
    case "$1" in
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

require_aws
require_cmd jq

# ---------------------------------------------------------------------------
# Plan
# ---------------------------------------------------------------------------

STACKS="$(list_chintan_stacks)"
INSTANCE_STACKS=()
UNPARSEABLE=()
HAS_BOOTSTRAP=0

while IFS= read -r stack; do
    [ -n "$stack" ] || continue
    if [ "$stack" = "$CHINTAN_BOOTSTRAP_STACK" ]; then
        HAS_BOOTSTRAP=1
        continue
    fi
    if parse_stack_name "$stack"; then
        INSTANCE_STACKS+=("$stack")
    else
        UNPARSEABLE+=("$stack")
    fi
done <<<"$STACKS"

info "plan"
if [ "${#INSTANCE_STACKS[@]}" -eq 0 ]; then
    dim "  no instance stacks found"
else
    for s in "${INSTANCE_STACKS[@]}"; do
        parse_stack_name "$s"
        dim "  delete instance stack  $s  (instance=$STACK_INSTANCE environment=$STACK_ENV)"
    done
fi
[ "$HAS_BOOTSTRAP" = "1" ] && dim "  delete bootstrap stack $CHINTAN_BOOTSTRAP_STACK"
dim "  KEEP  the CloudTrail trail, its bucket, and the agent IAM principal"

if [ "${#UNPARSEABLE[@]}" -gt 0 ]; then
    log ""
    warn "these chintan-* stacks do not parse as chintan-<instance>-<environment> and are SKIPPED:"
    for s in "${UNPARSEABLE[@]}"; do warn "  $s"; done
    warn "delete them by hand if they are yours; this script will not guess an instance name"
fi

if [ "${#INSTANCE_STACKS[@]}" -eq 0 ] && [ "$HAS_BOOTSTRAP" = "0" ]; then
    log ""
    ok "nothing to tear down"
    exit 0
fi

log ""
if ! confirm_apply "$APPLY" "delete every Chintan stack listed above and all of their data"; then
    exit 0
fi

confirm_destructive "DELETE ALL CHINTAN RESOURCES" \
    "This permanently deletes every note, transcript and audio recording in every instance." \
    "It cannot be undone. The CloudTrail record of it is preserved."

# Nested cleanup runs inherit both switches, so neither stops to ask again and
# neither silently falls back to a dry run half-way through.
export ASSUME_YES=1
export APPLY

# ---------------------------------------------------------------------------
# Instance stacks
# ---------------------------------------------------------------------------

for stack in "${INSTANCE_STACKS[@]}"; do
    parse_stack_name "$stack"
    log ""
    info "tearing down $stack"
    "$REPO_ROOT/scripts/cleanup-aws.sh" \
        --instance "$STACK_INSTANCE" \
        --environment "$STACK_ENV" \
        --yes --apply
done

# ---------------------------------------------------------------------------
# Bootstrap stack
# ---------------------------------------------------------------------------

if [ "$HAS_BOOTSTRAP" = "1" ]; then
    log ""
    info "tearing down $CHINTAN_BOOTSTRAP_STACK"

    # Only the buckets this stack owns. assert_not_protected_bucket inside
    # empty_s3_bucket refuses the CloudTrail bucket even if something one day
    # imports it into a stack.
    while IFS= read -r bucket; do
        [ -n "$bucket" ] || continue
        empty_s3_bucket "$bucket"
    done < <(stack_resources_of_type "$CHINTAN_BOOTSTRAP_STACK" 'AWS::S3::Bucket')

    aws_cli cloudformation delete-stack --stack-name "$CHINTAN_BOOTSTRAP_STACK"
    aws_cli cloudformation wait stack-delete-complete --stack-name "$CHINTAN_BOOTSTRAP_STACK"
    ok "$CHINTAN_BOOTSTRAP_STACK deleted"
fi

# ---------------------------------------------------------------------------
# Orphan report — read-only, on purpose
# ---------------------------------------------------------------------------
#
# Anything left that carries the chintan- prefix but belonged to no stack is
# REPORTED, never deleted. An orphan means CloudFormation lost track of a
# resource, and the right response to that is a human look, not a wider blast
# radius. This is the section that used to delete the audit bucket.

log ""
info "orphan report (nothing below is deleted)"

orphans=0
report_orphan() {
    warn "  orphan: $1"
    orphans=$((orphans + 1))
}

while IFS= read -r bucket; do
    [ -n "$bucket" ] || continue
    case "$bucket" in
        "${CHINTAN_PREFIX}cloudtrail-"*)
            dim "  keeping audit bucket $bucket"
            continue
            ;;
    esac
    report_orphan "s3://$bucket"
done < <(aws_cli s3api list-buckets \
    --query "Buckets[?starts_with(Name, \`${CHINTAN_PREFIX}\`)].Name" --output text 2>/dev/null |
    tr '\t' '\n' | grep -v '^$' || true)

while IFS= read -r table; do
    [ -n "$table" ] || continue
    report_orphan "dynamodb table $table"
done < <(aws_cli dynamodb list-tables \
    --query "TableNames[?starts_with(@, \`${CHINTAN_PREFIX}\`)]" --output text 2>/dev/null |
    tr '\t' '\n' | grep -v '^$' || true)

while IFS= read -r param; do
    [ -n "$param" ] || continue
    report_orphan "ssm parameter $param"
done < <(aws_cli ssm get-parameters-by-path --path "/${SYSTEM_ID}/" --recursive \
    --query 'Parameters[].Name' --output text 2>/dev/null |
    tr '\t' '\n' | grep -v '^$' || true)

if [ "$orphans" -eq 0 ]; then
    ok "no orphaned resources"
else
    log ""
    warn "$orphans orphaned resource(s) remain. Delete them deliberately, by name:"
    dim "  aws s3 rb s3://<bucket> --force"
    dim "  aws dynamodb delete-table --table-name <table>"
    dim "  aws ssm delete-parameter --name <name>"
fi

log ""
ok "teardown complete"
info "still present, by design: the CloudTrail trail and bucket, and the agent IAM principal."
dim "  remove them with a deliberate, separate action if the account is being decommissioned."
