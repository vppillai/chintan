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
#   INSTANCE, ENVIRONMENT, LAMBDA_BUCKET, LAMBDA_KEY, WORKER_KEY, PAGES_HOST, REPO_NAME
# Optional:
#   EXTRA_PARAMETERS      JSON array of "Key=Value" strings from the instance config
#   APP_HOST              the Pages custom domain (app_host in the instance
#                         config); the site base and the CORS origin follow it
#   ALLOWED_ORIGIN        default: https://$APP_HOST, else https://$PAGES_HOST
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

# --help is handled before the required-environment loop so it works with no
# environment set; otherwise it would fall through and print "INSTANCE is
# required".
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

# Where the bundles are served from. On GitHub Pages that is the owner's Pages
# host plus the repository name; behind a custom domain (app_host, which is
# also the Pages site's own custom domain) it is the host alone, because Pages
# serves a custom domain from its root. The template takes the base for the
# Cognito callback URLs and the origin for CORS separately — an origin has no
# path — and both come from the same host here so they cannot disagree.
if [ -n "${APP_HOST:-}" ]; then
    SITE_ORIGIN="https://${APP_HOST}"
    SITE_BASE_URL="$SITE_ORIGIN"
else
    SITE_ORIGIN="https://${PAGES_HOST}"
    SITE_BASE_URL="${SITE_ORIGIN}/${REPO_NAME}"
fi
ALLOWED_ORIGIN="${ALLOWED_ORIGIN:-$SITE_ORIGIN}"

# The worker is a SEPARATE main package (backend/cmd/worker) and needs its own
# artifact. Were the worker given the API zip instead, the API entrypoint fed
# an S3 notification would return {"statusCode":404} with a nil error,
# which Lambda reads as a successful invocation and never retries. That
# failure is silent all the way through — the health smoke test still passes.
# So it is required here, by name, before anything is deployed.
[ -n "${WORKER_KEY:-}" ] ||
    die "WORKER_KEY is required: the worker Lambda must get backend/cmd/worker, not the API zip"

# The passkey relying party is the pool's managed-login domain. Cognito caps the
# value at 63 characters and the prefix-domain scheme produces 62 for prod and
# 65 for staging, so it is passed only when it fits; the template treats an
# empty value as "password only". The account id is read from the caller
# rather than from an environment variable so a hand run needs nothing extra.
ACCOUNT_ID="$(aws_cli sts get-caller-identity --query Account --output text)"
[ -n "$ACCOUNT_ID" ] || die "could not resolve the AWS account id"
REGION="$(aws_cli configure get region 2>/dev/null || printf '%s' "${AWS_REGION:-${AWS_DEFAULT_REGION:-}}")"
[ -n "$REGION" ] || die "could not resolve the AWS region"
PASSKEY_RP_ID="chintan-${INSTANCE}-${ENVIRONMENT}-${ACCOUNT_ID}.auth.${REGION}.amazoncognito.com"
if [ "${#PASSKEY_RP_ID}" -gt 63 ]; then
    warn "passkeys stay off for $INSTANCE/$ENVIRONMENT: relying party '$PASSKEY_RP_ID' is ${#PASSKEY_RP_ID} characters, Cognito allows 63"
    PASSKEY_RP_ID=""
fi

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
    --parameter "SiteBaseUrl=${SITE_BASE_URL}"
    --parameter "SitePath=${SITE_PATH:-}"
    --parameter "PasskeyRelyingPartyID=${PASSKEY_RP_ID}"
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
