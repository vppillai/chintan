#!/usr/bin/env bash
#
# Install the VAPID key pair Web Push signs with (docs/design/push.md) for one
# instance, at /chintan/<instance>/vapid_private_key and vapid_public_key.
#
# Dry run by default: it says whether the pair is already there and prints the
# two `aws ssm put-parameter` commands --apply would run, with the values shown
# as <generated>. --apply generates a fresh P-256 pair with openssl and writes
# both parameters, but only when neither exists. It never overwrites: a new pair
# invalidates every browser's subscription, so an installed pair is left alone
# and the script reports it. --rotate is the one way to replace a pair. The
# private key is never printed, with or without --apply; it goes to AWS through
# a mode-600 temporary file rather than the command line, where `ps` would show
# it.
#
# scripts/setup.sh and scripts/bootstrap.sh run this with --apply for every
# instance whose config leaves `web_push` on (the default), so a new instance
# gets its pair when it is stood up. It needs credentials that may write under
# /chintan/<instance>/ — the owner's; the agent role is denied SSM writes
# (infrastructure/agent-policies).
#
# The pair is a P-256 key as RFC 8292 wants it: the private scalar (32 bytes)
# and the uncompressed public point (65 bytes, leading 0x04), each base64url
# without padding. The worker signs each push with the private key
# (VAPID_PRIVATE_KEY_PATH) and the API hands the public key to the browser
# (GET /v1/push/key, VAPID_PUBLIC_KEY_PATH); both read SSM at cold start, so
# the next cold start after the parameters exist turns the feature on (the
# next deploy does it). Only the private key is secret.
#
# Usage:
#   scripts/vapid-keys.sh --instance dev [--region us-west-2] [--apply] [--rotate [--yes]]
#
# Options:
#   --instance NAME   instance name, the <instance> in /chintan/<instance>/  (required)
#   --region REGION   AWS region                                            (default: $AWS_REGION)
#   --apply           write the pair; without it, print the plan and change nothing
#   --rotate          replace an existing pair. Every browser's subscription stops
#                     working until its owner turns the switch on You off and on.
#   --yes             skip the confirmation --rotate --apply asks for

# shellcheck source-path=SCRIPTDIR source=lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"

INSTANCE=""
ROTATE=0

while [ $# -gt 0 ]; do
    case "$1" in
        --instance)
            INSTANCE="${2:?--instance needs a value}"
            shift
            ;;
        --region)
            AWS_REGION="${2:?--region needs a value}"
            export AWS_REGION
            shift
            ;;
        --apply) APPLY=1 ;;
        --dry-run) APPLY=0 ;;
        --rotate) ROTATE=1 ;;
        --yes) ASSUME_YES=1 ;;
        -h | --help)
            usage_from_header "${BASH_SOURCE[0]}"
            exit 0
            ;;
        *) die "unknown flag '$1' (see --help)" ;;
    esac
    shift
done

[ -n "$INSTANCE" ] || die "--instance is required (see --help)"
validate_instance_name "$INSTANCE"
require_aws
require_cmd openssl python3

PRIV_NAME="/chintan/${INSTANCE}/vapid_private_key"
PUB_NAME="/chintan/${INSTANCE}/vapid_public_key"

# Which of the two exist. Names only: describe-parameters never returns a
# value. A failed call stops the script rather than reading as "absent",
# because writing a pair over one that could not be seen is the one thing
# this script must not do.
existing="$(aws_cli ssm describe-parameters \
    --parameter-filters "Key=Name,Option=Equals,Values=${PRIV_NAME},${PUB_NAME}" \
    --query 'Parameters[].Name' --output text)" ||
    die "could not check whether the pair exists (ssm:DescribeParameters failed); nothing was written"
have_priv=0
have_pub=0
case " $existing " in *"$PRIV_NAME"*) have_priv=1 ;; esac
case " $existing " in *"$PUB_NAME"*) have_pub=1 ;; esac

info "VAPID key pair for instance '$INSTANCE' in $(aws_region)"

if [ "$ROTATE" = 0 ]; then
    if [ "$have_priv" = 1 ] && [ "$have_pub" = 1 ]; then
        ok "already installed: $PRIV_NAME and $PUB_NAME; left alone (--rotate replaces them)"
        exit 0
    fi
    # Half a pair cannot sign, and filling in the other half would pair it
    # with a key it was never made with. Replacing both is a rotation.
    if [ "$have_priv" = 1 ] || [ "$have_pub" = 1 ]; then
        die "only one of $PRIV_NAME and $PUB_NAME exists; re-run with --rotate to replace it with a full pair"
    fi
fi

OVERWRITE_FLAG=""
[ "$ROTATE" = 1 ] && OVERWRITE_FLAG=" --overwrite"

if ! is_apply; then
    dim "  would run: aws ssm put-parameter --type SecureString${OVERWRITE_FLAG} --name $PRIV_NAME --value=<generated>"
    dim "  would run: aws ssm put-parameter --type SecureString${OVERWRITE_FLAG} --name $PUB_NAME --value=<generated>"
    [ "$ROTATE" = 1 ] && warn "--rotate invalidates every browser's subscription; each re-subscribes by turning You → Notifications off and on"
    confirm_apply "$APPLY" "generate a fresh pair with openssl and write both parameters (the private key is never printed)" || true
    exit 0
fi

if [ "$ROTATE" = 1 ]; then
    confirm_destructive rotate \
        "rotating the VAPID pair for '$INSTANCE' invalidates every browser's push subscription" \
        "each one stays silent until its owner turns You → Notifications off and on"
fi

tmp="$(mktemp -d)"
chmod 700 "$tmp"
trap 'rm -rf "$tmp"' EXIT

openssl ecparam -name prime256v1 -genkey -noout -out "$tmp/key.pem" 2>/dev/null

# `openssl ec -text` prints the scalar and the point as colon-separated hex
# under `priv:` and `pub:`. Only the hex lines between the two labels are
# taken; the scalar may carry a leading 00 when its top bit is set, which
# is stripped so it is exactly 32 bytes.
text="$(openssl ec -in "$tmp/key.pem" -noout -text 2>/dev/null)"
priv_hex="$(printf '%s\n' "$text" | sed -n '/^priv:/,/^pub:/p' | grep -v -e '^priv:' -e '^pub:' | tr -d ' :\n')"
pub_hex="$(printf '%s\n' "$text" | sed -n '/^pub:/,/^ASN1 OID/p' | grep -v -e '^pub:' -e '^ASN1' | tr -d ' :\n')"
case "${#priv_hex}" in
    66) priv_hex="${priv_hex#00}" ;;
    64) ;;
    *) die "unexpected private scalar length ${#priv_hex} hex characters" ;;
esac
[ "${#pub_hex}" = 130 ] || die "unexpected public point length ${#pub_hex} hex characters"

# The hex goes in on stdin (printf is a builtin, so it is never an argv) and
# comes out as two put-parameter inputs in the private directory, so no value
# ever reaches a command line or the terminal.
printf '%s\n%s\n' "$priv_hex" "$pub_hex" | python3 -c '
import base64, json, os, sys
priv, pub = sys.stdin.read().split()
for path, name, hexval in ((sys.argv[1], sys.argv[3], priv), (sys.argv[2], sys.argv[4], pub)):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w") as f:
        json.dump({
            "Name": name,
            "Type": "SecureString",
            "Value": base64.urlsafe_b64encode(bytes.fromhex(hexval)).decode().rstrip("="),
            "Overwrite": sys.argv[5] == "1",
        }, f)
' "$tmp/private.json" "$tmp/public.json" "$PRIV_NAME" "$PUB_NAME" "$ROTATE"

# Private first: an instance that ends up with only the private half answers
# GET /v1/push/key 404 and offers no switch, whereas a public key alone would
# let browsers subscribe to pushes nothing can sign.
aws_cli ssm put-parameter --cli-input-json "file://$tmp/private.json" >/dev/null
ok "wrote $PRIV_NAME (SecureString)"
aws_cli ssm put-parameter --cli-input-json "file://$tmp/public.json" >/dev/null
ok "wrote $PUB_NAME (SecureString)"

dim "The API and worker pick the pair up at their next cold start (the next deploy does it);"
dim "You → Notifications shows the switch once GET /v1/push/key answers 200."
