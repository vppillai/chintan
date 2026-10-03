#!/usr/bin/env bash
# record-replay.sh — records the live prompt evaluation's replies for the CI
# replay (docs/design/routing.md, "Replay"). The instance's key is read from
# SSM into the environment of the `go test` runs and never printed; every
# prompt is asked CHINTAN_RUNS times, and per prompt the reply a majority of
# runs gave (else the first run's) is written under
# backend/internal/provider/testdata/eval/recordings/. Run it on the VM in a
# mirror of the worktree, copy that directory back and commit it; the files
# are replies to the synthetic fixtures and hold no user content.
#
#   CHINTAN_INSTANCE  the instance whose key is read    (default: dev)
#   CHINTAN_RUNS      how many times each prompt is run  (default: 3)
#   AWS_PROFILE       a profile that may read the key    (default: the VM's default)
set -euo pipefail
cd "$(dirname "$0")/../../backend"
RUNS=${CHINTAN_RUNS:-3}
OUT=internal/provider/testdata/eval/recordings
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
LLM_API_KEY=$(aws ssm get-parameter --region us-west-2 --name "/chintan/${CHINTAN_INSTANCE:-dev}/llm_api_key" --with-decryption --query Parameter.Value --output text)
export LLM_API_KEY LIVE_LLM=1
for n in $(seq 1 "$RUNS"); do
    LLM_RECORD="$TMP/$n" go test ./internal/provider -run 'TestLiveEval$' -count=1 -v >"$TMP/run$n.log" 2>&1 || true
    passed=$(grep -cE '^ *--- PASS: TestLiveEval/[a-z]+/[0-9]+' "$TMP/run$n.log" || true)
    failed=$(grep -E '^ *--- FAIL: TestLiveEval/[a-z]+/[0-9]+' "$TMP/run$n.log" | awk '{print $3}' | tr '\n' ' ')
    echo "run $n: $passed passed; failed: ${failed:-none}"
done
unset LLM_API_KEY
python3 - "$TMP" "$OUT" "$RUNS" <<'PY'
import collections, pathlib, sys
tmp, out, runs = pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2]), int(sys.argv[3])
out.mkdir(parents=True, exist_ok=True)
names = sorted({p.name for n in range(1, runs + 1) for p in (tmp / str(n)).glob("*.json")})
agreed = 0
for name in names:
    replies = [(tmp / str(n) / name).read_bytes() for n in range(1, runs + 1) if (tmp / str(n) / name).exists()]
    best, count = collections.Counter(replies).most_common(1)[0]
    if count > len(replies) // 2:
        agreed += 1
    else:
        best = replies[0]
    (out / name).write_bytes(best)
print(f"{len(names)} prompts recorded under {out}: {agreed} with a majority reply, {len(names) - agreed} split (first run kept)")
PY
