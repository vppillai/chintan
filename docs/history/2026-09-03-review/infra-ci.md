# Chintan — infrastructure, scripts and CI review

Scope: `infrastructure/template.yaml` (2,291 lines), `infrastructure/bootstrap.yaml` (1,110), `infrastructure/agent-policies/*`, all 17 scripts in `scripts/` plus `scripts/lib/common.sh` (4,964 lines of bash total), the three workflows, CODEOWNERS, both instance configs, and the two build-time frontend scripts. Every file was read in full. GitHub state was queried read-only with `gh` as `vppillai`. No AWS credentials exist on the Mac, so the AWS side was not queried; findings about AWS are from the templates and from what the deploy logs prove. Repo HEAD: `9aa4435`.

## Summary

The templates are unusually well-commented and most individual resources are configured sensibly: Retain on the pool, table and bucket; PITR and TTL on; public access blocked; SSE on; a DLQ with an alarm; a 14-day log retention; an account budget; a JWT authorizer on `$default`; per-route throttling. Shellcheck and cfn-lint are both clean. The bash library is careful (no `eval`, three-way probes, dry-run default).

The problems are at the seams, and they are the ones that matter for a one-user app whose only two assets are the user's data and the user's wallet:

1. **The human gate the README promises does not exist.** `main` has no branch protection, no rulesets, and the `production` environment has no required reviewers. The last deploy's production job started 3 seconds after staging finished. Every push to `main` deploys prod unattended.
2. **`deploy.sh` executes every change set in CI, including ones that replace the user pool, the table or the bucket.** Retain stops the old resource being deleted, but the stack swaps to a fresh empty one, staging is green (an empty pool works), and prod follows. The printed change set is decoration because nothing reads it before `--apply`.
3. **The permissions boundary does not close the escalation it exists to close.** Neither the agent's grant policy nor the GitHub role requires `iam:PermissionsBoundary` when creating roles, and the boundary itself does not deny unbounded role creation. A principal under the boundary can mint an unbounded `chintan-*` role, attach `AdministratorAccess`, pass it to Lambda and invoke it.
4. **Staging cannot sign in.** The Cognito callback is `/chintan/dev/` for every environment while the staging bundle redirects to `/chintan/dev-staging/`. The staging gate therefore proves only that `GET /v1/health` returns 200 — the exact check the template's own comments say missed a multi-day prod outage.
5. **Roughly two-thirds of the ~10k lines here defend against threats a one-user project does not have**, and some of it (CODEOWNERS, agent boundary drift checks, multi-instance matrix) is not enforced or never runs.

Honest minimum: one template (~1,000 lines without the branding blob and comments), one OIDC deploy role, branch protection on `main`, a required reviewer on `production`, a change-set replacement guard, four alarms and the budget. Details in "Over-engineering / deletable".

Shellcheck (`--severity=warning`): clean. cfn-lint 1.54.0: clean (with `W3037` suppressed template-wide in `bootstrap.yaml:16`).

---

## Critical

### C1. Production deploys are not gated by a human; `main` is unprotected

- **Where:** `README.md:53`, `README.md:112`, `README.md:318`; `scripts/setup.sh:162-167`; `.github/workflows/deploy-backend.yaml:196-207`.
- **What's wrong:** README says setup "creates the `production` environment **with you as a required reviewer**" and that deploys are "gated by an environment that requires a human". Reality (`gh api repos/vppillai/chintan/environments/production`): `protection_rules` contains only `branch_policy`; there is no `required_reviewers` rule. `gh api repos/vppillai/chintan/branches/main/protection` → 404 "Branch not protected"; `rulesets` → `[]`. In run 32077268445, "Staging chintan-dev-staging" completed 22:47:32Z and "Production chintan-dev-prod" started 22:47:35Z. Three production deployments (Aug 13, 15, 17) all ran from unprotected `main` with no approval.
- **Why it matters:** The whole deploy design (staging → smoke → *human* → prod) is described as the control that keeps a bad change out of the one stack that holds the user's notes. It is not in force. Anything that lands on `main` — a mistaken merge, a compromised GitHub session, a Dependabot-less floating action tag going bad — reaches prod in ~6 minutes with no one looking. It also makes the `production` environment's "protected branches only" policy vacuous, because no branch is protected.
- **Fix:** `gh api -X PUT repos/vppillai/chintan/environments/production --input -` with `reviewers: [{type: User, id: <your id>}]` (this is what `setup.sh:163-166` *intends*; re-run it or do it once by hand and verify with the GET). Protect `main`: require the CI status checks, require linear history, block force-push. For a solo repo do **not** require PR reviews (you cannot approve your own PR); the environment reviewer is the human gate. Then update `README.md:53/318` only once the GET proves it.

### C2. `deploy.sh` auto-executes change sets that replace stateful resources

- **Where:** `scripts/deploy.sh:504-521` (prints changes incl. `.Replacement`, then `confirm_apply "$APPLY"`); `scripts/ci-deploy-stack.sh:74` (`--apply` unconditional); `infrastructure/template.yaml:142-193` (UserPool), `625-771` (table), `862-1075` (bucket).
- **What's wrong:** `Replacement` is printed to the log and then ignored. In CI `APPLY=1` always. `UpdateReplacePolicy: Retain` on the pool/table/bucket means the *old* resource survives, but the stack re-points to a *new, empty* one. Triggers are ordinary edits: any change to `UserPool.Schema`, `UsernameConfiguration`, or `UserPoolName`; changing a GSI key schema or `TableName`; changing `BucketName`. The staging stack accepts the replacement happily (an empty pool signs in fine — see H4 for why even that would not be noticed), so the "gate" passes, then prod's pool is replaced. Tenant IDs are Cognito `sub`s, so every note in the table becomes unreachable through the app and the token-vault-sealed refresh tokens become useless. Recovery means hand-repointing the stack at the retained resources, which CloudFormation does not support without import gymnastics.
- **Why it matters:** This is the single most plausible total-data-loss path in the repo, and both of the controls that would catch it (a human reading the change set — C1 — and a mechanical check) are absent.
- **Fix:** In `deploy.sh`, after `describe-change-set`, fail if any `ResourceChange.Replacement == "True"` (or `"Conditional"`) for `AWS::Cognito::UserPool`, `AWS::DynamoDB::Table`, `AWS::S3::Bucket`, `AWS::Cognito::UserPoolClient`, unless an explicit `--allow-replacement <LogicalId>` is passed. Additionally set a stack policy denying `Update:Replace` on those logical IDs (`permissions.json` already grants `cloudformation:SetStackPolicy`; nothing uses it). Add `DeletionProtectionEnabled: true` to the table (M1).

---

## High

### H1. The permissions boundary does not prevent privilege escalation through role creation

- **Where:** `infrastructure/agent-policies/permissions.json` `IamOnProjectRolesOnly` (iam:CreateRole, PutRolePolicy, AttachRolePolicy, PutRolePermissionsBoundary on `role/chintan-*`, **no** `iam:PermissionsBoundary` condition, **no** `iam:PolicyARN` restriction) and `PassRoleToLambdaAndCfnOnly`; `infrastructure/agent-policies/policy-source.json:3-31` (ceiling includes `iam:*`, `lambda:*`), `167-190` (`DenyCredentialCreationAndPrivilegeEscalation` omits CreateRole/AttachRolePolicy/PutRolePolicy/PassRole); `infrastructure/bootstrap.yaml:993-1004` (GitHub role: CreateRole/PutRolePolicy via CloudFormation, no boundary condition); `infrastructure/agent-policies/README.md:17` claims the boundary is "required on every role the agent creates".
- **What's wrong:** A permissions boundary constrains the principal it is attached to, not the roles that principal creates. Under the boundary, `chintan-agent` may: `iam create-role --role-name chintan-x --tags Project=chintan` (satisfies `DenyCreateUntaggedResources`), `iam attach-role-policy --policy-arn arn:aws:iam::aws:policy/AdministratorAccess`, `lambda create-function --role chintan-x` (PassRole to lambda.amazonaws.com is allowed), `lambda invoke`. The function runs as an unbounded administrator. `iam:PutRolePermissionsBoundary` on `chintan-*` (permissions.json) also lets the agent swap the boundary on `chintan-lambda-*` roles for any policy. The same path is open to the GitHub role *if* `CFN_DEPLOY_ROLE_ARN` is unset (`deploy.sh:200-210` treats that as a warning, not an error), because `bootstrap.yaml:993-1004` grants `iam:CreateRole`/`PutRolePolicy` (inline `"Action":"*"` is not restricted) with only `aws:CalledVia=cloudformation`. Only `CfnDeployRole` (`bootstrap.yaml:545-568`) implements the delegation pattern correctly. `guardrails-check.sh:373-380` would notice an unbounded `chintan-*` role after the fact — but only when a human runs it (M3).
- **Why it matters:** The boundary is the justification for 1,100 lines of policy JSON, 560 lines of `bootstrap-agent.sh`, 415 lines of drift checker and the CloudTrail setup. It was built so that an AI agent holding credentials could not escalate. It can.
- **Fix:** Add one statement to `policy-source.json` `Shared` (so it lands in both boundary and deny): `Deny iam:CreateRole, iam:PutRolePolicy, iam:AttachRolePolicy, iam:PutRolePermissionsBoundary` on `*` with `StringNotEquals iam:PermissionsBoundary = arn:aws:iam::ACCOUNT_ID:policy/chintan-agent-boundary` (plus a second statement with `Null iam:PermissionsBoundary = true` for CreateRole, since the key is absent when no boundary is passed). Also deny `iam:AttachRolePolicy` where `iam:PolicyARN` is not `AWSLambdaBasicExecutionRole`. The boundary has 76 characters of headroom (`agent-policies/README.md:98`), so this requires deleting the two `DefenceInDepth*` statements the README itself says "do not work for most services here" (`README.md:49-54`), which frees ~1,400 characters. Make `CFN_DEPLOY_ROLE_ARN` mandatory in `deploy.sh`.

### H2. The Cognito callback URL is wrong for every non-prod environment; staging cannot sign in

- **Where:** `infrastructure/template.yaml:247-250` (`CallbackURLs: https://${PagesHost}/${RepoName}/${InstanceName}/`); `frontend/src/features/auth/oauth.ts:34-36` (`redirectUri = origin + BASE_URL`); `scripts/ci-build-site.sh:133` (`VITE_BASE=${PAGES_BASE}/${site_path}/`); `config/instances/dev-staging.yaml:14` (`site_path: dev-staging`); `scripts/list-instances.sh:129` (default site_path `<name>-<env>` for non-prod).
- **What's wrong:** For `chintan-dev-staging` the registered callback is `https://vppillai.github.io/chintan/dev/`; the staging bundle sends `redirect_uri=https://vppillai.github.io/chintan/dev-staging/`. Cognito rejects with `redirect_mismatch` before the login form renders (the frontend comment at `oauth.ts:9-14` describes precisely this failure). The template never receives `site_path`.
- **Why it matters:** Staging exists "so a template or Lambda change that fails on real AWS fails somewhere that has no user data" (`dev-staging.yaml:5-6`). Nobody can log into it, so nothing about auth, WebAuthn, notes, captures or the worker is ever exercised there by a human, and the automated smoke checks only `/v1/health` (H4). Staging currently validates "CloudFormation accepted the template and the API Lambda cold-starts". It also means a wrong callback would ship to prod unnoticed, because staging never tests the callback.
- **Fix:** Add a `SitePath` parameter (default `!Ref InstanceName`) and use it in `CallbackURLs`/`LogoutURLs`; pass `SitePath=${site_path}` from `ci-deploy-stack.sh` (it already has the matrix entry; add `SITE_PATH` to the env block in `deploy-backend.yaml:179-190`). Make the staging deploy's smoke test do the OAuth `authorize` round-trip at least to the login page (a `curl -I` of `/oauth2/authorize?...&redirect_uri=<site>` returning 200 rather than 400 `redirect_mismatch` is enough).

### H3. Teardown/cleanup will fail part-way, after destroying data

- **Where:** `scripts/cleanup-aws.sh:159-161` (empties bucket), `:172-183` (`update-user-pool --deletion-protection INACTIVE` alone), `:198-216` (delete table/bucket/pool directly); `scripts/clean-instance-orphans.sh:196-204` (the same call *with* the required `--auto-verified-attributes email --user-attribute-update-settings ...`, and a comment explaining the call is rejected without them); `infrastructure/agent-policies/policy-source.json:49-67` (`DenyIrreversibleDeletesOutsideCloudFormation` denies `s3:DeleteBucket`, `dynamodb:DeleteTable`, `cognito-idp:DeleteUserPool` to any bounded principal not calling via CloudFormation); `README.md:38` ("Everything afterwards runs as `chintan-agent`"), `README.md:200`.
- **What's wrong:** Two independent failures. (a) `cleanup-aws.sh:180` calls `update-user-pool` with only `--deletion-protection`; the pool has `UserAttributeUpdateSettings` (`template.yaml:155-157`), so per the sibling script's own comment Cognito rejects it ("All attributes in AttributesRequireVerificationBeforeUpdate must exist in AutoVerifiedAttributes"). Under `set -e` the script dies — *after* `empty_s3_bucket` has already deleted every object version. (b) Even if (a) were fixed, when run as `chintan-agent` (the README's instruction) the boundary denies the retained-resource deletes at `:201`, `:207`, `:213`; `clean-instance-orphans.sh:13-20` documents this and demands elevated credentials, `cleanup-aws.sh`/`teardown.sh` do not. `README.md:200` also still says teardown "schedules the token-vault CMK for deletion" — the CMK was removed from the template; `KMS_KEYS` at `cleanup-aws.sh:97` is always empty.
- **Why it matters:** A destructive script that stops half-way is worse than one that refuses: the recordings are gone, the stack, table and pool remain, and the next run's `stack_exists` check still passes so it will try again.
- **Fix:** Reuse the working `update-user-pool` invocation from `clean-instance-orphans.sh:201-204`; assert up front (`aws sts get-caller-identity`) that the caller is *not* `chintan-agent` or `chintan-github-actions`, mirroring `bootstrap-agent.sh:129-131`; delete the stack *before* emptying the bucket for the retained-resource path (the bucket is retained anyway) so a failure leaves data intact; drop the CMK code and the README sentence.

### H4. The smoke test is `GET /v1/health` and nothing else

- **Where:** `scripts/deploy.sh:595-602`; `infrastructure/template.yaml:1109-1128` (comment: "The deploy succeeded, /v1/health stayed 200, and the notes list was broken for days"); `template.yaml:1710-1723` (`GET /v1/health/ready` — a readiness probe that checks dependencies, public, **unused by the smoke**); `scripts/guardrails-check.sh:222-227`.
- **What's wrong:** The one thing the pipeline verifies after a deploy is that the API Lambda cold-starts and answers a route that touches no table, no index, no bucket, no queue, no SSM parameter, no Cognito. The template documents a multi-day prod outage that this smoke passed. A readiness endpoint exists specifically to answer "are the dependencies reachable" and the deploy does not call it. The worker and expiry functions are not exercised at all (a worker that fails every SQS event is invisible until the DLQ alarm — which staging has disabled, `dev-staging.yaml:42`).
- **Why it matters:** This is the gate between staging and prod. Its strength is what "staging first" buys you. Today it buys almost nothing.
- **Fix:** Smoke `GET /v1/health/ready` and require the dependency list to be all-green. For staging only, add an authenticated call: create a throwaway Cognito user via `admin-create-user`/`admin-set-user-password --permanent` and `admin-initiate-auth` (the GitHub role has `cognito-idp:*` read but needs `AdminInitiateAuth`/`AdminCreateUser` — grant them for the staging pool only, tag-conditioned), then `GET /v1/notes` (exercises gsi2) and `POST /v1/captures` (exercises the queue). Even the readiness change alone is a large improvement for five lines.

### H5. The OIDC trust is environment-scoped but the `build` environment has no protection at all

- **Where:** `infrastructure/bootstrap.yaml:641-651` (subjects `environment:build|staging|production`, no `ref:` pin); GitHub: `build` environment `protection_rules: []`, `deployment_branch_policy: null`; `.github/workflows/deploy-frontend.yaml:47` and `deploy-backend.yaml:116` (`environment: build`).
- **What's wrong:** The comment at `bootstrap.yaml:627-633` argues environment scoping is stronger than branch scoping because GitHub enforces environment rules. That is only true if the environment has rules. `build` has none, and it assumes the **same** `chintan-github-actions` role as `production` — a role with `cloudformation:CreateStack/UpdateStack/DeleteStack` on `chintan-*`, `s3:PutObject/DeleteObject` on the artifact bucket and `lambda:UpdateAlias` (the live traffic pointer). Any workflow file on any branch that declares `environment: build` gets full deploy power, no gate, no branch restriction. Given C1 (no protected branches), `staging`/`production`'s "protected branches only" adds nothing either.
- **Why it matters:** The threat model is "someone with push access to a non-main branch", which for a solo repo is a compromised GitHub token. The README's "role scoped to chintan-*, gated by an environment that requires a human" is satisfied by neither half.
- **Fix:** Either (a) split roles: a `chintan-github-build` role trusted only for `environment:build` with `s3:PutObject` on the artifact bucket + `cloudformation:DescribeStacks` (that is all `build`/frontend need), keeping deploy rights on a role trusted only for `staging`/`production`; or (b) add `custom_branch_policies: [main]` to `build` and, after C1, rely on branch protection. (a) is ten lines in `bootstrap.yaml` and removes the problem structurally.

---

## Medium

### M1. DynamoDB table lacks `DeletionProtectionEnabled`

- **Where:** `infrastructure/template.yaml:625-771`.
- **What's wrong:** `DeletionPolicy: Retain` protects against stack deletion; `DeletionProtectionEnabled: true` protects against `dynamodb delete-table` from any principal (the boundary's `DenyIrreversibleDeletesOutsideCloudFormation` only covers bounded principals; the human's admin credentials are not bounded). The user pool has the equivalent (`DeletionProtection: ACTIVE`, `template.yaml:150`); the table does not. Free.
- **Fix:** Add `DeletionProtectionEnabled: true` (and `bootstrap.yaml` already grants `dynamodb:UpdateTable`). Make `cleanup-aws.sh` flip it off explicitly, as it does for the pool.

### M2. `guardrails-check.sh` remote half and `check-boundary-drift.sh` remote half never run anywhere automatic

- **Where:** `.github/workflows/ci.yaml:130-156` (runs `--local-only` and `--self-test` only); `deploy-backend.yaml` (credentialed jobs never call either); `infrastructure/bootstrap.yaml:684-706` (`iam:GetPolicy/GetPolicyVersion` granted to the GitHub role *so that* `check-boundary-drift.sh` can run "from a credentialed job" — no job does).
- **What's wrong:** The boundary-attached, boundary-drift, unbounded-`chintan-*`-role, CloudTrail-enabled and branch-protection checks are the ones with teeth, and they run only when a human remembers to type `scripts/guardrails-check.sh`. The `--local-only` set that does run in CI checks: CODEOWNERS mentions four paths; `deploy.sh` contains the string `CI` (`guardrails-check.sh:144` — matches the word in any comment); a credential regex; `secret_ref` values (no config anywhere uses `secret_ref`, so it iterates over nothing — `guardrails-check.sh:170-210`); and GSI grant coverage (the one real check). The `--self-test` proves detection by deleting CODEOWNERS, the weakest check.
- **Fix:** Add one step to the staging deploy job: `scripts/guardrails-check.sh` (no flags) and `scripts/check-boundary-drift.sh`. It has the credentials and the grants already exist. Delete the `secret_ref` block.

### M3. Lambda execution role: two dead/misleading grants; `ReservedConcurrentExecutions: 50` justified for the wrong reason

- **Where:** `infrastructure/template.yaml:1192-1195` (`kms:Decrypt` on `arn:aws:kms:...:alias/aws/ssm`); `template.yaml:1279-1287`; `template.yaml:1787-1788` (`ThrottlingRateLimit: 50`, burst 100).
- **What's wrong:** (a) An alias ARN in `Resource` does not authorize `kms:Decrypt` (aliases are only addressable via `kms:ResourceAliases`); SSM decrypts under `aws/ssm` because the AWS-managed key policy allows any account principal via `kms:ViaService`. The statement is dead, and the comment above it (`:1188-1191`) explains something it does not do. (b) 5 → 50: the commit message and comment say 5 tripped when one page fired ~6 parallel requests. That is correct and 5 was too tight, but the comment frames 50 as "well above any realistic single-tenant burst while still bounded". Reserved concurrency is not what bounds spend on the four unauthenticated routes (`/v1/health`, `/v1/health/ready`, WebAuthn login options/login) — at ~100 ms per call, 50 rps of API Gateway throttle fits in concurrency ~5; the wallet cap is `ThrottlingRateLimit`, which at 50 rps × 24 h is ~4.3M invocations ≈ $4–5/day if someone points a loop at `/v1/health`. 50 reserved is harmless (57 × 2 stacks = 114 of the account's 1,000) but arbitrary; 10–15 carries the same argument, and the frontend firing six requests per page load is the actual fix.
- **Fix:** Delete the `kms:Decrypt` statement (or keep it with a corrected comment). Set API reserved concurrency to ~15, lower `ThrottlingRateLimit` to ~10 rps / burst 30 for a one-user API, and batch the capture-status polling in the client.

### M4. Alarm set is more noise than signal for one user

- **Where:** `template.yaml:1878-1898` (api-errors, threshold 0, `OKActions`), `1900-1919` (api-throttles, threshold 0), `1920-1941` (worker-errors before retries), `2085-2106` (api-5xx-rate, Average > 0.05 over 2 × 5 min), most with `OKActions`.
- **What's wrong:** One transient 500 sends: api-errors ALARM, api-5xx-rate ALARM (one failure in ten requests is 10%), then two OK emails five minutes later — four emails for one blip. `worker-errors` fires on the first attempt of a capture that `maxReceiveCount: 3` (`template.yaml:800`) will retry successfully; the DLQ alarm is the signal that a recording was actually lost. `api-throttles` at threshold 0 is what made 5 reserved look wrong (M3). The alarms that carry information for a solo user: `capture-dlq`, `expiry-dlq`, `provider-key-rejected`, `spend-cap-tripped`, and the budget.
- **Fix:** Drop `OKActions` everywhere (you will notice the app working); delete `api-5xx-rate` and `worker-errors`; raise `api-errors` to `Threshold: 5` per 5 min; `api-throttles` to ≥10. This also gets under the 10-alarm free tier with room for staging if you want it.

### M5. Supply chain: actions by major tag, `bun-version: latest`, golangci-lint from `HEAD`, no Dependabot, SHA pinning off

- **Where:** `.github/workflows/*.yaml` (`actions/checkout@v7`, `setup-go@v7`, `configure-aws-credentials@v6`, `oven-sh/setup-bun@v2`, `deploy-pages@v5`, ...); `deploy-frontend.yaml:71-73` and `ci.yaml:202-204` (`bun-version: latest`); `ci.yaml:66-70` (`curl .../golangci-lint/HEAD/install.sh | sh`); `ci.yaml:90` (`govulncheck@latest`); repo: `sha_pinning_required: false`, `allowed_actions: all`, no `.github/dependabot.yml`.
- **What's wrong:** `configure-aws-credentials@v6` and `setup-bun@v2` run in jobs holding `id-token: write` and the deploy role. A tag can be moved. `bun-version: latest` makes the production bundle non-reproducible: a Bun release can change the output or break the build with no repo change (the "cancelled" and re-dispatched frontend runs on Aug 17 hint at this class of flake). The `curl | sh` installer is only in a credential-less CI job, so its blast radius is PR results, not credentials.
- **Fix:** Pin the actions used in credentialed jobs to SHAs (at minimum `configure-aws-credentials`, `checkout`, `setup-bun` in `deploy-*.yaml`); pin `bun-version` to the version in `frontend/package.json`/`bun.lock`; add `dependabot.yml` for `github-actions`, `gomod` and `bun`; turn on `sha_pinning_required` in repo settings once pinned.

### M6. Stale/unused GitHub secrets and an account-ID-as-secret contortion

- **Where:** GitHub secrets: `AWS_ACCOUNT_ID`, `AWS_REGION`, `AWS_ROLE_ARN`, `ALARM_EMAIL`; `deploy-backend.yaml:36` (`AWS_REGION: us-west-2` hardcoded), `:150-153` (bucket name deliberately not a job output because GitHub scrubs outputs containing a secret); `scripts/setup.sh:143-146` sets `AWS_REGION` as a secret; `README.md:53`.
- **What's wrong:** `AWS_ROLE_ARN` is used by nothing. `AWS_REGION` secret is used by nothing (the workflows hardcode the region). `AWS_ACCOUNT_ID` is not secret — it is in the Cognito hosted-UI domain (`template.yaml:218`, baked into the public bundle) and in the S3 bucket name — but treating it as one forces the output-scrubbing workaround and would mask it in any log where you actually want to read it.
- **Fix:** Delete `AWS_ROLE_ARN` and `AWS_REGION`; move `AWS_ACCOUNT_ID` to a repository *variable*; update `setup.sh` and README accordingly.

### M7. MFA is optional for the only account that matters

- **Where:** `template.yaml:170-172` (`MfaConfiguration: OPTIONAL`, TOTP only); `template.yaml:110-121` (ESSENTIALS tier, no threat protection).
- **What's wrong:** A single-user app whose sign-in is a public hosted UI with password only unless the user opts in. The refresh token is 7 days and is what biometric unlock vaults; a phished password is a week of access to every note. `PreventUserExistenceErrors` and a 12-char policy are good but not a second factor.
- **Fix:** Either set `MfaConfiguration: ON` (the hosted UI handles the TOTP challenge, so no frontend work) or, at minimum, enrol TOTP on your own account today. The pool's `AccountRecoverySetting` is verified-email-only, so the email account is the real root of trust — say so in the README's Security section.

### M8. Cognito `UserPoolClient` and both DLQs have no Retain; the budget deploys in staging with no subscriber

- **Where:** `template.yaml:231-264` (client), `776-788`/`817-829` (DLQs), `2158-2201` (budget, `Condition` absent; `NotificationsWithSubscribers` conditional).
- **What's wrong:** A replacement of `UserPoolClient` invalidates every session and every vault-sealed refresh token and forces a frontend rebuild (the frontend workflow does follow automatically). A DLQ replacement discards the exact messages you care about. The staging budget is a budget with no notifications — it can never tell anyone anything and consumes one of the two free budgets.
- **Fix:** `UpdateReplacePolicy: Retain` on the client and DLQs (cheap insurance, and the C2 guard covers the rest). `Condition: HasAlarmEmail` on `MonthlyBudget`.

### M9. The boundary is 76 characters from the IAM size limit

- **Where:** `infrastructure/agent-policies/README.md:95-102`; `generate-policies.py:33-37`.
- **What's wrong:** The next service, or the fix for H1, does not fit. The README already names the way out (drop the `DefenceInDepth*` statements that Access Analyzer says do not apply to the services in use). This is the price of the two-copies design (boundary + deny share the same 300 lines).
- **Fix:** Do what `README.md:49-54` says: remove `DefenceInDepthDenyModifyOtherProjectTagged` and `DefenceInDepthDenyDeleteProtectedTagged` from the boundary (keep them in `deny.json` if wanted — the deny policy is not size-constrained the same way since it is one of several attached policies), then spend the space on H1.

---

## Low

- **L1. `OPTIONS /{proxy+}` route is dead.** `template.yaml:1726-1732`. `CorsConfiguration` on an HTTP API answers preflight before routing; the route is never hit. Harmless; delete it and the comment.
- **L2. Access log `correlationId` is `$context.requestId` twice.** `template.yaml:1773`. The header the client sends (`X-Correlation-Id`, exposed at `:1655`) is not logged, so the field name misleads. Either log `$context.requestId` once or drop the duplicate.
- **L3. `AWSLambdaBasicExecutionRole` grants `logs:CreateLogGroup/CreateLogStream/PutLogEvents` on `*`.** `template.yaml:1091-1093`. Replace with an inline statement on `${ApiLogGroup.Arn}:*` etc. The CfnDeployRole would then not need the `AttachRolePolicy` carve-out at `bootstrap.yaml:558-568`.
- **L4. No TLS-only bucket policy on the content bucket.** `template.yaml:862-1075`. Presigned URLs are https, so this is hygiene only; a `aws:SecureTransport=false` deny is six lines.
- **L5. Hosted-UI domain embeds the account ID.** `template.yaml:218`. Not a secret per AWS, but inconsistent with M6, and `chintan-${InstanceName}-${Environment}-${AWS::AccountId}` is longer than a Cognito prefix needs to be. Use a short hash if you care.
- **L6. `--help` implementations disagree.** `bootstrap-agent.sh:90` (`sed -n '2,60p'`, header runs to line 70), `guardrails-check.sh:56` (`2,40p`), `check-boundary-drift.sh:65` (`2,43p`) hardcode ranges; `common.sh:719-728` provides `usage_from_header` precisely because "a hardcoded range silently truncates --help". `build-lambda.sh:33` and `clean-instance-orphans.sh:62` re-implement it inline.
- **L7. Doc drift in the scripts.** `list-instances.sh:30` documents `retention_days` (no such parameter; the template moved to per-object tags); `ci-deploy-stack.sh:78` mentions `RetentionDays`; `README.md:200` and `cleanup-aws.sh:97,105,223-235`, `common.sh:627-640`, `clean-instance-orphans.sh:92,153-229`, `deploy.sh:276` all still handle a KMS CMK the template no longer creates.
- **L8. `guardrails-check.sh:144` `grep -q 'CI\|GITHUB_ACTIONS'`** matches the letters "CI" anywhere in `deploy.sh`, including comments. Grep for the actual guard line (`[ "${CI:-}" != "true" ]`).
- **L9. `clean-instance-orphans.sh` bypasses the library conventions.** It uses bare `aws` (`:82,86,135,182-210`) rather than `aws_cli`, sets its own `set -euo pipefail` (`:33`) and `APPLY=0` (`:42`) after sourcing `common.sh`, and `confirm_destructive ... || exit 2` (`:178`) — `confirm_destructive` already exits. Works, but it is the one script that would drift.
- **L10. `check-tokens.mjs` scans `.ts/.tsx` for hex colours with a regex that also matches CSS id selectors and any `#abc`-shaped string** (`frontend/scripts/check-tokens.mjs:24,89`). No false positives today; note for when one appears. `make-icons.mjs` is fine and self-contained.
- **L11. `dev.yaml`/`dev-staging.yaml` `pwa:` blocks are read by nothing** (`README.md:98` admits it). Delete them rather than document a no-op.
- **L12. `deploy-frontend.yaml` runs twice for a commit touching both trees** (`push` + `workflow_run`), relying on `cancel-in-progress: true` — the Aug 17 "cancelled" run. Fine, but the `push` trigger could exclude commits that also touch `backend/**` by making the `workflow_run` path the only one.

---

## Over-engineering / deletable

Line counts: templates 3,401; agent policies ~1,300 (JSON + generator + README); scripts 4,964; workflows 686; README 330. About 10,700 lines of infrastructure and operations text for an app with one user, versus ~0 lines of infrastructure the user interacts with after day one.

**What a single user runs more than once:** nothing in `scripts/` by hand. After setup the only repeat operator action is `chintanctl backup`. CI runs `build-lambda.sh`, `list-instances.sh`, `ci-deploy-stack.sh` → `deploy.sh`, `ci-build-site.sh`. `invite-user.sh` runs once per user (one user). `bootstrap.sh` is disaster recovery. Everything else is one-shot or a checker.

**Honest minimum** for a one-user personal app that still protects the data and the wallet:

| Keep | Why |
|---|---|
| `template.yaml` minus `ManagedLoginBranding` (~330 lines incl. 4 base64 blobs) and minus the retrospective comments | The resources are right. Add `DeletionProtectionEnabled`, the replacement guard (C2), `SitePath` (H2). |
| `bootstrap.yaml` with **one** OIDC role (deploy) and optionally the CFN service role | The CFN service role is the one piece of the IAM work that actually implements the delegation pattern correctly. |
| Budget + 4 alarms (`capture-dlq`, `expiry-dlq`, `provider-key-rejected`, `spend-cap-tripped`) | These are the wallet and the "your recording was lost" signals. |
| DynamoDB PITR, S3 versioning + 7-day noncurrent expiry, Retain on pool/table/bucket, `chintanctl backup` on a schedule | This is the actual data protection. |
| Branch protection on `main` (status checks) + required reviewer on `production` | The human gate — currently absent (C1). |
| CloudTrail | First management-event trail is free; keep it, it costs nothing and answers "what happened". |
| `deploy.sh`, `ci-deploy-stack.sh`, `ci-build-site.sh`, `build-lambda.sh`, `lib/common.sh`, `bootstrap.sh`, `invite-user.sh`, `cleanup-aws.sh` (fixed, H3) | The working set. |

| Delete or fold | Lines | Reason |
|---|---|---|
| `bootstrap-agent.sh`, `agent-policies/*`, `check-boundary-drift.sh`, remote half of `guardrails-check.sh`, `LambdaExecutionRole.PermissionsBoundary` (`template.yaml:1083`) | ~2,300 | Exists to constrain an AI agent holding AWS credentials. As built it does not (H1), it is at its size limit (M9), its drift checker never runs (M2), and three of its deny statements are documented as not working (`agent-policies/README.md:49-54`). If you keep giving an agent AWS credentials, keep a **20-line** boundary: deny `DenyRunawayCostServices`, deny outside region, deny delete on pool/table/bucket, deny unbounded `iam:CreateRole`/`AttachRolePolicy`. That is the part that protects the wallet; the rest is ceremony. |
| `CODEOWNERS` | 34 | Unenforceable for a solo repo: "require review from code owners" needs a *second* approver, so it would block every PR you open. `guardrails-check.sh:114-137` asserts its presence. |
| `config/instances/` matrix, `list-instances.sh`, `discover` job, matrix strategy | ~300 | One instance. Hardcode `dev`/`us-west-2` in the workflow; a fork edits one `env:` block. Keep only if "deploy your own" is a real goal for others. |
| Staging stack | ~1 stack | Defensible only if the smoke test becomes meaningful (H4) and sign-in works (H2). Today it proves CloudFormation accepted the template. With C2's replacement guard and a real reviewer on `production`, a change-set-plus-human gate gives most of the same protection with half the stacks and none of the callback/alarm/budget duplication. |
| `check-log-hygiene.sh` (138), `check-vite-env.sh` (116) | 254 | Both are good checks in the wrong language. The log-hygiene rule is a 30-line Go test over `go/ast`; the VITE contract is a 15-line vitest that imports `env.ts` and asserts the set of names against `ci-build-site.sh`. Each drops a CI job, a bash file and a self-test. |
| `clean-instance-orphans.sh` | 231 | Fold into `cleanup-aws.sh --orphans`. It duplicates the probe logic and the `update-user-pool` dance. |
| `setup.sh` | 201 | One-shot. Keep as a documented list of five commands in the README, or keep the script but make it *verify* the reviewer it claims to set. |
| `ManagedLoginBranding` | ~330 | Pure aesthetics for a page one person sees for two seconds a week. Your call; it is the largest single resource in the template. |
| `Api5xxRateAlarm`, `WorkerFunctionErrorsAlarm`, `OKActions` | ~60 | Noise (M4). |
| Staging `MonthlyBudget` | — | Budget with no subscribers (M8). |

CI cost: the full CI run is ~8.5 job-minutes (Playwright 2.3, Go race 1.9, the rest under a minute each); `Deploy Backend` ~5.7; `Deploy Frontend` ~0.7. Public repo, so it is free; the three python+pyyaml setups (`ci.yaml:98,134,185`) and the separate `instance-configs`/`vite-env`/`log-hygiene`/`guardrails` jobs could be one "repo checks" job for readability, not cost.

---

## GitHub reality vs README

| README claim | Where | Reality (`gh api`, 2026-09-03) |
|---|---|---|
| `setup.sh` "creates the `production` environment **with you as a required reviewer**" | `README.md:53`, `setup.sh:162-167` | `production.protection_rules = [branch_policy]` only. No `required_reviewers`. |
| Production stacks "wait for your approval on the `production` environment"; deploys "gated by an environment that requires a human" | `README.md:112`, `:318` | Run 32077268445: staging done 22:47:32Z, production started 22:47:35Z. No approval step. Three unattended prod deployments on Aug 13/15/17. |
| Environments deploy "only from a protected branch" | `setup.sh:167`, GitHub env policy `protected_branches: true` | No branch protection on `main` (404 "Branch not protected"), no rulesets. The policy is vacuous. |
| CODEOWNERS "requires review on" four paths | `README.md:268` | Not enforced: no branch protection, and a solo owner cannot satisfy code-owner review anyway. The README's own caveat (`:268`) is the operative sentence. |
| `setup.sh` sets `AWS_ACCOUNT_ID` and `AWS_REGION` secrets | `README.md:53` | Secrets present: `ALARM_EMAIL`, `AWS_ACCOUNT_ID`, `AWS_REGION`, `AWS_ROLE_ARN`. `AWS_REGION` and `AWS_ROLE_ARN` are read by no workflow (region is hardcoded at `deploy-backend.yaml:36`). Variable `CFN_DEPLOY_ROLE_ARN=arn:aws:iam::<account>:role/chintan-cfn-deploy` is set and used. |
| `build` environment exists so "gating prod does not gate compiling" | `bootstrap.yaml:643-647` | `build`: `protection_rules: []`, `deployment_branch_policy: null`. Same IAM role as production. |
| Pages "build from Actions" | `README.md:53` | True: `build_type: workflow`, `https_enforced: true`, public. |
| Default branch `main`; PRs merged via CI | — | True: `default_branch: main`; recent merges are PRs (#6, #7) with green CI. `allow_auto_merge: false`, `delete_branch_on_merge: false`. Stale branches: `feat/v1`, `feat/v2`, `archive/phase0-wip`, `chore/ci-node24-warnings`, `pre-rewrite/main-2026-08-07`. `ci.yaml:15` still triggers on `feat/v2`. |
| — | — | Actions settings: `allowed_actions: all`, `sha_pinning_required: false`, `default_workflow_permissions: read` (good), `can_approve_pull_request_reviews: false` (good). No `.github/dependabot.yml`. Sole collaborator: `vppillai`. |
| `bootstrap-agent.sh` closing note: "still outstanding: branch protection, required checks, CODEOWNERS enforcement, secret scanning" | `bootstrap-agent.sh:558` | Still outstanding. |

AWS side: no credentials on this machine; not verified. The deploy logs prove the stacks `chintan-dev-staging` and `chintan-dev-prod` exist and update cleanly; nothing here verifies that the deployed boundary matches the repo or that `chintan-*` roles all carry it (M2 is why).

---

## Tool output

**Linux box (`ubuntu@orb`, `~/temp/chintan` at `9aa4435`)**

```
$ shellcheck --version | head -2
ShellCheck - shell script analysis tool
version: 0.10.0
$ shellcheck --severity=warning scripts/*.sh scripts/lib/common.sh
(no output)  exit=0
$ shellcheck --severity=style scripts/*.sh scripts/lib/common.sh | grep -oE 'SC[0-9]+' | sort | uniq -c
      7 SC2016   (JMESPath backticks in single quotes — all annotated as intentional)
      6 SC2015   (A && B || C patterns)
      2 SC1091   (sourced file not followed)

$ uvx cfn-lint --version
cfn-lint 1.54.0
$ uvx cfn-lint infrastructure/*.yaml
(no output)  exit=0
```
Note: `bootstrap.yaml:7-16` suppresses `W3037` for the whole template (justified in the comment: `apigateway:TagResource` is real and cfn-lint's list is stale). `template.yaml` has no suppressions.

**GitHub (from the Mac, `gh` as `vppillai`)**

```
$ gh api repos/vppillai/chintan/branches/main/protection
{"message":"Branch not protected", ... "status":"404"}
$ gh api repos/vppillai/chintan/rulesets
[]
$ gh api repos/vppillai/chintan/environments   (abridged)
build        protection_rules: []                    deployment_branch_policy: null
github-pages protection_rules: [branch_policy]       custom_branch_policies: true
production   protection_rules: [branch_policy]       protected_branches: true   (NO required_reviewers)
staging      protection_rules: [branch_policy]       protected_branches: true
$ gh api repos/vppillai/chintan/actions/secrets --jq '.secrets[].name'
ALARM_EMAIL  AWS_ACCOUNT_ID  AWS_REGION  AWS_ROLE_ARN
$ gh api repos/vppillai/chintan/actions/variables
CFN_DEPLOY_ROLE_ARN=arn:aws:iam::<account>:role/chintan-cfn-deploy
$ gh api repos/vppillai/chintan/actions/permissions
{"enabled":true,"allowed_actions":"all","sha_pinning_required":false}
$ gh api repos/vppillai/chintan/actions/permissions/workflow
{"default_workflow_permissions":"read","can_approve_pull_request_reviews":false}
$ gh api repos/vppillai/chintan/pages
{"status":null,"build_type":"workflow","html_url":"https://vppillai.github.io/chintan/","public":true,"https_enforced":true,...}
$ gh run list -R vppillai/chintan -L 15
completed success   Deploy Frontend   main  workflow_run        32077719424   51s  2026-08-17T22:49:09Z
completed success   Deploy Backend    main  push                32077268445  5m52s 2026-08-17T22:43:15Z  (#7, reserved concurrency 5->50)
completed success   CI                main  push                32077268421  2m21s 2026-08-17T22:43:15Z
completed success   CI                fix/api-lambda-reserved-concurrency  pull_request  2m08s
completed success   Deploy Frontend   main  workflow_dispatch   32071171777  1m48s
completed cancelled Deploy Frontend   main  push                32071134964   40s
completed success   CI                main  push                32071134952  2m17s
completed success   CI                fix/transcript-empty-state-contradiction  pull_request  4m13s
completed success   Deploy Frontend   main  workflow_dispatch   32060784851   55s
completed success   Deploy Frontend   main  push                31861635906   50s  2026-08-15
completed success   CI                main  push                31861635905  2m18s
completed success   CI                feat/ux-live-feedback-batch  pull_request  2m26s
completed success   Deploy Frontend   main  push                31858207089   45s
completed success   CI                main  push                31858207070  2m34s
completed success   CI                fix/404-redirect-belongs-at-pages-root  pull_request  2m20s
$ gh api repos/vppillai/chintan/actions/runs/32077268445/jobs  (started/completed)
Discover instances            22:43:18Z  22:43:23Z
Test                          22:43:18Z  22:45:07Z
Build Lambda                  22:45:10Z  22:45:57Z
Staging chintan-dev-staging   22:46:00Z  22:47:32Z
Production chintan-dev-prod   22:47:35Z  22:49:06Z     <- 3 s after staging; no approval wait
$ gh api "repos/vppillai/chintan/deployments?environment=production&per_page=3"
2026-08-17T22:47:32Z main 9aa4435 production
2026-08-15T01:13:46Z main 0b0340e production
2026-08-13T21:10:13Z main 38af0f4 production
Job-minutes: CI 8.5 (Playwright 2.3, Go race 1.9, contract 0.9, frontend 0.9, golangci 0.9, others <=0.5);
             Deploy Backend 5.7; Deploy Frontend 0.7.
$ gh api repos/vppillai/chintan/contents/.github/dependabot.yml -> 404
$ gh api repos/vppillai/chintan/collaborators --jq '.[].login' -> vppillai
```

**AWS:** `aws sts get-caller-identity` on the Mac → no credentials. Not queried.
