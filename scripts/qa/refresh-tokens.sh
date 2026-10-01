#!/usr/bin/env bash
# refresh-tokens.sh — refresh the review tokens file through Cognito's
# refresh-token flow (no password reset). The refresh token never touches
# argv: the CLI reads its parameters from stdin and Python reads the file.
#
#   TOKENS       the tokens file                                  (default: ~/review-tokens.json)
#   STACK        the stack whose user-pool client issued them     (default: chintan-dev-prod)
#   AWS_PROFILE  the profile that may read the stack's outputs    (default: chintan)
set -euo pipefail
export AWS_PROFILE=${AWS_PROFILE:-chintan} AWS_DEFAULT_REGION=${AWS_DEFAULT_REGION:-us-west-2}
TOKENS=${TOKENS:-$HOME/review-tokens.json}
STACK=${STACK:-chintan-dev-prod}
CLIENT=$(aws cloudformation describe-stacks --stack-name "$STACK" --query "Stacks[0].Outputs[?OutputKey==\`UserPoolClientId\`].OutputValue" --output text)
python3 -c 'import json, sys; print(json.dumps({"REFRESH_TOKEN": json.load(open(sys.argv[1]))["refreshToken"]}))' "$TOKENS" |
    aws cognito-idp initiate-auth --client-id "$CLIENT" --auth-flow REFRESH_TOKEN_AUTH --auth-parameters file:///dev/stdin --query AuthenticationResult --output json |
    TOKENS="$TOKENS" python3 -c '
import json, os, sys, time
a = json.load(sys.stdin)
path = os.environ["TOKENS"]
old = json.load(open(path))
json.dump({"idToken": a["IdToken"], "accessToken": a["AccessToken"],
           "refreshToken": a.get("RefreshToken") or old["refreshToken"],
           "expiresAt": int(time.time() * 1000) + a["ExpiresIn"] * 1000}, open(path, "w"))
'
echo "tokens refreshed $(date -u +%H:%MZ)"
