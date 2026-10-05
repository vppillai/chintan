#!/usr/bin/env bash
# local.sh — the development path without AWS (docs/design/local-dev.md):
# cmd/local (the API, the worker and the bucket in one process on loopback,
# over the fakes) and the frontend dev server pointed at it. Needs Go and Bun
# on this machine. Ctrl-C stops both; everything is in memory and is gone.
#
#   CHINTAN_LOCAL_ADDR        the backend's loopback address  (default 127.0.0.1:8787)
#   CHINTAN_LOCAL_TOKEN       the one bearer it accepts        (default local-dev-token)
#   CHINTAN_LOCAL_TRANSCRIPT  what every recording "says"      (default: a sentence)
#   CHINTAN_LOCAL_RECORDINGS  a replay directory for the model (default: none, the fakes answer)
set -eu
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
ADDR=${CHINTAN_LOCAL_ADDR:-127.0.0.1:8787}
BIN=${TMPDIR:-/tmp}/chintan-local

(cd "$ROOT/backend" && go build -o "$BIN" ./cmd/local)
# The built binary rather than `go run`, so the trap below reaches the server and not only the go tool.
CHINTAN_LOCAL=1 CHINTAN_LOCAL_ADDR=$ADDR ALLOWED_ORIGIN=http://localhost:5173 "$BIN" &
trap 'kill $! 2>/dev/null' EXIT

cd "$ROOT/frontend"
VITE_API_URL="http://$ADDR" VITE_COGNITO_DOMAIN="http://$ADDR" \
    VITE_USER_POOL_ID=local VITE_CLIENT_ID=local VITE_INSTANCE=local \
    bun run dev -- --host localhost --port 5173 --strictPort
