#!/usr/bin/env bash
#
# Generate a VAPID key pair for Web Push (docs/design/push.md) and print the
# two `aws ssm put-parameter` commands that install it for an instance.
#
# It writes nothing to AWS itself: the private key is a secret, and the one
# principal allowed to write under /chintan/<instance>/ is the owner — the
# agent role cannot (infrastructure/agent-policies). Run the printed commands
# yourself, with your own credentials, in the instance's region.
#
# The pair is a P-256 key as RFC 8292 wants it: the private scalar (32 bytes)
# and the uncompressed public point (65 bytes, leading 0x04), each base64url
# without padding. The worker signs each push with the private key
# (VAPID_PRIVATE_KEY_PATH) and the API hands the public key to the browser
# (GET /v1/push/key, VAPID_PUBLIC_KEY_PATH); both read SSM at start, so the
# next cold start after the parameters exist turns the feature on. Only the
# private key is secret. Losing it — or making a new pair — invalidates every
# browser's subscription, which then re-subscribes from the You screen.
#
# Usage:
#   scripts/vapid-keys.sh --instance dev [--region us-west-2]
#
# Options:
#   --instance NAME   instance name, the <instance> in /chintan/<instance>/  (required)
#   --region REGION   AWS region for the printed commands                   (default: $AWS_REGION)

# shellcheck source-path=SCRIPTDIR source=lib/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"

INSTANCE=""

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
require_cmd openssl
require_cmd python3

REGION="$(aws_region)"
REGION_FLAG=""
[ -n "$REGION" ] && REGION_FLAG=" --region $REGION"

tmp="$(mktemp -d)"
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

to_base64url() {
    python3 -c 'import base64, sys; print(base64.urlsafe_b64encode(bytes.fromhex(sys.argv[1])).decode().rstrip("="))' "$1"
}
priv_b64="$(to_base64url "$priv_hex")"
pub_b64="$(to_base64url "$pub_hex")"

info "VAPID key pair for instance '$INSTANCE'. Run these two commands yourself; the private one is a secret."
printf '\n'
# `--value=` rather than `--value `, so a key whose base64url starts with a
# dash is not read as another flag.
printf 'aws ssm put-parameter%s --type SecureString --overwrite --name /chintan/%s/vapid_private_key --value=%s\n' "$REGION_FLAG" "$INSTANCE" "$priv_b64"
printf 'aws ssm put-parameter%s --type SecureString --overwrite --name /chintan/%s/vapid_public_key  --value=%s\n' "$REGION_FLAG" "$INSTANCE" "$pub_b64"
printf '\n'
dim "Then let the API and worker Lambdas cold-start (the next deploy, or wait for the containers to recycle);"
dim "You → Notifications shows the switch once GET /v1/push/key answers 200."
