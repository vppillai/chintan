# Chintan — live AWS audit (read-only)

Date: 2026-09-03 (UTC). Account <account>, region us-west-2.
Access: `ssh ubuntu@orb 'aws --profile chintan …'` → `arn:aws:sts::<account>:assumed-role/chintan-agent/chintan-agent`
(role `chintan-agent`, boundary `chintan-agent-boundary` v5, attached `chintan-agent-permissions` v1 + `chintan-agent-deny` v2).
Nothing was created, modified or deleted. No SSM values were read (`ssm:GetParameter` is explicitly denied to this role anyway).
The box also has a `[default]` profile (root login session) and a `[chintan-agent-cli]` profile; neither was used.

Repo HEAD: `9aa4435` (`v0.5.0-1-g9aa4435`, "Raise the API Lambda reserved concurrency from 5 to 50 (#7)").

---

## Key facts

| Topic | Fact |
|---|---|
| **Deployed version** | Both stacks run commit **`9aa4435d14e…`** = repo HEAD. Lambda code keys `function-9aa4435….zip` / `worker-9aa4435….zip`; every `live` alias points at the version whose description is that SHA (prod: api v14, worker v14, expiry v9; staging: api v20, worker v20, expiry v11). Deployed 2026-08-17 22:46Z (staging) → 22:48Z (prod). Reserved concurrency 50 for the API is live, so the HEAD commit is deployed. |
| **Usage volume** | **Idle.** Last Lambda activity: api 2026-08-18 18:01Z, expiry 2026-08-18 21:18Z, worker 2026-08-17 21:05Z (matches last S3 write 21:05:16Z). No Lambda log stream has an event after 2026-08-18; all Lambda log groups report 0 stored bytes inside the 14-day retention window. Six active days in total (Aug 8, 9, 13, 14, 15, 17) — a development/testing window, not daily or weekly use. Only 2 API Gateway access-log streams exist after Aug 18 (Aug 20 and Sep 3 03:25Z) and no Lambda ran, i.e. requests rejected at the gateway (probes/401s); content not readable by this role. |
| **Users** | Prod Cognito pool: **2 users** — 1 `CONFIRMED` (last modified 2026-08-14), 1 stuck in `FORCE_CHANGE_PASSWORD` since 2026-08-06 (temp password validity is 3 days → expired, never signed in). Staging pool: 0 users. |
| **Data volume** | Prod DynamoDB `chintan-dev-prod`: **72 items / 82 KB** (keys sampled: 37 CAPTURE#, 30 NOTE#, 5 SPEND#; gsi1 16 items, gsi2 29 items). Prod S3 `chintan-content-dev-prod-…`: **210 objects / 5.6 MB**, 1 tenant, 30 captures (32 audio files), 33 notes. Staging: 0 items, 0 objects. Orphan bucket `chintan-content-dev-<account>`: 107 objects / 5.9 MB (+170 versions, 34 delete markers). Lambda artifact bucket: 126 objects / 693 MB. CloudTrail bucket: 24,051 objects / 46.6 MB. |
| **Cost** | Account total (unblended): **Jun $0.014, Jul $0.001, Aug $0.355, Sep-to-date $0.010**. Aug breakdown: S3 $0.139, Cost Explorer API $0.09, KMS $0.048 (the removed CMK, ~1.5 days), Tax $0.03, SQS $0.028, CloudWatch $0.017, API GW $0.002, DynamoDB $0.001. Budgets: actual $0.01 MTD, forecast $0.26. Filtering CE by `Project=chintan` returns **nothing** because `Project`/`Instance`/`Environment` are **Inactive** cost-allocation tags. Provider (Groq/MiniMax) spend is outside AWS and not visible here. Note: each `ce get-cost-and-usage` call costs $0.01; this audit made 7 (~$0.07). |
| **Error rate** | Not measurable via metrics: `cloudwatch:GetMetricStatistics/GetMetricData/ListMetrics/DescribeAlarms` are denied to the agent role. Logs: 0 events in every `/aws/lambda/chintan-*` group for the last 14 days (retention), so 0 errors and 0 invocations in that window. Nothing to report before that either — events older than 14 days are gone. Alarm states and DLQ depths are unreadable (see Findings #5). |
| **Stacks** | `chintan-dev-prod` UPDATE_COMPLETE (51 resources), `chintan-dev-staging` UPDATE_COMPLETE (41 resources, no alarms / no budget subscriber by design), `chintan-bootstrap` UPDATE_COMPLETE (3 resources). Drift: NOT_CHECKED (`DetectStackDrift` denied). |

---

## 1. CloudFormation stacks

### chintan-dev-prod
- Status `UPDATE_COMPLETE`; created 2026-08-07 01:42Z; last update 2026-08-17 22:48Z (Lambda code only). Role `chintan-cfn-deploy`, `CAPABILITY_NAMED_IAM`, termination protection **off**.
- Tags: Project=chintan, Instance=dev, Environment=prod, Application=Chintan.
- Parameters: InstanceName=dev, Environment=prod, AllowedOrigin=https://vppillai.github.io, PagesHost=vppillai.github.io, RepoName=chintan, LambdaCodeBucket=chintan-lambda-<account>-us-west-2, LambdaCodeKey=function-9aa4435….zip, WorkerCodeKey=worker-9aa4435….zip, EnableAlarms=true, AlarmEmail=<gmail address>, MonthlyBudgetUSD=10, LogRetentionDays=14, DailySpendCapMicros=5000000, CognitoTier=ESSENTIALS, RefreshTokenValidityDays=7.
- Outputs: ApiEndpoint https://3kg2xg9khf.execute-api.us-west-2.amazonaws.com; UserPoolId us-west-2_mhEsaNtml; UserPoolClientId 42oasgfr7uf328b4bdheh2innf; UserPoolDomainName chintan-dev-prod-<account>; ContentBucketName chintan-content-dev-prod-<account>; TableName chintan-dev-prod; queues chintan-captures-dev-prod, chintan-captures-dlq-dev-prod, chintan-expiry-dlq-dev-prod; AlarmTopicArn …:chintan-alarms-dev-prod; live alias ARNs for api/worker.
- Resources (51): ApiGatewayV2 Api 1, Authorizer 1, Integration 1, Route 6, Stage 1; Budgets::Budget 1; CloudWatch::Alarm **10**; Cognito UserPool/Client/Domain/ManagedLoginBranding 4; DynamoDB::Table 1; IAM::Role 1; Lambda Function 3, Version 3, Alias 3, EventSourceMapping 2, Permission 1; Logs::LogGroup 4; S3::Bucket 1; SNS Topic/TopicPolicy/Subscription 3; SQS Queue 3, QueuePolicy 1. All CREATE/UPDATE_COMPLETE.

### chintan-dev-staging
- `UPDATE_COMPLETE`; created 2026-08-08; last update 2026-08-17 22:47Z. Same parameters except Environment=staging, EnableAlarms=false, AlarmEmail="", DailySpendCapMicros=2000000.
- Outputs: ApiEndpoint https://wztju5p4qh.execute-api.us-west-2.amazonaws.com; UserPoolId us-west-2_VU8RKsQaC; client 2o1rsulfmee3tevui1tgrul9ke; domain chintan-dev-staging-<account>; bucket chintan-content-dev-staging-<account>.
- Resources (41): as prod minus the 10 alarms and the SNS subscription. A `MonthlyBudget` **is** created (with no notifications).

### chintan-bootstrap
- `UPDATE_COMPLETE`; created 2026-08-07, updated 2026-08-09. Params: GitHubOrg=vppillai, GitHubRepo=chintan, ExistingOIDCProviderArn=…oidc-provider/token.actions.githubusercontent.com, ArtifactRetentionDays=30.
- Resources: IAM::Role `chintan-cfn-deploy`, IAM::Role `chintan-github-actions`, S3::Bucket `chintan-lambda-<account>-us-west-2`.

### Other stacks in the account (not chintan)
`passbook-kids-prod`, `passbook-eatout-prod`, `passbook-bootstrap` — a separate project; the agent's deny policy blocks all access to `passbook-*` (verified: DescribeTable on those tables → explicit deny).

Drift detection: `cloudformation:DetectStackResourceDrift` **denied** for all three stacks → skipped; stacks show `StackDriftStatus: NOT_CHECKED`.

---

## 2. Lambda functions (`chintan-*`)

| Function | Runtime / arch | Mem | Timeout | Reserved conc. | Code size | Last modified | live → version | Versions kept |
|---|---|---|---|---|---|---|---|---|
| chintan-api-dev-prod | provided.al2023 / arm64 | 512 | 29 s | 50 | 8,728,398 | 2026-08-17 22:48Z | 14 | 14 (+$LATEST) |
| chintan-worker-dev-prod | provided.al2023 / arm64 | 2048 | 900 s | 5 | 8,163,617 | 2026-08-17 22:48Z | 14 | 14 |
| chintan-expiry-dev-prod | provided.al2023 / arm64 | 512 | 300 s | 2 | 8,163,617 | 2026-08-17 22:48Z | 9 | 9 |
| chintan-api-dev-staging | provided.al2023 / arm64 | 512 | 29 s | 50 | 8,728,398 | 2026-08-17 22:46Z | 20 | 17 (v4–v20) |
| chintan-worker-dev-staging | provided.al2023 / arm64 | 2048 | 900 s | 5 | 8,163,617 | 2026-08-17 22:46Z | 20 | 17 |
| chintan-expiry-dev-staging | provided.al2023 / arm64 | 512 | 300 s | 2 | 8,163,617 | 2026-08-17 22:46Z | 11 | 11 |

- All match the template (512/29/50, 2048/900/5, 512/300/2). Handler `bootstrap`, X-Ray tracing Active, Text log format, no layers, ephemeral storage 512 MB, no DLQ/EventInvokeConfig on the functions (DLQs are on SQS). Description empty; **the deployed commit is carried in the published version descriptions** (each version's description is the git SHA) and in the stack parameters.
- Env var **names** (api): TABLE_NAME, CONTENT_BUCKET, ALLOWED_ORIGIN, LLM_BASE_URL, LLM_MODEL, GROQ_API_KEY_PATH, LLM_API_KEY_PATH, USER_POOL_ID, USER_POOL_CLIENT_ID, TOKEN_VAULT_KEY_PATH, WEBAUTHN_RP_DISPLAY_NAME, CAPTURE_QUEUE_URL, DAILY_SPEND_CAP_MICROS. Worker/expiry: same minus TOKEN_VAULT_KEY_PATH. Matches template.
- Worker and expiry share an identical code SHA256 (`iyX3Uz…`) — same binary, different entrypoints/env (by design; template says worker built from backend/cmd/worker).
- Resource policy: only the `live` alias of the api function carries a policy (apigateway invoke from `execute-api:…:3kg2xg9khf/*/*`). Unqualified function has none — correct.
- Account: 8 functions total (6 chintan + 2 passbook), TotalCodeSize 749 MB (of 75 GB), UnreservedConcurrentExecutions 876 of 1000 (chintan reserves 57 per env = 114).
- Tags on all: Project/Instance/Environment/Application + CFN tags.
- `lambda:ListEventSourceMappings` **denied**; the two mappings per stack (SQS capture queue → worker, DynamoDB stream → expiry) are confirmed present via CFN (`CREATE_COMPLETE`, physical ids `7ef27d17…`, etc.) and the tagging API.

---

## 3. CloudWatch Logs

| Log group | Retention | Stored bytes | Streams | First event | Last event |
|---|---|---|---|---|---|
| /aws/lambda/chintan-api-dev-prod | 14 d | 0 | 50+ | 2026-08-07 | 2026-08-18 18:01Z |
| /aws/lambda/chintan-worker-dev-prod | 14 d | 0 | 16 | 2026-08-08 | 2026-08-17 21:05Z |
| /aws/lambda/chintan-expiry-dev-prod | 14 d | 0 | 12 | 2026-08-09 | 2026-08-18 21:18Z |
| /aws/lambda/chintan-api-dev-staging | 14 d | 0 | 20 | 2026-08-08 | 2026-08-17 22:47Z (deploy smoke test) |
| /aws/lambda/chintan-worker-dev-staging | 14 d | 0 | 5 | 2026-08-08 | 2026-08-08 18:11Z |
| /aws/lambda/chintan-expiry-dev-staging | 14 d | 0 | 0 | – | never invoked |
| /aws/apigateway/chintan-dev-prod | 14 d | 920 | 50+ | 2026-08-08 | **2026-09-03 03:25Z** |
| /aws/apigateway/chintan-dev-staging | 14 d | 0 | 21 | 2026-08-08 | 2026-08-17 22:47Z |

- All retention = 14 days, matching `LogRetentionDays`. No leftover log groups: the only non-chintan groups are `passbook-*`.
- `filter-log-events` over the last 30 days on every Lambda group with patterns `ERROR`, `{ $.level = "error" }`, `level=error`, `panic`, `WARN`, `Task timed out`, `Runtime.`, `INIT_REPORT`, `REPORT`: **0 events** for every pattern and every group (the whole 30-day window is empty because the last events, Aug 17–18, have aged out of the 14-day retention). So there are no error messages to summarise.
- API Gateway access-log groups: `logs:FilterLogEvents`/`GetLogEvents` are **denied** for `/aws/apigateway/*` (the agent policy only covers `/aws/lambda/chintan-*`). Stream names show gateway traffic on Aug 20 12:56Z and Sep 3 03:25Z with no corresponding Lambda invocation → requests rejected before integration (JWT/404/OPTIONS).

---

## 4. Invocation / API metrics

`cloudwatch:GetMetricStatistics`, `GetMetricData`, `ListMetrics`, `DescribeAlarms`, `DescribeAlarmsForMetric` are all **denied** to the agent role, so Invocations/Errors/Duration/Throttles and API 4xx/5xx counts could not be pulled.

Usage was reconstructed from log streams and object timestamps instead:
- Prod S3 writes by day: Aug 8 (38 objects), Aug 9 (82 — bulk copy from the old bucket), Aug 13 (16), Aug 14 (40), Aug 15 (26), Aug 17 (8). Nothing since.
- Prod worker log streams: Aug 9, 13, 14, 15, 17 (16 streams total ≈ 16 cold starts). API log streams: last on Aug 18.
- Verdict: the app was used on ~6 days during development (Aug 7–18) and has been **idle for 16 days**. Not daily, not weekly.

API Gateway configuration (readable): both HTTP APIs, `$default` stage auto-deploy, throttling 50 rps / burst 100, detailed metrics off, access logging on. Routes: `GET /v1/health`, `GET /v1/health/ready`, `OPTIONS /{proxy+}`, `POST /v1/auth/webauthn/login`, `POST /v1/auth/webauthn/login/options` (no auth) and `$default` → JWT authorizer (issuer = the stack's Cognito pool, audience = its client). CORS: single origin https://vppillai.github.io, credentials allowed, headers authorization/content-type/idempotency-key/x-correlation-id/x-amz-*, expose x-correlation-id, max-age 86400.

---

## 5. SQS

`sqs:ListQueues` and `sqs:GetQueueAttributes` are **denied**, so live depth/visibility/redrive could not be read. From CFN (all `CREATE_COMPLETE`) and the tagging API the six queues exist: `chintan-captures-{dev-prod,dev-staging}`, `chintan-captures-dlq-…`, `chintan-expiry-dlq-…`. Template values: capture queue VisibilityTimeout 960 s (> worker timeout 900), MessageRetentionPeriod 14 d, redrive to captures-dlq with maxReceiveCount 3; DLQs retain 14 d. **DLQ depth: unknown** — the `CaptureDLQDepthAlarm`/`ExpiryDLQDepthAlarm` exist in prod (CFN) but their state is unreadable. Given zero worker activity since Aug 17 and no worker errors in the logs that still exist, a non-empty DLQ is unlikely but unverified.

---

## 6. DynamoDB

| | chintan-dev-prod | chintan-dev-staging |
|---|---|---|
| Items / size | 72 / 83,899 B | 0 / 0 |
| Billing | PAY_PER_REQUEST | PAY_PER_REQUEST |
| Keys | pk (HASH), sk (RANGE) | same |
| GSIs | gsi1 (gsi1pk/gsi1sk, INCLUDE, 16 items), gsi2 (gsi2pk/gsi2sk, INCLUDE, 29 items) | same, empty |
| Stream | NEW_AND_OLD_IMAGES | same |
| TTL | ENABLED on `ttl` | ENABLED on `ttl` |
| PITR | ENABLED, 35 d (earliest restore 2026-08-07) | ENABLED, 35 d |
| Deletion protection | **false** | false |
| SSE | default (AWS-owned key) | default |
| Tags | Project/Instance/Environment/Application | same |

Key shape (keys-only scan of prod, 72 rows): `USER#…` partition with `CAPTURE#` (37), `NOTE#` (30), `SPEND#` (5) sort keys. One tenant.

---

## 7. S3 buckets (`chintan-*`)

| Bucket | In stack | Objects / bytes | Versions / delete markers | Versioning | Lifecycle | PAB | Encryption | Last write |
|---|---|---|---|---|---|---|---|---|
| chintan-content-dev-prod-<account> | dev-prod ContentBucket | 210 / 5.6 MB | 210 / 0 | Enabled | 7 rules: ExpireCaptureAudio{7,30,90,365} (prefix tenants/ + tags chintan-artifact=capture-audio & chintan-retention=N & chintan-processed=true), ExpireNoncurrentVersions 7 d, AbortStaleMultipartUploads 7 d, ExpireDeleteMarkers | all 4 true | SSE-S3 AES256 | 2026-08-17 21:05Z |
| chintan-content-dev-staging-<account> | dev-staging ContentBucket | 0 / 0 | 0 / 0 | Enabled | same 7 rules | all true | AES256 | never |
| **chintan-content-dev-<account>** | **none (orphan)** | 107 / 5.9 MB | 170 / 34 | Enabled | **none** | all true | AES256 | 2026-08-08 02:49Z |
| chintan-lambda-<account>-us-west-2 | bootstrap | 126 / 693 MB (83 zips, 43 templates) | 126 / 0 | Enabled | ExpireOldArtifacts 30 d (+noncurrent 1 d), CleanupExpiredDeleteMarkers, AbortStaleMultipartUploads 7 d | all true | AES256 | 2026-08-17 22:47Z |
| chintan-cloudtrail-<account>-us-west-2 | none (created by bootstrap-agent.sh, by design) | 24,051 / 46.6 MB | 24,051 / 0 | not enabled | ExpireTrailObjects 400 d | all true | AES256 | continuous |

- No bucket policies on the content/lambda buckets; the CloudTrail bucket policy allows only the trail to write and **explicitly denies `chintan-agent`** Put/Delete/PutBucketPolicy/DeleteBucket. Object ownership BucketOwnerEnforced everywhere. No CORS, no access logging.
- Other buckets in the account: `passbook-lambda-…`, two `appstream*` buckets in us-east-2 (April 2026) — not chintan.
- Object tags on prod audio (32 files): 16 uploaded natively carry `chintan-artifact=capture-audio` + `chintan-processed=true`; the 16 copied in on Aug 9 carry only `chintan-processed=true`. **None carries `chintan-retention`** → no expiry rule matches any audio; everything is kept indefinitely. This is what `retention_days = 0` (the default) means per the code and template comments, so it is by design for this user, but see Findings #6.

---

## 8. Cognito

Prod pool `us-west-2_mhEsaNtml` (chintan-dev-prod), tier ESSENTIALS, deletion protection ACTIVE, created 2026-08-07:
- Users: 2 (1 CONFIRMED, last modified 2026-08-14; 1 FORCE_CHANGE_PASSWORD since 2026-08-06, never signed in; temp-password validity is 3 days so that invite is dead).
- MFA: OPTIONAL; software TOTP enabled; WebAuthn passkeys NOT enabled (SignInPolicy.AllowedFirstAuthFactors is PASSWORD only; verified with describe-user-pool). Password policy: min 12, upper/lower/number/symbol required. Recovery: verified_email. Self sign-up **allowed** (`AllowAdminCreateUserOnly=false`) — note: the template governs this; invite flow uses admin-create.
- Domain: `chintan-dev-prod-<account>` (CloudFront dpp0gtxikpq3y.cloudfront.net), ManagedLoginVersion 2, branding present (4 assets, not Cognito defaults) — consistent with the README's "the two go together".
- App client `chintan-dev-prod-client` (42oasgfr7uf328b4bdheh2innf): no secret; auth flows ALLOW_USER_SRP_AUTH + ALLOW_REFRESH_TOKEN_AUTH; OAuth code flow, scopes email/openid/profile, provider COGNITO; callback & logout `https://vppillai.github.io/chintan/dev/`; token validity access 60 min, id 60 min, refresh 7 days; PreventUserExistenceErrors ENABLED; token revocation enabled; auth session validity 3 min.

Staging pool `us-west-2_VU8RKsQaC`: 0 users, same MFA/tier/deletion protection, domain v2 ACTIVE, client callback/logout **also `https://vppillai.github.io/chintan/dev/`** (see Findings #3).

---

## 9. Alarms, SNS, Budgets

- CloudWatch alarms: `DescribeAlarms` **denied**. CFN confirms the 10 prod alarms exist (`api-function-errors`, `api-function-throttles`, `worker-function-errors`, `worker-function-throttles`, `capture-dlq-depth`, `expiry-dlq-depth`, `provider-key-rejected`, `provider-rate-limited`, `api-5xx-rate`, `spend-cap-tripped`) and the tagging API sees them tagged Project=chintan; staging has none (EnableAlarms=false). **State and last state change: unreadable.**
- SNS: `ListTopics`/`GetTopicAttributes`/`ListSubscriptionsByTopic` **denied**. Topics `chintan-alarms-dev-prod` and `chintan-alarms-dev-staging` exist (CFN + tagging API); prod has one `AWS::SNS::Subscription` (email, `<redacted>@gmail.com` per the stack parameter); staging has no subscription. Confirmation status unknown.
- Budgets (account-scoped, all COST/MONTHLY/$10, actual $0.01, forecast $0.261):
  1. `Master Budget ` — manually created, unfiltered (not in any stack).
  2. `MonthlyBudget-us-west-2-1786400221000-eSrjDVzdOtwZ` — staging stack; **no notifications**.
  3. `MonthlyBudget-us-west-2-1786655487238-sUfB3uduGsLE` — prod stack; ACTUAL > 80% and FORECASTED > 100%, subscriber `<redacted>@gmail.com`, both notifications OK.
  None has a cost filter (consistent with the README: "the budget is account-scoped").

---

## 10. IAM and CloudTrail

Roles `chintan-*` (all MaxSessionDuration 3600):
| Role | Boundary | Trust | Policies | Last used |
|---|---|---|---|---|
| chintan-agent | chintan-agent-boundary | only `user/chintan-agent-cli` may `sts:AssumeRole` | managed: chintan-agent-permissions, chintan-agent-deny | reported 2026-08-08 (IAM last-used lags; it is in use now) |
| chintan-github-actions | **none** | OIDC token.actions.githubusercontent.com, aud=sts.amazonaws.com, sub ∈ repo:vppillai/chintan:environment:{build,staging,production} (+ `repo:vppillai@*/chintan@*` variants) | inline ChintanDeploymentPolicy (CFN on chintan-* stacks; most mutating actions require `aws:CalledVia=cloudformation`; PassRole to lambda/cloudformation; `ssm:GetParameter` on /chintan/*) | – |
| chintan-cfn-deploy | **none** | cloudformation.amazonaws.com, SourceAccount condition | inline ChintanStackResources (chintan-* scoped; `iam:CreateRole/PutRolePolicy/AttachRolePolicy` require `iam:PermissionsBoundary = chintan-agent-boundary`; AttachRolePolicy limited to AWSLambdaBasicExecutionRole; explicit deny on touching chintan-agent*/chintan-github-actions roles and policies) | 2026-08-17 22:48Z |
| chintan-lambda-dev-prod | chintan-agent-boundary | lambda.amazonaws.com | AWSLambdaBasicExecutionRole + inline CaptureQueueAccess, CognitoRefresh, DynamoDBAccess (table + index + stream), S3Access (own bucket, incl. Put/GetObjectTagging), SSMAccess (GetParameter on the 3 /chintan/dev/* params; kms:Decrypt on `alias/aws/ssm`), XRayTracing | 2026-09-03 13:34Z (health/ready probes or X-Ray) |
| chintan-lambda-dev-staging | chintan-agent-boundary | same | same | 2026-09-03 13:28Z |

Customer-managed policies `chintan-*`: `chintan-agent-permissions` v1 (attached 1), `chintan-agent-deny` v2 (attached 1), `chintan-agent-boundary` v5 (attachment count 0 — used only as a boundary). Boundary/deny statements: ceiling of allowed services; deny all on `passbook-*`; deny irreversible deletes outside CloudFormation; deny creating untagged (Project≠chintan) resources; deny modifying non-chintan-tagged resources; deny runaway-cost services (ec2, rds, sagemaker, …); deny credential creation/privilege escalation; deny tampering with own guardrails and the guardrail policies; deny disabling CloudTrail; deny writing to the audit bucket; deny reading/decrypting `/chintan/*` SSM values unless principal is `chintan-lambda-*`; deny deleting `Protected=true` resources; deny assuming any other role; deny everything outside us-west-2 except global services.

IAM users: `iam:ListUsers`/`GetUser`/`ListAccessKeys` **denied**; the trust policy proves `user/chintan-agent-cli` exists. OIDC provider `token.actions.githubusercontent.com` present (shared with passbook).

CloudTrail `chintan-trail`: multi-region, home us-west-2, global service events on, log-file validation on, **IsLogging true**, latest delivery 2026-09-03 13:39Z, latest digest 13:22Z, started 2026-08-07 01:18Z. `GetEventSelectors` denied. Bucket policy denies the agent role write/delete.

SSM parameters (names/metadata only): `/chintan/dev/groq_api_key` (SecureString v1, 2026-08-07), `/chintan/dev/llm_api_key` (SecureString v1, 2026-08-07), `/chintan/dev/token_vault_key` (SecureString v1, 2026-08-08). No other parameters in the account. KMS: `ListAliases` denied (README says the CMK was removed; KMS billed $0.048 in Aug and $0 in Sep, consistent with a key deleted early Aug).

---

## 11. Cost (Cost Explorer, unblended, account-wide)

| Month | Total | Notable lines |
|---|---|---|
| 2026-06 | $0.0137 | CE $0.01, S3 $0.003, API GW, DynamoDB |
| 2026-07 | $0.0010 | S3, API GW, DynamoDB |
| 2026-08 | $0.3553 | S3 $0.139, CE $0.09, KMS $0.048, Tax $0.03, SQS $0.028, CloudWatch $0.017, API GW $0.002, DynamoDB $0.001, IoT $0.00000375 |
| 2026-09 (to 3rd) | $0.0099 | S3 $0.0099, DynamoDB $0.000001 |

- `--filter Tags Project=chintan` → **empty for every month** (cost-allocation tags `Project`, `Instance`, `Environment` are all `Inactive`; `LastUsedDate` 2026-09-01, `Project` last updated 2026-08-06). Grouping by tag shows only `Project$` (untagged) plus `Project$voicenotes` $0.005 in Aug (a v1 leftover that no longer exists — the tagging API finds nothing tagged voicenotes today).
- Budgets show actual $0.01 MTD and forecast $0.26 for September.
- The README's "$0.00 idle, cents under use" claim holds for AWS; the only fixed-looking costs in Aug were CE API calls ($0.01 each) and the short-lived KMS key.

---

## 12. Findings (ranked)

1. **The app is not in use.** Zero Lambda invocations since 2026-08-18; 6 active days total, all in the Aug 7–18 development window; one real user; the second invited user has been in `FORCE_CHANGE_PASSWORD` for 4 weeks with an expired 3-day temporary password (re-invite with `scripts/invite-user.sh` or delete the user). Everything else below is secondary to this.

2. **Orphaned bucket with a copy of user data: `chintan-content-dev-<account>`.** Created 2026-08-07 by an earlier revision of the stack (before the bucket name gained the `-prod` suffix on 2026-08-15), retained on replacement, not referenced by any stack. Holds 107 objects / 5.9 MB of audio, transcripts and notes (170 versions + 34 delete markers), versioning on, **no lifecycle**, no `chintan-*` object tags. Prod has copies of this content (82 objects written 2026-08-09). `teardown.sh` would only report it. Confirm the Aug 9 copy is complete (e.g. `chintanctl reconcile`) and then empty and delete it by hand — it is the only place in the account where user content sits with no retention or expiry controls.

3. **Staging Cognito callback/logout URL is wrong.** `UserPoolClient.CallbackURLs` is `!Sub 'https://${PagesHost}/${RepoName}/${InstanceName}/'` → both pools use `https://vppillai.github.io/chintan/dev/`, but the staging bundle is published at `/chintan/dev-staging/` (`site_path`). `frontend/src/features/auth/oauth.ts` builds `redirect_uri` from the page location and Cognito matches it exactly, so the hosted sign-in on the staging site would fail with `redirect_mismatch`. Untested so far because staging has 0 users and the smoke test hits `/v1/health`. Fix: pass a `SitePath` parameter into the template.

4. **Cost-allocation tags are inactive**, so per-project cost reporting does not work: `Project`, `Instance`, `Environment` all show `Status: Inactive` and a `Project=chintan` CE filter returns nothing. Activating them is an admin-only Billing-console action (`ce:UpdateCostAllocationTagsStatus`), takes ~24 h, and does not backfill. Until then the only chintan cost view is "the whole account minus passbook".

5. **The agent role cannot see operational health.** Denied: `cloudwatch:GetMetricStatistics/GetMetricData/ListMetrics/DescribeAlarms`, `sns:*`, `sqs:ListQueues/GetQueueAttributes`, `lambda:ListEventSourceMappings`, `logs:FilterLogEvents/GetLogEvents` on `/aws/apigateway/*`, `cloudformation:DetectStackDrift`, `cloudtrail:GetEventSelectors`, `kms:ListAliases`, `iam:ListUsers`. The boundary allows all of these; `chintan-agent-permissions` v1 simply does not grant them. Consequence: alarm states, DLQ depth, error rates and drift cannot be verified from this seat. If the agent is meant to audit as well as deploy, add a read-only statement (Describe*/Get*/List* on cloudwatch, sns, sqs, lambda ESMs, logs for `/aws/apigateway/chintan-*`, cloudformation:DetectStackDrift/DescribeStackDriftDetectionStatus/DescribeStackResourceDrifts).

6. **Audio is retained indefinitely and partly untagged.** No prod audio object carries `chintan-retention`, so none of the four expiry rules can ever match — by design while the user's `retention_days` is 0 (the default). But the 16 audio objects bulk-copied on Aug 9 also lack `chintan-artifact=capture-audio`, so they would escape expiry even after the user sets a retention; the copy did not preserve upload tags. Either re-tag those 16 or accept that pre-migration audio is permanent.

7. **Three overlapping account-wide $10 budgets.** `Master Budget ` (manual, outside IaC), the staging stack's budget (no notifications, because AlarmEmail is empty — it does nothing) and the prod stack's budget. Budgets without actions are free, so this is clutter rather than cost, but the README's "two stacks subscribing one address send two emails" concern is already handled by leaving staging unsubscribed; making the staging `MonthlyBudget` conditional on `HasAlarmEmail` would remove the dead one, and `Master Budget ` duplicates the prod budget.

8. **No deletion protection on the prod DynamoDB table and no stack termination protection.** Cognito has `DeletionProtection: ACTIVE`; the table (the only index of the notes; PITR does not survive `DeleteTable`) and the S3 bucket rely on `DeletionPolicy: Retain` plus the boundary's "no deletes outside CloudFormation" rule. `DeletionProtectionEnabled: true` on the prod table is a one-line, free hardening; teardown.sh already has to flip Cognito's flag and could flip this one too.

9. **Published Lambda versions accumulate** (prod 14/14/9, staging 17/17/11 ≈ 82 versions × ~8.5 MB; account code storage 749 MB of 75 GB). Free at this scale, but `deploy.sh` could keep the last N to make rollback lists readable. The artifact bucket is fine: 30-day expiry, 83 zips / 43 templates, 693 MB, the oldest (Aug 8) expire from Sep 7.

10. **Reserved concurrency is generous for a single-user app**: 50 (api) + 5 + 2 per environment = 114 of the account's 1000, with staging permanently reserving 57 it will never use. No cost, but it is the HEAD commit's change and worth a second look once real usage exists (the README's own logic for staging alarms — "nobody is there" — applies here too).

11. **Minor IAM nit**: `chintan-lambda-*` `SSMAccess` grants `kms:Decrypt` on `arn:aws:kms:…:alias/aws/ssm`. KMS does not authorise on alias ARNs; decryption works because the AWS-managed `aws/ssm` key policy allows account principals via `kms:ViaService`. Harmless but misleading — either use the key ARN or drop the statement.

12. **Cognito self sign-up is allowed** (`AllowAdminCreateUserOnly=false`) on both pools while the app is invite-only per the README. The hosted UI may show a sign-up path; new sign-ups would still need email verification and would get an empty tenant. Consider `AdminCreateUserConfig.AllowAdminCreateUserOnly: true`.

13. Informational: the account also hosts `passbook-*` (2 Lambdas, 2 tables, 2 HTTP APIs, 1 bucket, 1 GitHub Actions role) and two AppStream buckets in us-east-2; `chintan-agent-deny` correctly blocks the former. Cost Explorer API calls are $0.01 each ($0.09 in Aug; ~$0.07 from this audit). The `[default]` root-session profile exists on the `orb` box and was not used. IAM `RoleLastUsed` for `chintan-agent` reads 2026-08-08 despite current use — a reporting lag, not a fault.

### What looks right
All three stacks UPDATE_COMPLETE on the same commit as repo HEAD with `live` aliases on the matching versions; every bucket has PAB on all four, SSE-S3, versioning and (except the orphan) lifecycle rules; log retention 14 d everywhere with no stray groups; PITR and TTL on both tables; JWT authorizer on `$default` with only health/webauthn/OPTIONS open; single-origin CORS; API throttling 50/100; Cognito with 12-char passwords, TOTP + passkeys, token revocation, user-existence errors hidden; CloudTrail multi-region with validation and an agent-proof bucket; secrets only as SecureStrings with the agent role denied `GetParameter`; deploy roles require the permissions boundary on any role they create.
