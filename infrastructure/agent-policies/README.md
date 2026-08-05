# Agent IAM policy documents

The permissions boundary, deny, grant, and trust documents applied by
`scripts/bootstrap-agent.sh` (§9.5).

**These are files rather than heredocs inside the script for two reasons.** They are
diffable, so a change to a guardrail appears in review — this directory is
CODEOWNERS-protected (§9.6). And `guardrails-check.sh` can compare what is deployed
against them, which is how a silently-altered guardrail is detected (§9.8).

`ACCOUNT_ID` and `REGION` are placeholders, substituted at apply time.

| File | Role |
|---|---|
| `boundary.json` | The permissions **ceiling**. Attached to the agent role *and* the CLI user, and required on every role the agent creates, so privilege cannot escalate through a Lambda execution role (G-046). |
| `permissions.json` | What the agent may actually do. Effective permission is this **intersected** with the boundary. |
| `deny.json` | Explicit denies. Override any allow. |
| `trust.json` | Who may assume the agent role. Rewritten by the bootstrap to name the CLI user specifically. |

## What actually enforces what

This matters more than the file list, because two of the controls in §9.5 do not work
as written and the remainder carry the weight.

**Resource-ARN scoping is the primary control.** Every statement in
`permissions.json` is scoped to `voicenotes-*` ARNs in the deploy region. ARN
conditions are supported by every service, always. This is what actually confines the
agent.

**Naming-prefix denies are the cross-project control.** `deny.json` denies all actions
against `passbook-*` ARNs by name. Also always supported.

**Tag-based denies are defence in depth only — they do not work for most services
here.** IAM Access Analyzer reports `aws:ResourceTag/Project` as unsupported for
authorization on cloudformation, cognito-idp, dynamodb, events, iam, lambda, logs,
resource-groups, s3, and ssm: "the actions for the listed services are not denied by
this statement." That is nearly everything this project uses. The statements are kept
for the services that do support them and are named `DefenceInDepth*` so they cannot be
mistaken for the control. See **G-067** and F-0002.

## Deliberate exemptions, and why

**`DenyCreateUntaggedResources` covers only the actions verified to support
tag-on-create authorization** — `dynamodb:CreateTable`, `lambda:CreateFunction`,
`iam:CreateRole`, `logs:CreateLogGroup`, `cloudformation:CreateStack`,
`resource-groups:CreateGroup`. Excluded, per G-047:

- **`s3:CreateBucket`** takes no request tags at all. A blanket deny conditioned on
  `aws:RequestTag` blocks bucket creation outright, and the deploy fails on its first
  resource.
- **`cognito-idp:CreateUserPool`** tags via `UserPoolTags`, not `aws:RequestTag`.
- **`apigateway:POST`** carries tags in the request body.

**`cognito-idp` cannot be prefix-scoped.** User pool IDs are service-generated, so
there is no ARN pattern to match before the pool exists. Verified 2026-08-04 that no
other user pool exists in any region of this account, so the practical exposure is nil —
but that is a fact about the account, not a property of the policy, and it stops being
true the moment another project adds a pool.

**The region deny uses `NotAction` to exempt global services.** `aws:RequestedRegion`
is absent for IAM, STS, CloudFront, Route 53, Budgets, and Organizations, so a naive
`StringNotEquals` across `*` denies `iam:CreateRole` and the boundary blocks its own
first deploy (G-060). The mirror-image trap — writing the condition so absent keys pass
— would quietly exempt every global service from the control, so `NotAction` names them
explicitly instead.

**`DenyAssumingAnyRoleExceptTheAgentRole` uses `NotResource`, not `Resource: "*"`.**
The blanket form denied the CLI user the only action it exists to perform, because the
boundary is attached to the user as well as the role (G-065).

## Changing these

1. Edit the document.
2. Run `scripts/bootstrap-agent.sh` (dry run) to render and validate it. Every document
   is pre-flighted through IAM Access Analyzer, which catches invalid action syntax,
   non-existent service namespaces, and unsupported condition keys — all three of which
   occurred while writing these.
3. Check the size. An IAM managed policy is capped at **6144 characters** and a
   permissions boundary is exactly one managed policy, so the boundary is deliberately
   coarser than the grant policy: the concatenation of grants plus denies was 10132.
4. Re-run with `--apply`. Policies are updated as new default versions rather than
   deleted and recreated, because a window in which a guardrail does not exist is a
   window.
5. **Test both directions.** Prove the boundary still permits a real deploy as well as
   blocking what it should. A boundary tested only for what it blocks is half-tested
   (G-052), and over-restriction is the failure that wastes the most time.
