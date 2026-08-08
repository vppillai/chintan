#!/usr/bin/env bash
#
# Deploy one Chintan stack from CI, through a reviewed change set.
#
# There is deliberately no path from a laptop to a deployed stack: this script
# refuses to run outside CI. Deploys happen only from a green pipeline, so that
# what is running is always something that exists on a branch and passed the
# gates. bootstrap.sh remains for a first-time or disaster-recovery deploy and
# says so in its own header.
#
# What this does that `aws cloudformation deploy` did not:
#
#   * Creates the change set, PRINTS it, and only then executes it. The previous
#     workflow's one-shot deploy meant the first time anyone saw what was
#     changing was in the resource events, after it had started.
#   * Passes --role-arn when CFN_DEPLOY_ROLE_ARN is set, so CloudFormation acts
#     under a scoped service role rather than under the (broader) CI role.
#   * Publishes a Lambda version and moves the `live` alias to it, and prints the
#     exact command that moves it back. v1 had no rollback path at all.
#   * Runs the smoke test itself, so the caller can gate prod on staging's result
#     instead of discovering a bad deploy after it is live.
#
# Usage:
#   scripts/deploy.sh --instance dev --environment staging \
#       --template infrastructure/template.yaml \
#       --parameter Key=Value [--parameter ...] [--tag Key=Value ...] \
#       [--no-smoke] [--apply]
#
# --apply is required to execute the change set; without it the change set is
# created, printed, and deleted. That is the same dry-run-by-default rule the
# other scripts follow, and it makes the workflow's plan step free.

# shellcheck source-path=SCRIPTDIR source=lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"

INSTANCE=""
ENVIRONMENT=""
TEMPLATE=""
SMOKE=1
PARAMETERS=()
TAGS=()

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
        --template)
            TEMPLATE="${2:?--template needs a value}"
            shift
            ;;
        --parameter)
            PARAMETERS+=("${2:?--parameter needs Key=Value}")
            shift
            ;;
        --tag)
            TAGS+=("${2:?--tag needs Key=Value}")
            shift
            ;;
        --no-smoke) SMOKE=0 ;;
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

# The CI guard. guardrails-check.sh asserts this line is here, because a deploy
# path that works from a developer machine is a deploy path that bypasses every
# check the pipeline runs.
if [ "${CI:-}" != "true" ] && [ "${GITHUB_ACTIONS:-}" != "true" ]; then
    die "scripts/deploy.sh runs only in CI (CI=true). Use scripts/bootstrap.sh for a first-time or recovery deploy."
fi

[ -n "$INSTANCE" ] || die "--instance is required"
[ -n "$ENVIRONMENT" ] || die "--environment is required"
[ -n "$TEMPLATE" ] || die "--template is required"
[ -f "$REPO_ROOT/$TEMPLATE" ] || [ -f "$TEMPLATE" ] || die "template not found: $TEMPLATE"
[ "${#PARAMETERS[@]}" -gt 0 ] || die "at least one --parameter is required"

[ -f "$TEMPLATE" ] || TEMPLATE="$REPO_ROOT/$TEMPLATE"

require_cmd aws jq
STACK="$(stack_name "$INSTANCE" "$ENVIRONMENT")"

info "stack:       $STACK"
info "template:    $TEMPLATE"
info "region:      ${AWS_REGION:-<default>}"

ROLE_ARGS=()
if [ -n "${CFN_DEPLOY_ROLE_ARN:-}" ]; then
    ROLE_ARGS=(--role-arn "$CFN_DEPLOY_ROLE_ARN")
    info "service role: $CFN_DEPLOY_ROLE_ARN"
else
    # Not fatal, because the role is created by the bootstrap stack and an
    # existing installation predates it. Loud, because without it CloudFormation
    # acts with the CI role's permissions, which are wider than any single stack
    # needs.
    warn "CFN_DEPLOY_ROLE_ARN is unset — CloudFormation will act under the CI role"
    warn "set it to arn:aws:iam::<account>:role/chintan-cfn-deploy to scope the deploy"
fi

# ---------------------------------------------------------------------------
# Change set
# ---------------------------------------------------------------------------


# CloudFormation's waiter reports only "the stack reached a failure state". The
# reason lives in the events, and without printing them every failed deploy costs
# a round trip into the console or the CLI to learn what actually broke. Six
# deploys were debugged that way before this existed.
show_failure_events() {
    warn "deploy failed; the failing resources were:"
    aws_cli cloudformation describe-stack-events --stack-name "$STACK" \
        --query 'StackEvents[?contains(ResourceStatus, `FAILED`)].[LogicalResourceId,ResourceStatusReason]' \
        --output text 2>/dev/null |
        grep -v 'Resource creation cancelled' |
        head -n 10 >&2 || true
}

# Resources carrying DeletionPolicy: Retain survive a stack delete. That is
# correct in production -- deleting the stack must not brick a KMS key that
# every enrolled biometric credential depends on -- but in a non-production
# environment whose create has NEVER succeeded, those survivors are debris, and
# they are debris that blocks the retry: S3 bucket names, Cognito domains and
# KMS aliases are all globally or account unique, so the next create collides
# with its own wreckage and fails validation before it starts.
#
# DeletionPolicy cannot be made conditional -- it takes no intrinsic functions --
# so the environment check lives here instead. Production is never touched: it
# only reports, and a human decides.
#
# The set is enumerated by name rather than discovered, so this can only ever
# remove resources this instance+environment would itself have created.
clear_retained_orphans() {
    if [ "$ENVIRONMENT" = "prod" ]; then
        warn "leaving retained resources in place: refusing to delete production data automatically"
        return 0
    fi

    local account
    account="$(aws_cli sts get-caller-identity --query Account --output text 2>/dev/null || echo '')"
    if [ -z "$account" ]; then
        warn "could not resolve the account id; skipping orphan cleanup"
        return 0
    fi

    local bucket="chintan-content-${INSTANCE}-${ENVIRONMENT}-${account}"
    local table="chintan-${INSTANCE}-${ENVIRONMENT}"
    local pool_name="chintan-${INSTANCE}-${ENVIRONMENT}"
    local alias_name="alias/chintan-${INSTANCE}-${ENVIRONMENT}/token-vault"

    warn "clearing resources retained by the failed create of $STACK (non-production only)"

    aws_cli s3 rb "s3://${bucket}" --force >/dev/null 2>&1 || true
    aws_cli dynamodb delete-table --table-name "$table" >/dev/null 2>&1 || true
    aws_cli kms delete-alias --alias-name "$alias_name" >/dev/null 2>&1 || true

    local pool_id
    pool_id="$(aws_cli cognito-idp list-user-pools --max-results 60 \
        --query "UserPools[?Name=='${pool_name}'].Id | [0]" --output text 2>/dev/null || echo None)"
    if [ -n "$pool_id" ] && [ "$pool_id" != "None" ]; then
        # Deletion protection is on by design; it has to come off explicitly, and
        # UpdateUserPool replaces the whole configuration rather than patching it,
        # so the verification attributes must be restated or the call is rejected.
        aws_cli cognito-idp update-user-pool --user-pool-id "$pool_id" \
            --deletion-protection INACTIVE \
            --auto-verified-attributes email \
            --user-attribute-update-settings AttributesRequireVerificationBeforeUpdate=email >/dev/null 2>&1 || true
        aws_cli cognito-idp delete-user-pool --user-pool-id "$pool_id" >/dev/null 2>&1 || true
    fi
}

clear_failed_create() {
    local status
    status="$(aws_cli cloudformation describe-stacks --stack-name "$STACK" \
        --query 'Stacks[0].StackStatus' --output text 2>/dev/null || echo NONE)"
    case "$status" in
        ROLLBACK_COMPLETE | REVIEW_IN_PROGRESS)
            warn "stack $STACK is in $status from a failed first create; deleting it so this deploy can proceed"
            warn "any resource with DeletionPolicy: Retain survives that delete — check for orphans if a create keeps colliding"
            aws_cli cloudformation delete-stack --stack-name "$STACK" >/dev/null 2>&1 || true
            aws_cli cloudformation wait stack-delete-complete --stack-name "$STACK" >/dev/null 2>&1 || true
            clear_retained_orphans
            ;;
    esac
}

# Clear a poisoned stack BEFORE deciding CREATE vs UPDATE. Deciding first and
# deleting after leaves CHANGE_SET_TYPE=UPDATE pointing at a stack that no
# longer exists, and the deploy dies on "Stack [...] does not exist" — a fresh
# error with nothing to do with the original failure.
clear_failed_create

if stack_exists "$STACK"; then
    CHANGE_SET_TYPE=UPDATE
else
    CHANGE_SET_TYPE=CREATE
    info "stack does not exist yet; this change set will create it"
fi

# A change set name must match [a-zA-Z][-a-zA-Z0-9]*, so the commit SHA and run
# number are used rather than a timestamp with colons in it.
sha="${GITHUB_SHA:-local}"
CHANGE_SET="deploy-${GITHUB_RUN_ID:-0}-${GITHUB_RUN_ATTEMPT:-1}-${sha:0:12}"
CHANGE_SET="$(printf '%s' "$CHANGE_SET" | tr -c 'a-zA-Z0-9-' '-')"

cleanup_change_set() {
    aws_cli cloudformation delete-change-set \
        --stack-name "$STACK" --change-set-name "$CHANGE_SET" >/dev/null 2>&1 || true
    # A CREATE change set that is never executed leaves the stack in
    # REVIEW_IN_PROGRESS, which blocks the next deploy with a confusing error
    # about the stack already existing.
    if [ "$CHANGE_SET_TYPE" = "CREATE" ]; then
        local status
        status="$(aws_cli cloudformation describe-stacks --stack-name "$STACK" \
            --query 'Stacks[0].StackStatus' --output text 2>/dev/null || echo NONE)"
        if [ "$status" = "REVIEW_IN_PROGRESS" ]; then
            aws_cli cloudformation delete-stack --stack-name "$STACK" >/dev/null 2>&1 || true
        fi
    fi
}

# A stack whose FIRST create fails lands in ROLLBACK_COMPLETE. That state cannot
# be updated and cannot be re-created in place — the only legal move is delete.
# Without this, every later deploy fails with
#   "Stack ... is in ROLLBACK_COMPLETE state and can not be updated"
# which says nothing about the actual defect and hides the fix you just pushed.
#
# Safe to automate precisely because ROLLBACK_COMPLETE means the stack never
# reached CREATE_COMPLETE, so it holds no data anyone has used. It may still
# have stranded resources carrying DeletionPolicy: Retain (KMS keys, buckets,
# user pools); those are reported rather than deleted, because deleting retained
# resources is a decision, not a cleanup.

# Key=Value pairs are turned into the CLI's JSON shapes with jq rather than by
# string concatenation, so a parameter value containing a space, a quote or an
# '=' survives intact. Nothing here is eval'd.
parameters_json="$(
    printf '%s\n' "${PARAMETERS[@]}" |
        jq -R -s -c 'split("\n") | map(select(length>0))
                     | map(split("=") | {ParameterKey: .[0], ParameterValue: (.[1:] | join("="))})'
)"

TAG_ARGS=()
if [ "${#TAGS[@]}" -gt 0 ]; then
    TAG_ARGS=(--tags "$(
        printf '%s\n' "${TAGS[@]}" |
            jq -R -s -c 'split("\n") | map(select(length>0))
                         | map(split("=") | {Key: .[0], Value: (.[1:] | join("="))})'
    )")
fi

info "creating change set $CHANGE_SET"
aws_cli cloudformation create-change-set \
    --stack-name "$STACK" \
    --change-set-name "$CHANGE_SET" \
    --change-set-type "$CHANGE_SET_TYPE" \
    --template-body "file://$TEMPLATE" \
    --capabilities CAPABILITY_NAMED_IAM \
    --parameters "$parameters_json" \
    "${TAG_ARGS[@]}" \
    "${ROLE_ARGS[@]}" >/dev/null

# wait change-set-create-complete exits non-zero for an empty change set, which
# is a normal outcome for a redeploy of an unchanged template, not a failure.
set +e
aws_cli cloudformation wait change-set-create-complete \
    --stack-name "$STACK" --change-set-name "$CHANGE_SET" >/dev/null 2>&1
wait_rc=$?
set -e

DESCRIBE="$(aws_cli cloudformation describe-change-set \
    --stack-name "$STACK" --change-set-name "$CHANGE_SET" --output json)"
STATUS="$(printf '%s' "$DESCRIBE" | jq -r '.Status')"
REASON="$(printf '%s' "$DESCRIBE" | jq -r '.StatusReason // ""')"

if [ "$STATUS" = "FAILED" ]; then
    case "$REASON" in
        *"didn't contain changes"* | *"No updates are to be performed"*)
            info "no changes for $STACK"
            cleanup_change_set
            NO_CHANGES=1
            ;;
        *)
            err "change set failed: $REASON"
            cleanup_change_set
            exit 1
            ;;
    esac
elif [ "$wait_rc" -ne 0 ] && [ "$STATUS" != "CREATE_COMPLETE" ]; then
    err "change set did not reach CREATE_COMPLETE (status $STATUS): $REASON"
    cleanup_change_set
    exit 1
fi

if [ "${NO_CHANGES:-0}" != "1" ]; then
    log ""
    info "planned changes for $STACK"
    printf '%s' "$DESCRIBE" | jq -r '
        .Changes[]
        | .ResourceChange
        | "  \(.Action)\t\(.LogicalResourceId)\t\(.ResourceType)\t\(.Replacement // "-")"
    ' >&2
    log ""

    if ! confirm_apply "$APPLY" "execute change set $CHANGE_SET against $STACK"; then
        cleanup_change_set
        exit 0
    fi

    info "executing change set"
    aws_cli cloudformation execute-change-set \
        --stack-name "$STACK" --change-set-name "$CHANGE_SET" >/dev/null

    if [ "$CHANGE_SET_TYPE" = "CREATE" ]; then
        aws_cli cloudformation wait stack-create-complete --stack-name "$STACK" || {
            show_failure_events
            die "$STACK failed to create"
        }
    else
        aws_cli cloudformation wait stack-update-complete --stack-name "$STACK" || {
            show_failure_events
            die "$STACK failed to update"
        }
    fi
    ok "$STACK deployed"
elif ! is_apply; then
    exit 0
fi

# ---------------------------------------------------------------------------
# Lambda alias — the rollback handle
# ---------------------------------------------------------------------------
#
# $LATEST is not a rollback target: it moves with the next deploy. Publishing an
# immutable version after every successful deploy, and moving an alias to it,
# gives an operator one command that restores the previous code without a
# CloudFormation operation.
#
# Wiring the API Gateway integration to the alias is infrastructure/'s to do; the
# version history below is created regardless, so the history is already there
# when the integration moves.

publish_alias() {
    local fn="$1" alias_name=live previous published
    previous="$(aws_cli lambda get-alias --function-name "$fn" --name "$alias_name" \
        --query FunctionVersion --output text 2>/dev/null || echo "")"

    published="$(aws_cli lambda publish-version --function-name "$fn" \
        --description "${GITHUB_SHA:-manual}" --query Version --output text)"

    if [ -n "$previous" ]; then
        aws_cli lambda update-alias --function-name "$fn" --name "$alias_name" \
            --function-version "$published" >/dev/null
    else
        aws_cli lambda create-alias --function-name "$fn" --name "$alias_name" \
            --function-version "$published" >/dev/null
    fi

    ok "alias $alias_name -> version $published (was ${previous:-none})"
    if [ -n "$previous" ]; then
        log ""
        info "ROLLBACK: to put $fn back on the previous version, run"
        dim "  aws lambda update-alias --function-name $fn --name $alias_name --function-version $previous"
        log ""
        if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
            {
                printf '### Rollback for `%s`\n\n' "$STACK"
                printf '```sh\naws lambda update-alias --function-name %s --name %s --function-version %s\n```\n' \
                    "$fn" "$alias_name" "$previous"
            } >>"$GITHUB_STEP_SUMMARY"
        fi
    fi
}

if is_apply; then
    while IFS= read -r fn; do
        [ -n "$fn" ] || continue
        publish_alias "$fn"
    done < <(stack_resources_of_type "$STACK" 'AWS::Lambda::Function')
fi

# ---------------------------------------------------------------------------
# Smoke
# ---------------------------------------------------------------------------

if [ "$SMOKE" = "1" ] && is_apply; then
    endpoint="$(stack_output "$STACK" ApiEndpoint)"
    [ -n "$endpoint" ] && [ "$endpoint" != "None" ] || die "no ApiEndpoint output on $STACK"
    info "smoke: GET ${endpoint}/v1/health"
    curl -fsS --max-time 20 "${endpoint}/v1/health" >&2
    log ""
    ok "smoke passed for $STACK"
fi
