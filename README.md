# Chintan

Chintan is a personal, mobile-friendly PWA for voice brain dumps: you speak unstructured thoughts while driving or walking, and it transcribes the recording, cleans up the text and files it into the right note. It runs on serverless AWS — Cognito, API Gateway, two Lambdas, DynamoDB and S3 — with a static frontend on GitHub Pages, and every instance is its own isolated stack. Speech-to-text is **Groq**, and cleanup and routing use an OpenAI-compatible endpoint, **MiniMax** by default, with the API keys in SSM Parameter Store.

- [Deploy your own](#deploy-your-own) · [Configure](#configure) · [Operate](#operate) · [Security](#security) · [Develop](#develop)
- What is planned: [`docs/backlog.md`](docs/backlog.md). The API: [`docs/api/openapi.yaml`](docs/api/openapi.yaml).

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
5. The account is left in `FORCE_CHANGE_PASSWORD`, so the password must be changed at first sign-in. Add `--print-password` to read it off the screen instead of from the file. After signing in with a password, **You → Sign in with a passkey** hands off to Cognito's managed-login page to register one.

---

## Configure

`config/instances/*.yaml` declares the instances; `scripts/list-instances.sh` is the one reader and produces the deploy matrix for both workflows. Every field except `name` has a default. Two files may share a `name` with different `environment`s — that is how a staging copy is expressed, and the repository ships `dev.yaml` (prod) and `dev-staging.yaml`.

| Field | Default | Meaning |
|---|---|---|
| `name` | required | The `<instance>` in `chintan-<instance>-<environment>` and in `/chintan/<instance>/`. Lowercase, digits, hyphens, ≤ 32 chars. |
| `environment` | `prod` | `prod`, `staging` or `dev`. |
| `region` | `us-west-2` | Must match the artifact bucket's region; CI enforces it. |
| `site_path` | `<name>`, or `<name>-<environment>` off prod | The GitHub Pages sub-path the bundle is served from. |
| `display_name` | `<name>` | Human label. |
| `daily_spend_cap_micros` | `0` (count, never enforce) | Instance-wide daily provider spend ceiling in **microdollars** (`1000000` = $1). The worker reserves against one `SPEND#<day>` counter before every paid call; a capture that would cross the cap fails with status `spend_capped`. Non-zero also creates the spend-cap alarm. |
| `enable_alarms` | `true` | Create this stack's four CloudWatch alarms: API function errors, the capture dead-letter queue, a rejected provider key, and the spend cap. `dev-staging.yaml` sets `false` — its failures are caught by the deploy's smoke test, and CloudWatch's free allowance is ten alarms per account. |
| `alarm_email` | none | Subscribed to the alarm topic and the budget. Because this repository is public, production reads it from the **`ALARM_EMAIL` repository secret** instead (`gh secret set ALARM_EMAIL`); a value in the file wins if present, which is what a private fork wants. |
| `monthly_budget_usd` | `10` | AWS Budgets limit. |
| `log_retention_days` | `14` | CloudWatch retention. |
| `refresh_token_validity_days` | `30` | Cognito refresh token lifetime. |

Per-user preferences — theme, cleanup mode, the default transcription language and each note's language — are settings in the app (**You**), stored per user, not instance configuration. Each note can also keep a **cleaned view** of its whole body (`structured` or `polished`), regenerated on request or, with `auto_clean`, after every recording appended, moved or deleted; it is read-only and derived from the body, and costs one cleanup call per run under the daily spend cap.

Check what the configs resolve to before pushing:

```bash
scripts/list-instances.sh --format text
# chintan-dev-staging us-west-2 dev-staging
# chintan-dev-prod    us-west-2 dev
```

---

## Operate

**Release flow.** Open a pull request; `ci.yaml` runs the gates in [Develop](#develop). Merge to `main`: `Deploy Backend` deploys staging, smoke-tests it, waits for your approval on `production`, deploys prod, tags `vX.Y.(Z+1)` and publishes a release; `Deploy Frontend` then builds and publishes the Pages site, and the You screen shows that tag. `scripts/deploy.sh`, the change-set deploy CI uses, prints the change set before executing it and refuses one that would *replace* the user pool, the table, the bucket or the user pool client — `Retain` would keep the old resource, but the stack would point at an empty one and every note would become unreachable. Pass `--allow-replacement <LogicalId>` once a migration is planned.

**Smoke test.** `GET /v1/health` (liveness) and `GET /v1/health/ready` (round-trips DynamoDB and S3 under the Lambda's own role). CI runs both against staging before prod and against prod after; run them by hand with `curl "$(aws cloudformation describe-stacks --stack-name chintan-dev-prod --query "Stacks[0].Outputs[?OutputKey=='ApiEndpoint'].OutputValue" --output text)/v1/health/ready"`.

**Rollback.** Every deploy publishes an immutable Lambda version and moves the `live` alias; the deploy log and the job summary print the exact `aws lambda update-alias … --function-version <N>` that puts the previous one back. To roll the whole stack back, redeploy the previous commit.

**Alarms.** Four per stack when `enable_alarms` is true (API errors, the capture dead-letter queue, a rejected provider key, the spend cap), e-mailed to `ALARM_EMAIL`. One message in the dead-letter queue is one recording that was not filed after three attempts; the recovery is the app's Retry. A weekly EventBridge rule runs the worker's expiry sweep, which deletes the objects and rows of archived notes past their thirty-day purge deadline; DynamoDB TTL is the backstop.

**`chintanctl`**, the operator CLI (`cd backend && go build -o chintanctl ./cmd/chintanctl`). Dry run is the default for everything destructive; `--json` puts results on stdout and diagnostics on stderr; no note content ever reaches a log line.

| Subcommand | What it does |
|---|---|
| `export --instance <i> --out <dir\|tar.gz>` | Every note as markdown with YAML front matter, beside each capture's `audio.*`, `raw.txt`, `clean.txt`, `segments.json` and `peaks.json`, in a layout Obsidian opens as a vault. Re-running skips unchanged objects. |
| `backup --instance <i> --out <dir>` | Full fidelity: the DynamoDB items verbatim, every S3 body, a sha256 per object, and `backup.json` written last so its presence means the backup finished. |
| `restore --instance <i> --in <dir> [--apply]` | Inverse of `backup`; verifies both manifests and re-hashes every body before writing anything. |
| `reconcile --instance <i> [--apply]` | Every disagreement between the table and the bucket in both directions, plus captures stuck in a non-terminal state. `--apply` deletes only objects whose owning entity has no index row. |
| `erase --instance <i> --tenant <t> [--apply]` | Deletes one tenant everywhere; `--apply` requires the tenant id typed exactly. |
| `backfill-search-text --instance <i> [--apply]` | Fills the searchable body text for notes written before it existed. |

The table name is derived from the instance; the bucket is read from the stack's `ContentBucketName` output, so the principal needs `cloudformation:DescribeStacks` on `chintan-*`. Both can be overridden with `--table` / `--bucket`.

**Other commands.**

| Task | Command |
|---|---|
| Readiness of this machine and account | `scripts/doctor.sh --instance dev` |
| What would be deployed | `scripts/list-instances.sh --format text` |
| Invite or reset a user | `scripts/invite-user.sh --instance dev --email you@example.com --apply` |
| Delete one stack and what it retains | `scripts/cleanup-aws.sh --instance dev --environment staging --apply` |
| Clear the wreckage of a failed create | `scripts/clean-instance-orphans.sh --instance dev --environment staging --apply` |
| Delete everything | `scripts/teardown.sh --apply` — every note and recording; the CloudTrail trail, its bucket and the agent principal are left alone |

**Costs.** At single-user volume the AWS side is a few cents a month — Lambda, DynamoDB, S3, SNS, EventBridge and CloudWatch sit inside the always-free tiers, the alarms and the CloudTrail digests are the lines that do not — and the transcription and cleanup providers are the real bill, from under a dollar a month for light use to the daily spend cap you set. Every resource carries `Project`, `Instance` and `Environment` tags, and the budget is defined in `infrastructure/template.yaml`; activate the tags once in Billing → Cost allocation tags to see the per-instance view.

---

## Security

This is a public repository; nothing in it is secret.

- **Authentication** is Cognito. Sign-in goes through the hosted managed-login page, which is branded from the app's own tokens by the template; passkeys are registered there too (**You → Sign in with a passkey**). The JWT is verified at the API Gateway authorizer and again in the service against the pool's JWKS; identity comes from the verified token and from nothing else.
- **Isolation** is per tenant: every DynamoDB key is prefixed by the verified user id, every S3 key sits under `tenants/<id>/`, and the repository tests assert that one tenant cannot read another.
- **Provider keys** live in SSM Parameter Store as `SecureString`, referenced by path, read by the Lambdas at run time, never by CloudFormation and never in a log. Nothing in the repository or the bundle holds a plaintext secret; the frontend carries only public endpoints and the Cognito client id.
- **Deploys** run from CI as `chintan-github-actions`, scoped to `chintan-*` and trusted only from the `staging` and `production` environments; production requires your approval. The build jobs assume `chintan-github-build`, which can upload artifacts and read stack outputs and nothing else.
- **Nothing derived from speech reaches a log.** `scripts/check-log-hygiene.sh` enforces it in CI.
- **The agent principal** from `scripts/bootstrap-agent.sh` runs under a permissions boundary, cannot read the provider keys, and cannot write to or stop the CloudTrail trail.
- **CORS** is restricted to your Pages origin. Never commit `.env` files or keys; `.gitignore` already excludes them.

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
