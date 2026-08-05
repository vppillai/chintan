# Findings

Empirical results answering a **pre-registered** question (§0.2).

The distinction from the other registers is the pre-registration. A finding answers
a question that was written down, with a pass/fail criterion, *before* any code was
written. That ordering is what stops a spike from concluding whatever the code
happened to do.

> "Write the question first, with a pass/fail criterion, before writing any code.
> 'Does X work?' is not a question. 'Does injecting 60 domain terms via Whisper's
> `prompt` parameter reduce error rate on those terms by >50% against a held-out
> sample?' is." (§0.2)

**Every spike produces a file here, regardless of outcome.** A refuted assumption is
a *more* valuable finding than a confirmed one and must be recorded with equal care.
Without the register, a future agent re-runs the same failed experiment — or worse,
builds on the assumption again.

A refuted finding usually also produces a gotcha. Record both: the finding documents
the experiment, `docs/gotchas.md` documents the trap.

## Format (§0.2)

```markdown
# F-0007: Whisper prompt biasing efficacy on domain vocabulary
Date: YYYY-MM-DD   Phase: 4   Status: confirmed | refuted | partial

## Question
<the pass/fail question, as written before starting>

## Method
<what was actually run — enough to reproduce>

## Result
<what happened, with numbers>

## Consequence for the build
<what this changes in the spec, if anything>
```

## When a spike refutes a spec assumption

Stop. Record the finding. Propose an alternative. **If the refutation invalidates a
phase's design rather than a detail, flag it for human decision rather than silently
redesigning** — the spec's author may have context the finding does not capture
(§0.2).

## The eight assumptions awaiting findings (§0.3)

Each carries an entire phase design. None is proven yet.

| # | Assumption | Validate in |
|---|---|---|
| A1 | Whisper's `prompt` measurably biases decoding, on the primary user's own accented speech — **the highest-risk assumption in the specification** | Phase 1 gate |
| A2 | Silero VAD via ONNX Runtime Web runs in real time on a mid-range Android phone | Phase 2 gate |
| A3 | The LLM reliably emits valid structured patches rather than rewriting prose | Phase 3 gate |
| A4 | Extraction classifies `prompt` content with high precision | Phase 3 gate |
| A5 | `getUserMedia` succeeds without a fresh gesture on an installed PWA | Phase 1 gate |
| A6 | 28-second segments measurably improve WER over shorter windows | Phase 2 gate |
| A7 | Browser and server VAD produce equivalent segment boundaries | Phase 2 gate |
| A8 | Baseline STT accuracy on the primary user's accented English keeps correction volume inside the prompt budget | Phase 2 gate |

**A gate failing is the system working**, not a defect in the spec (§0.8).

## Index

| Finding | Question | Status |
|---|---|---|
| [F-0001](F-0001-checks-demonstrated-red.md) | Does each §0.5A check actually fail when its subject is broken? | partial — 11 of 21 |
| [F-0002](F-0002-agent-boundary-bootstrap.md) | Does the permissions boundary permit legitimate work and block the intended actions? | confirmed, after six refutations |
| [F-0003](F-0003-first-deploy-through-ci.md) | Can CI deploy a working stack through the OIDC role, with no credentials held locally? | confirmed, after five failures |
| [F-0004](F-0004-parallel-implementation-with-adversarial-review.md) | Does parallel implementation behind settled contracts, with adversarial review, hold the invariants? | confirmed — but only because the review was adversarial |
