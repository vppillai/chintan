# Chintan v2 — Master Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement each phase plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Transform the v1 vertical slice into a production-ready, feature-complete application per `docs/superpowers/specs/2026-08-07-chintan-v2-design.md`.

**Architecture:** Keep the Go domain core (`routing`, `cleanup`, `provider`, `keys`, `match`) and rewrite the edges around it — real JWKS auth, a conditional-write repository, an SQS-driven worker for the slow pipeline, metering and a spend breaker. Replace the vanilla-JS frontend entirely with Vite + React + TypeScript.

**Tech Stack:** Go 1.25 (arm64 Lambda), DynamoDB single-table + GSI1, S3, SQS, Cognito, CloudFormation; Vite + React 19 + TypeScript, TanStack Query, Zustand, React Router, idb, Workbox.

---

## Global Constraints

These apply to every task in every phase plan. Copied verbatim from the spec.

**Environment (verified 2026-08-07):**
- Go is **not on `PATH`**. Every Go command must be prefixed: `export PATH=/usr/local/go/bin:$PATH`. Version is go1.25.0 linux/arm64.
- There is **no `npm` and no Node.js**. `node` is a shim that execs **Bun 1.3.13** (`/home/vpillai/.bun/bin/bun`). All frontend package management and script running uses `bun` / `bunx`.
- Docker 29.1.3 is available with a working daemon, as a fallback for anything Bun cannot run.
- Network access for package registries is available.

**Process:**
- All work lands on branch `feat/v2`. `main` stays deployable from tag `v0.2.0-prerewrite`.
- **Never deploy to AWS. Never push to `main`.** Both are the user's decision.
- Commit after every task. Never commit a red build.
- TDD: write the failing test, watch it fail, implement, watch it pass.

**Code rules:**
- No new dependency without justification recorded in the task.
- No literal colour or font-size outside design tokens (frontend).
- No transcript or audio content in any log line, ever. This is enforced by a CI check.
- Test doubles never ship in the production binary — `_test.go` or a `testing` build tag.
- Every list endpoint is cursor-paginated. No unbounded DynamoDB `Query` may exist after Phase 2.
- Every mutating handler accepts `Idempotency-Key`.
- One error envelope: RFC 9457 `application/problem+json`.

**Identity type (Phase 1 produces; every later phase consumes):**

```go
package auth

type Identity struct {
    UserID   string // Cognito sub
    TenantID string // == UserID in v2; the multi-user seam
}
```

**Verification commands:**

```bash
export PATH=/usr/local/go/bin:$PATH
cd backend && go build ./... && go vet ./... && go test -race ./...
gofmt -l .   # must print nothing

export PATH=/home/vpillai/.bun/bin:$PATH
cd frontend && bun run typecheck && bun run test && bun run build
```

---

## Phase index

Phases 1–3 are sequential (same files). Phase 4 depends on 1–3. Phases 5 and 6 are
independent of each other and can run in parallel once Phase 4 lands. Phase 7 is
independent of all backend work from the start.

| Phase | Plan | Depends on | Deliverable |
|---|---|---|---|
| 1 | `2026-08-07-chintan-v2-phase1-auth.md` | — | Real JWKS verification, tenant-scoped identity, `X-User-ID` removed, isolation test |
| 2 | `2026-08-07-chintan-v2-phase2-repository.md` | 1 | Repository interface seam, cursor pagination, GSI1, conditional writes, projected attributes, epoch TTL |
| 3 | `2026-08-07-chintan-v2-phase3-idempotency.md` | 2 | `Idempotency-Key`, optimistic concurrency, ETag-conditional S3 append, append guard |
| 4 | `2026-08-07-chintan-v2-phase4-pipeline.md` | 3 | SQS + worker Lambda, presigned-GET transcription, `segments.json`, `peaks.json`, correlation IDs |
| 5 | `2026-08-07-chintan-v2-phase5-spend.md` | 4 | `meter`, `breaker`, per-user quotas, WebAuthn login rate limit |
| 6 | `2026-08-07-chintan-v2-phase6-api.md` | 4 | Handler rewrite, problem+json, OpenAPI, slog + EMF, search, tags, export |
| 7 | `2026-08-07-chintan-v2-phase7-frontend.md` | contract from 6 (mockable) | Full Vite/React/TS rewrite |
| 8 | `2026-08-07-chintan-v2-phase8-infra.md` | 4, 5 | SQS/worker/DLQ, alarms, budget, retention policies, Cognito hardening, IAM tightening |
| 9 | `2026-08-07-chintan-v2-phase9-ops.md` | 8 | Script replacement, `chintanctl`, CODEOWNERS, guardrails in CI, staging, rollback, CI gates |

Phase plans are written just-in-time, immediately before the phase executes, so
that each is written against the code as it actually exists rather than as it was
predicted to exist three phases earlier.

---

## Definition of done for the whole effort

From spec §10, restated as checkable conditions:

1. An automated test proves tenant A cannot reach tenant B's data below the HTTP layer.
2. A simulated 20-minute capture completes without a gateway timeout and appends exactly once under an induced retry.
3. Network loss mid-recording, then reconnect, files the note with no data loss (Playwright).
4. `chintanctl export` recovers every note, transcript, and audio file.
5. A daily spend cap stops provider calls and surfaces a distinct status.
6. Lighthouse PWA and a11y > 90; zero critical axe violations in both themes.
7. README instructions alone let a third party deploy and tear down an instance.
8. `git checkout v0.2.0-prerewrite` restores a working deployment.

## Progress log

Appended by each phase as it completes. Kept here so a resumed session can
determine state without re-reading the whole diff.

- [x] Phase 0 — audit, design spec, rollback tag, branch `feat/v2`
- [x] Phase 1 — auth (JWKS verification, tenant identity, X-User-ID removed, isolation tests at both layers)
- [ ] Phase 2 — repository
- [ ] Phase 3 — idempotency
- [ ] Phase 4 — pipeline
- [ ] Phase 5 — spend
- [ ] Phase 6 — api
- [ ] Phase 7 — frontend
- [ ] Phase 8 — infra
- [ ] Phase 9 — ops
