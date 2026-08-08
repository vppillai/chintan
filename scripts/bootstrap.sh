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

# Two packages, not one. cmd/api serves HTTP and cmd/worker drains the capture
# queue; they are separate main packages and must be separate artifacts.
#
# This script used to build and upload one zip and pass only LambdaCodeKey,
# which the template reads as "the worker shares the API zip". The API
# entrypoint is Handler(ctx, events.APIGatewayV2HTTPRequest): fed a real SQS
# event it returns {"statusCode":404} with a nil error, and because the event
# source mapping declares FunctionResponseTypes: [ReportBatchItemFailures],
# Lambda reads a success with no batchItemFailures key as "the whole batch
# succeeded" and deletes every uploaded recording from the queue on first
# receive. No retry, no DLQ message, no Errors datapoint, no alarm — and the
# GET /v1/health smoke test below still passes, so the deploy reports success
# while every recording is silently discarded.
info "building the Lambda packages (api and worker)"
ZIP="$REPO_ROOT/backend/lambda-function.zip"
WORKER_ZIP="$REPO_ROOT/backend/worker-function.zip"
run "$REPO_ROOT/scripts/build-lambda.sh" --output "$ZIP" --worker-output "$WORKER_ZIP"

S3_KEY="${INSTANCE}/${ENVIRONMENT}/lambda-function.zip"
WORKER_S3_KEY="${INSTANCE}/${ENVIRONMENT}/worker-function.zip"
if is_apply; then
    [ -f "$ZIP" ] || die "Lambda zip not found after build: $ZIP"
    [ -f "$WORKER_ZIP" ] || die "worker zip not found after build: $WORKER_ZIP"
fi
run aws_cli s3 cp "$ZIP" "s3://${BUCKET}/${S3_KEY}"
run aws_cli s3 cp "$WORKER_ZIP" "s3://${BUCKET}/${WORKER_S3_KEY}"

# ---------------------------------------------------------------------------
# Refresh-token vault key
# ---------------------------------------------------------------------------
#
# Biometric unlock seals one Cognito refresh token per device with AES-256-GCM
# under this key. It is an SSM SecureString, encrypted by the AWS-managed
# aws/ssm key, and it replaced a customer-managed KMS key that cost $1/month —
# the entire idle bill of an instance.
#
# It is created here rather than in the template because CloudFormation cannot
# declare a SecureString parameter at all, the same reason the two provider keys
# are created out of band. Generated rather than asked for, because "invent 32
# secure bytes and base64 them" is not a thing to leave in a README step.
#
# Created BEFORE the stack, so the API Lambda finds it on its first init rather
# than starting with biometric unlock disabled until something forces a cold
# start.
VAULT_KEY_PATH="/chintan/${INSTANCE}/token_vault_key"
if aws_cli ssm get-parameter --name "$VAULT_KEY_PATH" >/dev/null 2>&1; then
    ok "refresh-token vault key already exists at $VAULT_KEY_PATH"
    dim "  left alone: replacing it makes every enrolled device re-enrol"
else
    info "creating the refresh-token vault key at $VAULT_KEY_PATH"
    if is_apply; then
        # 32 bytes from the kernel CSPRNG, base64 for transport. Never echoed.
        vault_key="$(head -c 32 /dev/urandom | base64)"
        aws_cli ssm put-parameter --name "$VAULT_KEY_PATH" --type SecureString \
            --description "Chintan ${INSTANCE} refresh-token vault key (AES-256)" \
            --value "$vault_key" >/dev/null ||
            die "could not create $VAULT_KEY_PATH"
        unset vault_key
        ok "vault key created"
    else
        dim "  would generate 32 random bytes and store them as a SecureString"
    fi
fi

# ---------------------------------------------------------------------------
# Deploy
# ---------------------------------------------------------------------------

info "planned parameters"
dim "  InstanceName=$INSTANCE"
dim "  Environment=$ENVIRONMENT"
dim "  AllowedOrigin=$ALLOWED_ORIGIN"
dim "  LambdaCodeBucket=$BUCKET"
dim "  LambdaCodeKey=$S3_KEY"
dim "  WorkerCodeKey=$WORKER_S3_KEY"
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
    "WorkerCodeKey=$WORKER_S3_KEY" \
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
