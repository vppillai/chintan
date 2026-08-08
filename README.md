# Chintan

Chintan is a personal, mobile-friendly PWA for voice brain dumps: you speak unstructured thoughts while driving or walking, and the system transcribes, lightly cleans up the text, and appends it to the right note. It runs on serverless AWS (Cognito, Lambda, SQS, DynamoDB, S3) with a static frontend on GitHub Pages, and each instance gets its own isolated stack.

Uses **Groq** (speech-to-text) and an OpenAI-compatible endpoint, **MiniMax-M3** by default (text cleanup), with API keys in AWS SSM Parameter Store.

- Architecture, data model and security requirements: [v2 design spec](docs/superpowers/specs/2026-08-07-chintan-v2-design.md)
- What v1 got wrong and why the rewrite exists: [production readiness audit](docs/audit/2026-08-07-production-readiness-audit.md)

---

## Deploy your own

Everything below is CloudFormation. There is no Terraform in this repository.

Naming is uniform: every stack is `chintan-<instance>-<environment>`, every provider secret is `/chintan/<instance>/<key>`, and every physical resource is `chintan-*`. The scripts, the workflows and the templates all derive names from that one rule, and `scripts/lib/common.sh` is the single place it is written down.

Every script has `--help`, and every script you run by hand defaults to a **dry run** — nothing changes until you pass `--apply`. The one exception is `scripts/ci-deploy-stack.sh`, the CI wrapper: it is invoked by an already-gated workflow job, takes no flags, and always applies.

### Prerequisites

- **AWS account with administrative credentials for step 0 only.** Every later step runs as the bounded role step 0 creates.
- `aws` CLI v2, `gh` (authenticated), `jq`, `python3` with PyYAML, `zip`, and Go 1.25 (for a local build).
- A GitHub OIDC provider for `token.actions.githubusercontent.com` in the account. `scripts/setup.sh` checks for it and prints the one command that creates it if it is missing. The templates never create it, because it is shared with other projects.
- API keys:
  - **Groq**, for speech-to-text — [console.groq.com](https://console.groq.com/)
  - **MiniMax**, or any OpenAI-compatible endpoint, for cleanup — [api.minimax.io](https://api.minimax.io/)

### 0. Create the agent principal, the boundary and CloudTrail

```bash
scripts/bootstrap-agent.sh --region us-west-2            # dry run
scripts/bootstrap-agent.sh --region us-west-2 --apply
```

**This is a hard prerequisite and it is easy to miss.** `infrastructure/bootstrap.yaml` references the permissions boundary `policy/chintan-agent-boundary`, which only this script creates, so step 1 fails without it. It is also the only thing in the repository that needs administrative credentials: it creates the constraints everything else runs under — a permissions boundary, explicit deny policies, a CloudTrail trail, and an IAM role whose sessions expire in an hour.

Run it once, as a human. Everything afterwards runs as `chintan-agent`.

To write the CLI user's access key to a file instead of ever printing it:

```bash
CHINTAN_KEY_OUT=~/.chintan/agent.env scripts/bootstrap-agent.sh --region us-west-2 --apply
```

### 1. Bootstrap the account and the repository

```bash
scripts/setup.sh --region us-west-2                      # dry run; --region is required
scripts/setup.sh --region us-west-2 --apply
```

This deploys `chintan-bootstrap` (the Lambda artifact bucket and the GitHub Actions deploy role, scoped to `chintan-*`), sets the `AWS_ACCOUNT_ID` and `AWS_REGION` repository secrets, creates the `production` environment **with you as a required reviewer**, creates `staging`, and switches GitHub Pages to build from Actions.

Add `--reviewer <login>` to require someone else's approval for a production deploy.

### 2. Store the provider keys

Keys are per **instance**, not per environment, so `dev` staging and `dev` prod share them:

```bash
aws ssm put-parameter --type SecureString \
  --name "/chintan/dev/groq_api_key" --value "gsk_..."

aws ssm put-parameter --type SecureString \
  --name "/chintan/dev/llm_api_key"  --value "..."
```

A third SecureString holds the **refresh-token vault key** — 32 random bytes, base64 — which seals the one Cognito refresh token biometric unlock keeps per device. `scripts/bootstrap.sh` generates it if it is absent, so you only need this if you are setting an instance up by hand:

```bash
aws ssm put-parameter --type SecureString \
  --name "/chintan/dev/token_vault_key" \
  --value "$(head -c 32 /dev/urandom | base64)"
```

All three are SecureStrings created outside CloudFormation because `AWS::SSM::Parameter` cannot declare that type. **Replacing the vault key makes every enrolled device enrol again**; the sealed blob records which parameter version sealed it, so a rotation is detectable rather than silent.

Biometric unlock fails closed: no vault key, no biometric unlock, and the API says so in its startup log. There is no plaintext fallback — the identity `SealBox` exists only in the test binary.

### 3. Declare your instances

`config/instances/*.yaml` is read by `scripts/list-instances.sh`, which produces the deploy matrix for both workflows. This is the real schema — every field except `name` has a default:

| Field | Default | Meaning |
|---|---|---|
| `name` | — (required) | The `<instance>` in `chintan-<instance>-<environment>`. Lowercase, digits, hyphens, ≤ 32 chars. |
| `environment` | `prod` | `prod`, `staging` or `dev`. The `<environment>` in the stack name. |
| `region` | `us-west-2` | Must match the region of the artifact bucket; CI enforces this. |
| `site_path` | `<name>` for prod, `<name>-<environment>` otherwise | The GitHub Pages sub-path. |
| `display_name` | `<name>` | Human label. |
| `alarm_email` | none | Subscribed to the alarm topic and the budget. |
| `monthly_budget_usd` | `10` | AWS Budgets limit. |
| `log_retention_days` | `14` | CloudWatch retention. |
| `daily_spend_cap_micros` | `0` (record, never enforce) | Instance-wide daily provider spend ceiling, in **microdollars** — `1000000` = $1. A tenant may set a lower cap of its own; the breaker enforces whichever is lower. Non-zero also creates the `chintan-<i>-<env>-spend-cap-tripped` alarm. |
| `refresh_token_validity_days` | `7` | Cognito refresh token lifetime. |
| `pwa` | none | A mapping (`name`, `short_name`, `description`, `theme_color`, `background_color`) that both shipped configs carry and **nothing currently reads** — `frontend/vite.config.ts` hardcodes the manifest. Setting it has no effect today. |

Two files may share a `name` as long as their `environment` differs — that is how a staging copy is expressed. The repository ships `dev.yaml` (prod) and `dev-staging.yaml`, which produce `chintan-dev-prod` and `chintan-dev-staging`.

Check what your configs resolve to before pushing:

```bash
scripts/list-instances.sh --format text
# chintan-dev-staging us-west-2 dev-staging
# chintan-dev-prod    us-west-2 dev
```

### 4. Deploy

Push to `main`. `Deploy Backend` runs `go test -race`, builds one arm64 binary, deploys every **staging** stack through a printed change set, smoke-tests each one, and only then deploys the **production** stacks — which wait for your approval on the `production` environment. `Deploy Frontend` then builds one Vite bundle per instance and publishes them to Pages.

For a first deploy, or when the pipeline itself is broken, deploy from a workstation:

```bash
scripts/bootstrap.sh --instance dev --region us-west-2 \
  --origin https://<owner>.github.io                     # dry run
scripts/bootstrap.sh --instance dev --region us-west-2 \
  --origin https://<owner>.github.io --apply
```

It targets the same `chintan-dev-prod` stack CI targets, so the two do not collide.

`scripts/deploy.sh` — the change-set deploy CI uses — deliberately refuses to run outside CI. Deploys happen from a green pipeline.

### 5. Create the first user

```bash
scripts/invite-user.sh --instance dev --email you@example.com          # dry run
scripts/invite-user.sh --instance dev --email you@example.com --apply
```

The temporary password is generated, written to `./chintan-invite-<email>` with mode 600, and **not printed**. Add `--print-password` if you would rather read it off the screen. The account is left in `FORCE_CHANGE_PASSWORD`, so it must be changed at first sign-in.

Delete the file once you have signed in.

### 6. Open it

`https://<owner>.github.io/<repo>/<site_path>/` — for the shipped configs, `https://<owner>.github.io/chintan/dev/` and `.../dev-staging/`.

---

## Operations

| Task | Command |
|---|---|
| See what would be deployed | `scripts/list-instances.sh` |
| Deploy one stack by hand | `scripts/bootstrap.sh --instance dev --region us-west-2 --origin https://<owner>.github.io --apply` |
| Invite or reset a user | `scripts/invite-user.sh --instance dev --email you@example.com --apply` |
| Check the guardrails | `scripts/guardrails-check.sh` |
| Prove the guardrail check still detects a removal | `scripts/guardrails-check.sh --self-test` |
| Check no user content reaches a log | `scripts/check-log-hygiene.sh` |
| Delete one instance | `scripts/cleanup-aws.sh --instance dev --environment staging --apply` |
| Delete everything | `scripts/teardown.sh --apply` |

### The sign-in page

The hosted sign-in page is branded to match the app, and the branding deploys with the stack — there is nothing to click in the console, and a fresh clone gets the same page.

Two resources in `infrastructure/template.yaml` do it:

- `UserPoolDomain` sets `ManagedLoginVersion: 2`, which selects **managed login** over the classic hosted UI. Without it the branding below applies cleanly and then renders nothing.
- `ManagedLoginBranding` carries the palette and the logo.

Every colour is a token from `frontend/src/styles/tokens.css`, so the sign-in page and the app cannot drift apart without someone editing both. `colorSchemeMode: DYNAMIC` follows `prefers-color-scheme`, so **Ink & Paper** and **Nocturne** are both covered and a dark-theme user does not get a white flash at night.

Managed login is included at the `ESSENTIALS` tier the pool already runs on, so this costs nothing. It styles the page; it does not change how sign-in works. The domain name, the OAuth endpoints, the callback URLs and the app client are all untouched.

What it cannot do: the page's typeface is fixed, so headings render in the managed-login sans rather than the app's serif. That is the one visible difference from the app's own screens.

The `Assets` blobs are the shipped app icon (`frontend/public/icon-192.png`), not a separate mark. The icon has an opaque `--color-ground` square baked in, so each variant carries the same artwork with that ground recovered to transparency and the foreground remapped to its theme's `--color-ink` and `--color-accent`. Changing the app icon does **not** regenerate them; that is a manual step, and the derivation is described above the resource.

### Rollback

Every successful deploy publishes an immutable Lambda version and moves the `live` alias to it. The exact command that puts the previous version back is printed in the deploy log and in the job summary:

```bash
aws lambda update-alias \
  --function-name chintan-api-dev-prod --name live --function-version <N>
```

To roll the whole stack back, redeploy the previous commit: the workflow is change-set based, so you can read what it will do before it happens.

### Teardown

```bash
scripts/teardown.sh                # dry run: prints the plan
scripts/teardown.sh --apply        # asks for a typed confirmation
scripts/teardown.sh --apply --yes  # unattended
```

It deletes every `chintan-<instance>-<environment>` stack, then `chintan-bootstrap`. For each stack it empties the content bucket, disables Cognito deletion protection, deletes the stack, and then deletes the resources the template deliberately retains — the DynamoDB table, the content bucket and the user pool — and schedules the token-vault CMK for deletion with a 30-day window, so a change of mind is still recoverable with `aws kms cancel-key-deletion`.

Provider secrets under `/chintan/<instance>/` are removed only when no other stack for that instance still needs them.

**What it deliberately does not touch:** the CloudTrail trail, the CloudTrail bucket, and the agent IAM principal. Those are the record of what was done to the account, including by teardown itself. Anything else left behind is reported as an orphan and never deleted automatically. Remove the agent principal separately, by hand, if you are decommissioning the account.

**⚠️ This deletes every note and audio recording. It cannot be undone.**

### `chintanctl` — the operator CLI

A Go CLI under `backend/cmd/chintanctl`. It closes the audit finding that **nothing backed up S3 content at all** — DynamoDB has point-in-time recovery; the notes, transcripts and audio in S3 had nothing.

```bash
cd backend && go build -o chintanctl ./cmd/chintanctl
```

| Subcommand | What it does |
|---|---|
| `export --instance <i> --out <dir\|tar.gz>` | Every note as markdown with YAML front matter (title, aliases, tags, timestamps, capture list), beside each capture's `audio.*`, `raw.txt`, `clean.txt`, `segments.json` and `peaks.json`, in a layout Obsidian opens as a vault. Re-running into the same directory skips objects whose ETag has not moved. |
| `backup --instance <i> --out <dir>` | Full fidelity: `items.jsonl` with the DynamoDB items verbatim, `objects/<key>` with the S3 bodies, `objects.jsonl` with a sha256 per object, and `backup.json` — written last, so its presence means the backup finished. |
| `restore --instance <i> --in <dir> [--apply]` | Inverse of `backup`. Verifies both manifests and re-hashes every object body **before** writing anything, and refuses on any mismatch. Dry run by default. |
| `reconcile --instance <i> [--apply]` | Reports every disagreement between the table and the bucket in both directions: index rows whose objects are gone, objects whose owning row is gone, objects nothing references, and captures stuck in a non-terminal state. `--apply` deletes only objects whose owning entity has no index row — never a row, never an object it does not understand. |
| `usage --instance <i> [--since <date>]` | Metered provider spend per tenant, provider, model, operation and unit, from the `USAGE#` records. |
| `erase --instance <i> --tenant <t> [--apply]` | Deletes one tenant everywhere and reports every key it removed. `--apply` requires the tenant id typed exactly, interactively or via `--confirm`. |
| `reindex --instance <i> [--apply]` | Writes the `gsi2` key attributes onto notes that lack them. Idempotent, and it touches only those two attributes so no `version` moves under an open editor. Dry run by default. **Required after any deploy that adds a secondary index — see below.** |

### Adding a secondary index is a two-step deploy

DynamoDB backfills a new global secondary index only from items that **already carry its key attributes**. Every row written before the index existed carries none, so it is absent from the index — and a list that reads through the index comes back empty. The stack update is therefore only the first half of the change:

```bash
scripts/deploy.sh ...                             # 1. the index exists but is empty of old rows
chintanctl reindex --instance dev --environment prod --apply   # 2. the old rows enter it
```

Between the two the affected list reads empty to the user, so run them back to back. `gsi2` (notes ordered by `updated_at`) was the first index to need this; anything added later needs the same pair of steps, and its own reason to be in `reindex`.

Every subcommand takes `--json` for machine-readable results on stdout, with diagnostics on stderr. The table name is derived from the instance (`chintan-<i>-<env>`); the **bucket is read from the stack's `ContentBucketName` output**, not re-derived, because a fourth hand-written copy of a naming convention is a fourth place for it to go stale. Both can be overridden with `--table` / `--bucket`. The principal running `chintanctl` therefore needs `cloudformation:DescribeStacks` on `chintan-*`; without it the tool warns and falls back to the convention.

`erase` and `export` refuse to report success when the index rows reference S3 keys and the bucket returns zero objects — that combination means the wrong bucket, and it previously produced a deletion request that exited 0 having deleted no recordings.

Design constraints it holds to: enumeration is by **S3 prefix and DynamoDB partition**, never by entity type, so a future schema addition cannot silently fall out of the export; no object body is ever held in memory; dry run is the default for everything destructive; and no note body, title or transcript ever reaches a log line.

---

## Continuous integration

`.github/workflows/ci.yaml` runs on every pull request, and on pushes to `main` and `feat/v2`:

| Job | Gate | Run it locally |
|---|---|---|
| `backend-test` | `gofmt`, `go vet`, `go test -race ./...` | `cd backend && go test -race ./...` |
| `backend-lint` | `golangci-lint` | `cd backend && golangci-lint run ./...` |
| `backend-vuln` | `govulncheck` (reachable vulnerabilities only) | `cd backend && govulncheck ./...` |
| `infrastructure-lint` | `cfn-lint` on both templates | `uvx cfn-lint infrastructure/*.yaml` |
| `shell` | `shellcheck`, `shfmt -d -i 4 -ci`, `bash -n` on every script | `shellcheck --severity=warning scripts/*.sh` |
| `guardrails` | `guardrails-check.sh --local-only` and `--self-test`; `check-boundary-drift.sh --self-test` | `scripts/guardrails-check.sh --local-only` |
| `log-hygiene` | no provider adapter logs a response body, plus a self-test | `scripts/check-log-hygiene.sh` |
| `vite-env` | the `VITE_*` names the deploy exports are the ones the bundle reads | `scripts/check-vite-env.sh` |
| `instance-configs` | every `config/instances/*.yaml` resolves to a unique stack | `scripts/list-instances.sh --format text` |
| `frontend` | `bun install --frozen-lockfile`, `bun run typecheck`, **`bun run lint`**, `bun run test`, `bun run build` | `cd frontend && bun run lint` |
| `contract` | the committed frontend↔backend fixtures are the ones both sides actually produce — it fails on stale fixtures | `cd backend && CHINTAN_UPDATE_FIXTURES=1 go test ./internal/handler/ -run Contract` |
| `e2e` | Playwright | `cd frontend && bunx playwright install chromium && bun run e2e` |

`bun run lint` is eslint **plus** `scripts/check-tokens.mjs`, which forbids literal colours and font sizes outside the design tokens. A change with `color: #1B4332` passes typecheck, test and build, and is blocked here.

The frontend toolchain is **Bun**, not npm. The lockfile is `bun.lock`.

`.github/CODEOWNERS` requires review on `/.github/workflows/`, `/infrastructure/`, `/scripts/` and `/config/instances/`. A fork must replace `@vppillai` with its own owner. CODEOWNERS is only enforced once branch protection on `main` has "Require review from Code Owners" enabled; `guardrails-check.sh` reports the two separately, because the file without the setting is a suggestion.

---

## Local development

```bash
# Backend
export PATH=/usr/local/go/bin:$PATH
cd backend && go build ./... && go vet ./... && go test -race ./...

# Frontend
cd frontend && bun install && bun run dev
```

For a local backend, use environment variables instead of SSM:

```bash
# .env — never commit this; .gitignore already excludes it
GROQ_API_KEY=...
LLM_API_KEY=...
LLM_BASE_URL=https://api.minimax.io/v1
LLM_MODEL=MiniMax-M3
```

The frontend reads its per-instance configuration from build-time `VITE_*` variables, which CI derives from the deployed stack's CloudFormation outputs:

| Variable | Source |
|---|---|
| `VITE_BASE` | `/<repo>/<site_path>/` |
| `VITE_API_URL` | stack output `ApiEndpoint` |
| `VITE_USER_POOL_ID` | stack output `UserPoolId` |
| `VITE_CLIENT_ID` | stack output `UserPoolClientId` — note the names differ |
| `VITE_COGNITO_DOMAIN` | stack output `UserPoolDomainName` |
| `VITE_INSTANCE` | the instance config |

`frontend/src/config/env.ts` is the authority on that list. A missing variable becomes an empty string there and only warns in dev, so a wrong name produces a bundle that builds green, deploys green, and cannot sign in.

Nothing is written into the bundle at deploy time, so a service worker cannot pin an installed client to a stale endpoint.

---

## Security

This is a **public repository**.

- **API keys** live in SSM Parameter Store as `SecureString`, referenced by path and never by value.
- **The frontend bundle** contains only public endpoints and the Cognito client ID.
- **Authentication** is Cognito with real JWKS signature verification; identity comes from the verified token and from nothing else.
- **CORS** is restricted to your Pages origin.
- **Deploys** happen only from CI, under a role scoped to `chintan-*`, gated by an environment that requires a human.
- **The agent principal** runs under a permissions boundary, cannot read provider secrets, and cannot write to the CloudTrail bucket.
- **Never commit** real API keys, `.env` files, or secrets.

---

## Spend

All resources carry `Project=chintan`, `Instance=<instance>` and `Environment=<prod|staging|dev>`, and the budget is defined in `infrastructure/template.yaml` rather than clicked into the console — set `monthly_budget_usd` and `alarm_email` in the instance config.

Typical monthly cost per instance: **$0.00 idle, and cents under use** on the AWS side — Lambda, DynamoDB, S3, SQS, SNS and CloudWatch all sit inside always-free tiers at single-user volume. The transcription and cleanup providers are the real bill (~$0.70/month light, ~$17 heavy). A staging instance adds nothing fixed.

This was $1.00/month idle until the customer-managed KMS key was removed — it was the entire idle cost, and it bought separation that an SSM SecureString provides for free. `docs/cost-analysis.md` has the itemised figures against the AWS Price List, and the earlier README claim of "$1–10, nearly all of it per-recording" was structurally wrong: the per-recording AWS cost is approximately zero and the fixed cost was everything.
