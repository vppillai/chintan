# Auth

Who a request is from, and how the app knows. Two principals exist: a person
signed in through Cognito, whose id token is the bearer on every API call,
and a device holding an inbox key (`inbox.md`). Code: the browser half in
`frontend/src/features/auth/` (`oauth.ts`, `pkce.ts`, `pending.ts`,
`useAuth.ts`, `signOut.ts`, `passkeys.ts`, `identity.ts`) with the token set
in `frontend/src/api/{tokens,session}.ts`; the server half in
`backend/internal/auth/` (`verifier.go`, `jwks.go`, `identity.go`) behind
`middleware.Auth` (`backend/internal/middleware/auth.go`); the pool, its app
client and the gateway's JWT authorizer in `infrastructure/template.yaml`
(`UserPool`, `UserPoolClient`, `JwtAuthorizer`).

## Sign-in: the hosted UI with PKCE

The app has no login form. `beginSignIn` (`useAuth.ts`) mints a `state`
(16 random bytes) and a verifier (32 bytes), both from
`crypto.getRandomValues` and base64url-encoded (`pkce.ts`); hashes the
verifier to an S256 challenge; remembers `{state, verifier, returnTo,
startedAt}` under `localStorage['chintan.auth.pending.v1']`
(`pending.ts`, `rememberPending`); and sends the browser to
`<cognitoDomain>/oauth2/authorize` with `response_type=code`, the scopes
`openid email profile`, the state and the challenge (`authorizeUrl`).

The redirect URI is the app's base URL — `redirectUri()` renders
`BASE_URL` on the current origin, trailing slash included — because the app
client registers exactly one `CallbackURLs` entry,
`<SiteBaseUrl>/<path>/`, and Cognito matches `redirect_uri` byte for byte.
So the code arrives on the query string of whatever page is mounted, and
`useAuthGate` consumes it there: it reads `code`/`state` once before the
first paint (no flash of the signed-out screen), takes the pending entry
(`takePending`: read and remove in one step), strips the parameters from
the address bar with `history.replaceState` before anything can fail, checks
`pending.state === state` (the CSRF boundary), and redeems the code at
`/oauth2/token` with the verifier (`exchangeCode`). A `redeemed` ref latches
the attempt so StrictMode's double effect cannot burn a single-use code.

The pending entry is in `localStorage`, not `sessionStorage`: an iOS
home-screen app that switches to Messages for an MFA code is routinely
killed, and `sessionStorage` dies with it. It is bounded instead:
`PENDING_TTL_MS` is ten minutes and `parsePending` refuses anything older
(`pending.test.ts`, "the verifier survives the redirect and nothing longer").

An `error` on the callback (a cancelled login, a disabled account) counts
only when `hasPendingFlow(state)` says this device asked: the same
parameters on a link someone sent are not an outcome to report. The sentence
is chosen by the code alone (`describeAuthError`: one fixed sentence each
for `access_denied`, for `server_error`/`temporarily_unavailable`, and for
everything else); `error_description` is free text on a URL anyone can
compose and is never rendered.

The scopes are exactly `openid email profile`. Not
`aws.cognito.signin.user.admin`: with the token set in `localStorage`, an
access token carrying it would let any script that read it call
`DeleteUser` or strip the person's passkeys. `oauth.test.ts` ("asks for no
Cognito admin scope") and the client's `AllowedOAuthScopes` agree.

## Tokens: storage and refresh

`tokenSetFromWire` (`tokens.ts`) is the one function that reads the
snake_case wire shape; everything else sees a `TokenSet` (`idToken`,
`accessToken`, `refreshToken`, `expiresAt`, `tokenType`) stored under
`localStorage['chintan.tokens.v2']` (`TOKEN_STORAGE_KEY`). A stored shape
that fails `isTokenSet` is discarded, not coerced: half a token set
authenticates and cannot refresh. The bearer is the **id token**
(`bearerHeader`), whose lifetime is 60 minutes (`IdTokenValidity`); the
access token is 60 minutes too and the refresh token
`RefreshTokenValidityDays`, thirty by default.

`Session` (`session.ts`) owns the set and refreshes single-flight: five
requests failing 401 at once produce one call to `/oauth2/token` with the
`refresh_token` grant (`CognitoRefresher`), whose answer omits the refresh
token, so the previous one is carried forward. The HTTP client
(`api/client.ts`) refreshes proactively when the token is within
`REFRESH_SKEW_MS` (two minutes, `api/tokens.ts`) of expiry and otherwise on the first 401,
once per request, replaying the same attempt with the same
`Idempotency-Key`. A refresh that fails because the network is down does not
clear the session (offline is not unauthenticated); one that Cognito refuses
does. A `generation` counter makes a refresh in flight during sign-out drop
its result rather than restore the session (`session.test.ts`, "signing out
beats a refresh that is already in flight").

The You screen reads the account from the id token's `email` and
`auth_time` claims (`identityFromIdToken`, `identity.ts`) without verifying
it — the API verifies on every request — and a token that is not a JWT
yields an empty identity, so the e2e stub's placeholder token renders
"Signed in" with no name.

## Passkeys: a hand-off, not a ceremony

The app cannot register a passkey itself: the pool's WebAuthn relying party
is its managed-login domain (`PasskeyRelyingPartyID`,
`WebAuthnRelyingPartyID`), and a page on the Pages host calling
`navigator.credentials.create` for that RP is refused by the browser. When
`PasskeyRelyingPartyID` is set (`HasPasskeys`) the pool allows `PASSWORD`
and `WEB_AUTHN` as first factors with user verification required, and
otherwise `PASSWORD` alone; the client enables `ALLOW_USER_AUTH` so the
hosted page can offer a passkey that exists; nothing offers to create one
after a password sign-in. So `passkeyAddUrl` (`passkeys.ts`) sends the
browser to `<cognitoDomain>/passkeys/add?client_id=…&redirect_uri=<base>`,
where the ceremony runs on its own origin, and the page returns to the base
URL with `?result=success` or, with no live managed-login session,
`?result=invalid_session`.

`usePasskeyReturn` catches that parameter on first mount, replaces the
library entry with a clean one and pushes You with `?passkey=<result>`, so
Back lands on the library; `PasskeyCard` on You renders one sentence per
outcome (an unknown value is reported as `other`, not dropped) and, for
`invalid_session`, offers `beginSignIn` again. `PasskeyNudge` is one 44 px
row at the top of the library, shown once there is at least one note, on a
browser with `PublicKeyCredential`, in a configured build; its dismiss
action and a `success` return both write
`localStorage['chintan.passkey.nudge.v1']`
(`dismissNudge`), per device because a passkey is per device. Tests:
`passkeys.test.ts`, `PasskeyCard.test.tsx`, `e2e/passkey.spec.ts`.

## The verifier

`middleware.Auth` runs in-process on every route the gateway's
`JwtAuthorizer` already guards; the gateway is not guaranteed to be the only
ingress. There is no header identity path: an `X-User-ID` is ignored with a
token and rejected without one (`TestAuthIgnoresUserIDHeader`,
`TestAuthRejectsUserIDHeaderAlone`), and a nil verifier fails closed
(`TestAuthFailsClosedWithNilVerifier`). `BearerToken` reads the
`Authorization` header with a case-insensitive scheme and is shared with the
inbox.

`CognitoVerifier.Verify` (`verifier.go`) parses with `RS256` pinned, the
issuer `https://cognito-idp.<region>.amazonaws.com/<pool>`
(`NewCognitoIssuer`), `exp` required, `iat` checked, 30 s leeway; then
checks `token_use`: an `id` token must carry the app client in `aud`, an
`access` token in `client_id`, and any other value is refused — accepting
either without the switch would let an access token satisfy an audience
check it is not subject to. Every failure is `ErrUnauthenticated` and the
caller sees "authentication required" with no cause: a precise error is a
probing oracle (`TestVerifyRejects` lists the cases).

**The tenant id is the token's `sub`.** `Verify` returns
`Identity{UserID: sub, TenantID: sub}` (`identity.go`); every storage key
derives from `TenantID`, and nothing below the `auth` package keys on
`UserID`. A restore operator reading `USER#<id>` rows (`data-model.md`) is
reading Cognito subjects: a user recreated in a new pool gets a new `sub`
and sees none of the old rows.

Keys come from `<issuer>/.well-known/jwks.json` through `keySet`
(`jwks.go`): cached for `defaultJWKSTTL` (one hour), refetched on an unknown
`kid` at most once per `minRefreshInterval` (30 s) so garbage key ids cannot
turn every request into a round trip, capped at `maxJWKSBytes` (1 MiB), and
a failed or empty fetch keeps the good keys
(`TestKeySetRateLimitsUnknownKidRefresh`,
`TestKeySetEmptyDocumentDoesNotDiscardGoodKeys`). `Warm` fetches the set
from `cmd/api`'s init so the first request on a fresh container does not pay
the round trip (`TestWarmFetchesTheKeySetSoTheFirstVerifyDoesNot`).

The inbox routes sit outside the authorizer (`AuthorizationType: NONE`): the
handler validates the device key and enters the same context through
`middleware.WithUserID(ctx, device.TenantID)`, so from the service layer down
a device request is indistinguishable from the person's own (`inbox.md`).

## The pool

`UserPool` admits nobody who is not created by an administrator
(`AllowAdminCreateUserOnly: true`; `scripts/setup.sh` creates the owner).
Passwords are at least 12 characters with upper, lower, digit and symbol,
and a temporary one lives three days (`TemporaryPasswordValidityDays`).
MFA is `OPTIONAL` with TOTP as the one method (`SOFTWARE_TOKEN_MFA`);
recovery is by verified email only. `PreventUserExistenceErrors: ENABLED`
makes a wrong user and a wrong password the same answer. Threat protection
(`AdvancedSecurityMode: ENFORCED`) is declared only when `CognitoTier` is
`PLUS` (`ThreatProtectionAvailable`), because the setting is refused on an
`ESSENTIALS` pool. In front of the Lambda, the gateway's `JwtAuthorizer`
reads the `Authorization` header and checks the audience (`UserPoolClient`)
and the issuer (the pool) before `middleware.Auth` repeats the check in
process. `index.html` declares no Content-Security-Policy while the token
set lives in `localStorage`, so the scope rule above is what bounds a
script that reads it.

## Sign-out

`performSignOut` (`signOut.ts`) leaves the device with nothing and Cognito
with no session, in this order: `session.clear()` and `clearPending()`;
`queryClient.clear()`; `clearAllLocalData()` (the cached corpus, buffered
audio and the edit queue, `offline/db.ts`); every `sessionStorage` key under
`chintan.` and the personal `localStorage` keys (`PERSONAL_LOCAL_KEYS`: the
Ask cost note and the two filing sets — the theme and the passkey nudge stay,
they are the device's); then `POST /oauth2/revoke` with the refresh token,
sent with `keepalive` and not awaited; then the browser goes to
`<cognitoDomain>/logout?client_id=…&logout_uri=<base>` (`logoutUrl`),
which ends the hosted UI's own cookie — without it the next Sign in would
pass straight through. The revoke is what makes the clear mean anything on a
lost phone: `/logout` ends a cookie, the revoke ends the thirty-day grant
(`EnableTokenRevocation: true`). An id token already copied out still passes
the stateless check for at most its remaining hour.

Before the confirm, `readUnsentWork` counts unconfirmed captures in
IndexedDB and queued edits, because an unconfirmed recording exists in
exactly one place; the dialog names what would be lost
(`gate.test.tsx`, "the sign-out confirmation names what it destroys";
`signOut.test.ts`; `e2e/auth.spec.ts`).

## Test conventions

Unit and e2e tests never hold a Cognito credential. `e2e/fixtures.ts` seeds
`chintan.tokens.v2` with placeholder strings and an hour's expiry and
answers `**/api/**` itself; the Go tests build tokens with a key pair made
in the test and a JWKS served by `httptest`. The live QA harness
(`scripts/qa/`) signs in with a review tokens file for a test user on the
staging stack, refreshed through the refresh-token grant with no password
(`refresh-tokens.sh`), and passes the id token to curl through a header
file, never on argv. The owner's tenant is not a test target.

History: `docs/backlog.md`.
