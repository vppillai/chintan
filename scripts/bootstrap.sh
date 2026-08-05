#!/usr/bin/env bash
#
# Deploy the shared bootstrap stack: artifact bucket, OIDC deployment role, and the
# tag-based Resource Group used to verify teardown (§Phase 0, §11.4).
#
# **This is the one stack deployed outside CI, and that is deliberate.** I16 and §0.5A
# require deploys to happen only from a green pipeline through deploy.sh — but this
# stack creates the CI role that pipeline assumes, so it cannot be applied by the thing
# it bootstraps. §4 describes it as "shared, manually deployed" for exactly this
# reason. Every per-instance deploy goes through CI.
#
# Handles the OIDC-provider collision (G-016): the provider is account-global and
# singleton, so a second declaration fails with "provider already exists". This script
# detects which case applies and sets CreateOIDCProvider accordingly rather than
# guessing — a guess works in a clean account, survives testing, and fails in
# production.
#
# Usage:
#   scripts/bootstrap.sh                # dry run
#   scripts/bootstrap.sh --apply
#   scripts/bootstrap.sh --region <r>   # default ca-central-1

# shellcheck source-path=SCRIPTDIR source=lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"

APPLY=0
REGION="ca-central-1"
GITHUB_ORG="vppillai"
GITHUB_REPO="chintan"
while [ $# -gt 0 ]; do
    case "$1" in
        --apply) APPLY=1 ;;
        --region)
            REGION="${2:?}"
            shift
            ;;
        --org)
            GITHUB_ORG="${2:?}"
            shift
            ;;
        --repo)
            GITHUB_REPO="${2:?}"
            shift
            ;;
        -h | --help)
            sed -n '2,25p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
            exit 0
            ;;
        *) die "unknown flag '$1' (see --help)" ;;
    esac
    shift
done
cd "$REPO_ROOT" || exit 1
export AWS_REGION="$REGION" AWS_PAGER=""

STACK="voicenotes-bootstrap"
TEMPLATE="infrastructure/bootstrap.yaml"
[ -f "$TEMPLATE" ] || die "missing $TEMPLATE"

ACCOUNT_ID="$(aws_cli sts get-caller-identity --query Account --output text 2>/dev/null)" ||
    die "no AWS credentials"
CALLER="$(aws_cli sts get-caller-identity --query Arn --output text)"

info "account $ACCOUNT_ID, region $REGION"
info "caller  $CALLER"

# Running this as root would create resources no boundary constrains, and every role
# it creates would inherit that. The bootstrap-agent script is the only thing here
# that may run with administrative credentials (§9.4).
if printf '%s' "$CALLER" | grep -q ':root$'; then
    die "refusing to deploy as the account root user. Assume voicenotes-agent first — every role this stack creates must carry the permissions boundary (G-046, §9.4)."
fi

# G-016: detect rather than assume.
OIDC_ARN="arn:aws:iam::${ACCOUNT_ID}:oidc-provider/token.actions.githubusercontent.com"
if aws_cli iam get-open-id-connect-provider --open-id-connect-provider-arn "$OIDC_ARN" >/dev/null 2>&1; then
    CREATE_OIDC=false
    ok "GitHub OIDC provider already exists — CreateOIDCProvider=false (G-016)"
else
    CREATE_OIDC=true
    ok "GitHub OIDC provider absent — CreateOIDCProvider=true"
fi

BOUNDARY_ARN="arn:aws:iam::${ACCOUNT_ID}:policy/voicenotes-agent-boundary"
if aws_cli iam get-policy --policy-arn "$BOUNDARY_ARN" >/dev/null 2>&1; then
    ok "permissions boundary found; every role in this stack will carry it"
else
    die "permissions boundary $BOUNDARY_ARN not found. Run scripts/bootstrap-agent.sh first (§0.8 item 1) — a role created without it can escalate (G-046)."
fi

info "validating template"
aws_cli cloudformation validate-template --template-body "file://$TEMPLATE" >/dev/null ||
    die "template failed validation"
ok "template valid"

log ""
info "plan"
dim "  stack:              $STACK"
dim "  CreateOIDCProvider: $CREATE_OIDC"
dim "  boundary:           $BOUNDARY_ARN"
dim "  repo:               ${GITHUB_ORG}/${GITHUB_REPO}"

if ! confirm_apply "$APPLY" "deploy the $STACK stack"; then
    exit 0
fi

# A stack whose first CREATE failed sits in ROLLBACK_COMPLETE and cannot be updated,
# only deleted. Left unhandled, the next --apply fails complaining about the stack
# state rather than about whatever actually broke, which sends the reader after the
# wrong problem. An empty ROLLBACK_COMPLETE stack holds no resources, so deleting it
# is safe — but say so rather than doing it silently.
STATUS="$(aws_cli cloudformation describe-stacks --stack-name "$STACK" \
    --query 'Stacks[0].StackStatus' --output text 2>/dev/null || echo "NONE")"
if [ "$STATUS" = "ROLLBACK_COMPLETE" ] || [ "$STATUS" = "REVIEW_IN_PROGRESS" ]; then
    warn "stack is in $STATUS from a previous failed create; it holds no resources and cannot be updated."
    info "deleting the failed stack before retrying"
    aws_cli cloudformation delete-stack --stack-name "$STACK"
    aws_cli cloudformation wait stack-delete-complete --stack-name "$STACK" || true
    ok "failed stack removed"
fi

info "deploying $STACK"
aws_cli cloudformation deploy \
    --template-file "$TEMPLATE" \
    --stack-name "$STACK" \
    --parameter-overrides \
    "GitHubOrg=$GITHUB_ORG" \
    "GitHubRepo=$GITHUB_REPO" \
    "CreateOIDCProvider=$CREATE_OIDC" \
    "AgentBoundaryPolicyArn=$BOUNDARY_ARN" \
    --capabilities CAPABILITY_NAMED_IAM \
    --no-fail-on-empty-changeset \
    --tags Project=voicenotes Instance=shared Environment=shared ManagedBy=iac Owner=vppillai CostCenter=voicenotes-shared

ok "$STACK deployed"
log ""
info "outputs"
aws_cli cloudformation describe-stacks --stack-name "$STACK" \
    --query 'Stacks[0].Outputs[].{Key:OutputKey,Value:OutputValue}' --output table

log ""
info "next, and both need a human (§9.6 withholds them from the agent):"
dim "  - create the 'production' GitHub environment; the OIDC trust policy is scoped"
dim "    to repo:${GITHUB_ORG}/${GITHUB_REPO}:environment:production, so a deploy job"
dim "    without it cannot assume the role"
dim "  - set the AWS_ACCOUNT_ID repository secret"
