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
#       [--no-smoke] [--allow-replacement LogicalId ...] [--apply]
#
# --apply is required to execute the change set; without it the change set is
# created, printed, and deleted. That is the same dry-run-by-default rule the
# other scripts follow, and it makes the workflow's plan step free.
#
# A change set that REPLACES a stateful resource — the user pool, the table, the
# content bucket, the user pool client — is refused even with --apply. Retain
# keeps the old resource alive, but the stack switches to an empty one: tenant
# ids are Cognito subs, so a replaced pool makes every note unreachable, and
# staging cannot catch it because an empty pool passes the health smoke. Pass
# --allow-replacement <LogicalId> for each resource you have decided to replace.

# shellcheck source-path=SCRIPTDIR source=lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"

INSTANCE=""
ENVIRONMENT=""
TEMPLATE=""
SMOKE=1
SELF_TEST=0
PARAMETERS=()
TAGS=()
ALLOW_REPLACEMENT=()

# The resources whose replacement loses data or identity. Everything else in
# the template can be replaced and recreated from the template alone.
STATEFUL_RESOURCES=(UserPool UserPoolClient UserPoolDomain DynamoDBTable ContentBucket)

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
        --allow-replacement)
            ALLOW_REPLACEMENT+=("${2:?--allow-replacement needs a LogicalResourceId}")
            shift
            ;;
        --self-test) SELF_TEST=1 ;;
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

# FAILURE_EVENT_JQ selects the failures worth reading out of a full
# describe-stack-events response. Two corrections to the obvious version:
#
# Oldest failure FIRST. Events come back newest-first and a failure cascades, so
# the newest ten are the rollback and the cancellations while the resource that
# actually broke has been pushed off the end. The first FAILED event is the
# cause; everything after it is a consequence.
#
# Bounded to the CURRENT operation. Every FAILED event in the stack's whole
# history is in this response, so an unfiltered list re-reports failures from
# deploys that were fixed weeks ago as though they were today's.
#
# The operation starts at the most recent stack-level CREATE_IN_PROGRESS or
# UPDATE_IN_PROGRESS — and at those two exactly. Matching any stack-level
# *_IN_PROGRESS instead picks UPDATE_ROLLBACK_IN_PROGRESS, which CloudFormation
# emits AFTER the resource failure, so the root cause falls outside the window
# and the function prints nothing at all.
FAILURE_EVENT_JQ='
    (.StackEvents
     | map(select(.ResourceType == "AWS::CloudFormation::Stack"
                  and (.ResourceStatus == "CREATE_IN_PROGRESS"
                       or .ResourceStatus == "UPDATE_IN_PROGRESS")))
     | max_by(.Timestamp) | .Timestamp // "") as $since
    | .StackEvents
    | map(select(.Timestamp >= $since
                 and (.ResourceStatus | endswith("_FAILED"))
                 and ((.ResourceStatusReason // "") | test("cancelled") | not)))
    | sort_by(.Timestamp)
    | .[0:10]
    | .[]
    | "  \(.LogicalResourceId) (\(.ResourceType))\n      \(.ResourceStatusReason // "no reason given")"
'

# ---------------------------------------------------------------------------
# Self-test (§0.5A): prove the failure report reports the failure
# ---------------------------------------------------------------------------
#
# This runs before the CI guard because its whole point is to be runnable from a
# laptop and from a pull request, where no stack and no credentials exist. It
# exercises FAILURE_EVENT_JQ against a fixture shaped like a real failed update:
# an old failure from a deploy that was fixed weeks ago, a successful update
# after it, then today's operation with one root cause, a cancellation cascade,
# and the rollback events that follow.
# refuse_stateful_replacements reads a describe-change-set response and returns
# non-zero if it would replace a resource in STATEFUL_RESOURCES that was not
# named in --allow-replacement. .Replacement is "True", "False" or
# "Conditional"; Conditional means CloudFormation only decides at execution
# time, which is too late to ask, so it is treated as a replacement.
refuse_stateful_replacements() {
    local describe="$1" replacing id r a stateful allowed refused=0
    replacing="$(printf '%s' "$describe" | jq -r '
        [.Changes[].ResourceChange
         | select(.Action == "Modify" and (.Replacement == "True" or .Replacement == "Conditional"))
         | .LogicalResourceId] | .[]
    ')"
    while IFS= read -r id; do
        [ -n "$id" ] || continue
        stateful=0
        for r in "${STATEFUL_RESOURCES[@]}"; do [ "$id" = "$r" ] && stateful=1; done
        [ "$stateful" = "1" ] || continue
        allowed=0
        for a in "${ALLOW_REPLACEMENT[@]+"${ALLOW_REPLACEMENT[@]}"}"; do [ "$id" = "$a" ] && allowed=1; done
        if [ "$allowed" = "1" ]; then
            warn "replacing $id because --allow-replacement $id was passed"
        else
            err "change set would REPLACE $id, a stateful resource; refusing"
            refused=1
        fi
    done <<<"$replacing"
    [ "$refused" = "0" ]
}

if [ "$SELF_TEST" = "1" ]; then
    require_cmd jq
    info "self-test: the failure report names the resource that actually broke"
    fixture="$(
        cat <<'JSON'
{"StackEvents":[
 {"Timestamp":"2026-08-08T12:00:09Z","LogicalResourceId":"chintan-dev-staging","ResourceType":"AWS::CloudFormation::Stack","ResourceStatus":"UPDATE_ROLLBACK_IN_PROGRESS","ResourceStatusReason":"The following resource(s) failed to update: [ContentBucket]"},
 {"Timestamp":"2026-08-08T12:00:08Z","LogicalResourceId":"WorkerFunction","ResourceType":"AWS::Lambda::Function","ResourceStatus":"UPDATE_FAILED","ResourceStatusReason":"Resource update cancelled"},
 {"Timestamp":"2026-08-08T12:00:07Z","LogicalResourceId":"ContentBucket","ResourceType":"AWS::S3::Bucket","ResourceStatus":"UPDATE_FAILED","ResourceStatusReason":"Unable to validate the following destination configurations"},
 {"Timestamp":"2026-08-08T12:00:00Z","LogicalResourceId":"chintan-dev-staging","ResourceType":"AWS::CloudFormation::Stack","ResourceStatus":"UPDATE_IN_PROGRESS","ResourceStatusReason":"User Initiated"},
 {"Timestamp":"2026-07-01T09:00:00Z","LogicalResourceId":"chintan-dev-staging","ResourceType":"AWS::CloudFormation::Stack","ResourceStatus":"UPDATE_COMPLETE","ResourceStatusReason":null},
 {"Timestamp":"2026-06-01T08:00:01Z","LogicalResourceId":"AncientTypo","ResourceType":"AWS::SQS::Queue","ResourceStatus":"CREATE_FAILED","ResourceStatusReason":"a failure from weeks ago that was fixed"},
 {"Timestamp":"2026-06-01T08:00:00Z","LogicalResourceId":"chintan-dev-staging","ResourceType":"AWS::CloudFormation::Stack","ResourceStatus":"CREATE_IN_PROGRESS","ResourceStatusReason":"User Initiated"}
]}
JSON
    )"
    selected="$(printf '%s' "$fixture" | jq -r "$FAILURE_EVENT_JQ")"

    printf '%s' "$selected" | head -n 2 >&2
    case "$selected" in
        *AncientTypo*)
            err "self-test FAILED: a failure from a previous, already-fixed deploy was reported as today's"
            exit 1
            ;;
    esac
    ok "a failure from an earlier operation is not reported"

    case "$selected" in
        *"Resource update cancelled"*)
            err "self-test FAILED: a cancellation was reported as a cause"
            exit 1
            ;;
    esac
    ok "cascade cancellations are not reported as causes"

    case "$(printf '%s' "$selected" | head -n 1)" in
        *ContentBucket*) ok "the root cause is reported first" ;;
        *)
            err "self-test FAILED: the first line is not the resource that broke"
            err "got: $(printf '%s' "$selected" | head -n 1)"
            exit 1
            ;;
    esac

    # And the premise: a fixture with no failures must produce nothing, or the
    # assertions above would pass on an empty string.
    empty="$(printf '%s' '{"StackEvents":[{"Timestamp":"2026-08-08T12:00:00Z","LogicalResourceId":"s","ResourceType":"AWS::CloudFormation::Stack","ResourceStatus":"UPDATE_IN_PROGRESS","ResourceStatusReason":"User Initiated"}]}' | jq -r "$FAILURE_EVENT_JQ")"
    if [ -n "$empty" ]; then
        err "self-test inconclusive: a clean operation reported failures"
        exit 1
    fi
    ok "self-test: a clean operation reports nothing"

    info "self-test: a change set that replaces the user pool is refused"
    replacing_pool='{"Changes":[
     {"ResourceChange":{"Action":"Modify","LogicalResourceId":"UserPool","ResourceType":"AWS::Cognito::UserPool","Replacement":"True"}},
     {"ResourceChange":{"Action":"Modify","LogicalResourceId":"ApiLambdaFunction","ResourceType":"AWS::Lambda::Function","Replacement":"False"}}]}'
    if refuse_stateful_replacements "$replacing_pool" 2>/dev/null; then
        die "self-test FAILED: a user pool replacement was not refused"
    fi
    ok "a user pool replacement is refused"

    info "self-test: a Conditional replacement of the table is refused"
    conditional_table='{"Changes":[
     {"ResourceChange":{"Action":"Modify","LogicalResourceId":"DynamoDBTable","ResourceType":"AWS::DynamoDB::Table","Replacement":"Conditional"}}]}'
    if refuse_stateful_replacements "$conditional_table" 2>/dev/null; then
        die "self-test FAILED: a Conditional table replacement was not refused"
    fi
    ok "a Conditional table replacement is refused"

    info "self-test: replacing a stateless resource, or a modify in place, is allowed"
    harmless='{"Changes":[
     {"ResourceChange":{"Action":"Modify","LogicalResourceId":"ApiLambdaFunction","ResourceType":"AWS::Lambda::Function","Replacement":"False"}},
     {"ResourceChange":{"Action":"Modify","LogicalResourceId":"UserPool","ResourceType":"AWS::Cognito::UserPool","Replacement":"False"}},
     {"ResourceChange":{"Action":"Modify","LogicalResourceId":"ApiLogGroup","ResourceType":"AWS::Logs::LogGroup","Replacement":"True"}},
     {"ResourceChange":{"Action":"Add","LogicalResourceId":"NewAlarm","ResourceType":"AWS::CloudWatch::Alarm"}}]}'
    refuse_stateful_replacements "$harmless" 2>/dev/null ||
        die "self-test FAILED: a harmless change set was refused"
    ok "in-place modifications and stateless replacements pass"

    info "self-test: --allow-replacement admits exactly the named resource"
    ALLOW_REPLACEMENT=(UserPool)
    refuse_stateful_replacements "$replacing_pool" 2>/dev/null ||
        die "self-test FAILED: --allow-replacement UserPool did not admit the replacement"
    if refuse_stateful_replacements "$conditional_table" 2>/dev/null; then
        die "self-test FAILED: --allow-replacement UserPool admitted a table replacement"
    fi
    ALLOW_REPLACEMENT=()
    ok "--allow-replacement is per resource"
    exit 0
fi

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
    local events
    warn "deploy failed; the failing resources were:"
    # Not silenced. A denied describe-stack-events used to print the heading
    # above and nothing under it, which reads as "no resource failed" — the
    # single most misleading thing this function could say, at the exact moment
    # the operator is trying to find out what broke.
    if ! events="$(aws_cli cloudformation describe-stack-events --stack-name "$STACK" --output json 2>&1)"; then
        err "  could not read the stack events: ${events##*$'\n'}"
        err "  the deploy still failed; look at $STACK in the CloudFormation console"
        return 0
    fi
    local selected
    selected="$(printf '%s' "$events" | jq -r "$FAILURE_EVENT_JQ")"
    if [ -z "$selected" ]; then
        # Also not silent. "Nothing matched" and "the stack failed for a reason
        # this filter does not model" look identical otherwise.
        err "  no resource-level failure was recorded for this operation"
        err "  look at $STACK in the CloudFormation console"
        return 0
    fi
    printf '%s\n' "$selected" >&2
}

# Resources carrying DeletionPolicy: Retain survive a stack delete. In production
# that is the point -- deleting the stack must not destroy the notes or the user
# pool the tenant ids come from. But when a create has NEVER succeeded, those
# survivors are debris, and they block the retry: bucket names and Cognito
# domains are globally or account unique, so the next create collides with its
# own wreckage.
#
# This deliberately does NOT delete them. The permissions boundary denies
# irreversible deletes to any principal not acting through CloudFormation
# (DenyIrreversibleDeletesOutsideCloudFormation), so a cleanup here is denied by
# design -- and an earlier version of this function swallowed those denials and
# reported success, which is worse than not trying. Destroying data is a human
# action with elevated credentials, same as scripts/bootstrap-agent.sh.
report_retained_orphans() {
    local account name found=0 blind=0 r
    if ! account="$(aws_cli sts get-caller-identity --query Account --output text 2>/dev/null)" ||
        [ -z "$account" ]; then
        warn "  could not resolve the account id, so retained resources were NOT checked"
        return 0
    fi

    name="${INSTANCE}-${ENVIRONMENT}"

    # Each probe answers present / absent / unknown. `unknown` is reported as a
    # blind spot rather than folded into "nothing found": head-bucket answers
    # 403 rather than 404 when the caller may not look, and treating that as
    # absent is how a report of "no orphans" gets printed over a bucket that is
    # about to collide with the next create.
    for r in \
        "$(probe_bucket "chintan-content-${name}-${account}")" \
        "$(probe_table "chintan-${name}")" \
        "$(probe_user_pool "chintan-${name}")"; do
        case "$(probe_state "$r")" in
            present)
                warn "  retained: $(probe_detail "$r")"
                found=1
                ;;
            unknown)
                err "  could not check: $(probe_detail "$r")"
                blind=1
                ;;
        esac
    done

    if [ "$found" = 1 ]; then
        warn "these survived a failed create and will collide with the next one."
        warn "clear them with elevated credentials, then re-run:"
        warn "  scripts/clean-instance-orphans.sh --instance ${INSTANCE} --environment ${ENVIRONMENT} --apply"
    fi
    if [ "$blind" = 1 ]; then
        warn "one or more probes were denied — 'no orphans' above is NOT a clean bill of health."
        warn "re-run scripts/clean-instance-orphans.sh with credentials that can read them."
    fi
}

clear_failed_create() {
    local status out
    status="$(aws_cli cloudformation describe-stacks --stack-name "$STACK" \
        --query 'Stacks[0].StackStatus' --output text 2>/dev/null || echo NONE)"
    case "$status" in
        ROLLBACK_COMPLETE | REVIEW_IN_PROGRESS)
            warn "stack $STACK is in $status from a failed first create; deleting it so this deploy can proceed"
            warn "any resource with DeletionPolicy: Retain survives that delete — check for orphans if a create keeps colliding"

            # Neither call is silenced, and neither is `|| true`. The first
            # version of this function was both: a denied delete-stack, or a
            # delete that ended in DELETE_FAILED, was swallowed, execution fell
            # through to stack_exists (still true), CHANGE_SET_TYPE became
            # UPDATE, and CloudFormation refused with the very
            # "is in ROLLBACK_COMPLETE state and can not be updated" this
            # function exists to prevent. The deploy then failed for a reason
            # that pointed at the wrong thing entirely.
            if ! out="$(aws_cli cloudformation delete-stack --stack-name "$STACK" 2>&1)"; then
                err "could not delete $STACK: ${out##*$'\n'}"
                report_retained_orphans
                die "$STACK is in $status and must be deleted before this deploy can proceed; the delete was refused (see above)"
            fi
            if ! out="$(aws_cli cloudformation wait stack-delete-complete --stack-name "$STACK" 2>&1)"; then
                err "waiting for $STACK to delete failed: ${out##*$'\n'}"
            fi

            # Verify, rather than assume. `wait` returning 0 is not the same as
            # the stack being gone, and DELETE_FAILED is the case that matters:
            # it leaves the stack in place, and the next deploy would inherit
            # exactly the confusion this function removes.
            status="$(aws_cli cloudformation describe-stacks --stack-name "$STACK" \
                --query 'Stacks[0].StackStatus' --output text 2>/dev/null || echo NONE)"
            if [ "$status" != "NONE" ]; then
                report_retained_orphans
                die "$STACK is still present in status $status after the delete; clear it before deploying"
            fi
            ok "$STACK deleted; this deploy will create it fresh"
            report_retained_orphans
            ;;
        DELETE_FAILED)
            # Deleting again without --retain-resources repeats the same
            # failure. Which resources to retain is a judgement call about data,
            # so this stops and says so rather than guessing.
            report_retained_orphans
            die "$STACK is in DELETE_FAILED. Delete it by hand, retaining whatever must survive:
  aws cloudformation delete-stack --stack-name $STACK --retain-resources <LogicalId> ...
The resources that blocked the delete are named in the stack events."
            ;;
        UPDATE_ROLLBACK_FAILED)
            # The only legal move on this state. An update change set against it
            # is rejected, and the rejection does not say this is why.
            die "$STACK is in UPDATE_ROLLBACK_FAILED and cannot be updated until its rollback completes. Run:
  aws cloudformation continue-update-rollback --stack-name $STACK
adding --resources-to-skip <LogicalId> for any resource whose rollback is what is stuck."
            ;;
        *_IN_PROGRESS)
            # A concurrent deploy, or one that was cancelled mid-flight. Every
            # call below would fail against it with a less clear message.
            die "$STACK is in $status — another operation is running against it. Wait for it to finish, or resolve it, then re-run."
            ;;
    esac
}

# CloudFormation caps an inline --template-body at 51200 bytes. Past that the
# call fails with "Member must have length less than or equal to 51200", which
# names a constraint and not the template, and the natural response — deleting
# comments until it fits — trades documentation for a limit that has a proper
# answer. A template staged in S3 may be 1MB, so it is always staged: no cliff to
# cross, and no behaviour that changes the first time the file grows a paragraph.
#
# The artifact bucket already exists and already holds the function zips; the
# deploy role can read it, and CloudFormation reads the template as that role.
stage_template() {
    local account bucket key region
    account="$(aws_cli sts get-caller-identity --query Account --output text 2>/dev/null || echo '')"
    [ -n "$account" ] || die "could not resolve the account id to stage the template"
    # The same region aws_cli acts in. Built from AWS_REGION alone, this named a
    # different bucket than the upload went to whenever CHINTAN_REGION was set.
    region="$(aws_region)"
    [ -n "$region" ] || die "no region resolved; set AWS_REGION (or CHINTAN_REGION) to the artifact bucket's region"
    bucket="chintan-lambda-${account}-${region}"
    # Bucket root, not a templates/ prefix: the function zips are written to the
    # root and that path is known to work, whereas a new prefix would be the
    # first thing this role has ever written there. simulate-principal-policy
    # reports explicitDeny for every key in this bucket including the zips it
    # demonstrably uploads, so it cannot be used to check a new prefix — the
    # deny is conditional and the simulator has no context keys to evaluate it.
    key="${STACK}-$(date -u +%Y%m%dT%H%M%SZ)-$$.template.yaml"

    aws_cli s3 cp "$TEMPLATE" "s3://${bucket}/${key}" >/dev/null ||
        die "could not stage the template to s3://${bucket}/${key}"
    TEMPLATE_URL="https://${bucket}.s3.${region}.amazonaws.com/${key}"
    info "template staged: s3://${bucket}/${key}"
}

# Clear a poisoned stack BEFORE deciding CREATE vs UPDATE. Deciding first and
# deleting after leaves CHANGE_SET_TYPE=UPDATE pointing at a stack that no
# longer exists, and the deploy dies on "Stack [...] does not exist" — a fresh
# error with nothing to do with the original failure.
clear_failed_create
stage_template

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
    --template-url "$TEMPLATE_URL" \
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

    # Refuse to replace a stateful resource unless told to, by name.
    if ! refuse_stateful_replacements "$DESCRIBE"; then
        err "a replaced user pool, table or bucket leaves the stack pointing at an empty one while Retain keeps the data in the old one."
        err "If that is really intended, re-run with --allow-replacement <LogicalId> for each, and plan the data migration first."
        cleanup_change_set
        exit 1
    fi

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
    # /health/ready round-trips DynamoDB and S3 under the Lambda's own role. The
    # liveness probe alone passed a multi-day outage in which the API could not
    # read the index it had just been deployed against (see the gsi2 note on
    # the table in the template).
    info "smoke: GET ${endpoint}/v1/health/ready"
    curl -fsS --max-time 20 "${endpoint}/v1/health/ready" >&2
    log ""
    ok "smoke passed for $STACK"
fi
