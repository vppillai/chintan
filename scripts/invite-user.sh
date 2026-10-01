#!/usr/bin/env bash
#
# Invite (or reset) a Cognito user for a Chintan instance.
#
# The temporary password is NOT printed. A credential echoed to the terminal
# ends up in a scrollback buffer and, when the script is run from an agent, in a
# transcript.
#
# It is written to a file, mode 600, the same way scripts/bootstrap-agent.sh
# handles the one access key it creates. --print-password overrides that when a
# human is sitting at the terminal and wants to read it off the screen; that is a
# deliberate act, not the default.
#
# The password is also genuinely temporary: the account is left in
# FORCE_CHANGE_PASSWORD so it must be changed at first sign-in. A --permanent
# password would make the invitation password the account password indefinitely.
#
# --disable and --delete are the offboarding half. Neither touches the tenant's
# data: a disabled account keeps its notes for when it is enabled again, and a
# deleted one must be erased with chintanctl FIRST, because once the Cognito
# user is gone nothing maps the email back to the tenant id its data lives under.
# --delete --apply asks you to type `delete`, the same prompt teardown.sh and
# cleanup-aws.sh use; --yes skips it for a scripted run.
#
# The shell never holds the password. A python block generates it, writes the
# AWS CLI's --cli-input-json document and the password file, both mode 600 in
# a private temporary directory, the way scripts/vapid-keys.sh handles the
# VAPID pair; the shell only runs the CLI on the one file and copies or cats
# the other, so the value is never an argv that `ps` would show.
#
# Usage:
#   scripts/invite-user.sh --instance dev --email you@example.com [--apply]
#   scripts/invite-user.sh --instance dev --email you@example.com --disable [--apply]
#   scripts/invite-user.sh --instance dev --email you@example.com --delete [--apply]
#
# Options:
#   --instance NAME       instance name                        (required)
#   --email ADDRESS       the user's email, also the username  (required)
#   --environment ENV     prod | staging | dev                 (default: prod)
#   --region REGION       AWS region                           (default: $AWS_REGION)
#   --password-out PATH   write the temporary password here    (default: ./chintan-invite-<email>)
#   --print-password      print it to the terminal instead of writing a file
#   --disable             refuse the user's sign-in and token refresh; their notes stay
#   --delete              remove the account from the pool; run
#                         `chintanctl erase --instance <name> --tenant <sub> --apply` first
#   --yes                 skip the typed confirmation --delete --apply asks for
#   --apply               execute; without it, print the plan and change nothing
#   --dry-run             the default: print the plan and change nothing
#
# TEMP_PASSWORD may be supplied in the environment to use a specific value.

# shellcheck source-path=SCRIPTDIR source=lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"

INSTANCE=""
EMAIL=""
ENVIRONMENT="prod"
PASSWORD_OUT=""
PRINT_PASSWORD=0
MODE=invite

while [ $# -gt 0 ]; do
    case "$1" in
        --instance)
            INSTANCE="${2:?--instance needs a value}"
            shift
            ;;
        --email)
            EMAIL="${2:?--email needs a value}"
            shift
            ;;
        --environment)
            ENVIRONMENT="${2:?--environment needs a value}"
            shift
            ;;
        --region)
            AWS_REGION="${2:?--region needs a value}"
            export AWS_REGION
            shift
            ;;
        --password-out)
            PASSWORD_OUT="${2:?--password-out needs a value}"
            shift
            ;;
        --print-password) PRINT_PASSWORD=1 ;;
        --disable) MODE=disable ;;
        --delete) MODE=delete ;;
        --yes) ASSUME_YES=1 ;;
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
[ -n "$EMAIL" ] || die "--email is required (see --help)"
validate_instance_name "$INSTANCE"
validate_environment "$ENVIRONMENT"

# No AWS_PROFILE is set here. A hardcoded profile name makes a clone with a
# correctly configured default profile fail with an opaque profile-not-found
# error. Pass --profile through the environment if you use one.
require_aws
require_cmd python3

STACK="$(stack_name "$INSTANCE" "$ENVIRONMENT")"
stack_exists "$STACK" || die "stack $STACK not found in region ${AWS_REGION:-<default>}"

POOL_ID="$(stack_output "$STACK" UserPoolId)"
[ -n "$POOL_ID" ] && [ "$POOL_ID" != "None" ] || die "could not resolve UserPoolId from $STACK"

info "stack:      $STACK"
info "user pool:  $POOL_ID"
info "email:      $EMAIL"

if [ -z "$PASSWORD_OUT" ]; then
    PASSWORD_OUT="./chintan-invite-$(printf '%s' "$EMAIL" | tr -c 'A-Za-z0-9.@_-' '-')"
fi

EXISTS=0
if aws_cli cognito-idp admin-get-user --user-pool-id "$POOL_ID" --username "$EMAIL" >/dev/null 2>&1; then
    EXISTS=1
fi

if [ "$MODE" != "invite" ]; then
    [ "$EXISTS" = "1" ] || die "$EMAIL is not in $POOL_ID"
    # The sub is the tenant id every DynamoDB key and S3 prefix hangs off
    # (README → Tenancy). It is printed before a delete because afterwards
    # nothing maps the email back to the data.
    SUB="$(aws_cli cognito-idp admin-get-user --user-pool-id "$POOL_ID" --username "$EMAIL" \
        --query "UserAttributes[?Name=='sub'].Value | [0]" --output text)"
    info "tenant id:  $SUB"
    if [ "$MODE" = "delete" ]; then
        warn "deleting the account leaves the tenant's notes and recordings in place; erase them first:"
        dim "  chintanctl erase --instance $INSTANCE --environment $ENVIRONMENT --tenant $SUB --apply"
    fi
    if ! confirm_apply "$APPLY" "$MODE $EMAIL in $POOL_ID"; then
        exit 0
    fi
    if [ "$MODE" = "disable" ]; then
        aws_cli cognito-idp admin-disable-user --user-pool-id "$POOL_ID" --username "$EMAIL"
        ok "disabled $EMAIL: sign-in and token refresh are refused until admin-enable-user"
    else
        confirm_destructive delete \
            "deleting $EMAIL from $POOL_ID cannot be undone" \
            "afterwards nothing maps the email back to tenant $SUB"
        aws_cli cognito-idp admin-delete-user --user-pool-id "$POOL_ID" --username "$EMAIL"
        ok "deleted $EMAIL from $POOL_ID"
    fi
    exit 0
fi

if [ "$EXISTS" = "1" ]; then
    info "user exists; this will reset the password and force a change at next sign-in"
else
    info "user does not exist; this will create it"
fi

if ! confirm_apply "$APPLY" "$([ "$EXISTS" = "1" ] && echo "reset" || echo "create") $EMAIL in $POOL_ID"; then
    exit 0
fi

tmp="$(mktemp -d)"
chmod 700 "$tmp"
trap 'rm -rf "$tmp"' EXIT

# --permanent is deliberately absent from the reset document:
# admin-set-user-password without it puts the account into
# FORCE_CHANGE_PASSWORD, which is the state the sign-in flow and the README
# both assume.
python3 -c '
import json, os, secrets, sys
pool, user, exists, tmp = sys.argv[1:5]
# 20 URL-safe characters plus one of each required class, so the value always
# satisfies the pool password policy without depending on chance.
password = os.environ.get("TEMP_PASSWORD") or secrets.token_urlsafe(15) + "Aa1!"
if exists == "1":
    doc = {"UserPoolId": pool, "Username": user, "Password": password}
else:
    doc = {
        "UserPoolId": pool,
        "Username": user,
        "UserAttributes": [
            {"Name": "email", "Value": user},
            {"Name": "email_verified", "Value": "true"},
        ],
        "MessageAction": "SUPPRESS",
        "TemporaryPassword": password,
    }
for name, text in (("input.json", json.dumps(doc)), ("password", password + "\n")):
    fd = os.open(os.path.join(tmp, name), os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w") as f:
        f.write(text)
' "$POOL_ID" "$EMAIL" "$EXISTS" "$tmp"

if [ "$EXISTS" = "1" ]; then
    aws_cli cognito-idp admin-set-user-password --cli-input-json "file://$tmp/input.json" >/dev/null
    ok "password reset for $EMAIL"
else
    aws_cli cognito-idp admin-create-user --cli-input-json "file://$tmp/input.json" >/dev/null
    ok "created $EMAIL"
fi

log ""
info "username: $EMAIL"
if [ "$PRINT_PASSWORD" = "1" ]; then
    warn "printing the temporary password because --print-password was passed"
    warn "clear your terminal afterwards"
    printf 'temporary password: '
    cat "$tmp/password"
else
    umask 077
    cp "$tmp/password" "$PASSWORD_OUT"
    chmod 600 "$PASSWORD_OUT"
    ok "temporary password written to $PASSWORD_OUT (mode 600, not printed)"
    dim "  delete it once the user has signed in: rm -f $PASSWORD_OUT"
fi

log ""
info "the user must change this password at first sign-in."
# The app URL is derived from the repository, not hardcoded: a fixed URL would
# point every operator of every fork at the original owner's deployment.
if repo="$(github_repo 2>/dev/null)"; then
    site_path="$INSTANCE"
    [ "$ENVIRONMENT" = "prod" ] || site_path="${INSTANCE}-${ENVIRONMENT}"
    dim "  app: https://${repo%/*}.github.io/${repo#*/}/${site_path}/"
else
    dim "  app: https://<owner>.github.io/<repo>/${INSTANCE}/"
fi
