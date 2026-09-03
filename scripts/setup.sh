#!/usr/bin/env bash
#
# One-time account and repository setup: the bootstrap stack with the GitHub OIDC
# deploy and build roles, the repository secrets and variables, the deployment
# environments and Pages.
#
# Run this once per AWS account + fork, before scripts/bootstrap.sh.
#
# It does NOT create the agent IAM principal, its permissions boundary or
# CloudTrail — scripts/bootstrap-agent.sh does that, it needs administrative
# credentials, and infrastructure/bootstrap.yaml refuses to deploy without the
# boundary it creates. bootstrap-agent.sh comes first.
#
# Usage:
#   scripts/setup.sh --region us-west-2 [--repo OWNER/NAME] [--apply]
#
# Options:
#   --region REGION        AWS region for the bootstrap stack (required)
#   --repo OWNER/NAME      GitHub repository (default: resolved with gh)
#   --reviewer LOGIN       GitHub user who must approve a production deploy
#                          (default: the repository owner; repeatable)
#   --apply                execute; without it, print the plan and change nothing

# shellcheck source-path=SCRIPTDIR source=lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"

REGION=""
REPO=""
REVIEWERS=()

while [ $# -gt 0 ]; do
    case "$1" in
        --region)
            REGION="${2:?--region needs a value}"
            shift
            ;;
        --repo)
            REPO="${2:?--repo needs a value}"
            shift
            ;;
        --reviewer)
            REVIEWERS+=("${2:?--reviewer needs a value}")
            shift
            ;;
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

[ -n "$REGION" ] || die "--region is required (see --help)"

export AWS_REGION="$REGION"
require_aws
require_gh
require_cmd jq

[ -n "$REPO" ] || REPO="$(github_repo)"
OWNER="${REPO%/*}"
NAME="${REPO#*/}"
[ "${#REVIEWERS[@]}" -gt 0 ] || REVIEWERS=("$OWNER")

ACCOUNT_ID="$(aws_account_id)"
TEMPLATE="$REPO_ROOT/infrastructure/bootstrap.yaml"
[ -f "$TEMPLATE" ] || die "bootstrap template not found: $TEMPLATE"

info "account:     $ACCOUNT_ID"
info "region:      $REGION"
info "repository:  $REPO"
info "reviewers:   ${REVIEWERS[*]}"

# ---------------------------------------------------------------------------
# Pre-flight: the OIDC provider
# ---------------------------------------------------------------------------
#
# infrastructure/bootstrap.yaml deliberately never creates the provider — it is
# shared with other projects in the account — and defaults to the deploying
# account's own. If the account has none, the stack fails while creating the
# role's trust policy, with an error that names the ARN rather than the cause.

if aws_cli iam list-open-id-connect-providers --output json |
    jq -e '.OpenIDConnectProviderList[]? | select(.Arn | contains("token.actions.githubusercontent.com"))' >/dev/null; then
    ok "GitHub OIDC provider present in account $ACCOUNT_ID"
else
    err "account $ACCOUNT_ID has no OIDC provider for token.actions.githubusercontent.com"
    err "create it once, as an administrator:"
    dim "  aws iam create-open-id-connect-provider \\"
    dim "    --url https://token.actions.githubusercontent.com \\"
    dim "    --client-id-list sts.amazonaws.com"
    exit 1
fi

# ---------------------------------------------------------------------------
# Plan
# ---------------------------------------------------------------------------

log ""
info "plan"
dim "  deploy stack        $CHINTAN_BOOTSTRAP_STACK in $REGION"
dim "  gh secret           AWS_ACCOUNT_ID, AWS_REGION"
dim "  gh variable         BUILD_ROLE_ARN, CFN_DEPLOY_ROLE_ARN"
dim "  gh environment      production (reviewers: ${REVIEWERS[*]}, protected branches only)"
dim "  gh environment      staging"
dim "  gh pages            build_type=workflow"

if ! confirm_apply "$APPLY" "create the bootstrap stack and configure $REPO"; then
    exit 0
fi

# ---------------------------------------------------------------------------
# Bootstrap stack
# ---------------------------------------------------------------------------

info "deploying $CHINTAN_BOOTSTRAP_STACK"
# CloudFormation takes at most 51,200 bytes of template inline; anything larger
# has to be staged through S3. The artifact bucket the stack itself creates is
# the natural place, so it is used whenever it already exists (every update),
# and the first-ever create — a smaller template, before the bucket exists —
# goes inline. A one-time bootstrap that fails on its own template size is the
# kind of thing this script exists to avoid.
BUCKET_NAME="chintan-lambda-${ACCOUNT_ID}-${REGION}"
stage_args=()
if aws_cli s3api head-bucket --bucket "$BUCKET_NAME" >/dev/null 2>&1; then
    stage_args=(--s3-bucket "$BUCKET_NAME" --s3-prefix bootstrap-templates)
fi
aws_cli cloudformation deploy \
    --template-file "$TEMPLATE" \
    --stack-name "$CHINTAN_BOOTSTRAP_STACK" \
    --capabilities CAPABILITY_NAMED_IAM \
    --no-fail-on-empty-changeset \
    "${stage_args[@]+"${stage_args[@]}"}" \
    --parameter-overrides \
    "GitHubOrg=$OWNER" \
    "GitHubRepo=$NAME" \
    --tags Application=Chintan Project=chintan Instance=shared Environment=shared

wait_for_stack "$CHINTAN_BOOTSTRAP_STACK"
ok "$CHINTAN_BOOTSTRAP_STACK deployed"

ROLE_ARN="$(stack_output "$CHINTAN_BOOTSTRAP_STACK" GitHubActionsRoleArn)"
BUILD_ROLE_ARN="$(stack_output "$CHINTAN_BOOTSTRAP_STACK" GitHubBuildRoleArn)"
CFN_DEPLOY_ROLE_ARN="$(stack_output "$CHINTAN_BOOTSTRAP_STACK" CfnDeployRoleArn)"
BUCKET="$(stack_output "$CHINTAN_BOOTSTRAP_STACK" LambdaDeploymentBucketName)"
ok "deploy role:     $ROLE_ARN"
ok "build role:      $BUILD_ROLE_ARN"
ok "CFN role:        $CFN_DEPLOY_ROLE_ARN"
ok "artifact bucket: $BUCKET"

# ---------------------------------------------------------------------------
# Repository secrets and variables
# ---------------------------------------------------------------------------
#
# The workflows build the deploy role ARN from AWS_ACCOUNT_ID rather than storing
# it. The build role and the CloudFormation service role are stored as plain
# variables: neither is secret (the account id is already in the public
# bundle's Cognito domain), and the workflows fall back sensibly when either is
# unset — to the deploy role for the build jobs, to the caller's own permissions
# for CloudFormation — which is what a repository whose bootstrap stack predates
# them gets until this runs.

info "setting repository secrets and variables"
gh secret set AWS_ACCOUNT_ID --repo "$REPO" --body "$ACCOUNT_ID"
gh secret set AWS_REGION --repo "$REPO" --body "$REGION"
gh variable set BUILD_ROLE_ARN --repo "$REPO" --body "$BUILD_ROLE_ARN"
gh variable set CFN_DEPLOY_ROLE_ARN --repo "$REPO" --body "$CFN_DEPLOY_ROLE_ARN"
ok "AWS_ACCOUNT_ID and AWS_REGION set; BUILD_ROLE_ARN and CFN_DEPLOY_ROLE_ARN set"

# ---------------------------------------------------------------------------
# Environments
# ---------------------------------------------------------------------------
#
# v1 created `production` with wait_timer=0 and no reviewers, which is an
# environment in name only: it gated nothing while appearing in the UI as
# protection. A production deploy now waits for a human.

reviewer_ids="$(
    for login in "${REVIEWERS[@]}"; do
        gh api "users/${login}" --jq '{type: "User", id: .id}'
    done | jq -sc .
)"

info "configuring the production environment"
jq -nc --argjson reviewers "$reviewer_ids" \
    '{wait_timer: 0, prevent_self_review: false, reviewers: $reviewers,
      deployment_branch_policy: {protected_branches: true, custom_branch_policies: false}}' |
    gh api "repos/${REPO}/environments/production" --method PUT --input - >/dev/null
ok "production requires approval from ${REVIEWERS[*]} and deploys only from a protected branch"

info "configuring the staging environment"
jq -nc '{wait_timer: 0,
         deployment_branch_policy: {protected_branches: true, custom_branch_policies: false}}' |
    gh api "repos/${REPO}/environments/staging" --method PUT --input - >/dev/null
ok "staging configured (no reviewer: staging exists to be deployed to freely)"

# ---------------------------------------------------------------------------
# Pages
# ---------------------------------------------------------------------------
#
# actions/deploy-pages@v4 requires the job to target the `github-pages`
# environment, which GitHub creates itself the first time Pages is set to build
# from a workflow. deploy-frontend.yaml names that environment; v1 named
# `production`, so the deployment was rejected.

info "enabling GitHub Pages with Actions as the build source"
if gh api -X POST "repos/${REPO}/pages" -f build_type=workflow >/dev/null 2>&1; then
    ok "Pages enabled (workflow)"
elif gh api -X PUT "repos/${REPO}/pages" -f build_type=workflow >/dev/null 2>&1; then
    ok "Pages updated to workflow build"
else
    warn "could not configure Pages via the API"
    warn "set Settings -> Pages -> Source: GitHub Actions by hand"
fi

log ""
ok "setup complete"
info "next:"
dim "  1. store the provider keys:"
dim "       aws ssm put-parameter --type SecureString --name /chintan/<instance>/groq_api_key --value ..."
dim "       aws ssm put-parameter --type SecureString --name /chintan/<instance>/llm_api_key  --value ..."
dim "  2. scripts/bootstrap.sh --instance <instance> --region $REGION --origin https://${OWNER}.github.io --apply"
dim "  3. scripts/invite-user.sh --instance <instance> --email you@example.com --apply"
