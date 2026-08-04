# ADR 0004: No frontend framework; TypeScript bundled with bun

Date: 2026-08-04
Status: accepted

§14.4 lists "frontend framework choice" as an open question to be decided during
implementation and recorded here.

## Context

§4 permits "TypeScript, vanilla or lightweight framework, built to static assets."
Two constraints narrow that more than they first appear:

1. **§4A.1 rules out a framework on the capture path.** The capture face must render
   and be interactive inside the 2-second trigger-to-recording budget (§11A.5),
   which the spec says "rules out booting a framework on its critical path." That
   budget is the driving-critical number, measured from launch intent on the target
   phone — not from page load on a desktop.
2. **§4A.1 also forbids one component tree spanning both faces.** The capture and
   triage faces "share tokens, type, and vocabulary. They share almost no layout."
   A framework's main benefit is composing a large component tree; here there are
   two small, deliberately unshared ones.

Additionally, the app is installable and must work offline, so every asset is
self-hosted (§4A.3). Bundle size is a load-time cost on the path with the tightest
budget in the product.

## Decision

**No framework. TypeScript, bundled and transpiled with `bun`, vanilla DOM.**

- **Capture face:** hand-written, no dependencies, inlined into the document where
  that helps the 2-second budget. It is one interactive element (§4A.5: "the
  capture face carries no text by default… a field of colour and one breathing
  form"), so a framework would be pure overhead on the one surface that cannot
  afford any.
- **Triage face:** a keyboard-first editing environment where the budget is
  irrelevant (§4A.1). Still vanilla, for the reason below.
- **bun** as the build tool: one binary, no dependency tree of its own, does TS
  transpilation and bundling. It is what passbook uses, and it keeps the toolchain
  image (ADR 0005) small enough to pull on every CI job.

The triage face is the arguable half of this — it is genuinely a rich editing
surface, and a framework would help there. It is vanilla anyway because a framework
used on one face and not the other means two idioms, two testing approaches, and a
standing temptation to "unify" them, which §4A.1 exists to prevent. If the triage
face later outgrows this, the right move is a framework **scoped to that face
alone**, adopted as a superseding ADR — not a gradual migration that reaches the
capture path.

## Consequences

- The 2-second budget is achievable and measurable rather than something to be
  fought for later. §Phase 1 acceptance measures it on the target phone.
- The frontend has no dependency tree to audit, which makes the dependency-scan
  gate (§0.5A) close to free on this side, and removes the most common source of
  supply-chain exposure in a web app.
- **Accepted cost:** more hand-written DOM code in the triage face, particularly
  the two-pane list-and-detail layout at ≥1024px (§4A.6) and the markdown editor.
  This is a real cost and it lands in Phase 3.
- The silence-scaled timeline (§4A.4) is inline SVG, hand-built. It is the one place
  §4A permits elaboration, and it is a custom visualisation no framework would have
  supplied anyway.
