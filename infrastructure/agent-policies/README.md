# Agent IAM policy documents

The four documents `scripts/bootstrap-agent.sh` applies, with `ACCOUNT_ID` and
`REGION` as placeholders substituted at apply time. Nothing else reads them.

| File | Applied as |
|---|---|
| `boundary.json` | `chintan-agent-boundary` — the permissions **ceiling**. Attached to the agent role and the CLI user, and named as `PermissionsBoundary` by the roles in `infrastructure/bootstrap.yaml`, so it must exist before `scripts/setup.sh` can deploy them. One managed policy, so capped at 6144 characters; the bootstrap measures it and refuses an oversized document. |
| `permissions.json` | `chintan-agent-permissions` — what the agent may do. Effective permission is this intersected with the boundary. Every statement is scoped to `chintan-*` ARNs in the deploy region except the listing and validation calls that take no resource. |
| `deny.json` | `chintan-agent-deny` — explicit denies, which override any allow: other projects' `passbook-*` names, runaway-cost services, regions other than the deploy region, the guardrail policies themselves, and the CloudTrail bucket. |
| `trust.json` | The agent role's trust policy as first created; the bootstrap rewrites it to name the CLI user alone. |

`boundary.json` and `deny.json` share their deny statements; keep them in step
by hand — nothing generates one from the other, and nothing compares the
deployed policy against the repository. Run the bootstrap without `--apply` to
pre-flight the documents through IAM Access Analyzer, then with `--apply` to
publish them as new default versions.

Known limits: the boundary does not require itself on roles the agent creates;
tag-based denies are ignored by most services in use and
are named `DefenceInDepth*` for that reason; Cognito cannot be prefix-scoped
because pool IDs are service-generated.
