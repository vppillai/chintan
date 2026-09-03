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
#
# Unlike every other script in scripts/, this one always applies. It is the CI
# wrapper for a job that has already been gated, so it passes --apply to
# deploy.sh unconditionally; there is no dry-run mode to ask for.
#
# Usage: ci-deploy-stack.sh   (no flags; every input is an environment variable)

# shellcheck source-path=SCRIPTDIR source=lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"

# README claims every script has --help. It did not: `--help` fell through to
# the required-environment loop below and printed "INSTANCE is required".
case "${1:-}" in
    -h | --help)
        usage_from_header "${BASH_SOURCE[0]}"
        exit 0
        ;;
    '') ;;
    *) die "ci-deploy-stack.sh takes no arguments; every input is an environment variable (see --help)" ;;
esac

# SITE_PATH is optional only so a hand run can omit it for a prod stack, where
# the template's fallback (the instance name) is the right answer. The
# workflow always passes it, because for a staging stack it is not.
for v in INSTANCE ENVIRONMENT LAMBDA_BUCKET LAMBDA_KEY PAGES_HOST REPO_NAME; do
    [ -n "${!v:-}" ] || die "$v is required"
done

TEMPLATE="${TEMPLATE:-infrastructure/template.yaml}"
ALLOWED_ORIGIN="${ALLOWED_ORIGIN:-https://${PAGES_HOST}}"

# The worker is a SEPARATE main package (backend/cmd/worker) and needs its own
# artifact. An empty WorkerCodeKey used to mean "share the API zip", and the API
# entrypoint fed an S3 notification returns {"statusCode":404} with a nil error,
# which Lambda reads as a successful invocation and never retries. That
# failure is silent all the way through — the health smoke test still passes.
# So it is required here, by name, before anything is deployed.
[ -n "${WORKER_KEY:-}" ] ||
    die "WORKER_KEY is required: the worker Lambda must get backend/cmd/worker, not the API zip"

args=(
    --instance "$INSTANCE"
    --environment "$ENVIRONMENT"
    --template "$TEMPLATE"
    --parameter "InstanceName=${INSTANCE}"
    --parameter "Environment=${ENVIRONMENT}"
    --parameter "AllowedOrigin=${ALLOWED_ORIGIN}"
    --parameter "LambdaCodeBucket=${LAMBDA_BUCKET}"
    --parameter "LambdaCodeKey=${LAMBDA_KEY}"
    --parameter "WorkerCodeKey=${WORKER_KEY}"
    --parameter "PagesHost=${PAGES_HOST}"
    --parameter "RepoName=${REPO_NAME}"
    --parameter "SitePath=${SITE_PATH:-}"
    --tag Application=Chintan
    --tag Project=chintan
    --tag "Instance=${INSTANCE}"
    --tag "Environment=${ENVIRONMENT}"
    --apply
)

# Optional parameters declared in the instance config: AlarmEmail,
# MonthlyBudgetUSD, RetentionDays and the rest. Absent means "keep the template
# default", so an untouched config still deploys.
have_alarm_email=0
if [ -n "${EXTRA_PARAMETERS:-}" ]; then
    while IFS= read -r param; do
        [ -n "$param" ] || continue
        case "$param" in
            AlarmEmail=*) have_alarm_email=1 ;;
        esac
        args+=(--parameter "$param")
    done < <(printf '%s' "$EXTRA_PARAMETERS" | jq -r '.[]? // empty')
fi

# The alarm address comes from a repository secret, because this repository is
# public and config/instances/*.yaml is committed. An instance config may still
# set alarm_email — a private fork legitimately would — and when it does it wins,
# because passing --parameter AlarmEmail twice leaves which one lands up to
# argument order rather than to intent.
if [ -n "${ALARM_EMAIL:-}" ]; then
    if [ "$have_alarm_email" = 1 ]; then
        warn "ALARM_EMAIL is set but ${INSTANCE}'s config already sets alarm_email; keeping the config's value"
    else
        args+=(--parameter "AlarmEmail=${ALARM_EMAIL}")
    fi
fi

exec "$REPO_ROOT/scripts/deploy.sh" "${args[@]}"
