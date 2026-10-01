#!/usr/bin/env bash
# inbox-live.sh — one live check of the device inbox: create a device key as the
# test user, POST the sample clip one-shot, poll the capture until it files, then
# revoke the key and purge what it created.
#   API     the instance's API endpoint (required)
#   TOKENS  the review tokens file (required; see refresh-tokens.sh)
#   CLIP    the webm to post (default: ~/e2e.webm)
set -euo pipefail
: "${API:?API is not set}" "${TOKENS:?TOKENS is not set}"
CLIP=${CLIP:-$HOME/e2e.webm}
# The id token goes to curl through a header file (`-H @file`), never on argv.
AUTH=$(mktemp)
trap 'rm -f "$AUTH"' EXIT
python3 -c "import json;print(\"Authorization: Bearer \" + json.load(open(\"$TOKENS\"))[\"idToken\"])" >"$AUTH"
DEV=$(curl -fsS -X POST "$API/v1/devices" -H "@$AUTH" -H "Content-Type: application/json" -H "Idempotency-Key: dev-$(date +%s)" -d "{\"name\":\"live check\"}")
DID=$(echo "$DEV" | python3 -c "import sys,json;print(json.load(sys.stdin)[\"id\"])")
KEY=$(echo "$DEV" | python3 -c "import sys,json;print(json.load(sys.stdin)[\"key\"])")
echo "device $DID created (key length $(echo -n "$KEY" | wc -c))"
CAP=$(curl -sS -w "\nHTTP %{http_code}" -X POST "$API/v1/inbox/audio" -H "Authorization: Bearer $KEY" -H "Content-Type: audio/webm" -H "X-Chintan-Duration-Ms: 7668" --data-binary @"$CLIP")
echo "$CAP" | tail -1
CID=$(echo "$CAP" | head -1 | python3 -c "import sys,json; d=json.load(sys.stdin); print((d.get(\"capture\") or d)[\"id\"])")
for i in $(seq 1 40); do
    S=$(curl -fsS "$API/v1/captures/$CID" -H "@$AUTH")
    ST=$(echo "$S" | python3 -c "import sys,json;print(json.load(sys.stdin)[\"status\"])")
    echo "  $i: $ST"
    case "$ST" in appended | needs_target | failed | no_content | spend_capped) break ;; esac
    sleep 3
done
echo "$S" | python3 -c "import sys,json; d=json.load(sys.stdin); print(\"source:\", d.get(\"source\"), \"note:\", d.get(\"note_id\"))"
echo "== wrong key → $(curl -s -o /dev/null -w "%{http_code}" -X POST "$API/v1/inbox/audio" -H "Authorization: Bearer ck_x_y" -H "Content-Type: audio/webm" --data-binary @"$CLIP")"
curl -fsS -X DELETE "$API/v1/devices/$DID" -H "@$AUTH" -o /dev/null -w "revoked → %{http_code}\n"
echo "== revoked key → $(curl -s -o /dev/null -w "%{http_code}" -X POST "$API/v1/inbox/audio" -H "Authorization: Bearer $KEY" -H "Content-Type: audio/webm" --data-binary @"$CLIP")"
NOTE=$(echo "$S" | python3 -c "import sys,json;print(json.load(sys.stdin).get(\"note_id\") or \"\")")
if [ -n "$NOTE" ]; then
    AGE=$(curl -fsS "$API/v1/notes/$NOTE" -H "@$AUTH" | python3 -c "import sys,json,datetime; d=json.load(sys.stdin); c=datetime.datetime.fromisoformat(d[\"created_at\"].replace(\"Z\",\"+00:00\")); print(int((datetime.datetime.now(datetime.timezone.utc)-c).total_seconds()))")
    if [ "$AGE" -lt 600 ]; then
        curl -fsS -X DELETE "$API/v1/notes/$NOTE" -H "@$AUTH" -o /dev/null
        curl -fsS -X DELETE "$API/v1/notes/$NOTE/permanent" -H "@$AUTH" -o /dev/null -w "purged the new note → %{http_code}\n"
    else
        echo "routed into an existing note ($AGE s old); deleting only the capture"
        curl -fsS -X DELETE "$API/v1/captures/$CID" -H "@$AUTH" -o /dev/null -w "capture deleted → %{http_code}\n"
    fi
fi
