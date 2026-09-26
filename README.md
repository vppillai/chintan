# Chintan

**Speak a thought. It files itself.**

Chintan is a personal, mobile-friendly PWA for voice brain dumps: you speak unstructured thoughts while driving or walking, and it transcribes the recording, cleans up the text and files it into the right note.

It runs on serverless AWS — Cognito, API Gateway, an API Lambda and a multi-task worker Lambda (captures, retries, the weekly expiry sweep, the daily AWS-cost reading, whole-note clean, Ask), DynamoDB and S3 — and every instance is its own isolated stack. GitHub Pages hosts one independently built frontend bundle per instance, compiled against that instance's stack outputs (API URL, user pool, client id, Cognito domain) by `scripts/ci-build-site.sh` from `deploy-frontend.yaml`. Speech-to-text is **Groq**, and cleanup and routing use an OpenAI-compatible endpoint, **MiniMax** by default, with the API keys in SSM Parameter Store.

- [Deploy your own](#deploy-your-own) · [Configure](#configure) · [Connect a device](#connect-a-device) · [Operate](#operate) · [Security](#security) · [Develop](#develop)
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

Then open `https://<owner>.github.io/<repo>/<site_path>/` — `https://<owner>.github.io/chintan/dev/` for the shipped config, or `https://<app_host>/<site_path>/` once a custom domain is configured (Configure → Custom domain).

What each step needs and leaves behind:

1. `bootstrap-agent.sh` is the only script that needs administrative credentials and the only one that touches IAM outside CloudFormation. It creates the `chintan-agent-boundary` managed policy that `infrastructure/bootstrap.yaml` names — so step 2 fails without it — plus the `chintan-agent` role and `chintan-agent-cli` user that everything else can run as, and the `chintan-trail` CloudTrail trail. `CHINTAN_KEY_OUT=~/.chintan/agent.env scripts/bootstrap-agent.sh --apply` also writes the CLI user's access key to that file instead of ever printing it. The policy documents are the JSON files in `infrastructure/agent-policies/`.
2. `setup.sh` needs the account to have a GitHub OIDC provider for `token.actions.githubusercontent.com`; it checks, and prints the one administrator command that creates it if missing (the templates never create it, because it is shared with other projects). It deploys `chintan-bootstrap`, sets the `AWS_ACCOUNT_ID` repository secret and the `BUILD_ROLE_ARN` and `CFN_DEPLOY_ROLE_ARN` variables (the region is fixed in the workflows' `env`), makes `production` require your approval, and switches Pages to build from Actions.
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
| `enable_alarms` | `true` | Create this stack's CloudWatch alarms — five, or six with a non-zero spend cap: API function errors, a gateway 4xx flood, refused inbox keys (a device still posting with a revoked or mistyped key), the worker dead-letter queue (a capture or a scheduled or queued worker task — sweep-expired, aws-cost, storage-snapshot, clean-note, ask — that exhausted its retries), a rejected provider key, and the spend cap. `dev-staging.yaml` sets `false` — its failures are caught by the deploy's smoke test, and CloudWatch's free allowance is ten alarms per account. |
| `alarm_email` | none | Subscribed to the alarm topic and the budget. Because this repository is public, production reads it from the **`ALARM_EMAIL` repository secret** instead (`gh secret set ALARM_EMAIL`); a value in the file wins if present, which is what a private fork wants. |
| `monthly_budget_usd` | `10` | AWS Budgets limit. |
| `log_retention_days` | `14` | CloudWatch retention. |
| `refresh_token_validity_days` | `30` | Cognito refresh token lifetime. |
| `api_host` | none | Custom hostname for this stack's API, e.g. `api.example.com` (staging: `api-staging.example.com`). A bare lowercase hostname. The stack requests a free, DNS-validated ACM certificate for it in its own region, fronts the HTTP API with it, and its `ApiEndpoint` output — so the bundle's API URL and the Devices card — becomes `https://<api_host>`. See Custom domain. |
| `app_host` | none | The GitHub Pages custom domain the bundles are served from, e.g. `app.example.com`. One per Pages site: every config sets the same value or none does. Sets every stack's Cognito callback URLs and CORS origin, and moves the bundles to the site root (`/<site_path>/`). |
| `dns_zone_id` | none | A Route 53 hosted zone holding `api_host`. Set, the stack writes the certificate's validation record and the API's alias itself. Refused without `api_host`. |

Check what the configs resolve to before pushing:

```bash
scripts/list-instances.sh --format text
# chintan-dev-staging us-west-2 dev-staging
# chintan-dev-prod    us-west-2 dev
```

### Custom domain

Nothing here needs a domain. When you have one, the three fields above move the API and the app onto it, and everything downstream follows in one deploy: the bundle's API URL, the Devices card and the recipes it prints, the CORS origin, the Cognito callback URLs. Without them every stack is exactly what it is today — the custom-domain resources in `infrastructure/template.yaml` (`ApiCertificate`, `ApiDomainName`, `ApiMapping`, `ApiAliasRecord`) are conditional on `api_host`, and `scripts/check-custom-domain-dormant.sh` fails CI if one stops being so.

The sign-in page keeps its `amazoncognito.com` address, deliberately. A Cognito custom domain is CloudFront-fronted and needs its certificate in **us-east-1**, which a us-west-2 stack cannot request; changing the domain *replaces* `UserPoolDomain`, a stateful resource `scripts/deploy.sh` refuses without `--allow-replacement`; and the passkey relying-party id is that domain, so every registered passkey would stop working. Three constraints for a page seen once a month.

**What it costs.** Nothing on AWS: ACM public certificates and API Gateway custom domains are free, and GitHub Pages custom domains are free. A Route 53 hosted zone, if you want one, is $0.50 a month; the templates never create one, because a zone outlives any stack and is a decision rather than a resource. The domain itself is the registrar's fee.

**Order of operations**, once per domain.

1. **Pages first.** Add `app_host` to *every* instance config and re-run `scripts/setup.sh --region us-west-2 --apply` — it is idempotent, and this is the step that tells GitHub the domain (a site published from a workflow ignores any `CNAME` file in the artifact, so the repository setting is the only place it can live); or set **Settings → Pages → Custom domain** by hand. At the DNS provider: `CNAME app.example.com → <owner>.github.io`. GitHub recommends verifying the domain beforehand (**your profile → Settings → Pages → Add a verified domain**) so a dangling CNAME can never be claimed by someone else.
2. **Then the API.** Add `api_host` — and `dns_zone_id` if the name lives in Route 53 — to each config, and push. `Deploy Backend` reaches staging first.
   - *With* `dns_zone_id`: the certificate validates and the alias record lands in a few minutes. Nothing to do.
   - *Without*: the stack pauses on `ApiCertificate` until the validation CNAME exists. Read it from the stack events —
     `aws cloudformation describe-stack-events --stack-name chintan-dev-staging --query "StackEvents[?LogicalResourceId=='ApiCertificate'].ResourceStatusReason" --output text`
     prints `Content of DNS Record is: {Name: _….api-staging.example.com, Type: CNAME, Value: _….acm-validations.aws.}` — and create it. When the stack finishes, create `CNAME api-staging.example.com → <the stack's ApiDomainTarget output>`. The deploy's smoke test on the domain retries for about a minute and, if the record is not resolving yet, fails naming it; add the record and re-run the workflow. Approve production and do the same for `api.example.com`.
     ACM issues the same validation record for the same name in the same account every time, so a stack rebuilt later validates against the record already there.
3. **Reinstall and sign in.** `Deploy Frontend` follows the backend and publishes to `https://app.example.com/dev/` (the root redirects there; staging is `/dev-staging/`). The old `github.io` URL redirects too, but an installed PWA is bound to its origin and its service worker cannot follow a redirect: remove the old icon, open the new URL, install, sign in. Once GitHub has issued the Pages certificate — up to a day — turn on **Enforce HTTPS**.

Two things to know. The custom domain deploys only through the `chintan-cfn-deploy` service role (`CFN_DEPLOY_ROLE_ARN`, set by `setup.sh`): the CI role's permissions boundary lists no ACM or Route 53, on purpose. And `scripts/bootstrap.sh`, the hand-run recovery deploy, takes `--site-base https://app.example.com` for a custom-domain instance, because `--origin` alone spells the Pages form of the callback URL.

### User preferences

Per-user preferences — theme, cleanup mode, audio retention and the default transcription language — are settings in the app (**You**), stored per user, not instance configuration; they are the fields of `GET /v1/settings`, which also reports the instance's `daily_spend_cap_micros` read-only.

### Per-note behaviour

Each note carries its own transcription `language` (absent, it inherits the user's default) and a `verbatim` switch that bypasses cleanup for it. Each note can also keep a **cleaned view** of its whole body (`structured` or `polished`, the note's `cleaned_mode`), regenerated on request or, with `auto_clean`, after every recording appended, moved or deleted; it is read-only and derived from the body, and costs one cleanup call per run under the daily spend cap.

---

## Connect a device

Any device or app that can make one HTTPS request with one header can drop a recording or a line of text into your notes — a watch, a ring's phone app, a desk recorder, an iOS Shortcut — and it is filed exactly as a recording made in the app: transcribed, routed, cleaned, appended. It authenticates with a **device key**, not your sign-in, so it can add to your notes and never read them, and you can revoke it on its own. `docs/design/inbox.md` has the design and the threat model.

**Issue a key.** In the app: **You → Devices & shortcuts → Add a device** shows the key once. Headless: `POST /v1/devices {"name": "Kitchen watch"}` with your session token; the key `ck_…` is in that response and nowhere else (only its hash is stored), so copy it then.

```bash
API=https://<api-id>.execute-api.us-west-2.amazonaws.com   # the stack's ApiEndpoint output — https://api.example.com behind a custom domain
curl -sS "$API/v1/devices" -H "Authorization: Bearer $ID_TOKEN" \
  -H 'Content-Type: application/json' -d '{"name":"Kitchen watch"}'
# → {"id":"dev_…","name":"Kitchen watch","created_at":"…","last_used_at":null,"key":"ck_dev_…_…"}
```

`GET /v1/devices` lists your devices (never the keys); `DELETE /v1/devices/{id}` revokes one, immediately. Ten devices, two hundred requests per device per day, 4 MiB per one-shot recording (about nine minutes at the iOS Shortcut's *Normal* quality; the gateway's limit, not the pipeline's — a longer recording goes through the two-step `/v1/inbox/captures` route, whose PUT goes straight to the bucket).

**curl.** Three ways in, each `Authorization: Bearer ck_…` (the bare `ck_…`, or `X-Device-Key: ck_…`, is the same key):

```bash
KEY=ck_dev_…
# One-shot: the recording is the body. Optional headers: X-Chintan-Note-Id, X-Chintan-Language (auto or a code), X-Chintan-Duration-Ms.
curl -sS "$API/v1/inbox/audio" -H "Authorization: Bearer $KEY" -H 'Content-Type: audio/mp4' --data-binary @memo.m4a
# Text: no transcription; routed, cleaned and appended as a transcript would be.
curl -sS "$API/v1/inbox/text" -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' -d '{"text":"call the roofer about the gutter"}'
# Two-step, for a client that can PUT: the same 201 as POST /v1/captures, then PUT the file to upload.url with upload.headers.
curl -sS "$API/v1/inbox/captures" -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' -d '{"content_type":"audio/mp4"}'
```

Accepted audio types: `audio/webm`, `audio/ogg`, `audio/mp4`, `audio/m4a`, `audio/mpeg`, `audio/wav`, `audio/x-wav`. Both one-shot routes answer 202 with the capture; watch it land in the app's Recordings tab, where a recording from a device says which one.

**iOS Shortcut.** *Record Audio* (Quality: Normal, Start: Immediately, Finish: On Tap) → *Get Contents of URL*: URL `$API/v1/inbox/audio`, Method **POST**, Headers `Authorization` = `Bearer ck_…` and `Content-Type` = `audio/mp4`, Request Body **File**, File = *Recorded Audio*. Add it to the Home Screen, the Action Button or a watch complication; *Dictate Text* → *Get Contents of URL* with Request Body **JSON** (`text` = *Dictated Text*) to `$API/v1/inbox/text` is the text version and works on an Apple Watch. To file into one note, add a `X-Chintan-Note-Id` header with the note's id from the app's address bar.

**Android.** The *HTTP Shortcuts* app (Play Store, open source): a shortcut with Method POST, URL `$API/v1/inbox/audio`, a header `Authorization` = `Bearer ck_…`, Request Body Type **File** — it then appears in the share sheet of any recorder, or records itself with the built-in "record audio" body option; a second shortcut with Request Body Type **Custom text** (`{"text": "{{prompt}}"}`, Content-Type `application/json`) to `/v1/inbox/text` asks for a line and sends it. Tasker's *HTTP Request* action does the same. Any watch or ring whose companion app can POST a file or a string with one header works the same way; there is no per-vendor integration.

**Pebble Index 01 ring.** In the Pebble app, *Index* → *Webhook*: URL `$API/v1/inbox/audio`, header `Authorization` = `Bearer ck_…` (the bare `ck_…` works too), send **Recording** (or **Both**). The ring posts a `multipart/form-data` form; its `audio` part is filed like any recording, and a form with *Transcription* alone is filed as `/v1/inbox/text` files text.

---

## Operate

**Release flow.** Open a pull request; `ci.yaml` runs the gates in [Develop](#develop). Merge to `main`: `Deploy Backend` deploys staging, smoke-tests it, waits for your approval on `production`, deploys prod, tags `vX.Y.(Z+1)` and publishes a release; `Deploy Frontend` then builds and publishes the Pages site, and the You screen shows that tag. `scripts/deploy.sh`, the change-set deploy CI uses, prints the change set before executing it and refuses one that would *replace* or *remove* the user pool, the table, the bucket or the user pool client — `Retain` would keep the old resource, but the stack would point at an empty one and every note would become unreachable. Pass `--allow-replacement <LogicalId>` once a migration is planned.

**Versions.** Three names, each for a different thing. The footnote at the bottom of You is the frontend build's `git describe --tags --always`, injected as `VITE_VERSION` by `scripts/ci-build-site.sh` and read in `frontend/src/config/env.ts` (a local build says `local build`); `VersionFootnote.tsx` shows a release as its tag alone and a later build as the tag plus `+N (sha)`. Backend releases are the `vX.Y.Z` tags: the `Tag and release` job in `deploy-backend.yaml` tags every production deploy `vX.Y.(Z+1)` from the latest `v*` tag and publishes a GitHub release, and because Deploy Frontend runs only after that job has finished, a frontend built after a backend deploy shows exactly that tag. `info.version` in `docs/api/openapi.yaml` is the API contract's version and moves only when the contract does.

**Smoke test.** `GET /v1/health` (liveness) and `GET /v1/health/ready` (round-trips DynamoDB and S3 under the Lambda's own role). CI runs both against staging before prod and against prod after; run them by hand with `curl "$(aws cloudformation describe-stacks --stack-name chintan-dev-prod --query "Stacks[0].Outputs[?OutputKey=='ApiEndpoint'].OutputValue" --output text)/v1/health/ready"`.

**Rollback.** Every deploy publishes an immutable Lambda version and moves the `live` alias; the deploy log and the job summary print the exact `aws lambda update-alias … --function-version <N>` that puts the previous one back. To roll the whole stack back, redeploy the previous commit.

**Alarms.** Five or six per stack when `enable_alarms` is true (API errors, a gateway 4xx flood, refused inbox keys, the worker dead-letter queue, a rejected provider key, and — when `daily_spend_cap_micros` is non-zero — the spend cap), e-mailed to `ALARM_EMAIL`. Refused inbox keys — twenty-five or more in fifteen minutes — is a device still posting with a key you revoked or mistyped: **You → Devices & shortcuts** shows which keys are live; fix or remove the device. The dead-letter queue is shared by every asynchronous worker invocation, so one message is one capture or one scheduled or queued worker task (sweep-expired, aws-cost, storage-snapshot, clean-note, ask) that exhausted its three attempts; the record names which. For a capture the recovery is the app's Retry; a scheduled task runs again on its next schedule, and a clean or an Ask on the next request. Read the record, act, then **purge the queue**: the alarm is on the queue's depth and stays in ALARM while the message sits there — fourteen days, the queue's retention — and an alarm already in ALARM sends no second e-mail, so a second dead-letter inside that window is silent until you purge. The queue is `chintan-captures-dlq-<instance>-<environment>` (`chintan-captures-dlq-dev-prod` for the default instance): `Q=$(aws sqs get-queue-url --queue-name chintan-captures-dlq-dev-prod --query QueueUrl --output text); aws sqs receive-message --queue-url "$Q" --max-number-of-messages 10 --visibility-timeout 0` to read, `aws sqs purge-queue --queue-url "$Q"` once acted on; the alarm returns to OK on its next period. A weekly EventBridge rule runs the worker's expiry sweep, which deletes the objects and rows of archived notes past their thirty-day purge deadline; DynamoDB TTL is the backstop.

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

**Users.** Self sign-up is closed on the pool, so every account is created by `scripts/invite-user.sh --instance dev --email them@example.com --apply`, which writes a temporary password to `./chintan-invite-<email>` (mode 600; `--print-password` shows it instead). Hand it over out of band — Cognito sends nothing — and delete the file once they are in. First sign-in: the username is the email, the temporary password must be changed within three days (`TemporaryPasswordValidityDays`), then **You → Passkeys → Add a passkey on this device** so the sign-in page offers the passkey from then on. Reset: the same command on an existing account issues a new temporary password and forces a change. Disable: `--disable` refuses sign-in and token refresh and keeps the notes, for a lost phone or a pause (`aws cognito-idp admin-enable-user` reverses it). Offboard, in this order: `chintanctl erase --instance dev --tenant <sub> --apply` (the script prints the sub; erase deletes every item and object under it), then `scripts/invite-user.sh … --delete`, because once the Cognito user is gone nothing maps the email to the tenant id. A second user shares the instance-wide daily spend cap and the API throttle; there is no per-tenant cap yet.

**Workflows.** Each names the command or two involved; `--help` on any of them has the flags.

| Workflow | Commands |
|---|---|
| Bootstrap an account | `scripts/bootstrap-agent.sh` once as an administrator, then `scripts/setup.sh`; `scripts/doctor.sh` says what is left. |
| Deploy an instance | `gh workflow run deploy-backend.yaml` (the frontend follows), or `scripts/bootstrap.sh` from your own machine; `scripts/list-instances.sh --format text` shows what would deploy. |
| Invite, reset, disable or offboard a user | `scripts/invite-user.sh` (`--disable`, `--delete`); `chintanctl erase` before a delete |
| Recover failed processing | The app's Retry for one capture; `chintanctl reconcile` for what the table and the bucket disagree about. |
| Back up, restore, export | `chintanctl backup` and `chintanctl restore`; `chintanctl export` for a vault Obsidian opens. |
| Inspect usage | `chintanctl usage` for every tenant; **You → Usage** in the app for your own. |
| Tear down | `scripts/cleanup-aws.sh` for one stack and what it retains; `scripts/clean-instance-orphans.sh` after a failed create; `scripts/teardown.sh` for everything — every note and recording; the CloudTrail trail, its bucket and the agent principal are left alone. |

**Costs.** At single-user volume the AWS side is a few cents a month — Lambda, DynamoDB, S3, SNS, EventBridge and CloudWatch sit inside the always-free tiers, the alarms and the CloudTrail digests are the lines that do not — and the transcription and cleanup providers are the real bill, from under a dollar a month for light use to the daily spend cap you set. Those are two different numbers: **provider spend** (Groq, MiniMax) is metered per call by the worker and attributable to each user, while **AWS spend** is the account's month-to-date actual read once a day from the stack's Budget, which is the instance's cost only on an account dedicated to it and an upper bound on a shared one. **You → Usage** shows both, plus each user's estimated share of the AWS figure, apportioned by provider cost (`docs/design/usage-accounting.md`). Every resource carries `Project`, `Instance` and `Environment` tags, and the budget is defined in `infrastructure/template.yaml`; activate the tags once in Billing → Cost allocation tags to see the per-instance view.

---

## Security

This is a public repository; nothing in it is secret.

- **Authentication** is Cognito. Sign-in goes through the hosted managed-login page, which is branded from the app's own tokens by the template; passkeys are registered there too (**You → Passkeys**). Self sign-up is closed on the pool (`AllowAdminCreateUserOnly`): only `scripts/invite-user.sh` creates accounts, and `scripts/doctor.sh` reports the flag. The JWT is verified at the API Gateway authorizer and again in the service against the pool's JWKS; identity comes from the verified token and from nothing else.
- **Isolation** is per tenant: every DynamoDB key is prefixed by the verified user id, every S3 key sits under `tenants/<id>/`, and the repository tests assert that one tenant cannot read another.
- **Provider keys** live in SSM Parameter Store as `SecureString`, referenced by path, read by the Lambdas at run time, never by CloudFormation and never in a log. Nothing in the repository or the bundle holds a plaintext secret; the frontend carries only public endpoints and the Cognito client id.
- **Deploys** run from CI as `chintan-github-actions`, scoped to `chintan-*` and trusted only from the `staging` and `production` environments; production requires your approval. The build jobs assume `chintan-github-build`, which can upload artifacts and read stack outputs and nothing else.
- **Nothing derived from speech reaches a log.** `scripts/check-log-hygiene.sh` enforces it in CI.
- **The agent principal** from `scripts/bootstrap-agent.sh` runs under a permissions boundary, cannot read the provider keys, and cannot write to or stop the CloudTrail trail.
- **CORS** is restricted to one origin: your Pages origin, or `app_host` once a custom domain is configured. Never commit `.env` files or keys; `.gitignore` already excludes them.

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

More: [`docs/backlog.md`](docs/backlog.md) · [`docs/api/openapi.yaml`](docs/api/openapi.yaml) · [`docs/design/`](docs/design/) · [`docs/ops/`](docs/ops/) · [`docs/qa/`](docs/qa/) · [`docs/reviews/`](docs/reviews/) (the review reports and the owner's queue) · [`docs/history/`](docs/history/) (dated reports, not current).
