# Chintan — Design Spec (Clean Slate)

**Date:** 2026-08-06  
**Status:** Approved for planning  
**Product name:** Chintan (Sanskrit: contemplation, reflective thinking)

## 1. Problem

I need a personal, mobile-friendly brain-dump tool I can use while driving or walking. I speak unstructured thoughts; the system turns them into notes I can find later.

I am not a native English speaker, so speech-to-text often mangles my words. I need an AI cleanup pass that makes text coherent **without fundamentally changing my wording** unless necessary. Cleanup aggressiveness is a **system setting** (`faithful` vs `polished`), not per-note.

I will have many notes on different topics. I often will not remember exact titles, so I need to select a note by a **vague description**. Sometimes I append to an existing note; sometimes I create a new one; sometimes I edit at a desk later.

Raw audio and the unedited transcript must always be kept (subject to configurable retention) so I can recover if cleanup goes wrong. Object storage must be **manually navigable in the AWS console**, not only understandable through a database.

The app will be on a **public domain / public repo**. Security (auth, keys, isolation) is paramount. Backend is serverless; frontend is on GitHub Pages. Deploy/teardown must be programmatic and CI-driven (passbook-style), safe to run in an AWS account that already has other projects. Per-deployment spend tracking must be available.

Eventually this may become a commercial product. **v1 is personal-first** (one user), designed so multi-tenant can be added later without a storage/API rewrite.

## 2. Goals (v1)

- Hands-busy capture on a phone PWA (start/stop **per note**, not one continuous multi-topic stream)
- Vague-description note matching: high confidence → auto-select; ambiguous → one large on-screen tap target (top matches + “New note”)
- Pipeline: record → STT → cleanup (system mode) → append to note
- Browse, edit, and download notes; download raw audio and raw/cleaned transcripts
- Cognito auth; no provider secrets in the client or repo
- Passbook-shaped ops: instance YAML, bootstrap + per-instance stacks, GitHub OIDC CI, `setup` / `teardown`, resource prefixes + cost tags

## 3. Non-goals (v1)

- Knowledge backlinks / Obsidian bulk export (paths stay markdown-friendly for later)
- Splitting one recording across multiple notes
- Commercial signup, billing, or multi-user product surface
- Native apps, CarPlay, Android Auto
- Real-time streaming transcription
- Process theater from the archived attempt (giant pre-product CI inventory, seeded gotcha registers, agent IAM boundary as a prerequisite to shipping value)

## 4. Architecture

```
GitHub Pages (PWA)
  /chintan/<instance>/     static frontend
        │ HTTPS + Cognito session / JWT
API Gateway  →  Lambda (Go, arm64)  →  DynamoDB (index / metadata / settings)
                                 └→  S3 (navigable objects: audio, raw STT, cleaned notes)

Shared bootstrap (once per AWS account + repo):
  artifact bucket, GitHub OIDC deploy role, naming/tag conventions for spend
```

**Instances** (passbook pattern): `config/instances/<name>.yaml` drives an isolated stack (`chintan-<name>-…`), own data, own API, own Pages path. Clone → `scripts/setup.sh` → add instance YAML → CI deploys. `teardown.sh` / per-instance cleanup remove only `chintan-*` resources.

**Approach:** product-first vertical slice with a non-negotiable public-domain security baseline. Infra is real and clone-deployable from day one, but we do not build months of platform before a working capture path.

## 5. Data model

### 5.1 S3 (content source of truth; console-navigable)

```
tenants/<userId>/notes/<noteId>/note.md
tenants/<userId>/notes/<noteId>/meta.json
tenants/<userId>/captures/<captureId>/audio.*
tenants/<userId>/captures/<captureId>/raw.txt
tenants/<userId>/captures/<captureId>/clean.txt
tenants/<userId>/captures/<captureId>/meta.json
```

Paths remain user-scoped even for a single-user deploy so commercialization does not force a storage rewrite.

`meta.json` for notes holds title, aliases (phrases used to find the note), timestamps. Capture meta holds `noteId`, cleanup mode used, status, timestamps.

### 5.2 DynamoDB (index only)

- **Notes:** id, title, aliases / description text, updated_at, S3 keys
- **Captures:** id → noteId, status, S3 keys, retention metadata
- **Settings:** cleanup mode (`faithful` | `polished`), retention policy (configurable; **default indefinite**)
- Auth identity is Cognito; app settings and indexes live in DynamoDB

The database must not be required to interpret stored content: opening S3 in the console should be enough to find audio and markdown.

### 5.3 Retention

Configurable retention for raw audio and transcripts; **default is indefinite**. Cleanup never deletes raw artifacts on success.

## 6. Capture and note flows

1. Authenticate
2. Describe the target note (voice or text) → rank by title + aliases + light content signals
3. High confidence → auto-select; else large tap list (candidates + “New note”)
4. Record → upload audio → STT → persist `raw.txt`
5. Cleanup with system mode → persist `clean.txt` → append to `note.md`
6. If the user used new descriptive phrases, update note aliases to improve future match

**Desk mode:** open a note, edit markdown, or start a voice append into that note.

**Cleanup modes (system setting):**

| Mode | Behavior |
|---|---|
| `faithful` | Fix STT garbling, punctuation, obvious grammar; keep phrasing and vocabulary |
| `polished` | Read like clean written notes; preserve meaning and technical terms; rephrase only when needed for clarity |

## 7. Auth, security, API

### 7.1 Security baseline

- Cognito User Pool **per instance**
- Email + password for v1; WebAuthn optional later
- Frontend holds only Cognito tokens — never STT/LLM keys
- API Gateway JWT authorizer on all note/audio routes
- CORS allowlist = that instance’s GitHub Pages origin only
- Provider keys in SSM `SecureString`; Lambda reads at runtime
- S3 private; short-lived presigned URLs for upload/download
- No secrets in the repository; deploy credentials via GitHub OIDC only
- Resource names prefixed `chintan-*`; tags include `Project=chintan`, `Instance=<name>` for spend tracking
- No transcript/audio content in application logs

### 7.2 API (v1)

| Area | Endpoints |
|---|---|
| Health | `GET /v1/health` |
| Settings | `GET/PUT /v1/settings` |
| Notes | `GET /v1/notes`, `POST /v1/notes`, `GET/PATCH/DELETE /v1/notes/{id}` |
| Match | `POST /v1/notes/match` `{ query }` → ranked candidates + confidence |
| Capture | `POST /v1/captures` (init + presigned upload); `POST /v1/captures/{id}/complete` (STT + cleanup + append) |
| Media | `GET /v1/captures/{id}/download?kind=audio\|raw\|clean` (presigned) |

Exact request/response schemas are fixed during implementation planning; behavior above is normative.

## 8. Errors and recovery

Capture is staged and resumable:

`uploaded → transcribed → cleaned → appended` (or `failed` at a stage)

- Audio upload success is independent of later STT/cleanup success
- Failed stages keep raw artifacts; retry from last good stage or download for manual recovery
- Low-confidence match never silently creates or overwrites a note — UI confirmation required
- Cleanup failure keeps `raw.txt` and does not mutate `note.md`
- Auth failure clears the client session

## 9. Ops (passbook-shaped)

- `scripts/setup.sh` — bootstrap stack + GitHub secrets/environment/Pages (idempotent; `--dry-run`)
- CI matrix over `config/instances/*.yaml` — build, test, deploy backend, publish frontend
- `scripts/teardown.sh` / per-instance cleanup — only `chintan-*` resources; dry-run default for destructive actions
- Cost: resource tags + documented AWS Budget setup; avoid alarm/SNS sprawl
- Another person cloning the repo must be able to deploy and tear down in their account alongside other projects

## 10. Testing

- Unit tests: match ranking, cleanup prompt contracts, S3 key layout, auth middleware
- Lean CI integration: health + authenticated note create/list (LocalStack or fixtures where full AWS is heavy)
- Manual acceptance before calling v1 done: phone PWA capture against a deployed instance (walk/drive-style)

## 11. Success criteria (v1)

On a phone, I can log in, describe a note vaguely, record, and later find cleaned text in the right note — with raw audio and raw transcript still downloadable. A third party can clone the repo, run setup, deploy an instance, see tagged spend, and tear down without affecting unrelated AWS resources.

## 12. Later (explicitly deferred)

- Obsidian-style auto backlinks / link suggestions
- Bulk markdown export packs
- Multi-tenant product, billing, self-service signup
- Stronger biometric unlock, richer desk editing, search UX polish

## 13. Relationship to archived work

Prior spec, Phase 0 implementation, docs, and README live on `archive/phase0-wip` (tag `archive/pre-clean-slate`). That attempt over-weighted process and platform before product. **Requirements intent** from that work (serverless, Pages, security, cost awareness, navigable storage, capture-and-file) still applies; **implementation and process** do not. This document is the clean-slate design.
