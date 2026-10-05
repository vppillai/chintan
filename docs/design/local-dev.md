# The local development path

Running the app on one machine with no AWS account, no key and no network:
`scripts/dev/local.sh` builds `backend/cmd/local` and starts the frontend dev
server pointed at it. Code: `backend/cmd/local/main.go` (the wiring, the
bucket endpoint, the hosted-UI stand-in), `cmd/local/local_test.go` (the boot
test CI runs), the doubles it wires (`internal/repository/dynamofake`,
`internal/repository/memory`, `internal/provider/fake`) and
`provider.NewReplayLLM`.

## What it is

One process on a loopback address serving three things the deployment
spreads over API Gateway, two Lambdas, S3 and Cognito:

- **The API** — `handler.New` over the same services `cmd/api` builds, on
  the real `repository.DynamoStore` over the fake table and the in-memory
  bucket. Every route is there; what differs is what is behind it.
- **The worker** — the same `pipeline.New` and `pipeline.NewWorker` as
  `cmd/worker`, called on a goroutine where the deployment invokes a Lambda:
  the API's hand-offs (`service.Invoker`) and the bucket's `ObjectCreated`
  notification both go through `Worker.Handle` with the payloads the
  deployment sends, so the capture path runs stage by stage as it does there
  (`worker.md`). No retries and no dead-letter queue: a run that fails is one
  log line.
- **The bucket** — `PUT` and `GET /objects/<key>` on the same port, which is
  what the "presigned" URLs the API hands out point at. The PUT carries the
  same `Content-Type` and `x-amz-tagging` headers as the S3 one, stores the
  object with its tags, and then hands the worker the S3 event for the key,
  so a recording uploaded from the app files exactly as a deployed one does.
  The tenant's retention tags are written and nothing expires them.

The models are doubles. Speech-to-text answers a fixed sentence
(`CHINTAN_LOCAL_TRANSCRIPT`) for every recording, or the `?text=` a PUT on
`/objects/<key>` carried — a scripted transcript from `curl`, since the app's
upload cannot add one. The language model is `provider/fake`: the router
files everything as a new note titled "Fake routed note" (`fake.Router`), the
cleanup lower-cases the transcript behind a `[faithful]` marker, items split
on commas and "and", Ask cites every packed note. With
`CHINTAN_LOCAL_RECORDINGS=backend/internal/provider/testdata/eval/recordings`
a prompt the replay set recorded is answered with the model's recorded reply
(`provider.NewReplayLLM`, `routing.md` "Replay") and a miss falls back to the
fakes. A fixture transcript sent as `?text=` replays the recorded cleanup and
items replies; the routing reply replays only when the tenant's notes carry
the fixture's titles and aliases, because the route prompt embeds the
candidate list and the recording key covers it, so in an empty tenant the
fake router files it.

Auth is one static bearer for one tenant (`local`): the verifier accepts
`CHINTAN_LOCAL_TOKEN` in constant time and nothing else, and the three
hosted-UI routes the app uses (`/oauth2/authorize`, `/oauth2/token`,
`/logout`) are served on the same port so the app's own sign-in
(`auth.md`) runs unchanged with `VITE_COGNITO_DOMAIN` pointed at it: the
code is the constant `local`, the token endpoint ignores `grant_type`, the
code and the PKCE verifier (the challenge is the client's alone) and answers
the accepted bearer, and a sign-out returns to the app. A request whose
`Host` is not the process's own address answers 421, against DNS rebinding. Passkeys have no stand-in; dismiss the nudge.

## Never deployable

- `cmd/local` refuses to start unless `CHINTAN_LOCAL=1` **and** the listen
  address is a literal loopback IP (`127.0.0.0/8`, `::1`): a static token on a
  reachable interface would be an open instance
  (`TestLocalRefusesWithoutTheFlagOrOffLoopback`).
- `scripts/build-lambda.sh` packages `cmd/api` and `cmd/worker` only, and
  `TestProductionBinaryDoesNotLinkTestDoubles` reads that list off the script
  and fails if it changes, then asserts neither of them (nor `chintanctl`)
  reaches the doubles. A `cmd/local` added to the build script is a failed
  test, not a deployment.
- `LLM_REPLAY` and `LLM_RECORD` stay test-only (`recordReplayAllowed`);
  `NewReplayLLM` is a client that can only read recordings, with a closed
  loopback port for a base URL, and the Lambda binaries do not call it.
- The `ALLOWED_ORIGIN` wildcard is refused here as it is in `cmd/api`.

## Running it

```bash
scripts/dev/local.sh            # backend on 127.0.0.1:8787, app on http://localhost:5173
```

Open the app, press Sign in (it returns at once), record, and watch the
filing row: the fixed sentence becomes a note. To script a recording:

```bash
T=local-dev-token; A=http://127.0.0.1:8787
C=$(curl -s -X POST $A/v1/captures -H "Authorization: Bearer $T" -H 'Content-Type: application/json' \
    -d '{"content_type":"audio/webm","size_bytes":3,"duration_ms":3000}')
U=$(echo "$C" | python3 -c 'import json,sys; print(json.load(sys.stdin)["upload"]["url"])')
curl -s -X PUT "$U?text=buy+milk+and+eggs" -H 'Content-Type: audio/webm' -H 'x-amz-tagging: chintan-artifact=capture-audio' -d abc
```

The boot test does the same round trip over the fakes, through every stage
to a note, and CI runs it with the rest of the Go suite.

## What it cannot do

- Nothing persists: Ctrl-C loses every note. There is no file store and
  none is planned; a local run is for working on the app, not for keeping
  notes.
- No real speech and no real model unless a prompt is in the replay set.
  Evaluating a prompt change is `prompts.md` "Changing a prompt", on the VM
  with the instance key.
- No Web Push (no VAPID pair), no AWS cost line on `/usage`, no scheduled
  tasks (the sweep, the snapshot, the reconcile run only from the
  deployment's rules), no retries of a failed worker run, no retention
  expiry.
- Playwright does not use it: `frontend/e2e/fixtures.ts` remains the e2e
  suite's stub server, because the specs script the API's answers per case
  and a live backend cannot be told what to say.
