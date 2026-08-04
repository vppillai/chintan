# ADR 0003: Repository visibility and Pages hosting

Date: 2026-08-04
Status: **proposed — blocked on a human decision (§0.8 item 3)**

This ADR records the observed state and the decision that remains. It is
deliberately not marked accepted: §0.8 item 3 lists this as one of six things that
require a human and block the start, and the agent cannot make it — one half needs
a plan choice, the other may need buying a domain.

## Context

§10.6 sets out four constraints that come with hosting the frontend on GitHub Pages.
Two are decisions that must be settled **before Phase 1**.

### Observed state, 2026-08-04

| Fact | Value | Source |
|---|---|---|
| Repository | `vppillai/chintan` | — |
| Visibility | **public** | GitHub API |
| Pages enabled | **no** | GitHub API |
| Plan | could not be read from the API | — |

### Constraint 1 — visibility and plan (G-057)

Pages is available in public repositories on Free, and in public *and private*
repositories on Pro and above.

| Situation | Outcome |
|---|---|
| Free plan, private repo | **Pages unavailable.** Hard blocker |
| Free plan, public repo | Works. Source is world-readable |
| Pro or above, private repo | Works. Source stays private |

The repository is currently public, so **Pages will work on any plan**. But §9.6
assumes the repository stays private, which requires the paid plan. Those two facts
are in tension and the resolution is a human's.

**Independent of which row applies: the deployed frontend is world-readable.** A
Pages site is public even when its source repository is private; privately published
sites require an Enterprise Cloud organisation with Pages access control. This is
fine — it is client-side code — but it makes explicit a rule the rest of the design
already assumes: **no secret, provider key, or credential ever reaches the frontend
bundle** (§10.6, §9.4).

### Constraint 2 — Digital Asset Links must be at the domain root (G-007)

Phase 1 requires a verified link at `https://{domain}/.well-known/assetlinks.json`
— at the **domain root**, not a subpath. A Pages *project* site serves at
`https://{user}.github.io/{repo}/`, so that path resolves to the user-site repo,
not this one.

This is not cosmetic. Without a verified Digital Asset Link:

- WebAPK link verification fails
- an NFC URL tap produces an app-disambiguation dialog instead of opening the app
  (§5.3, §Phase 8)

So it gates WebAPK verification, voice launch, and the entire Phase 8 NFC path.

### Constraint 3 — commercial use is prohibited (G-058)

GitHub's terms state Pages is not allowed as free web hosting for an online
business or commercial SaaS. Irrelevant to a personal tool; **disqualifying for the
commercial path in §2A.** Migration is listed in §8A with its trigger — *required
before charging for the product* — and is straightforward because the frontend is
static assets and the API's allowed origin is already config-driven.

## Decision required

**1. Visibility and plan.** Either:
   - *(a)* stay public on Free — works today, source is world-readable, and §9.6's
     assumption of a private repository is knowingly set aside; or
   - *(b)* go private on Pro — matches §9.6, costs a subscription.

**2. Assetlinks topology.** Either:
   - *(a)* **a custom domain on this repository (CNAME). Recommended by §10.6**,
     and the recommendation is worth restating: it also decouples the app from the
     GitHub Pages URL, which matters both for the §8A migration and for NFC tags,
     which **physically encode the URL** — changing it later means reprogramming
     every tag (§7.3). Costs domain registration; or
   - *(b)* serve `.well-known/assetlinks.json` from the `{user}.github.io`
     user-site repo. Free, but couples two repositories and the file must be
     maintained by hand.

## Current state of the code

`config/instances/*.yaml` sets `allowed_origin: https://vppillai.github.io`, which
is correct for a project site on the current setup and is what CORS needs (§10.6).
It is **not** sufficient for assetlinks, and `doctor.sh` reports that explicitly
rather than letting it be discovered at WebAPK-verification time:

> §0.8/3 unresolved: allowed_origin is a github.io project site. Digital Asset
> Links must be served from the DOMAIN ROOT…

Nothing else in the codebase depends on the outcome. When decided, the change is
`allowed_origin` plus a CNAME, and this ADR moves to accepted.

## Consequences of not deciding

Phase 0 completes without it. **Phase 1 cannot**: its entry gate includes
"assetlinks verification under the chosen hosting topology, via
`adb shell pm verify-app-links`", and there is no topology to verify.
