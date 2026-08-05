#!/usr/bin/env bash
#
# Deploy one instance (§11.4).
#
# **CI is the only sanctioned caller.** I16 and §0.5A: "Deploys happen only from a green
# pipeline, and only through deploy.sh invoked by CI. A local invocation is an
# incident-response measure, recorded as such, not a workflow." The reason is not
# ceremony — it is that deploy credentials live only in CI (§0.5A), which is what lets
# the agent hold none (I17).
#
# So this script refuses to run outside CI unless given --incident-response, which makes
# the exception visible in a terminal history and in whatever is pasted into an incident
# record.
#
# It does not build. `make build-lambda` produces the artifacts and resolves the version
# from git describe; this uploads and deploys them. Keeping the two separate is what
# makes the artifact a function of the commit rather than of the deploy (§0.6, G-036 —
# tag before deploying, because CI resolves git describe during the build and a tag
# pushed afterwards never reaches the artifact).
#
# Usage:
#   scripts/deploy.sh --instance dev                      # dry run
#   scripts/deploy.sh --instance dev --apply               # from CI
#   scripts/deploy.sh --instance dev --apply --incident-response

# shellcheck source-path=SCRIPTDIR source=lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"

APPLY=0
INSTANCE=""
INCIDENT=0
REGION=""
while [ $# -gt 0 ]; do
    case "$1" in
        --apply) APPLY=1 ;;
        --instance)
            INSTANCE="${2:?}"
            shift
            ;;
        --region)
            REGION="${2:?}"
            shift
            ;;
        --incident-response) INCIDENT=1 ;;
        -h | --help)
            sed -n '2,26p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
            exit 0
            ;;
        *) die "unknown flag '$1' (see --help)" ;;
    esac
    shift
done
cd "$REPO_ROOT" || exit 1

[ -n "$INSTANCE" ] || die "--instance is required"
CONFIG="config/instances/${INSTANCE}.yaml"
[ -f "$CONFIG" ] || die "no config for instance '$INSTANCE' at $CONFIG"

# Region comes from config, not from the ambient environment. The AWS CLI's default
# region on a developer machine is whatever they last worked on, and deploying an
# instance into the wrong region would create a parallel stack that looks correct.
if [ -z "$REGION" ]; then
    REGION="$(yq -r '.region' "$CONFIG")"
fi
[ -n "$REGION" ] && [ "$REGION" != "null" ] || die "no region in $CONFIG"
export AWS_REGION="$REGION" AWS_PAGER=""

# I16, enforced rather than asserted.
if [ "${CI:-}" != "true" ] && [ "$INCIDENT" != "1" ]; then
    err "refusing to deploy outside CI."
    err ""
    err "Deploys happen only from a green pipeline, through this script invoked by CI"
    err "(I16, §0.5A). Deploy credentials live only in CI, which is what lets the agent"
    err "hold none (I17)."
    err ""
    err "If this genuinely is incident response, re-run with --incident-response and"
    err "record it as such."
    exit 1
fi
if [ "$INCIDENT" = "1" ]; then
    warn "INCIDENT-RESPONSE DEPLOY — outside the pipeline, by explicit request."
    warn "Record this: who, when, why, and what the pipeline could not do (§0.5A)."
fi

STACK="voicenotes-${INSTANCE}"
BOOTSTRAP_STACK="voicenotes-bootstrap"

aws_cli sts get-caller-identity >/dev/null 2>&1 || die "no AWS credentials"
CALLER="$(aws_cli sts get-caller-identity --query Arn --output text)"
info "caller   $CALLER"
info "instance $INSTANCE, region $REGION"

if printf '%s' "$CALLER" | grep -q ':root$'; then
    die "refusing to deploy as the account root user (§9.4). Every role this stack creates must carry the permissions boundary (G-046)."
fi

# Validate the config with the same code the Lambda runs at cold start, before anything
# is uploaded. An invalid config must fail the deploy, not the first cold start (§7.4).
info "validating $CONFIG"
(cd backend && go run ./cmd/chintanctl config validate "../$CONFIG" >/dev/null) ||
    die "$CONFIG is invalid; nothing was deployed"
ok "config valid"

ARTIFACT_BUCKET="$(aws_cli cloudformation describe-stacks --stack-name "$BOOTSTRAP_STACK" \
    --query "Stacks[0].Outputs[?OutputKey=='ArtifactBucketName'].OutputValue" --output text 2>/dev/null)"
[ -n "$ARTIFACT_BUCKET" ] && [ "$ARTIFACT_BUCKET" != "None" ] ||
    die "cannot resolve the artifact bucket from $BOOTSTRAP_STACK. Run scripts/bootstrap.sh first."
ok "artifact bucket $ARTIFACT_BUCKET"

BUILD_DIR="${CHINTAN_BUILD_DIR:-build}"
for fn in api worker; do
    [ -f "${BUILD_DIR}/${fn}.zip" ] ||
        die "missing ${BUILD_DIR}/${fn}.zip — run 'make build-lambda' first. This script deploys; it does not build, so the artifact stays a function of the commit (§0.6)."
done

# Keys include the commit, so a deploy is traceable to a build and CloudFormation sees a
# changed key when the code changes. A fixed key would leave CFN believing nothing
# changed while the function's code silently differed.
SHA="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
API_KEY="functions/api-${SHA}.zip"
WORKER_KEY="functions/worker-${SHA}.zip"
CONFIG_KEY="config/${INSTANCE}.yaml"
ALLOWED_ORIGIN="$(yq -r '.allowed_origin' "$CONFIG")"
LOG_DAYS="$(yq -r '.retention.log_group_days' "$CONFIG")"

log ""
info "plan"
dim "  stack           $STACK"
dim "  api artifact    s3://${ARTIFACT_BUCKET}/${API_KEY}"
dim "  worker artifact s3://${ARTIFACT_BUCKET}/${WORKER_KEY}"
dim "  config          s3://${ARTIFACT_BUCKET}/${CONFIG_KEY}"
dim "  allowed origin  $ALLOWED_ORIGIN"
dim "  log retention   ${LOG_DAYS} days"

if ! confirm_apply "$APPLY" "upload the artifacts and deploy $STACK"; then
    exit 0
fi

info "uploading artifacts and config"
aws_cli s3 cp "${BUILD_DIR}/api.zip" "s3://${ARTIFACT_BUCKET}/${API_KEY}" >/dev/null
aws_cli s3 cp "${BUILD_DIR}/worker.zip" "s3://${ARTIFACT_BUCKET}/${WORKER_KEY}" >/dev/null
aws_cli s3 cp "$CONFIG" "s3://${ARTIFACT_BUCKET}/${CONFIG_KEY}" >/dev/null
ok "uploaded"

info "deploying $STACK"
aws_cli cloudformation deploy \
    --template-file infrastructure/template.yaml \
    --stack-name "$STACK" \
    --parameter-overrides \
    "InstanceName=$INSTANCE" \
    "AllowedOrigin=$ALLOWED_ORIGIN" \
    "ArtifactBucket=$ARTIFACT_BUCKET" \
    "ApiCodeKey=$API_KEY" \
    "WorkerCodeKey=$WORKER_KEY" \
    "ConfigKey=$CONFIG_KEY" \
    "LogRetentionDays=$LOG_DAYS" \
    --capabilities CAPABILITY_NAMED_IAM \
    --no-fail-on-empty-changeset \
    --tags Project=voicenotes "Instance=$INSTANCE" "Environment=$INSTANCE" \
    ManagedBy=iac Owner=vppillai "CostCenter=voicenotes-${INSTANCE}"

ok "$STACK deployed"

API_ENDPOINT="$(aws_cli cloudformation describe-stacks --stack-name "$STACK" \
    --query "Stacks[0].Outputs[?OutputKey=='ApiEndpoint'].OutputValue" --output text)"

log ""
info "outputs"
aws_cli cloudformation describe-stacks --stack-name "$STACK" \
    --query 'Stacks[0].Outputs[].{Key:OutputKey,Value:OutputValue}' --output table

# Smoke test the one endpoint this phase serves. A deploy that reports success while the
# function cannot start is the failure this catches — and a cold start failure is exactly
# what an invalid config or a missing permission produces.
info "smoke test: GET /v1/health"
if health="$(curl --fail --silent --show-error --max-time 20 "${API_ENDPOINT}/v1/health" 2>&1)"; then
    ok "health responded"
    printf '%s\n' "$health" | (jq . 2>/dev/null || cat) >&2
    deployed_sha="$(printf '%s' "$health" | jq -r '.commit' 2>/dev/null || echo '')"
    if [ -n "$deployed_sha" ] && [ "$deployed_sha" != "$SHA" ]; then
        warn "health reports commit $deployed_sha but this deploy built $SHA — the function may not have swapped code yet"
    fi
    if [ "$(printf '%s' "$health" | jq -r '.stamped' 2>/dev/null)" = "false" ]; then
        # G-036: CI resolves git describe during the build, so a tag pushed after the
        # deploy ran never reaches the artifact.
        warn "the deployed build is UNSTAMPED — tag before deploying, not after (G-036)"
    fi
else
    err "health did not respond: $health"
    err "the stack deployed but the function cannot serve. Check the cold-start log:"
    err "  aws logs tail /aws/lambda/voicenotes-api-${INSTANCE} --region ${REGION}"
    exit 1
fi

if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
    {
        echo "## Deployed \`${INSTANCE}\`"
        echo ""
        echo "| | |"
        echo "|---|---|"
        echo "| stack | \`${STACK}\` |"
        echo "| commit | \`${SHA}\` |"
        echo "| api | ${API_ENDPOINT} |"
    } >>"$GITHUB_STEP_SUMMARY"
fi
