# Chintan

*Sanskrit: contemplation, reflective thinking.*

A personal, mobile-friendly web app for **capturing spoken thought and triaging it
into structured, actionable items.**

This is not a note-taking app. It is a capture-and-triage system: you speak an
unstructured brain dump, and the system extracts the discrete things inside it,
classifies them, and files them where you will find them again.

```
speak → transcribe → extract items → classify → file → act on or retrieve later
```

The primary use context is **hands-busy capture** — driving, walking, in a
workshop, away from a desk. It optimises for capture with near-zero interaction,
not for a rich editing experience while capturing. Triage happens later, at a desk.

**Status: Phase 0, in progress.** The pipeline is green and deploys itself; the
application does not exist yet — the API serves exactly one endpoint. See
[Current state](#current-state).

---

## What a brain dump contains

A single three-minute recording usually holds several different *kinds* of content,
interleaved and unsignposted. Telling them apart is the system's central job:

| Kind | Example | Treatment |
|---|---|---|
| `action` | "I should email Dave about who owns the DFMA epics now" | Compressed to its essence, with completion state |
| `idea` | "what if the RPC layer used credit-based flow control instead" | Accumulated against an ongoing thread, reasoning preserved |
| `prompt` | a two-minute spoken architecture spec for a future coding agent | **Preserved verbatim.** Never summarised |
| `reference` | a fact, a name, a number, a link worth keeping | Stored compactly, made searchable |
| `question` | "do I actually need per-tenant keys before there's a second tenant" | Kept open, surfaced until resolved |
| `noise` | thinking aloud that resolves to nothing | Kept in the transcript; no item created |

The `prompt` row is the one that drives the design. That content exists to be pasted
into a coding agent months later, so its value *is* the full text — summarising it
destroys the artifact while appearing to succeed. A uniform clean-and-summarise pass
would be wrong for it, and CI has a dedicated gate asserting no such pass can reach
one.

## Documentation

**[`docs/spec.md`](docs/spec.md) is the complete and only specification.** It is
self-contained: everything needed to build the system is there, and it describes the
system in the present tense rather than as a record of how the design evolved.

Three registers sit alongside it, with distinct purposes:

| Location | Contains |
|---|---|
| [`docs/decisions/`](docs/decisions/) | ADRs — choices made, alternatives considered, consequences accepted |
| [`docs/findings/`](docs/findings/) | Empirical results answering a pre-registered question |
| [`docs/gotchas.md`](docs/gotchas.md) | Surprises — where reality differed from reasonable expectation. 69 entries |

`docs/gotchas.md` is a first-class deliverable, not a scratchpad. Most of its
entries describe failures that *pass testing and fail in the real situation*, which
is why each one records how the trap presents as well as what it is.

---

## Working on this

**Everything runs inside one container image.** Dev, CI, and deploy share it, so
"it passes on my machine" and "it passes in CI" are the same statement. You need
Docker (or podman via `DOCKER=podman`); you do not need Go, bun, yq, shellcheck, or
the AWS CLI installed.

```bash
make check      # every CI gate, in the image CI uses
make test       # unit tests and admin-script tests
make fmt        # format Go and shell
make shell      # interactive shell in the toolchain image
make help       # everything else
```

The first `make` builds the image (~2 minutes). After that it is reused, and
rebuilt only when `containers/toolchain/` changes — the tag is a hash of that
directory, so a stale image cannot be silently reused after a version bump.

`make check` is what CI runs. Not an approximation of it: the same target, in the
same image, resolved by the same content hash. See
[ADR 0005](docs/decisions/0005-containerised-toolchain.md).

### Layout

```
backend/          Go: sync API and async worker Lambdas, plus the admin binary
  internal/keys/    the ONLY place a DynamoDB or S3 key is constructed
config/instances/ one YAML per instance; the CI matrix discovers them here
containers/       the toolchain image, with every tool pinned by version + sha256
frontend/         static assets for GitHub Pages (Phase 1)
infrastructure/   CloudFormation: bootstrap and per-instance stacks
scripts/          operational scripts — the only sanctioned mutation surface
  checks/           the §0.5A check inventory bodies
docs/             the spec and the three registers
spikes/           disposable; excluded from build, lint, and coverage
```

### Two conventions worth knowing before you change anything

**`--dry-run` is the default for anything destructive or costly; `--apply`
executes.** This is the single most important convention for agent safety: a
mistaken invocation prints a plan instead of causing damage. Read-only scripts have
no `--apply` and need none.

**Out-of-band mutation of backend state happens only through the scripts in
`scripts/`.** No ad-hoc AWS CLI or SDK calls to inspect or change data — if an
operation is needed and no script exists, write the script first, with `--help`,
`--dry-run`, tests, and an audit record. Ad-hoc calls are untested, unaudited,
unrepeatable, and have no dry-run. This applies to an implementing agent as
strictly as to a human operator.

---

## Current state

**Phase 0 is in progress.** The pipeline is built and green before the application
exists, which is the ordering §0.5A requires: a check added after the code it
governs gets written to pass, whereas one wired first describes intended behaviour.

Done:

- **Containerised toolchain** — one image for dev, CI, and deploy; every tool pinned
  by version and sha256 for both architectures
- **The complete §0.5A check inventory, 21 gates, all wired.** Fourteen are active
  and passing on real subjects. Seven have no subject until a later phase; each tests
  for its subject rather than being hardcoded to pass, so it starts running the day
  the subject appears
- **Config system** — every key in §7.4 required, validated by the same code the
  Lambda runs at cold start, with 30+ negative tests
- **Tenant-scoped key helper (I11)** — the only place a DynamoDB or S3 key is
  constructed, enforced by a static check rather than by convention
- **`GET /v1/health`**, versioned routing, structured logging that cannot casually
  log transcript content
- **Registers seeded** — 69 gotchas, five ADRs, three findings
- **A deployed dev instance.** CI builds a reproducible arm64 artifact, assumes the
  deployment role via OIDC with no stored key, and deploys — then smoke-tests
  `/v1/health` before calling it a success. That last part caught a deploy where
  CloudFormation succeeded and the function returned 500 for everything
  ([F-0003](docs/findings/F-0003-first-deploy-through-ci.md))
- **The §9.5 guardrails, applied and verified in both directions.** 13 denials fire, 8
  required operations succeed ([F-0002](docs/findings/F-0002-agent-boundary-bootstrap.md))

Not done, and next:

- The remaining Phase 0 commercial-readiness foundations: metering and audit are written
  but not yet wired to a request path; consent, idempotency, the spend breaker, and the
  KMS indirection remain
- The DynamoDB repository adapter (the interface and in-memory fake exist)
- `users.sh` and the remaining admin scripts
- The frontend hello-world and its Pages deploy

### Blocked on a human

Six prerequisites require a human and block the start (§0.8). None is the agent's to
do; two are urgent because work is already touching them:

1. **The only AWS credentials available in this environment are the account root
   user.** §9.4 non-negotiable #1 is that the agent never receives root credentials,
   and no permissions boundary can constrain root — so every guardrail in §9.5 is
   currently unenforceable. `scripts/bootstrap-agent.sh` must be run by a human to
   create the agent principal. **Nothing in this repository has used those
   credentials**, and `guardrails-check.sh` fails if it is ever run under root.
2. **The GitHub token in use is a classic token**, whose scopes apply to every
   repository the user can reach (G-049). §9.6 requires a fine-grained token scoped
   to this repository alone.
3. **Repository visibility, plan, and the assetlinks topology** — see
   [ADR 0003](docs/decisions/0003-repository-visibility-and-pages.md). Phase 0
   completes without this; **Phase 1 cannot.**
4. **Provider keys in SSM** (Groq, MiniMax), as `SecureString` under `alias/aws/ssm`.
5. **Cost allocation tags activated in the Billing console.** Console-only, and they
   do not backfill — the first months are unrecoverable otherwise (G-023).
6. **Branch protection, required status checks, CODEOWNERS enforcement, and secret
   scanning.** The agent is deliberately denied the `administration` permission, so
   it cannot enable the protections that constrain it — a guardrail the constrained
   party can switch on is not a guardrail.

`make doctor` reports the state of all of these, with what to do about each.

---

## Cost

The target is **~$1/month, demonstrably under $5**, with a documented worst case
under $20 (§10.7). Third-party STT and LLM calls are the entire bill; AWS at this
scale is a rounding error.

Five choices hold that figure, and each is load-bearing rather than incidental —
removing any one moves the total by more than the total: GitHub Pages hosting,
AWS-managed keys instead of a customer-managed KMS key, no CloudWatch alarms or SNS
topics, explicit 14-day log retention, and LLM calls batched per session and gated
on need.

**If you are cloning this:** do not read "$1/month" as "$0/month". AWS replaced the
legacy Free Tier for accounts created on or after 2025-07-15 (G-027). Always-Free
allowances — Lambda 1M requests, DynamoDB 25GB — are permanent and cover the compute
and database layers outright. S3's allowance is legacy-tier only, so a newer account
may see a small S3 charge once signup credits lapse. Those allowances are also
**account-wide**, shared with every other project in the account, not per-project
(G-028).

## Not building

Explicit non-goals, not deferred items: native mobile apps, Android Auto or CarPlay
integration ([why](docs/decisions/0001-no-android-auto.md)), real-time streaming
transcription, multi-user collaboration, self-service signup or billing, speaker
diarisation, and fine-tuning or self-hosting an STT model.

## Licence

See [LICENSE](LICENSE).
