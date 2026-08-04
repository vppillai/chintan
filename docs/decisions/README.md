# Decision records

ADRs — choices made, alternatives considered, consequences accepted (§0.4).

One of three registers, each with a distinct purpose:

| Location | Contains | Origin |
|---|---|---|
| `docs/decisions/` | this directory: choices, alternatives, consequences | a decision point in the build |
| `docs/findings/` | empirical results answering a **pre-registered** question (§0.2) | a deliberate spike |
| `docs/gotchas.md` | surprises — where reality differed from reasonable expectation | incidental discovery |

## When to write one

Where the spec says *"decide during implementation"*, make the decision, record it
here, and proceed — **do not block** (§0.1). §14.4 lists the questions that are
genuinely open. Also record any deviation from the reference patterns in
`vppillai/passbook`, which are normative (§10.1): "Deviate only with a recorded
ADR."

## Format

Present tense, describing the decision as it stands (§0.4). Include the rationale —
it is required, not optional:

> Stating *why* the design is what it is prevents a future agent from optimising
> away a constraint whose purpose isn't obvious.

What to leave out is the narrative of how the decision was reached. Git holds that.

```markdown
# ADR NNNN: title

Date: YYYY-MM-DD
Status: proposed | accepted | superseded by NNNN

## Context
What forces are in play. Cite the spec sections and gotchas that constrain it.

## Decision
What is being done.

## Consequences
What this costs, what it buys, and what was rejected — with the reason each
alternative lost. A rejected option with no stated reason gets re-proposed.
```

## Index

| ADR | Title | Status |
|---|---|---|
| [0001](0001-no-android-auto.md) | No Android Auto integration | accepted |
| [0002](0002-go-arm64-runtime.md) | Go on ARM64 Lambda | accepted |
| [0003](0003-repository-visibility-and-pages.md) | Repository visibility and Pages hosting | **proposed — needs a human** |
| [0004](0004-frontend-stack.md) | No frontend framework; TypeScript bundled with bun | accepted |
| [0005](0005-containerised-toolchain.md) | One container image for dev, CI, and deploy | accepted |
