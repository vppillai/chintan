#!/usr/bin/env bash
# The single shared library for every script in scripts/.
#
# Sourced, never executed.
#
# This file is the merge of what used to be two mutually incompatible libraries:
# lib/common.sh (log_info/DRY_RUN/execute_cmd+eval) and lib/agent-common.sh
# (info/APPLY/confirm_apply). Two libraries meant two conventions for the same
# thing, and every script picked one at random — which is how teardown.sh came to
# export SKIP_CONFIRMATION for a cleanup-aws.sh that never read it.
#
# The conventions that survived, and why:
#
#   APPLY=0 is the default          A mistaken invocation prints a plan instead of
#                                   destroying data. v1 got this right in four
#                                   scripts and it is kept verbatim.
#   run() takes argv, not a string  execute_cmd built a command string and eval'd
#                                   it, so every interpolated bucket name, ARN and
#                                   instance name was an injection surface. Nothing
#                                   in this file calls eval.
#   Diagnostics go to stderr        so --json output on stdout stays parseable.
#
# shellcheck shell=bash

# Guard against double-sourcing, which would reset counters mid-run.
if [ -n "${CHINTAN_COMMON_SOURCED:-}" ]; then
    return 0
fi
CHINTAN_COMMON_SOURCED=1

set -euo pipefail

# ---------------------------------------------------------------------------
# Paths and identifiers
# ---------------------------------------------------------------------------

# REPO_ROOT is resolved from this file's location, so a script works regardless
# of the directory it was invoked from. Every path in every script is built from
# this rather than from $PWD.
CHINTAN_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [ -n "${CHINTAN_REPO_ROOT:-}" ]; then
    # Explicit override, for tests that need to run a check against a doctored
    # tree — the --self-test modes of check-log-hygiene.sh and check-vite-env.sh
    # prove they fail when the thing they check is broken, which requires
    # pointing them at a tree where it is. Not for ordinary use.
    REPO_ROOT="$(cd "$CHINTAN_REPO_ROOT" && pwd)"
else
    REPO_ROOT="$(cd "${CHINTAN_LIB_DIR}/../.." && pwd)"
fi
export REPO_ROOT

# The frozen system identifier. Every physical resource this project creates is
# named chintan-*, and every enumeration in this file is anchored on it.
SYSTEM_ID="chintan"
CHINTAN_PREFIX="${SYSTEM_ID}-"
CHINTAN_BOOTSTRAP_STACK="${CHINTAN_PREFIX}bootstrap"
export SYSTEM_ID CHINTAN_PREFIX CHINTAN_BOOTSTRAP_STACK

# The environments a stack name may end in. Stack naming is
# chintan-<instance>-<environment>, unified across the scripts and the workflows;
# v1 had the scripts assume chintan-<instance> while CI deployed
# chintan-<instance>-prod, so teardown derived the instance name "dev-prod" and
# deleted SSM parameters under a path that had never existed.
CHINTAN_ENVIRONMENTS="prod staging dev"

# ---------------------------------------------------------------------------
# Output
# ---------------------------------------------------------------------------

# Colour only when attached to a terminal. CI logs are not a terminal, and
# escape codes in a CI log make a failure harder to read, not easier.
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
    C_RED=$'\033[31m'
    C_GREEN=$'\033[32m'
    C_YELLOW=$'\033[33m'
    C_DIM=$'\033[2m'
    C_BOLD=$'\033[1m'
    C_OFF=$'\033[0m'
else
    C_RED=''
    C_GREEN=''
    C_YELLOW=''
    C_DIM=''
    C_BOLD=''
    C_OFF=''
fi

log() { printf '%s\n' "$*" >&2; }
info() { printf '%s==>%s %s\n' "$C_BOLD" "$C_OFF" "$*" >&2; }
warn() { printf '%swarning:%s %s\n' "$C_YELLOW" "$C_OFF" "$*" >&2; }
err() { printf '%serror:%s %s\n' "$C_RED" "$C_OFF" "$*" >&2; }
ok() { printf '%s  ok%s %s\n' "$C_GREEN" "$C_OFF" "$*" >&2; }
dim() { printf '%s%s%s\n' "$C_DIM" "$*" "$C_OFF" >&2; }

die() {
    err "$*"
    exit 1
}

# ---------------------------------------------------------------------------
# Violation accumulation
# ---------------------------------------------------------------------------
#
# Checks report every violation rather than stopping at the first. An operator
# fixing a config or a template should see the whole list, not discover the next
# problem after each push.

VIOLATION_COUNT=0
VIOLATIONS=()

violation() {
    VIOLATION_COUNT=$((VIOLATION_COUNT + 1))
    VIOLATIONS+=("$1")
    printf '%s  FAIL%s %s\n' "$C_RED" "$C_OFF" "$1" >&2
}

# finish_check prints a summary and exits with 0 or 1.
#
# Takes the check's name and, optionally, "--json" to emit a machine-readable
# result instead of prose.
finish_check() {
    local name="$1" as_json="${2:-}"

    if [ "$as_json" = "--json" ]; then
        # Build the array with jq rather than string concatenation so a
        # violation containing a quote cannot produce invalid JSON.
        local arr='[]'
        if [ "$VIOLATION_COUNT" -gt 0 ]; then
            arr="$(printf '%s\n' "${VIOLATIONS[@]}" | jq -R . | jq -sc .)"
        fi
        jq -nc \
            --arg check "$name" \
            --argjson ok "$([ "$VIOLATION_COUNT" -eq 0 ] && echo true || echo false)" \
            --argjson violations "$arr" \
            '{check: $check, ok: $ok, violation_count: ($violations|length), violations: $violations}'
    fi

    if [ "$VIOLATION_COUNT" -eq 0 ]; then
        ok "$name"
        return 0
    fi
    err "$name: $VIOLATION_COUNT violation(s)"
    exit 1
}

# ---------------------------------------------------------------------------

# APPLY is the one dry-run switch. 0 (the default) prints the plan; 1 executes.
: "${APPLY:=0}"

is_apply() { [ "${APPLY:-0}" = "1" ]; }

# run executes a command, or prints it when APPLY is not 1.
#
# It takes the command as separate arguments and invokes it directly. Its
# predecessor, execute_cmd, took a single string and eval'd it, which meant a
# bucket name or ARN interpolated into that string was executed as shell source.
# printf %q is used for the *display* only, so what is printed can be pasted into
# a terminal without changing what is run.
run() {
    if ! is_apply; then
        dim "  would run: $(quoted_cmd "$@")"
        return 0
    fi
    dim "  + $(quoted_cmd "$@")"
    "$@"
}

quoted_cmd() {
    local out=""
    local a
    for a in "$@"; do
        out+="$(printf '%q' "$a") "
    done
    printf '%s' "${out% }"
}

# confirm_apply implements the convention that matters most for agent safety:
# --dry-run is the DEFAULT for anything destructive or costly, and --apply
# executes.
#
# Usage: confirm_apply "$APPLY" "delete 412 objects"
confirm_apply() {
    local apply="$1" description="$2"
    if [ "$apply" != "1" ]; then
        log ""
        info "DRY RUN — nothing was changed."
        dim "  Would: ${description}"
        dim "  Re-run with --apply to execute."
        return 1
    fi
    return 0
}

# confirm_destructive asks for an explicit typed phrase before a destructive
# --apply run.
#
# ASSUME_YES (set by --yes, and exported by teardown.sh so a nested cleanup run
# does not stop to ask again) skips the prompt. Without a TTY and without
# ASSUME_YES it FAILS rather than blocking: v1 read from stdin unconditionally,
# so an automated teardown hung forever on a prompt nobody could see.
confirm_destructive() {
    local phrase="$1"
    shift
    local line
    for line in "$@"; do
        warn "$line"
    done

    if [ "${ASSUME_YES:-0}" = "1" ]; then
        dim "  --yes: skipping the confirmation prompt"
        return 0
    fi

    if [ ! -t 0 ]; then
        err "refusing to run destructively without a terminal and without --yes"
        err "a prompt on a closed stdin blocks forever; pass --yes if that is what you meant"
        exit 2
    fi

    local reply=""
    printf 'Type %s to confirm: ' "$phrase" >&2
    read -r reply
    if [ "$reply" != "$phrase" ]; then
        info "cancelled"
        exit 0
    fi
    return 0
}

# ---------------------------------------------------------------------------
# Preconditions
# ---------------------------------------------------------------------------

require_cmd() {
    local c
    for c in "$@"; do
        command -v "$c" >/dev/null 2>&1 || die "$c is required but not installed"
    done
}

require_aws() {
    require_cmd aws
    aws_cli sts get-caller-identity >/dev/null 2>&1 ||
        die "no usable AWS credentials (aws sts get-caller-identity failed)"
}

require_gh() {
    require_cmd gh
    gh auth status >/dev/null 2>&1 || die "gh is not authenticated; run 'gh auth login'"
}

aws_account_id() { aws_cli sts get-caller-identity --query Account --output text; }

# github_repo returns owner/name, preferring the CI-provided value so the scripts
# do not need gh in a workflow.
github_repo() {
    if [ -n "${GITHUB_REPOSITORY:-}" ]; then
        printf '%s' "$GITHUB_REPOSITORY"
        return 0
    fi
    require_gh
    gh repo view --json nameWithOwner --jq .nameWithOwner
}

# ---------------------------------------------------------------------------
# AWS
# ---------------------------------------------------------------------------

# aws_cli wraps the AWS CLI so region handling has one place to live, and so no
# script has to remember to pass --region. AWS_PAGER is cleared because a script
# that opens a pager in CI hangs.
aws_cli() {
    local region
    region="$(aws_region)"
    if [ -n "$region" ]; then
        AWS_PAGER='' command aws --region "$region" "$@"
    else
        AWS_PAGER='' command aws "$@"
    fi
}

# aws_region is the single answer to "which region is this script acting in".
#
# Callers that build a region-qualified NAME — the artifact bucket is
# chintan-lambda-<account>-<region>, and an S3 URL carries the region too — must
# use the same answer aws_cli uses. deploy.sh built the artifact bucket name
# from AWS_REGION while aws_cli honoured CHINTAN_REGION first, so with the two
# set differently the template was uploaded to one region and its URL named
# another.
aws_region() { printf '%s' "${CHINTAN_REGION:-${AWS_REGION:-}}"; }

# ---------------------------------------------------------------------------
# Naming
# ---------------------------------------------------------------------------

validate_instance_name() {
    local name="$1"
    [[ "$name" =~ ^[a-z0-9-]+$ ]] ||
        die "instance name '$name' must contain only lowercase letters, numbers and hyphens"
    [ "${#name}" -le 32 ] || die "instance name '$name' must be 32 characters or less"
}

validate_environment() {
    local env="$1" e
    for e in $CHINTAN_ENVIRONMENTS; do
        [ "$env" = "$e" ] && return 0
    done
    die "environment '$env' must be one of: $CHINTAN_ENVIRONMENTS"
}

# stack_name is the single definition of the naming scheme. Everything —
# bootstrap.sh, deploy.sh, cleanup-aws.sh, teardown.sh and both workflows —
# derives its stack name from here or from the identical expression in YAML.
stack_name() {
    local instance="$1" env="$2"
    validate_instance_name "$instance"
    validate_environment "$env"
    printf '%s%s-%s' "$CHINTAN_PREFIX" "$instance" "$env"
}

# parse_stack_name is the inverse, and it refuses to guess. Given
# chintan-dev-prod it sets STACK_INSTANCE=dev and STACK_ENV=prod; given anything
# that is not chintan-<instance>-<known environment> it returns 1 so the caller
# can skip the stack rather than invent an instance name for it.
STACK_INSTANCE=""
STACK_ENV=""
parse_stack_name() {
    local stack="$1" rest env matched=0 e
    case "$stack" in
        "$CHINTAN_PREFIX"*) ;;
        *) return 1 ;;
    esac
    rest="${stack#"$CHINTAN_PREFIX"}"
    env="${rest##*-}"
    for e in $CHINTAN_ENVIRONMENTS; do
        [ "$env" = "$e" ] && matched=1
    done
    [ "$matched" = "1" ] || return 1
    [ "$rest" != "$env" ] || return 1
    STACK_INSTANCE="${rest%-*}"
    # shellcheck disable=SC2034  # read by callers, not by this file.
    STACK_ENV="$env"
    [ -n "$STACK_INSTANCE" ] || return 1
    return 0
}

# ---------------------------------------------------------------------------
# CloudFormation
# ---------------------------------------------------------------------------

stack_exists() {
    aws_cli cloudformation describe-stacks --stack-name "$1" >/dev/null 2>&1
}

stack_output() {
    local stack="$1" key="$2"
    aws_cli cloudformation describe-stacks --stack-name "$stack" \
        --query "Stacks[0].Outputs[?OutputKey=='${key}'].OutputValue" --output text
}

wait_for_stack() {
    local stack="$1" op="${2:-}"
    if [ -z "$op" ]; then
        local status
        status="$(aws_cli cloudformation describe-stacks --stack-name "$stack" \
            --query 'Stacks[0].StackStatus' --output text 2>/dev/null || echo NOT_EXISTS)"
        case "$status" in
            *CREATE_IN_PROGRESS*) op="stack-create-complete" ;;
            *UPDATE_IN_PROGRESS*) op="stack-update-complete" ;;
            *DELETE_IN_PROGRESS*) op="stack-delete-complete" ;;
            *) return 0 ;;
        esac
    fi
    info "waiting for $stack ($op)"
    aws_cli cloudformation wait "$op" --stack-name "$stack"
}

# list_chintan_stacks lists stack NAMES only. Enumerating stacks by prefix is
# safe; enumerating *resources* by prefix is not, which is why nothing else in
# this library does it. Every resource acted on below is discovered through
# describe-stack-resources on a named stack.
# The filter is an EXCLUSION of one status, not an allow-list of six.
#
# The allow-list version omitted DELETE_FAILED, CREATE_FAILED and every
# *_IN_PROGRESS, and both of its callers read the omission as "no such stack":
#
#   * teardown.sh printed "nothing to tear down" and exited 0 over a
#     half-deleted DELETE_FAILED stack — one whose Cognito deletion protection
#     the failed delete had already turned OFF.
#   * cleanup-aws.sh computes SIBLINGS from this function to decide whether the
#     provider secrets at /chintan/<instance>/ are still in use. With
#     chintan-dev-prod merely UPDATE_IN_PROGRESS during a concurrent CI deploy,
#     SIBLINGS came back empty and a staging cleanup deleted prod's groq and llm
#     API keys, breaking every prod transcription.
#
# A stack in any state other than DELETE_COMPLETE exists and must be counted.
list_chintan_stacks() {
    aws_cli cloudformation list-stacks \
        --query "StackSummaries[?StackStatus!='DELETE_COMPLETE' && starts_with(StackName, \`${CHINTAN_PREFIX}\`)].StackName" \
        --output text | tr '\t' '\n' | grep -v '^$' || true
}

# list_chintan_stacks_with_status prints "<name>\t<status>" so a caller can say
# WHICH abnormal state a stack is in rather than silently skipping it.
list_chintan_stacks_with_status() {
    aws_cli cloudformation list-stacks \
        --query "StackSummaries[?StackStatus!='DELETE_COMPLETE' && starts_with(StackName, \`${CHINTAN_PREFIX}\`)].[StackName,StackStatus]" \
        --output text | grep -v '^$' || true
}

# stack_resources_of_type prints the physical IDs of every resource of a given
# type that BELONGS TO the named stack.
#
# This function is the whole fix for the teardown blast radius. v1 answered
# "which buckets should I empty?" with `s3api list-buckets | starts_with(chintan-)`,
# which matches chintan-cloudtrail-<account>-<region> — the audit bucket
# bootstrap-agent.sh creates precisely so that a destructive action is
# attributable. Teardown's first act was to destroy the record of it.
stack_resources_of_type() {
    local stack="$1" type="$2"
    aws_cli cloudformation describe-stack-resources --stack-name "$stack" \
        --query "StackResources[?ResourceType=='${type}'].PhysicalResourceId" \
        --output text 2>/dev/null | tr '\t' '\n' | grep -v '^$' || true
}

# ---------------------------------------------------------------------------
# Protected resources
# ---------------------------------------------------------------------------

# Even stack-scoped, a destructive helper is given one more guard: the audit
# trail is never a legitimate target, and a bucket carrying Protected=true has
# been marked by a human as not-to-be-deleted. Belt and braces, because the cost
# of being wrong here is losing the evidence of having been wrong.
assert_not_protected_bucket() {
    local bucket="$1"
    case "$bucket" in
        "${CHINTAN_PREFIX}cloudtrail-"*)
            die "refusing to touch $bucket: it is the CloudTrail audit bucket created by scripts/bootstrap-agent.sh"
            ;;
    esac
    local tags
    tags="$(aws_cli s3api get-bucket-tagging --bucket "$bucket" --output json 2>/dev/null || echo '{}')"
    if printf '%s' "$tags" | jq -e '.TagSet[]? | select(.Key=="Protected" and .Value=="true")' >/dev/null 2>&1; then
        die "refusing to touch $bucket: it is tagged Protected=true"
    fi
}

# ---------------------------------------------------------------------------
# Retained-resource probes
# ---------------------------------------------------------------------------
#
# Resources carrying DeletionPolicy: Retain survive a failed create and then
# collide with the retry, because bucket names, table names, Cognito pool names
# and KMS aliases are unique. Two scripts need to ask "is this still there?" —
# deploy.sh, to report it, and clean-instance-orphans.sh, to delete it.
#
# They must be able to tell ABSENT from COULD-NOT-LOOK. The obvious spelling,
#
#     aws s3api head-bucket --bucket "$b" >/dev/null 2>&1 && found=1
#
# reports "no orphans" for a bucket that exists but whose probe was denied:
# head-bucket answers 403 rather than 404 when the caller may not see it, and
# both are simply "non-zero" to the shell. That is the same silent-denial defect
# that made an earlier deploy.sh report a cleanup it had never been allowed to
# perform. Every probe below therefore returns one of three answers on stdout:
#
#     present<TAB><detail>
#     absent
#     unknown<TAB><reason>
#
# and callers must treat `unknown` as a failure to report, never as `absent`.

# probe_result is a small constructor so the three shapes have one definition.
probe_result() { printf '%s\t%s\n' "$1" "${2:-}"; }
probe_state() { printf '%s' "${1%%$'\t'*}"; }
probe_detail() { printf '%s' "${1#*$'\t'}"; }

# probe_bucket distinguishes a missing bucket from one it may not look at.
probe_bucket() {
    local bucket="$1" errout
    if errout="$(aws_cli s3api head-bucket --bucket "$bucket" 2>&1)"; then
        probe_result present "s3://${bucket}"
        return 0
    fi
    case "$errout" in
        *404* | *"Not Found"* | *NoSuchBucket*) probe_result absent ;;
        *403* | *Forbidden* | *AccessDenied* | *"not authorized"*)
            # Exists, or exists in another account: either way this caller
            # cannot say it is gone.
            probe_result unknown "s3://${bucket}: access denied (${errout##*$'\n'})"
            ;;
        *) probe_result unknown "s3://${bucket}: ${errout##*$'\n'}" ;;
    esac
}

probe_table() {
    local table="$1" errout
    if errout="$(aws_cli dynamodb describe-table --table-name "$table" 2>&1)"; then
        probe_result present "dynamodb table ${table}"
        return 0
    fi
    case "$errout" in
        *ResourceNotFoundException*) probe_result absent ;;
        *) probe_result unknown "dynamodb table ${table}: ${errout##*$'\n'}" ;;
    esac
}

# probe_user_pool pages. cognito-idp:ListUserPools caps --max-results at 60 and
# the AWS CLI does NOT auto-paginate it — verified: --max-results 1 returns one
# pool and a NextToken. A single 60-pool call therefore reports "absent" for a
# pool that exists, in an account with more than 60 pools, with nothing said.
probe_user_pool() {
    local name="$1" token="" page id
    while :; do
        if [ -n "$token" ]; then
            page="$(aws_cli cognito-idp list-user-pools --max-results 60 --next-token "$token" --output json 2>&1)" || {
                probe_result unknown "cognito user pool ${name}: ${page##*$'\n'}"
                return 0
            }
        else
            page="$(aws_cli cognito-idp list-user-pools --max-results 60 --output json 2>&1)" || {
                probe_result unknown "cognito user pool ${name}: ${page##*$'\n'}"
                return 0
            }
        fi
        id="$(printf '%s' "$page" | jq -r --arg n "$name" '.UserPools[] | select(.Name==$n) | .Id' | head -n 1)"
        if [ -n "$id" ]; then
            probe_result present "$id"
            return 0
        fi
        token="$(printf '%s' "$page" | jq -r '.NextToken // ""')"
        [ -n "$token" ] || break
    done
    probe_result absent
}

# list_user_pools_by_prefix prints "<id>\t<name>" for every pool whose name
# starts with the prefix, across every page. Same pagination trap as
# probe_user_pool: --max-results caps at 60 and the CLI does not auto-paginate,
# so a single call quietly reports fewer pools than exist.
list_user_pools_by_prefix() {
    local prefix="$1" token="" page args
    while :; do
        args=(cognito-idp list-user-pools --max-results 60 --output json)
        [ -n "$token" ] && args+=(--next-token "$token")
        page="$(aws_cli "${args[@]}" 2>/dev/null)" || return 0
        printf '%s' "$page" | jq -r --arg p "$prefix" \
            '.UserPools[] | select(.Name | startswith($p)) | "\(.Id)\t\(.Name)"'
        token="$(printf '%s' "$page" | jq -r '.NextToken // ""')"
        [ -n "$token" ] || break
    done
}

# probe_kms_alias returns the alias's TARGET KEY ID as the detail, not just the
# alias name. The key id is the only handle on the CMK once the alias is gone,
# and an alias delete that does not surface it leaves a key nobody can name —
# which is exactly how this account came to hold three CMKs described "Chintan
# dev Cognito refresh-token vault" behind one alias, billing indefinitely.
probe_kms_alias() {
    local alias_name="$1" out target
    out="$(aws_cli kms list-aliases --output json 2>&1)" || {
        probe_result unknown "kms alias ${alias_name}: ${out##*$'\n'}"
        return 0
    }
    target="$(printf '%s' "$out" | jq -r --arg a "$alias_name" \
        '.Aliases[] | select(.AliasName==$a) | .TargetKeyId // ""' | head -n 1)"
    if [ -n "$target" ]; then
        probe_result present "$target"
    else
        probe_result absent
    fi
}

# ---------------------------------------------------------------------------
# S3
# ---------------------------------------------------------------------------

# empty_s3_bucket deletes every object version and delete marker in a bucket.
#
# Two defects in the v1 implementation are fixed here.
#
#   1. It piped the output of list-object-versions straight into delete-objects.
#      On an already-empty bucket that produces {"Objects": []}, which the CLI
#      rejects — and under `set -euo pipefail` the rejection killed teardown
#      mid-run, after some resources were gone and before others were. The loop
#      below breaks on an empty page and never calls delete-objects with nothing.
#
#   2. list-object-versions returns at most 1000 keys, and v1 called it once. A
#      bucket of audio recordings exceeds that on the first day. The loop re-lists
#      after each delete — the deleted versions are gone, so the next page is the
#      next 1000 — and terminates when nothing is left.
empty_s3_bucket() {
    local bucket="$1"
    assert_not_protected_bucket "$bucket"

    if ! aws_cli s3api head-bucket --bucket "$bucket" >/dev/null 2>&1; then
        dim "  bucket $bucket does not exist or is not accessible"
        return 0
    fi

    if ! is_apply; then
        local count
        count="$(aws_cli s3api list-object-versions --bucket "$bucket" --output json 2>/dev/null |
            jq '[.Versions[]?, .DeleteMarkers[]?] | length' 2>/dev/null || echo 0)"
        dim "  would empty s3://$bucket (at least $count object version(s))"
        return 0
    fi

    info "emptying s3://$bucket"
    local payload tmp n total=0
    tmp="$(mktemp)"
    # shellcheck disable=SC2064  # $tmp must be expanded now, not at trap time.
    trap "rm -f '$tmp'" RETURN

    while :; do
        payload="$(aws_cli s3api list-object-versions --bucket "$bucket" \
            --max-items 1000 --output json 2>/dev/null || echo '{}')"
        printf '%s' "$payload" | jq -c \
            '{Objects: ([.Versions[]?, .DeleteMarkers[]?]
                        | map({Key, VersionId})
                        | map(select(.VersionId != null))),
              Quiet: true}' >"$tmp"
        n="$(jq '.Objects | length' <"$tmp")"
        if [ "$n" -eq 0 ]; then
            break
        fi
        aws_cli s3api delete-objects --bucket "$bucket" --delete "file://$tmp" >/dev/null
        total=$((total + n))
        dim "  deleted $total object version(s) so far"
    done

    ok "emptied s3://$bucket ($total object version(s))"
}

delete_s3_bucket() {
    local bucket="$1"
    assert_not_protected_bucket "$bucket"
    if ! aws_cli s3api head-bucket --bucket "$bucket" >/dev/null 2>&1; then
        dim "  bucket $bucket already gone"
        return 0
    fi
    empty_s3_bucket "$bucket"
    run aws_cli s3api delete-bucket --bucket "$bucket"
}

# ---------------------------------------------------------------------------
# Usage conventions
# ---------------------------------------------------------------------------

# usage_from_header prints the leading comment block of the calling script as its
# --help text, so the documentation and the help output cannot drift apart.
#
# It reads to the end of the block rather than to a fixed line number: a hardcoded
# range silently truncates --help the first time someone adds a paragraph, and the
# truncation is invisible unless you already know what the missing text said.
usage_from_header() {
    awk 'NR == 1 { next }
         /^#/    { sub(/^# ?/, ""); print; next }
                 { exit }' "$1"
}
