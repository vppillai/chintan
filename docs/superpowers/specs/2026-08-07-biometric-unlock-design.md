# Chintan biometric unlock (passbook-style) — Design

**Date:** 2026-08-07  
**Status:** Approved (approach A)  
**Reference:** `passbook` WebAuthn unlock (`backend/internal/service/webauthn.go`, `frontend/js/webauthn.js`)

## Goal

On mobile (and desktop platforms with a user-verifying platform authenticator), let a returning user unlock Chintan with Face ID / Touch ID / Windows Hello **without re-entering the Cognito password**, after a one-time Cognito Hosted UI login and optional enrollment.

First login / new devices / recovery still use Cognito password via Hosted UI.

## Non-goals

- Cognito native passkeys (IdP-managed WebAuthn)
- Replacing API Gateway JWT authorizer with opaque app sessions (passbook’s exact session model)
- Cross-device credential sync (each device enrolls separately)
- Changing PIN/password policy in Cognito

## Decision

**Approach A — WebAuthn + Cognito refresh-token vault**

1. Port passbook’s `go-webauthn` register/login ceremonies (platform authenticator, discoverable or allow-list as needed).
2. On successful enrollment (while Cognito-authenticated), store:
   - WebAuthn credential public key material (Dynamo)
   - Cognito **refresh token**, KMS-encrypted, bound to `sub` (+ credential id)
3. On biometric login success, Lambda runs Cognito `REFRESH_TOKEN_AUTH` with the vaulted refresh token and returns the same token shape the SPA already stores (`id_token`, `access_token`, `refresh_token`).
4. API Gateway JWT authorizer and existing Bearer/`sub` middleware stay unchanged.

## UX

| Moment | Behavior |
|--------|----------|
| Login screen | If this instance/device is marked enrolled (localStorage flag, passbook pattern), show a primary **Unlock with biometrics** button above “Sign In with Cognito”. |
| After Cognito login | Settings → **Biometric unlock** toggle. Off → On runs enroll ceremony; On → Off deletes credentials + vault. |
| Unsupported device | Toggle hidden or disabled with short help text; Cognito button only. |
| Failed assertion | Toast “Biometric unlock failed”; do **not** clear Cognito session / bounce as if JWT expired (passbook AUTH_ENDPOINTS pattern). |
| Refresh token revoked / expired | Biometric login fails with clear message; fall back to Cognito Hosted UI; clear local enrolled flag. |

## API (unauthenticated vs authenticated)

All under `/v1/auth/webauthn/…`. CORS same as existing API. JWT authorizer: **NONE** for login\* routes; **JWT** for register\* and DELETE.

| Method | Path | Auth | Purpose |
|--------|------|------|---------|
| POST | `/v1/auth/webauthn/register/options` | JWT | Begin enrollment |
| POST | `/v1/auth/webauthn/register` | JWT | Finish enrollment; vault refresh token from body |
| POST | `/v1/auth/webauthn/login/options` | none | Begin assertion |
| POST | `/v1/auth/webauthn/login` | none | Finish assertion; return Cognito tokens |
| DELETE | `/v1/auth/webauthn` | JWT | Disable; delete all creds + vault for `sub` |
| GET | `/v1/auth/webauthn/status` | JWT | `{ enrolled: bool }` for Settings toggle |

### Register finish body (extra vs passbook)

```json
{
  "challenge_id": "...",
  "credential": { /* standard WebAuthn attestation JSON */ },
  "refresh_token": "<cognito refresh token from current SPA session>"
}
```

The SPA already holds the refresh token after Hosted UI PKCE. Sending it only over HTTPS to our API during enroll is acceptable for personal v1; it is immediately vaulted and not logged.

### Login finish response

Same shape as current SPA token storage:

```json
{
  "id_token": "...",
  "access_token": "...",
  "refresh_token": "...",
  "expires_in": 3600,
  "token_type": "Bearer"
}
```

If Cognito rotates refresh tokens, persist the new refresh token in the vault.

## Data model (Dynamo, per user)

Reuse single-table `pk` / `sk`:

| pk | sk | Purpose |
|----|----|---------|
| `USER#<sub>` | `WACHAL#<challenge_id>` | In-flight ceremony (TTL ~5 min) |
| `USER#<sub>` | `WACRED#<cred_id_b64url>` | Credential + metadata |
| `USER#<sub>` | `WAREFRESH` | Encrypted Cognito refresh token blob + KMS key id / ciphertext |

Challenges are single-use (delete on consume). Credentials store go-webauthn-compatible public key JSON, sign count, and created_at.

**List-for-discoverable-login:** Query `pk=USER#*` is not feasible without GSI. Use **discoverable credentials (resident keys)** with user handle = Cognito `sub` bytes, **or** maintain a small global index:

| pk | sk | Purpose |
|----|----|---------|
| `WACREDLIST` | `WACRED#<cred_id>` | Mirror pointing at `USER#<sub>` for login/options allowCredentials / user lookup |

Prefer passbook’s `WACREDLIST` pattern so login/options can enumerate credentials without knowing `sub` up front (userless begin). On assertion, resolve `sub` from credential record.

## WebAuthn config

| Field | Value |
|-------|--------|
| RPID | Host of `ALLOWED_ORIGIN` (e.g. `vppillai.github.io`) |
| RPOrigins | Exact `ALLOWED_ORIGIN` (`https://vppillai.github.io`) |
| RPDisplayName | `Chintan` (or instance display name) |
| Authenticator | Platform, user verification **required** |
| User handle | Cognito `sub` (stable across enrollments for that account) |

Note: GitHub Pages path (`/chintan/dev/`) does not affect RPID; credentials are origin-scoped to the Pages host. Multiple Chintan instances on the same host share RPID — user handle/`sub` keeps accounts distinct; localStorage enrollment flag remains per `CHINTAN_INSTANCE`.

## Security

- Never log refresh tokens, attestation/assertion raw bodies, or KMS plaintext.
- Vault ciphertext only; decrypt only inside login finish.
- Delete vault + all `WACRED` on Settings disable and on Cognito user delete (if added later).
- Login endpoints: no JWT; rate-limit by IP (simple Dynamo counter or reuse passbook-style limiter if cheap; v1 minimum: Lambda reserved concurrency already limits blast radius — add IP rate limit if porting is small).
- Register endpoints require valid Cognito JWT; refresh token in body must belong to same `sub` (decode JWT `sub` and Cognito GetUser / token introspection via JWT claims only — refresh token is opaque; bind vault to JWT `sub` and on refresh Cognito returns tokens for that user; mismatch is Cognito’s problem if attacker supplies wrong refresh — **mitigation:** call Cognito refresh and verify resulting ID token `sub` matches the WebAuthn credential’s user handle before returning tokens / updating vault).
- Agent IAM boundary: Lambda already excepted for SSM; ensure KMS decrypt for vault key is allowed (customer-managed or `alias/aws/ssm`-style — prefer dedicated `alias/chintan-webauthn` or encrypt with AWS-managed Dynamo encryption + app-level AES via KMS data key).

**KMS choice (v1):** Use AWS Encryption SDK or `kms:GenerateDataKey` with CMK `alias/chintan/<instance>/token-vault` created in instance CFN; Lambda role decrypt only that key; permissions boundary must allow `kms:Decrypt` / `GenerateDataKey` for that ARN (extend ceiling like SSM).

## Frontend

- New `frontend/js/webauthn.js` — port passbook helpers (base64url, create/get, feature detect).
- `auth.js` — biometric button on login screen; after success `storeTokens`.
- `settings.js` — enroll/disable toggle + status fetch.
- `api.js` — webauthn methods; **do not** treat 401 from `/v1/auth/webauthn/login*` as session-expired.
- localStorage key: `chintan_biometric_<instance>` = `1` when enrolled on this browser (optimistic UI like passbook).
- Service worker: do not cache auth endpoints.

## Infra

- CFN: KMS key + alias; Lambda env `WEBAUTHN_RP_DISPLAY_NAME`, existing `ALLOWED_ORIGIN`; IAM for KMS.
- API Gateway: explicit routes for webauthn paths — **critical:** `$default` JWT would block unauthenticated login options. Add:
  - `POST /v1/auth/webauthn/login/options` Auth NONE
  - `POST /v1/auth/webauthn/login` Auth NONE  
  Register/status/DELETE can ride `$default` JWT **or** explicit JWT routes.
- Dependency: `github.com/go-webauthn/webauthn` (pin compatible with passbook if practical).

## Testing

- Unit: challenge TTL/single-use; register requires auth context; login rejects bad assertion; refresh `sub` mismatch fails; disable removes vault.
- Handler tests with fake WebAuthn service.
- Manual: iOS Safari PWA + Android Chrome — enroll, kill app, biometric unlock, disable, password login still works.

## Rollout

1. Backend + CFN deploy  
2. Frontend Pages deploy  
3. Existing Cognito user enrolls from Settings on phone  
4. Password unchanged until user chooses to rotate  

## Open points resolved in this spec

| Topic | Resolution |
|-------|------------|
| Session vs Cognito tokens | Cognito tokens via refresh vault (A) |
| Multi-user | `sub` as user handle + per-user Dynamo keys |
| Same GH Pages host | RPID shared; instance flag local; creds tied to `sub` |
| Passbook parity | UX + ceremony shape; not opaque sessions |
