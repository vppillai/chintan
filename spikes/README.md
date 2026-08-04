# Spikes

Disposable code that answers one pre-registered question (§0.2).

**This directory is excluded from build, lint, and coverage. Nothing in the main
tree may import from it, and the code here is never promoted into production** —
it has no tests, no error handling, and no invariant compliance, and the
prototype-becomes-implementation path is how untested code reaches production
wearing a disguise.

## Protocol (§0.2)

1. **Write the question first**, with a pass/fail criterion, before writing any
   code. "Does X work?" is not a question. "Does injecting 60 domain terms via
   Whisper's `prompt` parameter reduce error rate on those terms by >50% against a
   held-out sample?" is.
2. **Build the smallest thing that answers it.** A script, a scratch HTML page, a
   curl loop. No abstractions, no tests, no error handling.
3. **Time-box it.** A spike that overruns is itself a finding — the thing is
   harder than assumed, and that changes the plan.
4. **Record the finding in `docs/findings/`, then delete the code.** The code is
   disposable; the knowledge is not.

A refuted assumption is a **more** valuable finding than a confirmed one and must
be recorded with equal care. Without the register, a future agent re-runs the same
failed experiment — or worse, builds on the assumption again.

When a spike refutes a spec assumption: stop, record the finding, propose an
alternative. If the refutation invalidates a phase's *design* rather than a
detail, **flag it for human decision rather than silently redesigning.**

The git history keeps deleted spike code recoverable, so deleting it loses
nothing. Leaving it invites reuse.
