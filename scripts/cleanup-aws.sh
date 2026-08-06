#!/usr/bin/env bash
#
# Sweep orphaned resources left by failed deploys (§11.4, Phase 0).
#
# An "orphan" here has one precise meaning: a resource that carries the project tag set
# (§6.4) and that NO CloudFormation stack claims. That is the only definition that
# distinguishes rubbish from infrastructure — deleting something a stack still owns
# strands the stack, which is worse than the leak being swept, because the next deploy
# then fails on a resource CloudFormation believes exists (G-070 arrived at exactly that
# state by deleting a bucket out from under a stack).
#
# HOW A CLAIM IS ESTABLISHED, because everything here rests on it:
#   describe-stacks enumerates every stack the caller can see; describe-stack-resources
#   lists each stack's PhysicalResourceId. A tagged resource is CLAIMED when its ARN or
#   its physical identifier appears in that set. Resources of a stack that is mid-create
#   or mid-rollback are claimed too — describe-stack-resources reports them while the
#   operation is in flight, which is the case a name-matching sweep gets wrong.
#
# Identification is TAG-BASED, never name-based (§10.3: "Never a wildcard delete — a
# shared account makes an over-broad teardown catastrophic rather than merely annoying").
# This account also hosts passbook. The project-name prefix appears below only as a VETO,
# never as a selector: a candidate must be in the tag query AND carry the prefix. That is
# the ordering G-067 pushes towards — tag conditions do not authorize, but ARN and
# name-prefix scoping always does, so the prefix is the check that still holds when the
# tag is wrong.
#
# THIS SCRIPT CANNOT RELY ON IAM TO STOP IT (G-067). `aws:ResourceTag` is unsupported for
# authorization on cloudformation, dynamodb, lambda, logs, s3, iam, cognito-idp, events
# and resource-groups — very nearly everything here — so the Protected=true deny in the
# agent boundary is decorative for these services. The refusal is implemented in this
# script, in the open, and tested. Nothing underneath will catch a mistake.
#
# WHAT IT WILL NOT DELETE, ever, regardless of claim state:
#   - anything tagged Protected=true
#   - the DynamoDB table, the S3 buckets, the Cognito user pool: they hold the corpus
#     (I1), the audio (I2) and the identities. After a teardown these are RETAINED by
#     DeletionPolicy and therefore genuinely unclaimed — which is precisely why "unclaimed"
#     alone must never authorize a delete. Removing them is tenant erasure (§9.3), a
#     separately permissioned operation with its own audit record.
#   - the shared voicenotes-bootstrap stack, for the same reason teardown.sh spares it
#   - anything whose name does not carry the project prefix
#   - anything of a type this script has no delete handler for. It reports those instead:
#     per I16, if an operation is needed and no script exists, the script is written
#     first. A half-implemented delete path is how a sweep leaves a resource in a state
#     nobody planned for.
#
# It is a LIFECYCLE script, so per §11.3 it takes no --tenant: infrastructure has no
# tenant, and requiring the flag would mean inventing a meaningless value. It writes no
# audit record either (I13 covers access to user *content*); CloudTrail is the audit
# substrate for infrastructure mutation, which is why bootstrap-agent.sh enables it
# before the agent runs. It spends no provider money, so §11.3's cost-estimate and
# spend-breaker clauses do not apply and it emits no metering event (I12) — there is no
# tenant to attribute one to, and every action here reduces spend rather than incurring it.
#
# Usage:
#   scripts/cleanup-aws.sh                        # dry run, all instances
#   scripts/cleanup-aws.sh --instance dev         # dry run, one instance
#   scripts/cleanup-aws.sh --json                 # machine-readable plan
#   scripts/cleanup-aws.sh --region ca-central-1  # defaulted; shown for completeness
#   scripts/cleanup-aws.sh --apply                # execute the plan
#
# Prerequisites: AWS credentials for the agent principal (never root), the Project
# Resource Group from bootstrap.sh, and no stack operation in flight.
#
# Exit codes: 0 clean or plan printed, 1 a deletion failed, 2 bad arguments,
#             3 refused to run — root credentials, a deploy in flight, or an inventory
#               this script cannot prove. Distinct from 1 because "nothing happened" and
#               "something half-happened" call for different next actions.

# shellcheck source-path=SCRIPTDIR source=lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"

APPLY=0
AS_JSON=0
INSTANCE=""
REGION="ca-central-1"
while [ $# -gt 0 ]; do
    case "$1" in
        --apply) APPLY=1 ;;
        --json) AS_JSON=1 ;;
        --instance)
            INSTANCE="${2:?}"
            shift
            ;;
        --region)
            REGION="${2:?}"
            shift
            ;;
        -h | --help)
            sed -n '2,68p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
            exit 0
            ;;
        # Named explicitly so the refusal explains itself. §11.3: a lifecycle script must
        # NOT require --tenant, and someone who has been running data scripts all morning
        # will pass it out of habit.
        --tenant)
            err "--tenant is not accepted: this sweeps infrastructure, which has no tenant (§11.3)."
            err "A meaningless required argument is how a real one gets ignored. Use --instance to narrow."
            exit 2
            ;;
        *) die "unknown flag '$1' (see --help)" ;;
    esac
    shift
done
cd "$REPO_ROOT" || exit 1
export AWS_REGION="$REGION" AWS_PAGER=""

BOOTSTRAP_STACK="${SYSTEM_ID}-bootstrap"

# Physical identifiers of every resource some stack claims. A file rather than an array
# because it is looked up with grep -Fxq per candidate and it is the one structure whose
# completeness the safety argument depends on.
CLAIMED="$(mktemp)"
STACKS="$(mktemp)"
trap 'rm -f "$CLAIMED" "$STACKS"' EXIT

PLAN=()           # kind|identifier|arn — rendered by dry-run, executed by --apply, same order
REFUSED=()        # reason|arn          — deliberately out of reach
REPORTED=()       # reason|arn          — an operator's problem; no handler exists
UNREADABLE=()     # foreign stacks, tolerated
OWN_UNREADABLE=() # our own stacks, fatal — see the branch that fills it
IN_FLIGHT=()
DELETED=0
DELETE_FAILURES=0

# ---------------------------------------------------------------------------
# Preconditions
# ---------------------------------------------------------------------------

aws_cli sts get-caller-identity >/dev/null 2>&1 || die "no AWS credentials"
CALLER="$(aws_cli sts get-caller-identity | jq -r '.Arn // "unknown"')"
info "caller $CALLER"
info "region $REGION${INSTANCE:+, instance $INSTANCE}"

# The root user bypasses every deny in the boundary (§9.4), so a sweep as root has no
# backstop at all — and this script is the one place where "no backstop" and "deletes
# things" meet.
if printf '%s' "$CALLER" | grep -q ':root$'; then
    err "refusing to sweep as the account root user (§9.4): root bypasses every guardrail this script assumes."
    err "Re-run as the agent or deploy principal, whose boundary and explicit denies are the"
    err "backstop the plan below is written against."
    exit 3
fi

# ---------------------------------------------------------------------------
# Stack inventory — the claim set
# ---------------------------------------------------------------------------
#
# FAIL CLOSED. If the stack inventory cannot be read, every tagged resource looks
# unclaimed and the sweep would propose deleting the entire deployment. An empty claim set
# is indistinguishable from "nothing is claimed", so this refuses rather than guesses.
if ! stacks_json="$(aws_cli cloudformation describe-stacks 2>/dev/null)" ||
    ! printf '%s' "$stacks_json" | jq -e 'has("Stacks")' >/dev/null 2>&1; then
    err "cannot enumerate CloudFormation stacks."
    err "Refusing to continue: without the stack inventory every tagged resource looks"
    err "unclaimed, and this script would propose deleting the live deployment."
    exit 3
fi

printf '%s' "$stacks_json" |
    jq -r '.Stacks[]? | [.StackName, .StackId, .StackStatus,
                         (([.Tags[]? | select(.Key=="Project") | .Value] | first) // "-"),
                         (([.Tags[]? | select(.Key=="Instance") | .Value] | first) // "-")]
                        | @tsv' >"$STACKS"

info "reading resources of $(wc -l <"$STACKS" | tr -d ' ') stack(s) to establish claims"
while IFS=$'\t' read -r name id status project instance; do
    [ -n "$name" ] || continue
    if res_json="$(aws_cli cloudformation describe-stack-resources --stack-name "$id" 2>/dev/null)" &&
        printf '%s' "$res_json" | jq -e 'has("StackResources")' >/dev/null 2>&1; then
        printf '%s' "$res_json" | jq -r '.StackResources[]? | .PhysicalResourceId' >>"$CLAIMED"
    elif [ "$project" = "$SYSTEM_ID" ]; then
        # OUR OWN stack, and its resources are unknown. This must fail closed, and the
        # distinction from the foreign case below is the whole reason the two branches
        # exist: the veto argument that makes an unreadable foreign stack safe is that a
        # foreign stack cannot own a ${SYSTEM_ID}-* resource. It says nothing about OUR
        # stacks, whose resources are exactly the ones named with the prefix — so a
        # throttle or a transient error here silently empties the claim set for the live
        # deployment, and every live function then classifies as an orphan. The symptom
        # would be a plan proposing to delete the running API on a run that looked
        # ordinary apart from one warning line.
        OWN_UNREADABLE+=("${name} (${status})")
    else
        # Expected for another project's stacks: the agent's permissions scope
        # DescribeStackResources to stack/voicenotes-* (§9.5). Recorded rather than
        # ignored — see the veto argument at the classification step, which is what makes
        # an unreadable foreign stack safe to proceed past.
        UNREADABLE+=("$name")
    fi

    # A sweep during a deploy can delete a resource the deploy is midway through
    # creating: CloudFormation registers a resource only once its create event lands, so
    # there is a window where the resource exists, carries its tags, and is not yet
    # claimed. REVIEW_IN_PROGRESS is excluded deliberately — it ends in _IN_PROGRESS but
    # means "a change set was created and never executed", which is a cleanup target, not
    # an operation in flight.
    if [ "$project" = "$SYSTEM_ID" ] && [ "$status" != "REVIEW_IN_PROGRESS" ]; then
        case "$status" in
            *_IN_PROGRESS) IN_FLIGHT+=("${name} (${status})") ;;
        esac
    fi
done <"$STACKS"

if [ "${#OWN_UNREADABLE[@]}" -gt 0 ]; then
    err "cannot read the resources of ${SYSTEM_ID} stack(s): ${OWN_UNREADABLE[*]}"
    err "Refusing to continue: the claim set is incomplete for exactly the resources this"
    err "script is willing to delete, so the live deployment would classify as orphaned."
    err "Retry (a throttle clears), or investigate the permission if it persists."
    exit 3
fi

if [ "${#UNREADABLE[@]}" -gt 0 ]; then
    warn "${#UNREADABLE[@]} stack(s) could not be read: ${UNREADABLE[*]}"
    # Why this is not fatal: §10.3 makes the project name prefix mandatory for every
    # resource in this account ("This is mandatory, not stylistic"), so a stack belonging
    # to another project cannot own a resource named with THIS project's prefix. Every
    # deletion below requires that prefix, so an unreadable foreign stack cannot hide a
    # claim on anything this script will touch. If that premise ever breaks, this warning
    # is the thread to pull.
    dim "  a resource named ${SYSTEM_ID}-* cannot belong to another project's stack (§10.3),"
    dim "  and nothing without that prefix is deleted here — so the sweep stays sound."
fi

if [ "${#IN_FLIGHT[@]}" -gt 0 ]; then
    err "a deploy is in flight: ${IN_FLIGHT[*]}"
    err "Refusing to sweep. A resource created seconds ago is not yet claimed by its stack,"
    err "so sweeping now can delete what the deploy is midway through creating."
    err "Wait for the stack to settle and re-run."
    exit 3
fi

# ---------------------------------------------------------------------------
# Category A — stacks left unrecoverable by a failed deploy
# ---------------------------------------------------------------------------
#
# This is the orphan a failed deploy actually leaves. A stack in ROLLBACK_COMPLETE holds
# no usable resources and CloudFormation refuses to update it — the next deploy fails
# until it is deleted, which is exactly the "sweep after a failed deploy" this script
# exists for.
while IFS=$'\t' read -r name id status project instance; do
    [ "$project" = "$SYSTEM_ID" ] || continue
    [ -z "$INSTANCE" ] || [ "$instance" = "$INSTANCE" ] || continue

    case "$status" in
        ROLLBACK_COMPLETE | CREATE_FAILED | REVIEW_IN_PROGRESS) ;;
        DELETE_FAILED | ROLLBACK_FAILED | UPDATE_ROLLBACK_FAILED)
            # These need a decision this script has no basis to make: DELETE_FAILED
            # usually means a resource refused to go (a non-empty bucket), and
            # UPDATE_ROLLBACK_FAILED wants continue-update-rollback, not deletion.
            # Retrying delete-stack blindly would repeat the same failure and read as
            # progress.
            REPORTED+=("stack in ${status} needs an operator decision, not a sweep|${id}")
            continue
            ;;
        *) continue ;;
    esac

    if [ "$name" = "$BOOTSTRAP_STACK" ]; then
        # Shared across instances, and it owns the artifact bucket and the account-global
        # OIDC provider (G-016). teardown.sh spares it for the same reason.
        REPORTED+=("the shared bootstrap stack is in ${status}; delete it deliberately, not by sweep|${id}")
        continue
    fi
    PLAN+=("cloudformation:stack|${name} (${status})|${id}")
done <"$STACKS"

# ---------------------------------------------------------------------------
# Category B — tagged resources no stack claims
# ---------------------------------------------------------------------------

if ! tagged_json="$(aws_cli resourcegroupstaggingapi get-resources \
    --tag-filters "Key=Project,Values=${SYSTEM_ID}" 2>/dev/null)" ||
    ! printf '%s' "$tagged_json" | jq -e 'has("ResourceTagMappingList")' >/dev/null 2>&1; then
    err "cannot query the ${SYSTEM_ID} Resource Group."
    err "Refusing to continue: identification here is tag-based (§10.3), and without the"
    err "tag query the only remaining way to find candidates is name matching, which is"
    err "the wildcard-delete failure mode this script is built to avoid."
    exit 3
fi

# claimed_by returns success when some stack owns this resource. Both forms are checked
# because a PhysicalResourceId is sometimes the ARN and sometimes the bare name or opaque
# id, and which one depends on the resource type.
#
# The comparison is deliberately type-blind, and that costs precision in one direction
# only: IAM roles and Lambda functions share a name in this template (both are
# voicenotes-api-<instance>), so a claimed function's name makes an identically-named
# orphaned role look claimed. The error is a resource NOT swept, never one swept that was
# owned — which is the direction to be wrong in. Making it type-aware means threading
# ResourceType through the claim set, and buys only the deletion of a role this principal
# is denied from deleting anyway (§9.5).
claimed_by() {
    grep -Fxq "$1" "$CLAIMED" || grep -Fxq "$2" "$CLAIMED"
}

while IFS=$'\t' read -r arn protected instance; do
    [ -n "$arn" ] || continue
    [ -z "$INSTANCE" ] || [ "$instance" = "$INSTANCE" ] || continue

    # Protected first, before anything else is even evaluated. The tag is the marker for
    # "deleting this is a decision no automated sweep gets to make", and IAM will not
    # enforce it (G-067).
    if [ "$protected" = "true" ]; then
        REFUSED+=("tagged Protected=true|${arn}")
        continue
    fi

    # kind: what it is. ident: the physical identifier CloudFormation would report, which
    # is also what the delete API takes. prefix_ok: does the name carry the project
    # prefix. Extraction is per-type rather than "last path component" because log group
    # ARNs contain slashes after the final colon and a generic rule silently mis-parses
    # them.
    kind=""
    ident=""
    prefix_ok=0
    case "$arn" in
        arn:aws:cloudformation:*:stack/*)
            # Stacks are Category A's business. A stack is not a resource of itself, so it
            # is unclaimed by construction and a naive rule would sweep the live stack.
            continue
            ;;
        arn:aws:lambda:*:function:*)
            kind="lambda:function"
            ident="${arn##*:function:}"
            ;;
        arn:aws:logs:*:log-group:*)
            kind="logs:log-group"
            ident="${arn#*:log-group:}"
            ident="${ident%:\*}"
            ;;
        arn:aws:dynamodb:*:table/*)
            kind="dynamodb:table"
            ident="${arn##*:table/}"
            ;;
        arn:aws:s3:::*)
            kind="s3:bucket"
            ident="${arn##*:::}"
            ;;
        arn:aws:iam::*:role/*)
            kind="iam:role"
            ident="${arn##*:role/}"
            ;;
        arn:aws:cognito-idp:*:userpool/*)
            kind="cognito-idp:userpool"
            ident="${arn##*:userpool/}"
            ;;
        arn:aws:events:*:rule/*)
            kind="events:rule"
            ident="${arn##*:rule/}"
            ;;
        arn:aws:apigateway:*::/apis/*)
            kind="apigateway:api"
            ident="${arn##*/apis/}"
            ;;
        arn:aws:resource-groups:*:group/*)
            kind="resource-groups:group"
            ident="${arn##*:group/}"
            ;;
        *)
            kind="unknown"
            ident="$arn"
            ;;
    esac

    case "$kind" in
        logs:log-group) case "$ident" in "/aws/lambda/${SYSTEM_ID}-"*) prefix_ok=1 ;; esac ;;
        *) case "$ident" in "${SYSTEM_ID}-"*) prefix_ok=1 ;; esac ;;
    esac

    # Data and identities are refused before the claim question is even asked, because
    # after a teardown they are legitimately unclaimed — DeletionPolicy: Retain is exactly
    # what makes them so. "Unclaimed" is therefore never sufficient grounds.
    case "$kind" in
        dynamodb:table | s3:bucket)
            REFUSED+=("holds the corpus and the audio (I1, I2); deletion is erasure (§9.3)|${arn}")
            continue
            ;;
        cognito-idp:userpool)
            REFUSED+=("holds user identities; deleting it locks every user out|${arn}")
            continue
            ;;
    esac

    if claimed_by "$arn" "$ident"; then
        dim "  claimed  ${kind} ${ident}"
        continue
    fi

    # Two kinds cannot be judged by the prefix veto at all, and saying so beats letting
    # them fall through to the veto's message. An HTTP API's identifier is an opaque id
    # (`a1b2c3d4e5`) — never `voicenotes-*` however correctly it was named — and an
    # unrecognised ARN type has no identifier this script knows how to read. The veto
    # below would refuse both with "not named voicenotes-*", which reads as a naming
    # violation and sends the operator to inspect a convention that was never broken.
    case "$kind" in
        apigateway:api)
            REPORTED+=("orphaned ${kind} ${ident}; its identifier is opaque, so the §10.3 name-prefix veto cannot apply and no delete handler exists — write one first (I16)|${arn}")
            continue
            ;;
        unknown)
            REPORTED+=("tagged ${SYSTEM_ID} but of a resource type this script does not recognise; classify it before anything deletes it|${arn}")
            continue
            ;;
    esac

    if [ "$prefix_ok" != "1" ]; then
        # Tagged for this project but not named for it. Either a mistagged foreign
        # resource or a naming-convention violation — both are findings, neither is
        # something to delete on a tag alone (§10.3, G-067).
        REFUSED+=("tagged ${SYSTEM_ID} but not named ${SYSTEM_ID}-*; investigate, do not sweep|${arn}")
        continue
    fi

    case "$kind" in
        lambda:function)
            # The one Category B handler. A function delete is complete in one call, needs
            # no dependency ordering, recreates identically from the template, and the
            # agent boundary permits lambda:* on ${SYSTEM_ID}-* — so it can actually be
            # exercised rather than failing at 2am on a permission.
            PLAN+=("lambda:function|${ident}|${arn}")
            ;;
        logs:log-group | iam:role)
            # Denied to the agent outside CloudFormation by design
            # (DenyIrreversibleDeletesOutsideCloudFormation). Writing a handler that
            # cannot succeed would be worse than reporting it: it would fail every run and
            # train the operator to ignore the output.
            REPORTED+=("orphaned ${kind}; deleting it outside CloudFormation is denied to this principal by design (§9.5)|${arn}")
            ;;
        *)
            REPORTED+=("orphaned ${kind}; no delete handler exists — write one first (I16), do not improvise|${arn}")
            ;;
    esac
done < <(printf '%s' "$tagged_json" |
    jq -r '.ResourceTagMappingList[]? |
           [.ResourceARN,
            (([.Tags[]? | select(.Key=="Protected") | .Value] | first) // "-"),
            (([.Tags[]? | select(.Key=="Instance") | .Value] | first) // "-")] | @tsv')

# ---------------------------------------------------------------------------
# Plan, then apply
# ---------------------------------------------------------------------------

emit_json() {
    local dry="true"
    [ "$APPLY" = "1" ] && dry="false"
    local plan refused reported unreadable
    plan='[]'
    refused='[]'
    reported='[]'
    unreadable='[]'
    [ "${#PLAN[@]}" -gt 0 ] && plan="$(printf '%s\n' "${PLAN[@]}" |
        jq -R 'split("|") | {kind: .[0], identifier: .[1], arn: .[2]}' | jq -sc .)"
    [ "${#REFUSED[@]}" -gt 0 ] && refused="$(printf '%s\n' "${REFUSED[@]}" |
        jq -R 'split("|") | {reason: .[0], arn: .[1]}' | jq -sc .)"
    [ "${#REPORTED[@]}" -gt 0 ] && reported="$(printf '%s\n' "${REPORTED[@]}" |
        jq -R 'split("|") | {reason: .[0], arn: .[1]}' | jq -sc .)"
    [ "${#UNREADABLE[@]}" -gt 0 ] && unreadable="$(printf '%s\n' "${UNREADABLE[@]}" | jq -R . | jq -sc .)"

    jq -nc \
        --arg region "$REGION" \
        --arg instance "$INSTANCE" \
        --argjson dry_run "$dry" \
        --argjson plan "$plan" \
        --argjson refused "$refused" \
        --argjson reported "$reported" \
        --argjson unreadable_stacks "$unreadable" \
        --argjson deleted "$DELETED" \
        --argjson delete_failures "$DELETE_FAILURES" \
        '{script: "cleanup-aws", region: $region, instance: $instance, dry_run: $dry_run,
          planned: ($plan | length), plan: $plan, refused: $refused, reported: $reported,
          unreadable_stacks: $unreadable_stacks, deleted: $deleted,
          delete_failures: $delete_failures}'
}

for entry in ${REFUSED[@]+"${REFUSED[@]}"}; do
    warn "REFUSE ${entry%%|*} — ${entry##*|}"
done
for entry in ${REPORTED[@]+"${REPORTED[@]}"}; do
    warn "REPORT ${entry%%|*} — ${entry##*|}"
done

if [ "${#PLAN[@]}" -eq 0 ]; then
    ok "no orphans to sweep"
    [ "$AS_JSON" = "1" ] && emit_json
    exit 0
fi

log ""
info "plan — ${#PLAN[@]} deletion(s)"
# One line per action, printed from the same array --apply iterates, in the same order.
# §11.5: "dry-run output is asserted to describe precisely what --apply then does." Two
# renderings of the same intent would eventually disagree; one array cannot.
for entry in "${PLAN[@]}"; do
    IFS='|' read -r kind ident arn <<<"$entry"
    dim "  delete ${kind} ${ident}"
    dim "         ${arn}"
done

if ! confirm_apply "$APPLY" "delete ${#PLAN[@]} orphaned resource(s) — every one named individually above, never by wildcard"; then
    [ "$AS_JSON" = "1" ] && emit_json
    exit 0
fi

for entry in "${PLAN[@]}"; do
    IFS='|' read -r kind ident arn <<<"$entry"
    info "deleting ${kind} ${ident}"
    case "$kind" in
        cloudformation:stack)
            # By stack ID, not name: a stack in REVIEW_IN_PROGRESS can only be deleted by
            # id, and the id also cannot be re-bound to a differently-named stack created
            # between the plan and the apply.
            if aws_cli cloudformation delete-stack --stack-name "$arn" >/dev/null 2>&1; then
                aws_cli cloudformation wait stack-delete-complete --stack-name "$arn" >/dev/null 2>&1 || true
                DELETED=$((DELETED + 1))
                ok "deleted ${ident}"
            else
                DELETE_FAILURES=$((DELETE_FAILURES + 1))
                err "could not delete stack ${ident}"
            fi
            ;;
        lambda:function)
            if aws_cli lambda delete-function --function-name "$arn" >/dev/null 2>&1; then
                DELETED=$((DELETED + 1))
                ok "deleted ${ident}"
            else
                DELETE_FAILURES=$((DELETE_FAILURES + 1))
                err "could not delete function ${ident}"
            fi
            ;;
        *)
            # Unreachable unless a kind reaches PLAN without a handler. Loud, because the
            # alternative is a plan line that silently does nothing while the run reports
            # success.
            DELETE_FAILURES=$((DELETE_FAILURES + 1))
            err "no handler for ${kind} — it should never have entered the plan"
            ;;
    esac
done

log ""
info "verifying against the Project=${SYSTEM_ID} Resource Group (§10.3)"
if remaining="$(aws_cli resourcegroupstaggingapi get-resources \
    --tag-filters "Key=Project,Values=${SYSTEM_ID}" 2>/dev/null |
    jq -r '[.ResourceTagMappingList[]?.ResourceARN] | length')"; then
    dim "  ${remaining} resource(s) still tagged Project=${SYSTEM_ID} (the live deployment plus retained data)"
else
    warn "could not re-query the tagging API; this sweep is UNVERIFIED"
fi

[ "$AS_JSON" = "1" ] && emit_json

if [ "$DELETE_FAILURES" -gt 0 ]; then
    err "${DELETED} deleted, ${DELETE_FAILURES} failed"
    exit 1
fi
ok "${DELETED} orphan(s) swept"
