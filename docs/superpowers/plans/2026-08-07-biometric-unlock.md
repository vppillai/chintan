# Biometric Unlock Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add passbook-style Face ID / Touch ID unlock that returns Cognito tokens via a KMS-encrypted refresh-token vault, without changing API Gateway JWT authorization for existing APIs.

**Architecture:** Port passbook WebAuthn ceremonies (`go-webauthn`). Enroll while Cognito-authenticated; vault refresh token. Biometric login verifies assertion, refreshes Cognito tokens, returns them to the SPA. Unauthenticated login routes are explicit API Gateway routes with Auth NONE.

**Tech Stack:** Go Lambda, `github.com/go-webauthn/webauthn`, DynamoDB single-table, KMS, Cognito `REFRESH_TOKEN_AUTH`, vanilla JS PWA on GitHub Pages.

**Spec:** `docs/superpowers/specs/2026-08-07-biometric-unlock-design.md`

## Global Constraints

- RPID = host of `ALLOWED_ORIGIN`; RPOrigins = exact `ALLOWED_ORIGIN`; RPDisplayName = `Chintan`
- User handle = Cognito `sub`
- Never log refresh tokens or WebAuthn raw payloads
- Login `401` must not clear SPA session / trigger “session expired” redirect
- Keep passbook-* AWS resources untouched; day-to-day ops via `AWS_PROFILE=chintan`
- Password rotation deferred until user requests it

## File map

| File | Responsibility |
|------|----------------|
| `backend/internal/model/types.go` | WebAuthn + vault structs |
| `backend/internal/repository/store.go` (+ memory/dynamo) | Challenge/cred/vault/list CRUD |
| `backend/internal/service/webauthn.go` | Ceremonies + Cognito refresh |
| `backend/internal/handler/webauthn.go` | HTTP routes |
| `backend/internal/handler/router.go` | Wire routes (auth split) |
| `backend/cmd/api/main.go` | Init WebAuthn + KMS + Cognito IDP client |
| `infrastructure/template.yaml` | KMS key, IAM, explicit NONE routes |
| `frontend/js/webauthn.js` | Browser WebAuthn helpers |
| `frontend/js/auth.js` / `settings.js` / `api.js` / `index.html` | UX |
| `frontend/sw.js` | Bump cache; don’t cache auth |

---

### Task 1: Dynamo repository for WebAuthn + vault

**Files:**
- Modify: `backend/internal/model/types.go`
- Modify: `backend/internal/repository/store.go`
- Modify: `backend/internal/repository/memory.go`
- Modify: `backend/internal/repository/dynamo.go`
- Test: `backend/internal/repository/memory_test.go` (or new `webauthn_store_test.go`)

- [ ] **Step 1: Write failing tests** for Put/Get/Delete challenge (TTL field), Put/Get/List credentials, WACREDLIST mirror, Put/Get/Delete refresh vault blob on MemoryStore

- [ ] **Step 2: Run tests — expect FAIL**

- [ ] **Step 3: Extend `Store` interface + MemoryStore + DynamoStore** with methods:
  - `PutWebAuthnChallenge` / `GetWebAuthnChallenge` / `DeleteWebAuthnChallenge`
  - `PutWebAuthnCredential` / `GetWebAuthnCredential` / `ListWebAuthnCredentials` / `DeleteAllWebAuthnCredentials`
  - `PutRefreshVault` / `GetRefreshVault` / `DeleteRefreshVault`
  - SK prefixes per spec (`WACHAL#`, `WACRED#`, `WAREFRESH`, global `WACREDLIST`)

- [ ] **Step 4: Run tests — expect PASS**

- [ ] **Step 5: Commit**
  ```bash
  git add backend/internal/model backend/internal/repository
  git commit -m "Add Dynamo/memory storage for WebAuthn credentials and refresh vault."
  ```

---

### Task 2: WebAuthn service (ceremonies + Cognito refresh)

**Files:**
- Create: `backend/internal/service/webauthn.go`
- Create: `backend/internal/service/webauthn_test.go`
- Modify: `backend/go.mod` / `go.sum` (`github.com/go-webauthn/webauthn`)

- [ ] **Step 1: Add dependency** `go get github.com/go-webauthn/webauthn@latest` (pin after resolve)

- [ ] **Step 2: Write failing tests** with mocked store + fake Cognito refresher interface:
  - BeginRegistration requires userID
  - FinishRegistration stores cred + vault
  - BeginLogin with no creds → `ErrWebAuthnNotEnrolled`
  - FinishLogin success calls refresh; rejects if ID token `sub` ≠ credential user
  - Disable deletes creds + vault

- [ ] **Step 3: Implement `WebAuthnService`** mirroring passbook (`BeginRegistration`, `FinishRegistration`, `BeginLogin`, `FinishLogin`, `Disable`, `Status`) plus:
  - `RefreshTokenClient` interface (`Refresh(ctx, refreshToken) (TokenSet, error)`)
  - `TokenVault` encrypt/decrypt via injected `Seal`/`Open` (KMS wrapper interface for tests)

- [ ] **Step 4: Tests PASS**

- [ ] **Step 5: Commit**
  ```bash
  git commit -am "Add WebAuthn service with Cognito refresh-token vault."
  ```

---

### Task 3: HTTP handlers + router + Lambda wiring

**Files:**
- Create: `backend/internal/handler/webauthn.go`
- Modify: `backend/internal/handler/router.go`
- Modify: `backend/cmd/api/main.go`
- Modify: `backend/internal/middleware/auth.go` only if needed for public paths under CORS

- [ ] **Step 1: Handler tests** for status codes (401 login fail ≠ session semantics; 400 expired challenge; 503 if service nil)

- [ ] **Step 2: Implement handlers** and register:
  - Public: `POST /v1/auth/webauthn/login/options`, `POST /v1/auth/webauthn/login`
  - Auth: register options/register, DELETE, GET status
  - Wire in `NewRouter` with optional `*service.WebAuthnService`

- [ ] **Step 3: `main.go`** init WebAuthn from `ALLOWED_ORIGIN`, Cognito client from env (`USER_POOL_CLIENT_ID` or existing), KMS seal; soft-fail (nil service) if misconfigured so password login still works

- [ ] **Step 4: `go test ./...` + `gofmt`**

- [ ] **Step 5: Commit**
  ```bash
  git commit -am "Expose WebAuthn HTTP API and wire Lambda startup."
  ```

---

### Task 4: CloudFormation — KMS + explicit Auth NONE routes

**Files:**
- Modify: `infrastructure/template.yaml`
- Modify: `infrastructure/agent-policies/boundary.json` if KMS key ARN must be allowed (publish with root carefully only if needed)

- [ ] **Step 1: Add** `AWS::KMS::Key` + Alias `alias/chintan-${InstanceName}/token-vault`

- [ ] **Step 2: Lambda role** `kms:Encrypt|Decrypt|GenerateDataKey` on that key; env `TOKEN_VAULT_KMS_KEY_ID`, `USER_POOL_CLIENT_ID`, `WEBAUTHN_RP_DISPLAY_NAME`

- [ ] **Step 3: Add API routes** (Auth NONE) for login options + login; keep JWT `$default` for the rest

- [ ] **Step 4: Commit**
  ```bash
  git commit -am "Add token-vault KMS key and public WebAuthn API routes."
  ```

---

### Task 5: Frontend WebAuthn + login/settings UX

**Files:**
- Create: `frontend/js/webauthn.js`
- Modify: `frontend/js/api.js`, `auth.js`, `settings.js`, `app.js`, `index.html`, `css/styles.css`, `sw.js`

- [ ] **Step 1: Port** passbook base64url + create/get helpers into `webauthn.js`

- [ ] **Step 2: `api.js`** methods; exclude webauthn login paths from session-expired on 401

- [ ] **Step 3: Login screen** biometric button when `chintan_biometric_<instance>` set and platform authenticator available

- [ ] **Step 4: Settings** toggle enroll/disable using refresh token from `auth.getStoredTokens()`

- [ ] **Step 5: Bump SW cache to v4; skip caching `/v1/auth/`

- [ ] **Step 6: Commit**
  ```bash
  git commit -am "Add biometric unlock UI and WebAuthn client helpers."
  ```

---

### Task 6: Deploy and verify

- [ ] **Step 1: Push `main`**; wait for Deploy Backend + Frontend

- [ ] **Step 2: Confirm** OPTIONS/login routes Auth NONE via `apigatewayv2 get-routes`

- [ ] **Step 3: Manual** — desktop or phone: Cognito login → Settings enable biometrics → sign out → biometric unlock → disable

- [ ] **Step 4: Confirm** passbook stacks untouched

- [ ] **Step 5: Commit any fixups**; note password still not rotated (user preference)

---

## Done when

- Enrolled device unlocks with platform biometrics and receives working Cognito tokens
- Unenrolled / new device still uses Hosted UI password
- Existing notes/capture APIs unchanged behind JWT
- Unit tests cover store + service happy path and `sub` mismatch
