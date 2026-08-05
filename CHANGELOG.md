# Changelog

All notable changes to this project are documented here.

**This file is the single place project history lives.** §0.4 requires every spec and
document in this repository to describe the system as it currently *is*, not as it
changed — and that rule only works because the history goes somewhere. The two rules
are complements rather than a contradiction: specs stay present-tense precisely
because this file absorbs the narrative.

Entries describe **user-visible change**, not commits. `git log` already has commits.

Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Versioning
is `vMAJOR.MINOR.PATCH`, pre-1.0 throughout initial development: a **minor** bump at
each phase completion (`v0.2.0` = Phase 2 complete), a **patch** bump for a deployed
slice within a phase.

**Tag before deploying, not after.** CI resolves `git describe` during the build, so
a tag pushed afterwards does not reach the artifact — the deployed app keeps
reporting the previous version (G-036).

## [Unreleased]

Phase 0 — Foundation. Accumulates until `v0.1.0`, which is tagged at the end of
Phase 1 (MVP: speak into the app, get a transcript back, see it).

### Added

- **Containerised toolchain.** One image for dev, CI, and deploy, so a check runs
  against the same tools everywhere. Every tool pinned by version and sha256 for
  both architectures; the base image pinned by its multi-arch index digest. `make`
  re-executes itself inside the image, so the local command and the CI command are
  the same command. ([ADR 0005](docs/decisions/0005-containerised-toolchain.md))
- **CI pipeline, built before any application code.** The complete §0.5A check
  inventory — 21 gates — wired from the first commit, including those whose subject
  arrives in a later phase. A dormant check tests for its subject rather than being
  hardcoded to pass, so it begins running the day the subject exists.
- **Configuration system.** Every key in §7.4 required, with no defaults to fall back
  on: a missing threshold fails the deploy rather than silently taking a value
  nobody chose. Validated by the same code the Lambda runs at cold start, so CI and
  runtime cannot disagree about what a valid config is. Cross-field constraints are
  enforced too — VAD hysteresis ordering, `prompt` confidence held strictly above the
  general bar, retire-below-demote, and the Telegram 20MB ceiling.
- **Tenant-scoped key helper (I11).** The only place a DynamoDB or S3 key is
  constructed. `TenantID` is a distinct type, every constructor refuses an empty
  tenant, and identifiers carrying the key delimiter or a path traversal are
  rejected rather than escaped. A static check fails the build on any key literal
  outside the package, which is what makes the monopoly a control rather than a
  convention.
- **`GET /v1/health`** returning API version, build SHA, config version, and
  instance — what the frontend compares against its own build to flag drift.
- **Versioned routing (I15)** with the `/v1/` prefix applied structurally, so a
  hand-written path cannot omit it.
- **Structured JSON logging** with correlation IDs and no comfortable path to logging
  transcript content: attributes go through helpers that name a safe scalar or
  deliberately redact.
- **Operational script foundations** — `doctor.sh`, `guardrails-check.sh`,
  `verify.sh`, `build-lambda.sh`, and the fake-AWS test harness. `--dry-run` default,
  `--json` output, `--tenant` required on data operations.
- **CloudFormation templates.** `bootstrap.yaml` (artifact bucket, OIDC deployment
  role behind a `CreateOIDCProvider` condition for G-016, tag-based Resource Group
  for provable teardown) and `template.yaml` (single table with a sparse GSI1, S3
  with direct Lambda notification, two arm64 functions with reserved concurrency,
  HTTP API v2 with a 5rps throttle, Cognito admin-create-only). No VPC, no alarms,
  no SNS, explicit log retention, full tag set on every resource.

  The CI deployment role deliberately holds **no item-level DynamoDB actions and no
  `s3:GetObject` on the data bucket**: it provisions storage but cannot read the
  corpus, so a compromised workflow cannot exfiltrate it. The worker's
  `s3:DeleteObject` is scoped to the continuous safety copies alone, which makes
  I1's "application code has no delete path for L0" structural rather than a
  convention the code is trusted to follow.
- **Agent IAM principal, boundary, deny policies, and CloudTrail** — the §9.5
  guardrails, applied and verified against the live API. `scripts/bootstrap-agent.sh`
  creates them; `infrastructure/agent-policies/` holds the documents as reviewable,
  diffable files in a CODEOWNERS-protected path. Every document is pre-flighted through
  IAM Access Analyzer before creation.

  All 13 denials in §9.5 fire when attempted, and all 8 operations the project needs
  succeed — both directions, which G-052 insists on because "over-restriction is at
  least as likely and considerably more expensive in wasted time". The agent now runs
  as `voicenotes-agent` under its boundary, with short-lived credentials, and
  `guardrails-check.sh` fails if it is ever run under root.
- **The bootstrap stack is deployed** — artifact bucket, OIDC deployment role, and the
  tag-based Resource Group that makes teardown provable. Deployed *under the boundary*,
  which settles the Phase 0 gate item that matters most: §9.5 warns CloudFormation is
  the actual caller, so the tags it propagates rather than the ones typed are what
  conditions see. Verified, including that the role CloudFormation created carries the
  boundary (G-046).
- **`bootstrap.sh` and `teardown.sh`.** Teardown proves completeness by querying the
  Resource Group rather than matching a name pattern — never a wildcard delete, because
  this account also hosts passbook. The corpus table and data bucket are retained by
  policy; removing them is tenant erasure (§9.3), not a side effect of teardown.
- **The §2A.1 irreversible foundations**: metering (I12), audit (I13), consent (I14),
  idempotency, the per-tenant daily spend breaker (§10.5.9), the KMS key indirection (I8),
  and the DynamoDB repository adapter. 55 defects fixed and 95 tests added across two
  adversarial review passes.

  Three §3 invariant violations were caught before merge, each demonstrated with a
  reproduction: the `audit` validation that keeps PII out of the audit store was writing that
  PII to CloudWatch and returning it to the caller; a concurrent settings write silently
  reverted an *acknowledged consent withdrawal* to granted; and repointing a tenant onto a
  customer-managed key flipped an erasure-completeness claim true for data written before the
  repoint. Full account in
  [F-0004](docs/findings/F-0004-parallel-implementation-with-adversarial-review.md).
- **Registers seeded.** `docs/gotchas.md` with 87 entries (57 from §13 with IDs
  preserved verbatim, plus G-062 found while building the pipeline), five ADRs, and
  the first finding.

- **The dev instance is deployed, by CI, through the OIDC role.** This is §0.5A's first
  slice — "a repository whose CI runs, gates, and deploys a hello-world through the real
  OIDC role" — and it is now true rather than intended. `GET /v1/health` on the deployed
  API returns its version, commit, config version, and instance; an unknown path returns
  404; CORS is the single configured origin.

  Two Lambda entrypoints (sync API and async worker), config read from S3 at cold start,
  and `deploy.sh` refusing to run outside CI unless given `--incident-response`. Cold
  start 142ms, 32MB peak against a 256MB allocation, 3.4MB artifact — recorded as the
  baseline the Phase 2 native-ONNX gate is measured against.

### Notes

- **The Phase 0 entry gate is partly passed.** The permissions boundary is proven in
  both directions (F-0002) and the OIDC-provider collision predicted by G-016 is
  confirmed empirically — the provider already exists, so `CreateOIDCProvider=false`.
  Still outstanding: provider reachability, a trivial stack deployed under the
  boundary, and the hosting decision.
- **Four of the six §0.8 prerequisites remain**, down from six: the app-name voice
  test, the visibility/assetlinks decision (ADR 0003), provider keys in SSM, and the
  GitHub protections. `make doctor` reports each.
- **Three assumptions about §9.5 were refuted while bootstrapping**, each recorded:
  its ABAC snippet uses IAM action syntax that does not exist (G-066); root cannot
  assume a role, so an IAM user is unavoidable on a root-only account (G-064); and
  `aws:ResourceTag` authorization is unsupported for nearly every service in use, which
  makes the tag-based half of the ABAC design decorative (G-067). Resource-ARN scoping
  and naming-prefix denies carry the weight instead. F-0002 has the detail.
- **The first deploy took five attempts, and four of the five errors named something
  other than the cause.** GitHub now issues immutable OIDC subjects with numeric ids
  embedded, so the documented trust-policy form no longer matches — and a sibling project
  in the same account still using the old form made the nearest precedent misleading
  (G-071). An S3 notification to a Lambda whose role referenced the bucket was a circular
  dependency reported as nine resources with no edges named (G-072). git refused the CI
  checkout as unsafe, so the build silently produced `commit=unknown`, which would have
  collided every artifact on one S3 key. And `lambda.Start` accepted a struct, compiled,
  logged "api ready", and then returned 500 for every request (G-073).

  The deploy smoke test caught the last one. Without a real request at the end of the
  deploy, CI would have reported success on a completely broken service. Full account in
  [F-0003](docs/findings/F-0003-first-deploy-through-ci.md).
- **Four checks were found to be wrong only by watching them go red**, two of them
  serious enough to be worth naming here:
  - The **I11 tenant-key check** — the one §Phase 0 acceptance names explicitly —
    was **vacuous**. It selected files with a bare `git ls-files`, which lists
    committed files only, so a *new* file building a key by hand was invisible to
    it. It reported green on exactly the change it exists to reject, and CI would
    have caught what local did not (G-063).
  - The **guardrails self-test** reported pass while never exercising its premise:
    its attempt to point the check at a doctored tree was silently ignored, and an
    unrelated bug made every run fail, so "it failed, therefore it detects the
    removal" held for the wrong reason (G-062).

  Both are fixed and re-demonstrated. Eleven of 21 checks have now been demonstrated
  red; the outstanding ten are listed in
  [F-0001](docs/findings/F-0001-checks-demonstrated-red.md) against the phase that
  gives each a subject. This is the §0.5A argument in miniature: the risk is not an
  absent check, it is a believed one.
