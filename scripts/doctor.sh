#!/usr/bin/env bash
#
# Check that this machine and this AWS account are ready to deploy Chintan, and
# print the next command to run.
#
# Read-only. Nothing here creates, changes or deletes anything, so it is safe to
# run at any point: after cloning, and again after each step of the deploy
# sequence in the README. The last lines it prints are the next thing to do.
#
# What it checks, in the order the deploy needs them:
#
#   tools       aws (v2), gh, jq, curl, zip, python3 with PyYAML; go and bun are
#               reported but only needed for a local build or scripts/bootstrap.sh
#   aws         credentials resolve (sts get-caller-identity), and who you are
#   gh          gh is authenticated and the repository resolves
#   oidc        the account has an OIDC provider for token.actions.githubusercontent.com
#   agent       the chintan-agent-boundary policy exists (scripts/bootstrap-agent.sh)
#   bootstrap   the chintan-bootstrap stack exists (scripts/setup.sh)
#   secrets     /chintan/<instance>/groq_api_key and llm_api_key exist in SSM
#   stacks      which of the instance's stacks from config/instances/*.yaml exist
#
# A check the current credentials are not allowed to make is reported as
# "unknown" rather than as a failure; the bounded agent role, for example, may
# not read IAM policies.
#
# Usage:
#   scripts/doctor.sh [--instance NAME] [--region REGION] [--json]
#
# Options:
#   --instance NAME   instance to check secrets and stacks for   (default: dev)
#   --region REGION   AWS region                    (default: $AWS_REGION, else us-west-2)
#   --json            machine-readable result on stdout; the report stays on stderr
#
# Exit status: 0 when every check passed, 1 when something is missing or unknown,
# 2 on a usage error.

# shellcheck source-path=SCRIPTDIR source=lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"

INSTANCE="dev"
REGION="${AWS_REGION:-us-west-2}"
AS_JSON=0

while [ $# -gt 0 ]; do
    case "$1" in
        --instance)
            INSTANCE="${2:?--instance needs a value}"
            shift
            ;;
        --region)
            REGION="${2:?--region needs a value}"
            shift
            ;;
        --json) AS_JSON=1 ;;
        -h | --help)
            usage_from_header "${BASH_SOURCE[0]}"
            exit 0
            ;;
        *)
            err "unknown flag '$1' (see --help)"
            exit 2
            ;;
    esac
    shift
done

validate_instance_name "$INSTANCE"
export AWS_REGION="$REGION"

# ---------------------------------------------------------------------------
# Results
# ---------------------------------------------------------------------------
#
# Every check records one line: name, state (ok | missing | unknown), detail.
# The report is printed as it goes; the JSON and the "next step" are derived
# from the records at the end, so the two can never disagree.

RESULTS=()
FAILED=0
NEXT=""

record() {
    local name="$1" state="$2" detail="${3:-}"
    RESULTS+=("$name"$'\t'"$state"$'\t'"$detail")
    case "$state" in
        ok) ok "$name: $detail" ;;
        missing)
            err "$name: $detail"
            FAILED=1
            ;;
        unknown)
            warn "$name: $detail"
            FAILED=1
            ;;
    esac
}

# The first missing step is the next step. Later checks still run, so the
# report is complete, but the instruction at the end names only one command.
suggest() {
    [ -n "$NEXT" ] || NEXT="$1"
}

# ---------------------------------------------------------------------------
# Tools
# ---------------------------------------------------------------------------

info "tools"

# The scripts need aws CLI version 2: version 1 does not accept some of the arguments
# they pass and prints different JSON shapes.
if command -v aws >/dev/null 2>&1; then
    aws_ver="$(aws --version 2>&1 | sed -n 's|^aws-cli/\([0-9.]*\).*|\1|p')"
    case "$aws_ver" in
        2.*) record "aws" ok "aws-cli $aws_ver" ;;
        *)
            record "aws" missing "aws-cli $aws_ver found; version 2 is required"
            suggest "install the AWS CLI v2: https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html"
            ;;
    esac
else
    record "aws" missing "not installed"
    suggest "install the AWS CLI v2: https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html"
fi

for tool in gh jq curl zip; do
    if command -v "$tool" >/dev/null 2>&1; then
        record "$tool" ok "$(command -v "$tool")"
    else
        record "$tool" missing "not installed"
        suggest "install $tool"
    fi
done

# scripts/list-instances.sh parses config/instances/*.yaml in Python.
if command -v python3 >/dev/null 2>&1; then
    if python3 -c 'import yaml' >/dev/null 2>&1; then
        record "python3" ok "$(python3 --version 2>&1) with PyYAML"
    else
        record "python3" missing "PyYAML is not importable"
        suggest "python3 -m pip install pyyaml"
    fi
else
    record "python3" missing "not installed"
    suggest "install python3 and PyYAML"
fi

# Go and Bun build the Lambda and the site. CI has its own copies, so a
# deployment driven entirely through GitHub Actions never needs them here; a
# local build, the test suites and scripts/bootstrap.sh do. Reported, not
# required.
go_mod_version="$(sed -n 's/^go \([0-9.]*\).*/\1/p' "$REPO_ROOT/backend/go.mod" | head -n 1)"
if command -v go >/dev/null 2>&1; then
    go_ver="$(go version 2>/dev/null | sed -n 's/^go version go\([0-9.]*\).*/\1/p')"
    record "go" ok "go $go_ver (go.mod wants $go_mod_version; GOTOOLCHAIN=auto fetches it when older)"
else
    dim "  -- go: not installed; needed only for a local build, the tests and scripts/bootstrap.sh (go.mod wants $go_mod_version)"
fi

bun_pinned="$(sed -n 's/.*bun-version: *\([0-9.]*\).*/\1/p' "$REPO_ROOT/.github/workflows/deploy-frontend.yaml" | head -n 1)"
if command -v bun >/dev/null 2>&1; then
    record "bun" ok "bun $(bun --version 2>/dev/null) (CI pins $bun_pinned)"
else
    dim "  -- bun: not installed; needed only for a local frontend build and its tests (CI pins $bun_pinned)"
fi

# ---------------------------------------------------------------------------
# AWS credentials
# ---------------------------------------------------------------------------

info "aws account"

HAVE_AWS=0
ACCOUNT_ID=""
if command -v aws >/dev/null 2>&1; then
    if identity="$(aws_cli sts get-caller-identity --output json 2>/dev/null)"; then
        HAVE_AWS=1
        ACCOUNT_ID="$(printf '%s' "$identity" | jq -r .Account)"
        record "credentials" ok "account $ACCOUNT_ID as $(printf '%s' "$identity" | jq -r .Arn) in $REGION"
    else
        record "credentials" missing "aws sts get-caller-identity failed"
        suggest "configure AWS credentials (aws configure, or export AWS_PROFILE) for the account to deploy into"
    fi
fi

# aws_probe runs a read-only call and classifies the outcome, so a denied call
# reads as "unknown" rather than as "does not exist".
#
#   0  the call succeeded and printed its output
#   1  the resource does not exist (the CLI reported a not-found error)
#   2  denied, or another error; the message is on stderr
aws_probe() {
    local out err_out rc
    err_out="$(mktemp)"
    if out="$(aws_cli "$@" 2>"$err_out")"; then
        rm -f "$err_out"
        printf '%s' "$out"
        return 0
    fi
    rc=1
    if grep -qi "AccessDenied\|not authorized\|UnauthorizedOperation" "$err_out"; then
        rc=2
    elif ! grep -qi "NoSuchEntity\|does not exist\|ParameterNotFound\|ValidationError" "$err_out"; then
        rc=2
    fi
    rm -f "$err_out"
    return "$rc"
}

# ---------------------------------------------------------------------------
# GitHub
# ---------------------------------------------------------------------------

info "github"

REPO=""
if command -v gh >/dev/null 2>&1; then
    if gh auth status >/dev/null 2>&1; then
        if REPO="$(gh repo view --json nameWithOwner --jq .nameWithOwner 2>/dev/null)"; then
            record "gh" ok "authenticated; repository $REPO"
        else
            record "gh" missing "authenticated, but no repository resolves from this directory"
            suggest "run this from a clone of your fork (gh repo view must resolve it)"
        fi
    else
        record "gh" missing "not authenticated"
        suggest "gh auth login"
    fi
fi

# ---------------------------------------------------------------------------
# Account state, in deploy order
# ---------------------------------------------------------------------------

if [ "$HAVE_AWS" = 1 ]; then
    info "account state"

    # The bootstrap stack's deploy roles trust the GitHub OIDC provider, and the
    # templates never create it because it is shared with other projects.
    if providers="$(aws_probe iam list-open-id-connect-providers --output json)"; then
        if printf '%s' "$providers" | jq -e '.OpenIDConnectProviderList[]? | select(.Arn | contains("token.actions.githubusercontent.com"))' >/dev/null; then
            record "oidc" ok "GitHub OIDC provider present"
        else
            record "oidc" missing "no OIDC provider for token.actions.githubusercontent.com"
            suggest "as an administrator: aws iam create-open-id-connect-provider --url https://token.actions.githubusercontent.com --client-id-list sts.amazonaws.com"
        fi
    else
        record "oidc" unknown "iam:ListOpenIDConnectProviders is denied to these credentials"
    fi

    # infrastructure/bootstrap.yaml names this boundary, so setup.sh cannot
    # succeed before bootstrap-agent.sh has run.
    boundary_arn="arn:aws:iam::${ACCOUNT_ID}:policy/chintan-agent-boundary"
    rc=0
    aws_probe iam get-policy --policy-arn "$boundary_arn" >/dev/null || rc=$?
    case "$rc" in
        0) record "agent" ok "chintan-agent-boundary exists" ;;
        1)
            record "agent" missing "chintan-agent-boundary does not exist"
            suggest "as an administrator: scripts/bootstrap-agent.sh --region $REGION --apply"
            ;;
        *) record "agent" unknown "iam:GetPolicy is denied to these credentials" ;;
    esac

    if stack_exists "$CHINTAN_BOOTSTRAP_STACK"; then
        record "bootstrap" ok "$CHINTAN_BOOTSTRAP_STACK exists"
    else
        record "bootstrap" missing "$CHINTAN_BOOTSTRAP_STACK does not exist"
        suggest "scripts/setup.sh --region $REGION --apply"
    fi

    # The Lambdas read the two keys by path at run time, so the stack deploys
    # without them and every capture then fails at the provider call.
    for key in groq_api_key llm_api_key; do
        name="/chintan/${INSTANCE}/${key}"
        if found="$(aws_probe ssm describe-parameters --parameter-filters "Key=Name,Values=${name}" --query 'Parameters[0].Name' --output text)"; then
            if [ "$found" = "$name" ]; then
                record "secret $key" ok "$name"
            else
                record "secret $key" missing "$name is not in SSM"
                suggest "aws ssm put-parameter --region $REGION --type SecureString --name $name --value ..."
            fi
        else
            record "secret $key" unknown "ssm:DescribeParameters is denied to these credentials"
        fi
    done

    # The instance's stacks, as config/instances/*.yaml resolves them.
    prod_stack="$(stack_name "$INSTANCE" prod)"
    if command -v python3 >/dev/null 2>&1 && python3 -c 'import yaml' >/dev/null 2>&1; then
        while read -r stack stack_region _; do
            [ -n "$stack" ] || continue
            case "$stack" in
                "${CHINTAN_PREFIX}${INSTANCE}-"*) ;;
                *) continue ;;
            esac
            if [ "$stack_region" = "$REGION" ] && stack_exists "$stack"; then
                record "stack $stack" ok "deployed in $stack_region"
            else
                record "stack $stack" missing "not deployed in $stack_region"
                if [ "$stack" = "$prod_stack" ]; then
                    suggest "gh workflow run deploy-backend.yaml   (staging, smoke test, then production after your approval; or scripts/bootstrap.sh --instance $INSTANCE --region $REGION --origin https://<owner>.github.io --apply from this machine)"
                fi
            fi
        done < <("$REPO_ROOT/scripts/list-instances.sh" --format text 2>/dev/null)
    fi
fi

# ---------------------------------------------------------------------------
# Verdict
# ---------------------------------------------------------------------------

log ""
if [ "$FAILED" = 0 ]; then
    ok "everything is in place for instance '$INSTANCE' in $REGION"
    dim "  next: scripts/invite-user.sh --instance $INSTANCE --email you@example.com --apply   (first user)"
    dim "        then open https://<owner>.github.io/<repo>/<site_path>/"
else
    if [ -n "$NEXT" ]; then
        info "next: $NEXT"
    else
        warn "some checks could not be made with these credentials; see above"
    fi
fi

if [ "$AS_JSON" = 1 ]; then
    printf '%s\n' "${RESULTS[@]}" |
        jq -R -s --arg instance "$INSTANCE" --arg region "$REGION" --arg next "$NEXT" '
            {instance: $instance, region: $region, next: $next,
             checks: (split("\n") | map(select(length > 0) | split("\t") | {name: .[0], state: .[1], detail: .[2]})),
             ok: (split("\n") | map(select(length > 0) | split("\t") | .[1]) | all(. == "ok"))}'
fi

exit "$FAILED"
