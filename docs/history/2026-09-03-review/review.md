# Chintan review — 2026-09-03

**Scope:** every non-test file in `backend/`, `frontend/src`, `infrastructure/`, `scripts/`, `.github/`; the live AWS account (read-only, as `chintan-agent`); the GitHub repository settings; and the previous reviews, to separate what is still open from what was fixed.
**Method:** five parallel deep reads with file:line evidence, each finding re-verified against the code before it was accepted, plus the full build and test suites run on a Linux box. The per-area reports are in [`review-2026-09-03/`](./) and carry the Medium and Low items this summary only names.
**Tree reviewed:** `9aa4435` (the deployed commit). **Fixes shipped with this review:** the three commits before this document on the same branch.

---

## 1. The verdict, in one paragraph

The code is careful. Identity comes only from a verified Cognito token, every DynamoDB key is prefixed by it, conditional writes are real, the prompts fence dictation as data, and the tests are behaviour tests rather than mock theatre. No cross-tenant or auth-bypass defect exists. The problems sit at the seams no test crosses: a worker that dies after writing the note leaves the capture spinning forever; a recording killed mid-dictation is on disk but unreachable; a transient 500 locks an idempotency key for a day; the deploy gate the README describes does not exist on GitHub; and staging cannot sign in. Around that core sit roughly **17,600 lines of production Go and 12,300 of TypeScript** — plus 2,300 of CloudFormation and 5,000 of shell — serving a product that has had **one user, six active days, and zero invocations since 2026-08-18**. About half of that machinery exists for tenants, throughput, and threats the project does not have, and it is where the defects live.

## 2. What the live system says

| Fact | Value | Source |
|---|---|---|
| Deployed version | `9aa4435`, both stacks, `live` aliases match | Lambda descriptions, stack outputs |
| Usage | Six active days (Aug 8–17). Last worker run Aug 17, last API call Aug 18. **Zero invocations in the 14-day log window.** | CloudWatch Logs |
| Users | 1 confirmed; 1 invite stuck in `FORCE_CHANGE_PASSWORD` for four weeks with an expired 3-day temporary password | Cognito |
| Data | 30 notes, 37 captures, 5.6 MB in S3, 82 KB in DynamoDB | `list-objects`, `describe-table` |
| Orphan | `chintan-content-dev-<account>`: the pre-rename bucket, 107 objects of user data, versioned, **no lifecycle**, in no stack | `list-buckets` |
| AWS cost | Aug $0.36 (of which $0.14 S3, $0.09 Cost Explorer API calls, $0.05 the CMK since removed); Sep forecast $0.26 | Cost Explorer |
| Cost tags | `Project`/`Instance`/`Environment` **inactive** in Billing, so the per-project view the template tags for is empty | Cost Explorer |
| Alarm state, DLQ depth, error rate | **Not readable by the agent role** — `cloudwatch:DescribeAlarms`, `GetMetricData`, `sqs:GetQueueAttributes` are denied to it | IAM |
| GitHub | `main` unprotected; no rulesets; `production` environment had **no required reviewer** until this review set one; three unattended prod deploys (Aug 13/15/17), the last starting 3 s after staging finished | GitHub API |

The full audit, with every command's result, is in [`aws-live-audit.md`](aws-live-audit.md).

## 3. Findings

Severity is about consequence for the one user's recordings, notes and wallet. **Fixed** means fixed on this branch with a test that fails against `9aa4435`.

### Critical

| # | Finding | Where | Status |
|---|---|---|---|
| C1 | **An interrupted append strands the capture in `appending` forever.** The worker takes a 20-minute claim right before writing the note. If it dies after the write (Lambda timeout, DynamoDB fault, five version conflicts), SQS redelivers at 960 s — inside the lease. The redelivery found its own token still claimed, *conceded*, and acked the message. Nothing is redelivered after a concede. The redelivery test passed because it fabricated a third delivery SQS never makes. | `pipeline.go` append, `store.go` `AppendClaimLease`, `template.yaml` `VisibilityTimeout` | **Fixed.** A delivery that finds its own unfinished claim finishes the bookkeeping when the text is already in the note body, and otherwise returns an error so SQS redelivers after the lease, where the existing same-token resume takes over. Tests replay the real 3-delivery schedule for both "died after writing" and "died before writing". |
| C2 | **A recording in progress cannot be recovered after a reload or an iOS kill.** Chunks stream to IndexedDB from the first `ondataavailable`, but the `captures` row that makes them findable was written on the `review` transition — i.e. on Stop. `unconfirmedCaptures()` reads only that row. A twenty-minute dictation interrupted by the OS was durable and unreachable, which is indistinguishable from lost, and never pruned. | `store.ts:81`, `buffer.ts:92`, `ResumePrompt.tsx:31` | **Fixed.** The row is written when the stream is ready and refreshed on review. Test asserts it exists before any chunk arrives. Caveat: on Safari a partial `audio/mp4` is only decodable if WebKit emits fragmented MP4 with `timeslice`; this needs one manual check on a real iPhone. |
| C3 | **No human gate on production, and `main` unprotected.** The README says setup creates `production` "with you as a required reviewer" and prod "waits for your approval". The environment had only a branch policy; prod ran 3 s after staging. | GitHub settings | **Fixed (settings).** `production` now requires `@vppillai`. Branch protection on `main` is left to you: with a solo repo the useful rule is *required status checks*, which forces PRs; decide whether you want that. |
| C4 | **`deploy.sh` executes a change set that replaces the user pool, table or bucket.** `Retain` keeps the old resource, but the stack switches to an empty one. Tenant ids are Cognito `sub`s, so a replaced pool makes every note unreachable. Staging cannot catch it: an empty pool passes the health smoke. | `deploy.sh` change-set section | **Fixed.** Replacements (`True` or `Conditional`) of `UserPool`, `UserPoolClient`, `UserPoolDomain`, `DynamoDBTable`, `ContentBucket` are refused unless named in `--allow-replacement`. `deploy.sh --self-test` proves the refusal. |

### High

| # | Finding | Where | Status |
|---|---|---|---|
| H1 | A 5xx pinned the `Idempotency-Key` for 24 h: the claim stayed unfinished, every retry with the same key (which the frontend deliberately sends) got `409 an identical request is still in flight`. | `handler/idempotency.go`, `dynamo.go` `BeginIdempotent` | **Fixed.** The handler abandons the claim on any response it does not record (5xx, panic, failed completion); a bare claim is honoured only for a 60 s lease as a backstop. Test: 500, then 201 on the same key, then a real replay. |
| H2 | Failed enrolment of a *second* biometric device called `DeleteAllWebAuthnCredentials`, deleting the first device's working credential too. | `service/webauthn.go:183-202` | **Fixed.** Everything that can reject the enrolment now runs before anything is written. Two-device test. |
| H3 | Staging Cognito callback registered `/chintan/dev/`; the staging bundle redirects to `/chintan/dev-staging/`. Every staging sign-in ended in `redirect_mismatch`. Masked because staging has no users and the smoke only hits `/v1/health`. | `template.yaml` `CallbackURLs`, `oauth.ts:34` | **Fixed.** `SitePath` parameter, passed from the workflow matrix. |
| H4 | The smoke test was `GET /v1/health` alone — which the template itself records as having passed a multi-day outage where the API could not read its index. `/v1/health/ready` existed and was unused. | `deploy.sh` smoke | **Fixed.** Both probes. A signed-in staging smoke (create user, list notes, create capture) is the next step and is scoped in the infra report. |
| H5 | A failed chunk write moved the machine to `review` but never stopped the recorder: microphone open, wake lock held, behind a screen saying the recording had ended. The test enshrined it by asserting only the event. | `recorder.ts:200` | **Fixed.** Stops; test asserts track stopped and wake lock released. |
| H6 | One `AudioContext` per recording, never closed. WebKit caps live contexts; after a handful of captures the constructor threw, the failure was swallowed, and the waveform and start/stop tones — the only eyes-off feedback on iOS, where `vibrate` does not exist — silently vanished. | `recorder.ts:176`, `teardown()` | **Fixed.** Previous context closed before the next opens (not in teardown, so the stop tone plays). |
| H7 | IndexedDB handle cached forever; Safari terminates connections behind a backgrounded page; every later write rejected until reload and every caller swallows it (which also feeds H5). | `db.ts:117` | **Fixed.** `terminated` drops the cached promise. |
| H8 | Autosave has no in-flight guard: a slow PATCH plus continued typing sends a second PATCH with the same `version`, gets 409, and shows "another device saved this note" about the user's own keystrokes. | `useNoteEditor.ts:82-114`, `autosave.ts:111` | **Open.** Serialise saves and take `version` from the response. Half a day. |
| H9 | The offline-queue flush can run twice concurrently (reconnect refetch and the Background Sync message both call `refetchQueries` with `cancelRefetch`), and `markDead` can re-put an entry the other loop delivered. Chromium only; Background Sync does not exist on WebKit. | `useOfflineQueue.ts:138-157`, `queue.ts:183-230` | **Open.** Recommended fix is to *delete Background Sync* (see §5). |
| H10 | Spend metering is never persisted. The worker wires `SlogSink{}`; `DynamoUsageSink` has no production caller; `chintanctl usage` reads `USAGE#` rows nobody writes; the test that "proves" the key shape connects two components never wired together. | `cmd/worker/main.go:124-130`, `usage_sink.go` | **Open.** Wire it or delete ~310 lines. The spend cap still works (it uses the `SPEND#` counter, not the rows). |
| H11 | Infrastructure faults inside a provider call become *permanent* capture failures, with the raw AWS SDK error string stored on the capture and served to the client. A 30-second MiniMax blip fails every in-flight capture for good. | `pipeline.go:940-1007`, `breaker.go:156` | **Open.** Classify: counter/transport/5xx/429 → return error (retry); auth/spend-cap → fail. Fixed user-facing sentence on `capture.Error`. |
| H12 | `chintanctl restore` overwrites live rows unconditionally, leaves post-backup rows in place, and restores audio without the retention tags, so it never expires. It needs only `--apply` where `erase` demands a typed tenant id. | `restore.go:183-207` | **Open.** Refuse a non-empty target without `--force`; or delete it and rely on PITR + S3 versioning, which already exist. |
| H13 | The permissions boundary does not close the escalation it exists to close: a bounded principal may create an *unbounded* `chintan-*` role, attach `AdministratorAccess`, and pass it to Lambda. Only `CfnDeployRole` requires the boundary on created roles. `agent-policies/README.md` claims otherwise. | `permissions.json` `IamOnProjectRolesOnly`, `policy-source.json` | **Open.** Add `iam:PermissionsBoundary` condition to `CreateRole`/`PutRolePolicy`/`AttachRolePolicy`; drop the two `DefenceInDepth*` statements the README admits do nothing to make room under the 6,144-char limit. Or accept that a one-person account does not need a boundary at all (§5). |
| H14 | `cleanup-aws.sh` / `teardown.sh` die mid-way *after* emptying the bucket (the `update-user-pool --deletion-protection` call lacks the attributes Cognito requires — the sibling script documents the rejection), and the retained deletes are denied to the agent role the README says runs everything. | `cleanup-aws.sh:159-213` | **Open.** Delete the stack before emptying the bucket; reuse the working call; refuse to run as the agent. |
| H15 | The `build` GitHub environment has no protection and no branch pin, and assumes the *same* IAM role as prod. Any branch's workflow declaring `environment: build` gets `cloudformation:*Stack` on `chintan-*`. | `bootstrap.yaml:641-651` | **Open.** Separate build role (S3 put + DescribeStacks), or `custom_branch_policies: [main]`. |

### Medium (summary; full text in the area reports)

**Backend API** — JWKS refresh failure after the 1 h TTL is a total 401 outage instead of serving the stale key (`jwks.go:71-99`, and the test pins it); WebAuthn `CloneWarning` ignored (`webauthn.go:254-266`); store-level version conflict reports `current_version: 0` (`notes.go:345`); biometric login hands the refresh token back to the browser, which persists it in `localStorage`, so the sealed vault protects nothing the device does not also hold; `GET /v1/notes/{id}` silently caps captures at 50; `?tag=` is case-sensitive against lowercased tags; `/health/ready` is an unauthenticated, unthrottled DynamoDB + S3 round trip.

**Backend pipeline** — a routing failure silently files the dictation in a *new* note and note creation is not atomic with the capture write; a deleted destination note dead-letters instead of failing the capture; LLM cost never charges output tokens (~5× understatement on cleanup) and `meter.DefaultPrices` are 3–10× high per the cost analysis, so `DailySpendCapMicros` binds at a fraction of the dollars you set; `thinking: {type: disabled}` is MiniMax-only and breaks the "any OpenAI-compatible endpoint" claim; `MaxCaptureBytes` is 256 MiB against Groq's 25/100 MB; `reconcile --apply` can delete an object whose row is milliseconds old (S3-first writes, eventually-consistent scan); "terminal" is defined three different ways; `erase` and "delete forever" leave bytes for 7 days on the versioned bucket, undocumented; `RetryCapture` cannot rescue an `appending` capture (largely moot after C1).

**Frontend** — refresh token in `localStorage` and no CSP (`tokens.ts`, `index.html`); a Cognito 5xx/429 on refresh signs the user out (`session.ts:134`); `alreadyLanded` prunes local audio on *any* status other than `uploaded`, including the backend's oversize `failed`, which deleted the server copy (`uploader.ts:86`) — latent data loss the day a "stale uploaded" sweeper is added; the progress card fires **four list requests every 4 s** including `status=all`, plus four on every focus, for the whole two-minute pipeline — trivial in dollars, not in battery on a phone in a car (`queries.ts:348`); PKCE pending state in `sessionStorage` is lost when iOS kills the standalone PWA while the user reads an MFA code (`pending.ts:8`); `returnTo` is never read; track `mute` (a pocketed phone, a lock screen) is a flag shown only after Stop — while it happens the timer counts and the waveform goes flat and nothing says so.

**Infra / CI** — the DynamoDB table has no `DeletionProtectionEnabled` (the pool does; it is free); the guardrails and boundary-drift checks with teeth run in no workflow — CI runs only `--local-only`, whose real content is one GSI check; a dead `kms:Decrypt` on an alias ARN with a comment explaining what it does not do; reserved concurrency 50 + a 50 rps throttle on four unauthenticated routes is ~$4–5/day if someone points a loop at `/v1/health`; the alarm set sends four emails for one transient 500 (`api-errors` and `api-5xx-rate` both fire, then both `OK`), and `worker-errors` fires on the first attempt of a capture SQS will retry — the signals for one user are `capture-dlq`, `expiry-dlq`, `provider-key-rejected`, `spend-cap-tripped` and the budget; actions pinned by major tag in jobs holding the deploy role, `bun-version: latest` making the production bundle non-reproducible, no Dependabot; three overlapping account-wide $10 budgets; self sign-up allowed on an invite-only pool; the `pwa:` config block that nothing reads (open since August).

### Previously recommended, still not done

From [`prior-reviews-digest.md`](prior-reviews-digest.md): five backend capabilities with client wrappers and **no UI** (create a note manually, match-by-description, record into a chosen note, export, tag filter); the prod bucket rename's migration story (the orphan bucket above is its residue); tokens out of `localStorage` + CSP; correcting `DefaultPrices` against an invoice; the audit log "required before a second user"; a WebKit Playwright project; reconciling `cost-analysis.md` with itself.

## 4. What is over-built, and by how much

Counted from the code, not estimated from the spec. "Deletable" means removable with no loss for one user; "collapsible" means replaceable by something an order of magnitude smaller.

| Area | Lines (prod + test) | What it is for | Verdict |
|---|---|---|---|
| Custom WebAuthn + refresh-token vault + SSM vault key + Cognito refresh plumbing + frontend enrolment/unlock | ~1,900 Go + ~700 TS + tests | Skipping one Cognito redirect per week | **Delete.** The user pool is on the ESSENTIALS tier with managed login v2, which supports **native passkeys** (`SignInPolicy.AllowedFirstAuthFactors: [PASSWORD, WEB_AUTHN]`, `WebAuthnRelyingPartyID`). Same biometric unlock, zero custom code, no vault, no vault key, and it removes H2, M-CloneWarning, M-vault-decorative and the 7-day refresh cap. Confirmed live: passkeys are *not* enabled today (`PASSWORD` only). |
| Per-tenant spend caps, cap resolver, `SpendGate`, `DynamoUsageSink`, `MultiSink`, `USAGE#` rows, `chintanctl usage` | ~475 Go + tests | Metering tenants | **Collapse** to one instance-wide `ADD`-and-compare on `SPEND#<day>`, which is the part that actually stops a runaway loop. |
| GSI2 + `ReindexNotes` + `chintanctl reindex` + the two-step-deploy README section | ~250 Go + a GSI | Ordering a few hundred notes by `updated_at` | **Delete.** `decideTarget` already drains up to 500 notes from the base table and sorts in Go; `ListNotes` can do the same. |
| Expiry Lambda + DynamoDB Streams mapping + `ExpiryDLQ` + 2 alarms | ~260 Go + template | Cascading S3 deletes when TTL removes a note | **Collapse** to a weekly EventBridge sweep calling the existing `PurgeNoteArtifacts` (~40 lines). |
| SQS + DLQ + queue policy + event-source mapping + `ReportBatchItemFailures` + two message shapes + `s3:TestEvent` handling | ~150 Go + template | Backpressure that a few captures a day never produce | **Collapse.** S3 `ObjectCreated` → worker Lambda directly (async invoke retries twice; `OnFailure` destination for the dead-letter). C1 was an SQS-specific coupling — `VisibilityTimeout > Timeout` forced a 16-min redelivery inside a 20-min lease. |
| `chintanctl` (11 subcommands, 4,766 lines) | 4.8k Go | Ops tooling | **Keep `export`** (the README's recovery promise). `backup`/`restore` are PITR + S3 versioning, which are on. `erase` is `DELETE /v1/notes/{id}/permanent`. `reconcile` and `reindex` go with the things they reconcile. |
| Readiness probe, per-container rate limiter, export "job" ceremony, Lambda-side CORS that API Gateway already handles, interfaces with a single implementation | ~800 Go | Defensive layering | **Delete** most; API Gateway throttles and does CORS. |
| Staging stack, second Cognito pool, `enable_alarms`, 10 alarms × 2, multi-instance matrix, agent permissions boundary + deny policy + generator + drift check, CloudTrail trail, CODEOWNERS | ~1,800 shell/YAML/py | Multi-tenant, multi-operator process | **Reduce.** Staging that cannot sign in and smokes only `/health` caught nothing in six weeks; a real signed-in smoke on *prod* plus the Lambda alias rollback that already exists is a better gate for one person. The boundary is complexity theatre with an escalation hole (H13); a one-person account either trusts its owner or uses SCPs. Keep: budget, `capture-dlq`, `provider-key-rejected`, `spend-cap-tripped`. |
| Frontend: Background Sync (cannot do work — nothing enqueues the mutation kinds it flushes), five queued-mutation kinds, sheet reducer used only by its tests, dead API wrappers, three-way theme, twin Notes/Archive screens, bulk select, the token linter | ~1,800–2,300 TS | Spec completeness | **Delete** Background Sync (also fixes H9), the dead wrappers and mutation kinds, the sheet reducer. The rest is taste. |

Total: roughly **9–11k of 17.6k production Go lines** and **~2k of 12.3k TS**, plus about half the template and scripts, can go without the one user losing anything — and C1, H2, H9, H10, H12, H13, H14 and most of the Medium list go with them.

## 5. Recommendation: Chintan v3, one user, a tenth of the machinery

Two rearchitectures were evaluated. The first is recommended; the second is described so it is not re-derived later.

### 5.1 Recommended: keep the core, cut the seams (weeks, not months)

The parts the tests prove correct — the capture state machine, the recorder, the note/player screens, the routing and cleanup prompts with their output verifier, the JWT verification, the tenant-keyed repository, the conditional-write discipline — stay. Everything in §4 marked delete or collapse goes. Order of work, each step independently deployable:

1. **Auth → Cognito passkeys.** Add `Policies.SignInPolicy.AllowedFirstAuthFactors: [PASSWORD, WEB_AUTHN]`, `WebAuthnRelyingPartyID: vppillai.github.io`, `WebAuthnUserVerification: required`, `ALLOW_USER_AUTH` on the client; raise `RefreshTokenValidityDays` to 30–90. Delete `service/webauthn.go`, `vault_box.go`, `cognito_refresh.go`, the two public routes, the SSM vault key, `features/settings/webauthn.ts`, `BiometricSetting.tsx`, `enrolment.ts`, the unlock e2e. Keep tokens in memory + refresh token in IndexedDB, add a `<meta>` CSP. (Removes ~2,600 lines and four findings.)
2. **Pipeline transport → S3 → Lambda.** Drop SQS/DLQ/mapping; `OnFailure` → SNS → the one alarm email. API `retry`/`target` call `lambda.Invoke(Event)`. Make the append marker exact (`<!-- chintan:capture:<id> -->` in the body) instead of `strings.Contains`, and drop the lease arithmetic entirely: the marker check is the idempotency guard, the claim is only a mutex.
3. **Spend → one counter.** Instance-wide `SPEND#<day>` `ADD` with a cap from the template; delete the tenant resolver, the sinks, `USAGE#`, `chintanctl usage`. Fix `DefaultPrices` from one real invoice while at it.
4. **Data → base table only.** Delete GSI2 and the reindex; sort in Go. Weekly EventBridge sweep replaces the expiry Lambda. `DeletionProtectionEnabled: true`.
5. **Ops → prod + rollback.** Delete the staging stack and its config; smoke prod with `/health/ready` plus an authenticated `GET /v1/notes` using a long-lived test user; document the alias rollback as *the* recovery. Delete the agent boundary/deny/generator/drift-check, CloudTrail trail, CODEOWNERS, `guardrails-check.sh`, `check-boundary-drift.sh`, multi-instance matrix. Keep `bootstrap.yaml`'s deploy role scoped to `chintan-*`, the budget, four alarms.
6. **Frontend trim.** Delete Background Sync, dead mutation kinds and wrappers, the sheet reducer; fix H8 (serialise autosave); one `status=all` poll per 4 s (or 8 s) instead of four; wire the five backend features that have no UI or delete their endpoints; add a WebKit Playwright project for the non-microphone specs.

Data migration: none. The table, bucket and pool are untouched; only the pool's *sign-in policy* changes. Existing notes and captures continue to work.

### 5.2 Considered and not recommended: no API server at all

The PWA could talk to DynamoDB and S3 directly with Cognito Identity Pool credentials scoped by `${cognito-identity.amazonaws.com:sub}` (`dynamodb:LeadingKeys`, S3 prefix), leaving only the worker Lambda triggered by S3. It deletes API Gateway, the handlers, JWT verification, presigning, CORS, idempotency, OpenAPI and the contract tests — perhaps 10k lines. It is a legitimate pattern for a single user.

It is not recommended because it removes the *healthiest* part of the system (the API layer had zero Critical or High findings of its own) and keeps the two seams where the defects actually live: worker-vs-client coordination on the note body, and the client's durability story. It also re-keys every item (identity-pool ids are not user-pool `sub`s), so it *does* need a data migration, and it moves tenant isolation from Go tests into IAM condition keys that nothing in the repo can unit-test. If Chintan ever gets a second user, the server is the place that decision wants to live.

## 6. Actions for the owner (outside the code)

1. **Merge this branch.** It deploys the four Critical and seven High fixes. Production now waits for your approval in the Actions UI (`production` environment); the run's staging job smokes `/health/ready` first.
2. **Verify C2 on an iPhone once:** start a recording in the installed PWA, background it for a minute, kill it from the app switcher, reopen. The stranded recording should be offered back and play.
3. **Decide the orphan bucket.** `chintan-content-dev-<account>` holds 107 objects of your own notes and audio from before the Aug 15 rename, in no stack, with no lifecycle. Confirm prod has them (`chintanctl export` on prod, compare), then empty and delete it by hand.
4. **Delete or re-invite the dead user** in the prod pool (`FORCE_CHANGE_PASSWORD` since Aug 6).
5. **Activate the `Project`, `Instance`, `Environment` cost-allocation tags** in Billing if you want the per-project view; ~24 h, no backfill.
6. **Grant the agent role read on CloudWatch alarms/metrics and SQS attributes** if it is meant to audit health; today it cannot see alarm state or DLQ depth.
7. **Branch protection on `main`:** required status checks (`ci`), no required reviews. Optional, but it is what makes the deploy gate meaningful.
8. **Decide on §5.** If yes, step 1 (Cognito passkeys) is the highest value for the least risk and is a template change plus deletions.

## 7. What was verified and how

- `go build`, `go vet`, `staticcheck`, `golangci-lint`, `govulncheck`, `go test -race ./...`: clean before and after the fixes (orb, Go 1.25.13).
- `bun run typecheck`, `lint` (eslint + token check), `test` (414 → 419 tests), `build`: clean.
- `shellcheck --severity=warning`, `shfmt -d -i 4 -ci`, `bash -n`, `deploy.sh --self-test`, `cfn-lint`, `list-instances.sh`, `check-vite-env.sh`, `guardrails-check.sh --local-only`: clean.
- Every Critical and High was re-read at the cited lines before being accepted; two candidate findings from the area reviews were rejected on re-reading and do not appear here.
- The AWS audit was read-only, as `chintan-agent`, and read no SecureString values.
