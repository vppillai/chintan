#!/usr/bin/env bash
# Invite (or reset) the first Cognito user for a Chintan instance.
# Uses the chintan agent profile. Does not print the temporary password to logs
# if TEMP_PASSWORD is provided via env; otherwise generates one and prints once.
set -euo pipefail

INSTANCE="${1:-dev}"
EMAIL="${2:-}"
REGION="${AWS_REGION:-us-west-2}"
export AWS_PROFILE="${AWS_PROFILE:-chintan}"

if [ -z "$EMAIL" ]; then
  echo "Usage: $0 <instance> <email>" >&2
  exit 1
fi

STACK="chintan-${INSTANCE}-prod"
POOL_ID="$(aws cloudformation describe-stacks --stack-name "$STACK" --region "$REGION" \
  --query "Stacks[0].Outputs[?OutputKey=='UserPoolId'].OutputValue" --output text)"

if [ -z "$POOL_ID" ] || [ "$POOL_ID" = "None" ]; then
  echo "Could not resolve UserPoolId for $STACK" >&2
  exit 1
fi

TEMP_PASSWORD="${TEMP_PASSWORD:-$(openssl rand -base64 18 | tr -d '/+=' | cut -c1-16)Aa1}"

if aws cognito-idp admin-get-user --user-pool-id "$POOL_ID" --username "$EMAIL" --region "$REGION" >/dev/null 2>&1; then
  aws cognito-idp admin-set-user-password \
    --user-pool-id "$POOL_ID" \
    --username "$EMAIL" \
    --password "$TEMP_PASSWORD" \
    --permanent \
    --region "$REGION"
  echo "Reset permanent password for existing user $EMAIL in $POOL_ID"
else
  aws cognito-idp admin-create-user \
    --user-pool-id "$POOL_ID" \
    --username "$EMAIL" \
    --user-attributes Name=email,Value="$EMAIL" Name=email_verified,Value=true \
    --message-action SUPPRESS \
    --temporary-password "$TEMP_PASSWORD" \
    --region "$REGION" >/dev/null
  aws cognito-idp admin-set-user-password \
    --user-pool-id "$POOL_ID" \
    --username "$EMAIL" \
    --password "$TEMP_PASSWORD" \
    --permanent \
    --region "$REGION"
  echo "Created user $EMAIL in $POOL_ID"
fi

echo "Sign-in username: $EMAIL"
echo "Temporary password (store then clear terminal): $TEMP_PASSWORD"
echo "App: https://vppillai.github.io/chintan/${INSTANCE}/"
