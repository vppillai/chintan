#!/usr/bin/env bash
#
# Deploy one Chintan instance stack from a workstation.
#
# The normal deploy path is CI (scripts/deploy.sh, driven by
# .github/workflows/deploy-backend.yaml). This script exists for the two cases CI
# cannot serve: the very first deploy of a new instance, before the pipeline has
# anything to deploy, and recovery when the pipeline itself is broken.
#
# It targets the SAME stack CI targets — chintan-<instance>-<environment> — so a
# stack created here is subsequently updated by CI rather than colliding with it.
# v1 deployed `chintan-dev` while CI deployed `chintan-dev-prod`, and because both
# templates create identically named physical resources, the second stack failed
# with AlreadyExists after partially creating the rest.
#
# It also queries the bootstrap stack output `LambdaDeploymentBucketName`. v1
# asked for `LambdaArtifactBucketName`, which infrastructure/bootstrap.yaml has
# never exported, so the query returned empty and the guard below exited: the
# script could not succeed on any input.
#
# Usage:
#   scripts/bootstrap.sh --instance dev --region us-west-2 \
#       --origin https://owner.github.io [--environment prod] [--apply]
#
# Options:
#   --instance NAME     instance name (lowercase, digits, hyphens)
#   --region REGION     AWS region
#   --origin ORIGIN     CORS allowed origin, scheme and host only
#   --environment ENV   prod | staging | dev   (default: prod)
#   --repo OWNER/NAME   GitHub repository (default: resolved with gh)
#   --apply             execute; without it, print the plan and change nothing

# shellcheck source-path=SCRIPTDIR source=lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"

INSTANCE=""
REGION=""
ALLOWED_ORIGIN=""
ENVIRONMENT="prod"
REPO=""

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
        --origin)
            ALLOWED_ORIGIN="${2:?--origin needs a value}"
            shift
            ;;
        --environment)
            ENVIRONMENT="${2:?--environment needs a value}"
            shift
            ;;
        --repo)
            REPO="${2:?--repo needs a value}"
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

[ -n "$INSTANCE" ] || die "--instance is required (see --help)"
[ -n "$REGION" ] || die "--region is required (see --help)"
[ -n "$ALLOWED_ORIGIN" ] || die "--origin is required (see --help)"
validate_instance_name "$INSTANCE"
validate_environment "$ENVIRONMENT"

export AWS_REGION="$REGION"
require_aws
require_cmd jq

STACK="$(stack_name "$INSTANCE" "$ENVIRONMENT")"
TEMPLATE="$REPO_ROOT/infrastructure/template.yaml"
[ -f "$TEMPLATE" ] || die "instance template not found: $TEMPLATE"

[ -n "$REPO" ] || REPO="$(github_repo)"
REPO_NAME="${REPO#*/}"
PAGES_HOST="${ALLOWED_ORIGIN#*://}"
PAGES_HOST="${PAGES_HOST%%/*}"

info "instance:    $INSTANCE"
info "environment: $ENVIRONMENT"
info "stack:       $STACK"
info "region:      $REGION"
info "origin:      $ALLOWED_ORIGIN"
info "repository:  $REPO"

stack_exists "$CHINTAN_BOOTSTRAP_STACK" ||
    die "bootstrap stack '$CHINTAN_BOOTSTRAP_STACK' not found — run scripts/setup.sh --region $REGION --apply first"

# ---------------------------------------------------------------------------
# Artifact bucket
# ---------------------------------------------------------------------------

BUCKET="$(stack_output "$CHINTAN_BOOTSTRAP_STACK" LambdaDeploymentBucketName)"
if [ -z "$BUCKET" ] || [ "$BUCKET" = "None" ]; then
    die "bootstrap stack has no LambdaDeploymentBucketName output; redeploy infrastructure/bootstrap.yaml"
fi
ok "artifact bucket: $BUCKET"

# ---------------------------------------------------------------------------
# Build and upload
# ---------------------------------------------------------------------------

info "building the Lambda package"
run "$REPO_ROOT/scripts/build-lambda.sh"

ZIP="$REPO_ROOT/backend/lambda-function.zip"
S3_KEY="${INSTANCE}/${ENVIRONMENT}/lambda-function.zip"
if is_apply; then
    [ -f "$ZIP" ] || die "Lambda zip not found after build: $ZIP"
fi
run aws_cli s3 cp "$ZIP" "s3://${BUCKET}/${S3_KEY}"

# ---------------------------------------------------------------------------
# Deploy
# ---------------------------------------------------------------------------

info "planned parameters"
dim "  InstanceName=$INSTANCE"
dim "  Environment=$ENVIRONMENT"
dim "  AllowedOrigin=$ALLOWED_ORIGIN"
dim "  LambdaCodeBucket=$BUCKET"
dim "  LambdaCodeKey=$S3_KEY"
dim "  PagesHost=$PAGES_HOST"
dim "  RepoName=$REPO_NAME"

if ! confirm_apply "$APPLY" "deploy $STACK in $REGION"; then
    exit 0
fi

ROLE_ARGS=()
if [ -n "${CFN_DEPLOY_ROLE_ARN:-}" ]; then
    ROLE_ARGS=(--role-arn "$CFN_DEPLOY_ROLE_ARN")
fi

aws_cli cloudformation deploy \
    --template-file "$TEMPLATE" \
    --stack-name "$STACK" \
    --capabilities CAPABILITY_NAMED_IAM \
    --no-fail-on-empty-changeset \
    --parameter-overrides \
    "InstanceName=$INSTANCE" \
    "Environment=$ENVIRONMENT" \
    "AllowedOrigin=$ALLOWED_ORIGIN" \
    "LambdaCodeBucket=$BUCKET" \
    "LambdaCodeKey=$S3_KEY" \
    "PagesHost=$PAGES_HOST" \
    "RepoName=$REPO_NAME" \
    "${ROLE_ARGS[@]}" \
    --tags Application=Chintan Project=chintan \
    "Instance=$INSTANCE" "Environment=$ENVIRONMENT"

wait_for_stack "$STACK"
ok "$STACK deployed"

ENDPOINT="$(stack_output "$STACK" ApiEndpoint)"
log ""
ok "API endpoint: $ENDPOINT"
log ""
info "still required before the instance works:"
dim "  aws ssm put-parameter --type SecureString --name /chintan/${INSTANCE}/groq_api_key --value ..."
dim "  aws ssm put-parameter --type SecureString --name /chintan/${INSTANCE}/llm_api_key  --value ..."
dim "  scripts/invite-user.sh --instance ${INSTANCE} --environment ${ENVIRONMENT} --email you@example.com --apply"
