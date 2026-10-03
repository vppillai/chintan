#!/usr/bin/env bash
# record-replay.sh — records the live prompt evaluation's replies for the CI
# replay (docs/design/routing.md, "Replay"). The instance's key is read from
# SSM into the environment of the `go test` runs and never printed; every
# prompt is asked CHINTAN_RUNS times, and per prompt the reply a majority of
# runs gave (else the first run's) is written under
# backend/internal/provider/testdata/eval/recordings/, the directory emptied
# first so a recording of a prompt that no longer exists is not left behind.
# The runs' logs go to testdata/eval/record-logs/ (fixture text and replies,
# no key). Run it on the VM in a mirror of the worktree, copy both directories
# back and commit them; the files are replies to the synthetic fixtures and
# hold no user content.
#
#   CHINTAN_INSTANCE  the instance whose key is read    (default: dev)
#   CHINTAN_RUNS      how many times each prompt is run  (default: 3)
#   AWS_PROFILE       a profile that may read the key    (default: the VM's default)
set -euo pipefail
cd "$(dirname "$0")/../../backend"
RUNS=${CHINTAN_RUNS:-3}
OUT=internal/provider/testdata/eval/recordings
LOGS=internal/provider/testdata/eval/record-logs
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
# The model and base URL are part of a recording's key, and the replay runs
# with neither set, so the recording has to be made with the same defaults.
unset LLM_MODEL LLM_BASE_URL
LLM_API_KEY=$(aws ssm get-parameter --region us-west-2 --name "/chintan/${CHINTAN_INSTANCE:-dev}/llm_api_key" --with-decryption --query Parameter.Value --output text)
export LLM_API_KEY LIVE_LLM=1
mkdir -p "$LOGS"
for n in $(seq 1 "$RUNS"); do
    LLM_RECORD="$TMP/$n" go test ./internal/provider -run 'TestLiveEval$' -count=1 -v >"$LOGS/run$n.log" 2>&1 || true
    passed=$(grep -cE '^ *--- PASS: TestLiveEval/[a-z]+/[0-9]+' "$LOGS/run$n.log" || true)
    failed=$(grep -E '^ *--- FAIL: TestLiveEval/[a-z]+/[0-9]+' "$LOGS/run$n.log" | awk '{print $3}' | tr '\n' ' ' || true)
    echo "run $n: $passed passed; failed: ${failed:-none}"
done
unset LLM_API_KEY
mkdir -p "$OUT"
rm -f "$OUT"/*.json
python3 - "$TMP" "$OUT" "$RUNS" <<'PY'
import collections, json, pathlib, sys
tmp, out, runs = pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2]), int(sys.argv[3])
names = sorted({p.name for n in range(1, runs + 1) for p in (tmp / str(n)).glob("*.json")})
agreed = 0
for name in names:
    files = [tmp / str(n) / name for n in range(1, runs + 1) if (tmp / str(n) / name).exists()]
    # The vote is on the reply alone: the token usage beside it differs run to run.
    replies = [json.loads(f.read_text())["reply"] for f in files]
    best, count = collections.Counter(replies).most_common(1)[0]
    if count > len(replies) // 2:
        agreed += 1
        chosen = files[replies.index(best)]
    else:
        chosen = files[0]
    (out / name).write_bytes(chosen.read_bytes())
print(f"{len(names)} prompts recorded under {out}: {agreed} with a majority reply, {len(names) - agreed} split (first run kept)")
PY
