#!/usr/bin/env bash
#
# Assemble the deploy.sh invocation for one matrix entry.
#
# This exists as a script rather than as a `run: |` block so that the lint and
# format gates cover it like everything else in scripts/, and so the staging and
# production jobs cannot drift apart — they were identical thirty-line blocks, and
# identical blocks stop being identical.
#
# Every input arrives through the environment. A ${{ }} expression interpolated
# into a run block is substituted before bash parses the line, so a value
# containing a quote becomes shell syntax rather than data.
#
# Required environment:
#   INSTANCE, ENVIRONMENT, LAMBDA_BUCKET, LAMBDA_KEY, PAGES_HOST, REPO_NAME
# Optional:
#   EXTRA_PARAMETERS      JSON array of "Key=Value" strings from the instance config
#   ALLOWED_ORIGIN        default: https://$PAGES_HOST
#   CFN_DEPLOY_ROLE_ARN   passed through to deploy.sh
#   TEMPLATE              default: infrastructure/template.yaml

# shellcheck source-path=SCRIPTDIR source=lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"

for v in INSTANCE ENVIRONMENT LAMBDA_BUCKET LAMBDA_KEY PAGES_HOST REPO_NAME; do
    [ -n "${!v:-}" ] || die "$v is required"
done

TEMPLATE="${TEMPLATE:-infrastructure/template.yaml}"
ALLOWED_ORIGIN="${ALLOWED_ORIGIN:-https://${PAGES_HOST}}"

args=(
    --instance "$INSTANCE"
    --environment "$ENVIRONMENT"
    --template "$TEMPLATE"
    --parameter "InstanceName=${INSTANCE}"
    --parameter "Environment=${ENVIRONMENT}"
    --parameter "AllowedOrigin=${ALLOWED_ORIGIN}"
    --parameter "LambdaCodeBucket=${LAMBDA_BUCKET}"
    --parameter "LambdaCodeKey=${LAMBDA_KEY}"
    --parameter "WorkerCodeKey=${WORKER_KEY:-}"
    --parameter "PagesHost=${PAGES_HOST}"
    --parameter "RepoName=${REPO_NAME}"
    --tag Application=Chintan
    --tag Project=chintan
    --tag "Instance=${INSTANCE}"
    --tag "Environment=${ENVIRONMENT}"
    --apply
)

# WorkerCodeKey is passed when the caller supplies WORKER_KEY. Empty means the
# template shares LambdaCodeKey, which was correct only while both
# handlers ship in one binary built from backend/cmd/api. Pass it here the day
# the worker gets its own main package.

# Optional parameters declared in the instance config: AlarmEmail,
# MonthlyBudgetUSD, RetentionDays and the rest. Absent means "keep the template
# default", so an untouched config still deploys.
if [ -n "${EXTRA_PARAMETERS:-}" ]; then
    while IFS= read -r param; do
        [ -n "$param" ] || continue
        args+=(--parameter "$param")
    done < <(printf '%s' "$EXTRA_PARAMETERS" | jq -r '.[]? // empty')
fi

exec "$REPO_ROOT/scripts/deploy.sh" "${args[@]}"
