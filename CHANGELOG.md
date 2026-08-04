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
- **Registers seeded.** `docs/gotchas.md` with 59 entries (57 from §13 with IDs
  preserved verbatim, plus G-062 found while building the pipeline), five ADRs, and
  the first finding.

### Notes

- **The Phase 0 entry gate has not passed**, and cannot in the current environment:
  it needs provider keys, an agent IAM principal under a permissions boundary, and a
  hosting decision. All six §0.8 human prerequisites are outstanding. `make doctor`
  reports each with what to do about it.
- **Nothing here has used the AWS credentials present in the environment**, which
  are the account root user. §9.4 non-negotiable #1 forbids the agent holding root,
  and no permissions boundary can constrain root, so those credentials cannot be the
  ones this project deploys with. `guardrails-check.sh` fails if run under them.
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
