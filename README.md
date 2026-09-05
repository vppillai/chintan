# Chintan

**Speak a thought. It files itself.**

Chintan is a personal, mobile-friendly PWA for voice brain dumps: you speak unstructured thoughts while driving or walking, and it transcribes the recording, cleans up the text and files it into the right note.

It runs on serverless AWS — Cognito, API Gateway, an API Lambda and a multi-task worker Lambda (captures, retries, the weekly expiry sweep, the daily AWS-cost reading, whole-note clean, Ask), DynamoDB and S3 — and every instance is its own isolated stack. GitHub Pages hosts one independently built frontend bundle per instance, compiled against that instance's stack outputs (API URL, user pool, client id, Cognito domain) by `scripts/ci-build-site.sh` from `deploy-frontend.yaml`. Speech-to-text is **Groq**, and cleanup and routing use an OpenAI-compatible endpoint, **MiniMax** by default, with the API keys in SSM Parameter Store.

- [Deploy your own](#deploy-your-own) · [Configure](#configure) · [Operate](#operate) · [Security](#security) · [Develop](#develop)
- Current backlog and QA ledger (open and completed items, with decisions): [`docs/backlog.md`](docs/backlog.md). The API: [`docs/api/openapi.yaml`](docs/api/openapi.yaml).

---

## Deploy your own

Start with the doctor. It checks every prerequisite below, looks at the AWS account and the GitHub repository, and prints the next command to run; run it again after each step.

```bash
scripts/doctor.sh --instance dev --region us-west-2
```

Everything is CloudFormation, every stack is `chintan-<instance>-<environment>`, every secret is `/chintan/<instance>/<key>`, and every script has `--help`. Every script you run by hand is a **dry run until you pass `--apply`**.

### Prerequisites

- A fork of this repository, cloned, with `gh` authenticated to it.
- An AWS account. **Administrative credentials for step 1 only**; every later step runs as the bounded role step 1 creates, or from CI.
- `aws` CLI v2, `gh`, `jq`, `curl`, `zip`, `python3` with PyYAML.
- Go (the version in `backend/go.mod`) and Bun (the version pinned in `.github/workflows/deploy-frontend.yaml`) — only for a local build, the test suites and a deploy from your own machine. CI has its own.
- API keys: **Groq** for speech-to-text ([console.groq.com](https://console.groq.com/)) and **MiniMax**, or any OpenAI-compatible endpoint, for cleanup ([api.minimax.io](https://api.minimax.io/)).

### The sequence

```bash
# 1. Once, as an administrator: the agent principal, its permissions boundary and CloudTrail.
scripts/bootstrap-agent.sh --region us-west-2 --apply

# 2. Once per account and fork: the bootstrap stack (artifact bucket, GitHub OIDC deploy
#    and build roles), the repository secrets and variables, the environments, Pages.
scripts/setup.sh --region us-west-2 --apply

# 3. The provider keys, per instance. The Lambdas read them by path at run time.
aws ssm put-parameter --type SecureString --name /chintan/dev/groq_api_key --value "gsk_..."
aws ssm put-parameter --type SecureString --name /chintan/dev/llm_api_key  --value "..."

# 4. Deploy. Staging first, smoke-tested; production waits for your approval on the
#    `production` environment; the frontend follows to Pages on its own.
gh workflow run deploy-backend.yaml

# 5. The first user. The temporary password goes to ./chintan-invite-<email>, mode 600.
scripts/invite-user.sh --instance dev --email you@example.com --apply
```

Then open `https://<owner>.github.io/<repo>/<site_path>/` — `https://<owner>.github.io/chintan/dev/` for the shipped config.

What each step needs and leaves behind:

1. `bootstrap-agent.sh` is the only script that needs administrative credentials and the only one that touches IAM outside CloudFormation. It creates the `chintan-agent-boundary` managed policy that `infrastructure/bootstrap.yaml` names — so step 2 fails without it — plus the `chintan-agent` role and `chintan-agent-cli` user that everything else can run as, and the `chintan-trail` CloudTrail trail. `CHINTAN_KEY_OUT=~/.chintan/agent.env scripts/bootstrap-agent.sh --apply` also writes the CLI user's access key to that file instead of ever printing it. The policy documents are the JSON files in `infrastructure/agent-policies/`.
2. `setup.sh` needs the account to have a GitHub OIDC provider for `token.actions.githubusercontent.com`; it checks, and prints the one administrator command that creates it if missing (the templates never create it, because it is shared with other projects). It deploys `chintan-bootstrap`, sets the `AWS_ACCOUNT_ID` and `AWS_REGION` repository secrets and the `BUILD_ROLE_ARN` and `CFN_DEPLOY_ROLE_ARN` variables, makes `production` require your approval, and switches Pages to build from Actions.
3. The keys are `SecureString`s created outside CloudFormation because `AWS::SSM::Parameter` cannot declare that type. They are per instance, so `dev` staging and `dev` prod share them.
4. `Deploy Backend` runs the tests, builds the two arm64 Lambda packages, deploys `chintan-dev-staging` through a printed change set and smoke-tests it (`/v1/health` and `/v1/health/ready`, which round-trips DynamoDB and S3), then deploys `chintan-dev-prod` once you approve, tags the commit `vX.Y.(Z+1)` and publishes a release. `Deploy Frontend` runs when that completes, builds one Vite bundle per instance from the deployed stacks' outputs and publishes them to Pages. Every later push to `main` does the same.
   To deploy the backend from your own machine instead — before the pipeline exists, or when it is broken — `scripts/bootstrap.sh --instance dev --region us-west-2 --origin https://<owner>.github.io --apply` targets the same `chintan-dev-prod` stack, so CI takes over from it without colliding. The frontend still needs the workflow (`gh workflow run deploy-frontend.yaml`).
5. The account is left in `FORCE_CHANGE_PASSWORD`, so the password must be changed at first sign-in. Add `--print-password` to read it off the screen instead of from the file. After signing in, **You → Passkeys → Add a passkey on this device** opens Cognito's managed-login `/passkeys/add` page; the app performs no WebAuthn registration itself, because the relying-party id is the Cognito domain, and afterwards the sign-in page offers **Sign in with a passkey**.

---

## Configure

### Instance configuration

`config/instances/*.yaml` declares the instances; `scripts/list-instances.sh` is the one reader and produces the deploy matrix for both workflows. Every field except `name`, `display_name` and `description` has a default. Two files may share a `name` with different `environment`s — that is how a staging copy is expressed, and the repository ships `dev.yaml` (prod) and `dev-staging.yaml`.

The YAML owns an instance's identity and description — `display_name`, `short_name`, `description` — and nothing about its look: colours belong to the design tokens (`frontend/src/styles/tokens.css`), and the frontend build derives the page `<title>`, the PWA manifest and the meta description from the YAML. A value therefore lives in exactly one place, which is why this README names the fields and repeats none of their values.

`dev` is a name the owner chose for the shipped instance, not a tier: its `environment` defaults to `prod`, so `chintan-dev-prod` is production infrastructure, and `dev-staging.yaml` is its staging twin. `scripts/list-instances.sh` derives the stack as `chintan-<name>-<environment>` and the Pages path as `<name>` for prod and `<name>-<environment>` otherwise, unless `site_path` says so explicitly; `scripts/ci-deploy-stack.sh` hands that path to the template as `SitePath` and `scripts/ci-build-site.sh` builds the bundle under it.

| Instance | Environment | Stack | Pages path |
|---|---|---|---|
| `dev` | `prod` | `chintan-dev-prod` | `/chintan/dev/` |
| `dev` | `staging` | `chintan-dev-staging` | `/chintan/dev-staging/` |

| Field | Default | Meaning |
|---|---|---|
| `name` | required | The `<instance>` in `chintan-<instance>-<environment>` and in `/chintan/<instance>/`. Lowercase, digits, hyphens, ≤ 32 chars. |
| `environment` | `prod` | `prod`, `staging` or `dev`. |
| `region` | `us-west-2` | Must match the artifact bucket's region; CI enforces it. |
| `site_path` | `<name>`, or `<name>-<environment>` off prod | The GitHub Pages sub-path the bundle is served from. |
| `display_name` | required | The instance's human name: the page `<title>`, the manifest name, the wordmark and the About heading. None of `"`, `<`, `>`, `&` — it is written into `index.html` as-is, and `list-instances.sh` refuses them. |
| `short_name` | `display_name` | ≤ 12 characters: the manifest's `short_name` and the iOS home-screen title, what a phone shows under the installed icon; the default must then fit. Same character rule. |
| `description` | required | One sentence for the manifest description, the meta description and the lede on About; the shipped configs use the tagline. Same character rule. |
| `daily_spend_cap_micros` | `5000000` ($5/day) | Instance-wide daily provider spend ceiling in **microdollars** (`1000000` = $1). The worker reserves against one `SPEND#<day>` counter before every paid call; a capture that would cross the cap fails with status `spend_capped`. `0` disables the cap and the spend-cap alarm — do that only deliberately. |
| `enable_alarms` | `true` | Create this stack's CloudWatch alarms — three, or four with a non-zero spend cap: API function errors, the worker dead-letter queue (a capture or a scheduled or queued worker task — sweep-expired, aws-cost, storage-snapshot, clean-note, ask — that exhausted its retries), a rejected provider key, and the spend cap. `dev-staging.yaml` sets `false` — its failures are caught by the deploy's smoke test, and CloudWatch's free allowance is ten alarms per account. |
| `alarm_email` | none | Subscribed to the alarm topic and the budget. Because this repository is public, production reads it from the **`ALARM_EMAIL` repository secret** instead (`gh secret set ALARM_EMAIL`); a value in the file wins if present, which is what a private fork wants. |
| `monthly_budget_usd` | `10` | AWS Budgets limit. |
| `log_retention_days` | `14` | CloudWatch retention. |
| `refresh_token_validity_days` | `30` | Cognito refresh token lifetime. |

Check what the configs resolve to before pushing:

```bash
scripts/list-instances.sh --format text
# chintan-dev-staging us-west-2 dev-staging
# chintan-dev-prod    us-west-2 dev
```

### User preferences

Per-user preferences — theme, cleanup mode, audio retention and the default transcription language — are settings in the app (**You**), stored per user, not instance configuration; they are the fields of `GET /v1/settings`, which also reports the instance's `daily_spend_cap_micros` read-only.

### Per-note behaviour

Each note carries its own transcription `language` (absent, it inherits the user's default) and a `verbatim` switch that bypasses cleanup for it. Each note can also keep a **cleaned view** of its whole body (`structured` or `polished`, the note's `cleaned_mode`), regenerated on request or, with `auto_clean`, after every recording appended, moved or deleted; it is read-only and derived from the body, and costs one cleanup call per run under the daily spend cap.

---

## Operate

**Release flow.** Open a pull request; `ci.yaml` runs the gates in [Develop](#develop). Merge to `main`: `Deploy Backend` deploys staging, smoke-tests it, waits for your approval on `production`, deploys prod, tags `vX.Y.(Z+1)` and publishes a release; `Deploy Frontend` then builds and publishes the Pages site, and the You screen shows that tag. `scripts/deploy.sh`, the change-set deploy CI uses, prints the change set before executing it and refuses one that would *replace* the user pool, the table, the bucket or the user pool client — `Retain` would keep the old resource, but the stack would point at an empty one and every note would become unreachable. Pass `--allow-replacement <LogicalId>` once a migration is planned.

**Versions.** Three names, each for a different thing. The footnote at the bottom of You is the frontend build's `git describe --tags --always`, injected as `VITE_VERSION` by `scripts/ci-build-site.sh` and read in `frontend/src/config/env.ts` (a local build says `local build`); `VersionFootnote.tsx` shows a release as its tag alone and a later build as the tag plus `+N (sha)`. Backend releases are the `vX.Y.Z` tags: the `Tag and release` job in `deploy-backend.yaml` tags every production deploy `vX.Y.(Z+1)` from the latest `v*` tag and publishes a GitHub release, and because Deploy Frontend runs only after that job has finished, a frontend built after a backend deploy shows exactly that tag. `info.version` in `docs/api/openapi.yaml` is the API contract's version and moves only when the contract does.

**Smoke test.** `GET /v1/health` (liveness) and `GET /v1/health/ready` (round-trips DynamoDB and S3 under the Lambda's own role). CI runs both against staging before prod and against prod after; run them by hand with `curl "$(aws cloudformation describe-stacks --stack-name chintan-dev-prod --query "Stacks[0].Outputs[?OutputKey=='ApiEndpoint'].OutputValue" --output text)/v1/health/ready"`.

**Rollback.** Every deploy publishes an immutable Lambda version and moves the `live` alias; the deploy log and the job summary print the exact `aws lambda update-alias … --function-version <N>` that puts the previous one back. To roll the whole stack back, redeploy the previous commit.

**Alarms.** Three or four per stack when `enable_alarms` is true (API errors, the worker dead-letter queue, a rejected provider key, and — when `daily_spend_cap_micros` is non-zero — the spend cap), e-mailed to `ALARM_EMAIL`. The dead-letter queue is shared by every asynchronous worker invocation, so one message is one capture or one scheduled or queued worker task (sweep-expired, aws-cost, storage-snapshot, clean-note, ask) that exhausted its three attempts; the record names which. For a capture the recovery is the app's Retry; a scheduled task runs again on its next schedule, and a clean or an Ask on the next request. Read the record, act, then **purge the queue**: the alarm is on the queue's depth and stays in ALARM while the message sits there — fourteen days, the queue's retention — and an alarm already in ALARM sends no second e-mail, so a second dead-letter inside that window is silent until you purge. The queue is `chintan-captures-dlq-<instance>-<environment>` (`chintan-captures-dlq-dev-prod` for the default instance): `Q=$(aws sqs get-queue-url --queue-name chintan-captures-dlq-dev-prod --query QueueUrl --output text); aws sqs receive-message --queue-url "$Q" --max-number-of-messages 10 --visibility-timeout 0` to read, `aws sqs purge-queue --queue-url "$Q"` once acted on; the alarm returns to OK on its next period. A weekly EventBridge rule runs the worker's expiry sweep, which deletes the objects and rows of archived notes past their thirty-day purge deadline; DynamoDB TTL is the backstop.

**`chintanctl`**, the operator CLI (`cd backend && go build -o chintanctl ./cmd/chintanctl`). Dry run is the default for everything destructive; `--json` puts results on stdout and diagnostics on stderr; no note content ever reaches a log line.

| Subcommand | What it does |
|---|---|
| `export` | Every note as markdown with YAML front matter, beside each capture's `audio.*`, `raw.txt`, `clean.txt`, `segments.json` and `peaks.json`, in a layout Obsidian opens as a vault. Re-running skips unchanged objects. |
| `backup` | Full fidelity: the DynamoDB items verbatim, every S3 body, a sha256 per object, and `backup.json` written last so its presence means the backup finished. |
| `restore` | Inverse of `backup`; verifies both manifests and re-hashes every body before writing anything. |
| `reconcile` | Every disagreement between the table and the bucket in both directions, plus captures stuck in a non-terminal state. `--apply` repairs five finding kinds and nothing else: `orphan_object` (an object whose owning entity has no row; deleted), `dangling_capture` (a capture filed into a note that has no row; the row and its objects deleted), `unlisted_note` and `unindexed_capture` (August-2026 rows without their promoted index attributes; re-promoted from the blob), `capture_size_unknown` (`audio_bytes` written from the bucket listing). `--only <kind>` narrows a run to some of those; the other kinds are reported only. |
| `erase` | Deletes one tenant everywhere; `--apply` requires the tenant id typed exactly. |
| `backfill-search-text` | Fills the searchable body text for notes written before it existed. |
| `usage` | Every tenant's provider spend, calls, audio, API requests and per-operation cost for one month, from the `USAGE#` rows. Read-only. |

The table name is derived from the instance; the bucket is read from the stack's `ContentBucketName` output, so the principal needs `cloudformation:DescribeStacks` on `chintan-*`. Both can be overridden with `--table` / `--bucket`.

**Workflows.** Each names the command or two involved; `--help` on any of them has the flags.

| Workflow | Commands |
|---|---|
| Bootstrap an account | `scripts/bootstrap-agent.sh` once as an administrator, then `scripts/setup.sh`; `scripts/doctor.sh` says what is left. |
| Deploy an instance | `gh workflow run deploy-backend.yaml` (the frontend follows), or `scripts/bootstrap.sh` from your own machine; `scripts/list-instances.sh --format text` shows what would deploy. |
| Invite or reset a user | `scripts/invite-user.sh` |
| Recover failed processing | The app's Retry for one capture; `chintanctl reconcile` for what the table and the bucket disagree about. |
| Back up, restore, export | `chintanctl backup` and `chintanctl restore`; `chintanctl export` for a vault Obsidian opens. |
| Inspect usage | `chintanctl usage` for every tenant; **You → Usage** in the app for your own. |
| Tear down | `scripts/cleanup-aws.sh` for one stack and what it retains; `scripts/clean-instance-orphans.sh` after a failed create; `scripts/teardown.sh` for everything — every note and recording; the CloudTrail trail, its bucket and the agent principal are left alone. |

**Costs.** At single-user volume the AWS side is a few cents a month — Lambda, DynamoDB, S3, SNS, EventBridge and CloudWatch sit inside the always-free tiers, the alarms and the CloudTrail digests are the lines that do not — and the transcription and cleanup providers are the real bill, from under a dollar a month for light use to the daily spend cap you set. Those are two different numbers: **provider spend** (Groq, MiniMax) is metered per call by the worker and attributable to each user, while **AWS spend** is the account's month-to-date actual read once a day from the stack's Budget, which is the instance's cost only on an account dedicated to it and an upper bound on a shared one. **You → Usage** shows both, plus each user's estimated share of the AWS figure, apportioned by provider cost (`docs/design/usage-accounting.md`). Every resource carries `Project`, `Instance` and `Environment` tags, and the budget is defined in `infrastructure/template.yaml`; activate the tags once in Billing → Cost allocation tags to see the per-instance view.

---

## Security

This is a public repository; nothing in it is secret.

- **Authentication** is Cognito. Sign-in goes through the hosted managed-login page, which is branded from the app's own tokens by the template; passkeys are registered there too (**You → Passkeys**). The JWT is verified at the API Gateway authorizer and again in the service against the pool's JWKS; identity comes from the verified token and from nothing else.
- **Isolation** is per tenant: every DynamoDB key is prefixed by the verified user id, every S3 key sits under `tenants/<id>/`, and the repository tests assert that one tenant cannot read another.
- **Provider keys** live in SSM Parameter Store as `SecureString`, referenced by path, read by the Lambdas at run time, never by CloudFormation and never in a log. Nothing in the repository or the bundle holds a plaintext secret; the frontend carries only public endpoints and the Cognito client id.
- **Deploys** run from CI as `chintan-github-actions`, scoped to `chintan-*` and trusted only from the `staging` and `production` environments; production requires your approval. The build jobs assume `chintan-github-build`, which can upload artifacts and read stack outputs and nothing else.
- **Nothing derived from speech reaches a log.** `scripts/check-log-hygiene.sh` enforces it in CI.
- **The agent principal** from `scripts/bootstrap-agent.sh` runs under a permissions boundary, cannot read the provider keys, and cannot write to or stop the CloudTrail trail.
- **CORS** is restricted to your Pages origin. Never commit `.env` files or keys; `.gitignore` already excludes them.

### Tenancy

One Cognito user is one tenant, and that equality is load-bearing. `auth.Identity` (`backend/internal/auth/identity.go`) carries a `UserID` and a `TenantID`; the middleware sets both to the verified token's subject, and nothing below that package may key data on the user id. Everything that isolates data hangs off the tenant id:

- The DynamoDB partition `USER#<tenant>` holds the tenant's settings, notes, captures and `USAGE#` rows (`internal/repository/dynamo.go`, `internal/usage/usage.go`); the instance's `SPEND#` counter deliberately sits outside it (`internal/pipeline/spend.go`).
- Every S3 key is `tenants/<tenant>/…` (`internal/keys/keys.go`); the worker reads the tenant back out of the object key when a recording arrives (`internal/pipeline/worker.go`).
- `chintanctl` discovers tenants from those prefixes and walks one partition at a time (`cmd/chintanctl/enumerate.go`); `erase --tenant` is the unit of deletion; `chintanctl usage` attributes spend per tenant.
- Ownership is the key, not a check: a note or capture is read under the caller's partition, so another tenant's id is simply not found (`internal/repository/isolation_test.go` asserts it). A capture's `targeted` flag records that a person, not the router, chose its note; it is not an access flag.

Sharing a note, or several sign-ins over one library, means populating `TenantID` from a claim or a membership lookup rather than the subject — the seam `Identity`'s comment reserves — plus an explicit ownership check wherever the partition key alone did the work, and a decision on whether usage is attributed to the tenant or the user. Until then a user's data is entirely their own.

---

## Develop

```bash
# Backend
cd backend && go build ./... && go vet ./... && go test -race ./...

# Frontend (the toolchain is Bun; the lockfile is bun.lock)
cd frontend && bun install && bun run dev
cd frontend && bun run typecheck && bun run lint && bun run test && bun run build

# End-to-end
cd frontend && bunx playwright install chromium && bun run e2e -- --project=chromium
```

For a local backend, put the keys in the environment instead of SSM (`GROQ_API_KEY`, `LLM_API_KEY`, `LLM_BASE_URL`, `LLM_MODEL`) in a `.env` that is never committed.

**The e2e projects.** `frontend/playwright.config.ts` defines two: `chromium` runs every spec; `webkit` runs the auth, archive, playback, a11y, manifest and a reduced layout matrix with the service worker blocked, because Playwright's route interception does not see requests that pass through a worker outside Chromium; the worker's own behaviour is proven in `offline.spec.ts` on Chromium. `LAYOUT_SHOTS=1 bun run e2e -- layout` writes the ~280-image layout sweep to `frontend/e2e/__screenshots__/sweep/` (gitignored) for a human to page through.

**The QA scripts.** `scripts/check-log-hygiene.sh` (no provider adapter logs a response body; includes a self-test), `scripts/check-vite-env.sh` (the `VITE_*` names the deploy exports are the ones the bundle reads), `frontend/scripts/check-tokens.mjs` (run by `bun run lint`; forbids literal colours and font sizes outside the design tokens), `scripts/list-instances.sh --format text` (every config resolves to a unique stack). `docs/qa/` holds the exploratory QA reports.

**CI** (`ci.yaml`, every pull request): `gofmt`, `go vet`, `go test -race`, `golangci-lint`, `govulncheck`, `cfn-lint` on both templates, `shellcheck` + `shfmt -d -i 4 -ci` + `bash -n` on every script, the two check scripts, the instance configs, the frontend typecheck/lint/test/build, the contract fixtures (`cd backend && CHINTAN_UPDATE_FIXTURES=1 go test ./internal/handler/ -run Contract` regenerates `frontend/src/api/__fixtures__/responses.ts`; CI fails on a stale one), and the Playwright e2e on Chromium.

The frontend reads its per-instance configuration from build-time `VITE_*` variables that CI derives from the stack outputs (`frontend/src/config/env.ts` is the list). Nothing is written into the bundle at deploy time, so a service worker cannot pin an installed client to a stale endpoint.

More: [`docs/backlog.md`](docs/backlog.md) · [`docs/api/openapi.yaml`](docs/api/openapi.yaml) · [`docs/design/`](docs/design/) · [`docs/ops/`](docs/ops/) · [`docs/qa/`](docs/qa/) · [`docs/history/`](docs/history/) (dated reports, not current).
