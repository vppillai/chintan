# F-0002: Does the permissions boundary permit legitimate work and block the intended actions?
Date: 2026-08-04   Phase: 0   Status: **confirmed, after three refutations**

## Question

The Phase 0 entry gate asks two things, and insists on both:

> **The permissions boundary permits legitimate work.** Deploy a trivial stack using
> the agent principal under its boundary. **An over-restrictive policy blocking real
> deploys is at least as likely as a permissive one letting damage through**, and it is
> the failure that will waste the most time if discovered mid-phase.
>
> **The denies actually fire.** Attempt to create an untagged resource, attempt to
> modify a `passbook-*` resource, attempt `iam:CreateAccessKey`, attempt a deploy in
> another region. Assert each fails. Also confirm which services in use lack tag-based
> authorization coverage (G-047) rather than presuming coverage.

Pass criterion: every denial in §9.5 fires when attempted, and every operation the
project legitimately needs succeeds — both directions, as G-052 requires.

## Method

`scripts/bootstrap-agent.sh` created the boundary, the deny and grant policies, the
agent role, a minimal CLI user, and CloudTrail. Every policy document was pre-flighted
through IAM Access Analyzer before creation. Then 21 operations were attempted against
the live API as the bounded principal — 13 that must be denied, 8 that must be
permitted.

Authorisation note, recorded because it is a documented deviation from §0.8: the
account owner explicitly authorised using root credentials for this bootstrap and this
bootstrap only. §0.8 item 1 assigns it to a human because "the agent cannot create its
own credentials by design (I17)". The invariant's *purpose* is preserved — root was
used to create the constraints and for nothing else, and every subsequent operation
runs as `voicenotes-agent` under its boundary. `guardrails-check.sh` fails if it is
ever run under root, so a regression is detected rather than assumed away.

## Result

**Three assumptions were refuted before the boundary worked at all.** Each is recorded
as a gotcha; each would have cost a lot more to discover mid-phase.

### 1. §9.5's ABAC snippet cannot be created (G-066)

The spec's example uses `"Action": ["*:Create*", "*:Run*"]`. IAM rejects it:

```
MalformedPolicyDocument: Action vendors (e.g., aws, ec2, etc.) must not contain wildcards.
```

Both forms in the snippet — `*:Create*` and `*:Delete*` — fail. §9.5 already says the
snippet is "a template, not a policy to paste unchanged" and gives two reasons; this is
a third it does not mention, and it blocks the bootstrap before either of the others can
matter. It fails loudly, which is the good outcome: the alternative would have been a
deny that matched nothing while appearing to be in force.

Access Analyzer also caught four service namespaces that do not exist: `emr:*` (it is
`elasticmapreduce`), `efs:*` (it is `elasticfilesystem`), and `neptune:*` / `docdb:*`
(both managed through `rds:*`, which was already denied).

### 2. Root cannot assume a role (G-064)

With the role created and its boundary attached, the first `sts:AssumeRole` failed:

```
AccessDenied: Roles may not be assumed by root accounts.
```

The trust policy is irrelevant — the restriction is on the caller. So on an account
whose only credentials are root, and with no IAM Identity Center configured, **there is
no path from root to boundary-limited credentials that does not pass through an IAM
user.** §9.5 prefers a role assumed via short-lived credentials and treats an IAM user
as the fallback; on a root-only account the fallback is not optional.

Resolved with two objects in layers: a user (`voicenotes-agent-cli`) whose only
permission is `sts:AssumeRole` on the one role, with the boundary attached to it as
well; and the role that carries the project permissions. The stored key buys nothing
except a bounded session, and the credentials doing the work expire in an hour.

### 3. The boundary locked the user out of the action it existed for (G-065)

`DenyRoleChainingToEscalate` denied `sts:AssumeRole` on `"*"`, intending to stop the
role chaining onward to something more privileged. But the boundary carries every deny
and is attached to the **user** as well, so it denied the user the single action it
existed to perform. The grant was plainly present in the identity policy; the boundary
was what refused, and a boundary is not the policy anyone reads first.

This is precisely G-052's shape, from the inside: over-restriction surfacing mid-task
as a confusing `AccessDenied` on something obviously supposed to be allowed. Fixed with
`NotResource` naming the permitted role, which preserves the intent exactly — the one
way in is this role, and neither principal can chain onward.

### 4. Tag-based denies are decorative for most services (G-067)

The gate asks specifically to "confirm which services in use lack tag-based
authorization coverage rather than presuming coverage." Access Analyzer answers
directly:

> Using the effect Deny with the condition key `aws:ResourceTag/Project` and actions
> for services with the following prefixes can be overly permissive: **cloudformation,
> cognito-idp, dynamodb, events, iam, lambda, logs, resource-groups, s3, ssm.** The
> actions for the listed services are not denied by this statement.

That is very nearly every service this project uses. G-047 predicts the category; the
extent is the finding. **The naming-prefix denies and the resource-ARN scoping in the
grant policy are the actual control** — ARN-based conditions are always supported. The
tag statements are retained as defence in depth for the services that do support them,
and renamed so they do not claim more than they deliver.

### Both directions, verified against the live API

Denied — 13 of 13:

| Attempt | Result |
|---|---|
| create an IAM user | denied |
| create an access key for itself | denied |
| read a provider secret value (`ssm:GetParameter --with-decryption`) | denied |
| read a `passbook-*` table | denied |
| delete the `passbook-github-actions` role | denied |
| operate outside `ca-central-1` | denied |
| EC2 | denied |
| OpenSearch (I7) | denied |
| `cloudtrail:StopLogging` | denied |
| write to the audit bucket | denied |
| detach its own permissions boundary | denied |
| alter its own deny policy | denied |
| chain to the `passbook` CI role | denied |

Permitted — 8 of 8, after one fix:

`sts:GetCallerIdentity`, list project tables in the deploy region, describe
CloudFormation stacks, list buckets, read parameter *metadata*,
`cloudtrail:DescribeTrails`, `access-analyzer:ValidatePolicy`, list roles.

`ValidatePolicy` was initially **blocked**: the boundary's ceiling included
`access-analyzer:*` but the grant policy did not, and effective permission is the
intersection of the two. A ceiling is not a grant — worth stating because the failure
looks identical to a missing deny exemption.

## Consequence for the build

1. **§9.5's ABAC snippet needs three caveats, not two.** Recorded as G-066. The policy
   documents now live in `infrastructure/agent-policies/` as reviewable files — a
   CODEOWNERS-protected path — with the exemption lists and their reasons beside them.

2. **§9.5's principal model needs an IAM user on a root-only account.** Recorded as
   G-064. The two-layer design keeps the working credentials short-lived, which is what
   the preference in §9.5 is actually protecting.

3. **The tag-based half of the ABAC design does not work here.** Recorded as G-067.
   This does not weaken the boundary — resource-ARN scoping covers the same ground and
   is reliably enforced — but it does mean the tag conditions must not be counted as
   the control. §9.8's whole premise is that a guardrail trusted while absent is worse
   than none.

4. **The `Protected=true` deletion deny is affected by the same gap** and should not be
   relied on for the services listed in G-067. The `DeletionPolicy: Retain` on the
   table and bucket in `template.yaml`, and the CI role's CloudFormation-only
   `DeleteTable` grant, are the controls that actually hold there.

5. **The boundary permits a real deploy.** Not yet proven end to end: the gate also
   requires deploying a trivial stack, which needs the bootstrap CloudFormation stack
   and is the next step. What is proven is that every read and describe the deploy path
   needs succeeds, and that CloudFormation stack operations are permitted in the deploy
   region.

6. **Still outstanding, and not settled by this finding:** the GitHub token remains a
   classic token (G-049), which `guardrails-check.sh` correctly reports as a violation
   rather than a warning. Provider keys are not in SSM. The `Project` cost allocation
   tag cannot be activated until a resource carries it, so it moves to the first deploy
   rather than being a pre-deploy step as §0.8 item 5 implies.
