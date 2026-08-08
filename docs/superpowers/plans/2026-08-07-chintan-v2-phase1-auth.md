# Phase 1 — Authentication and Identity

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Replace header-trusting, signature-free identity extraction with real JWKS verification, and introduce a tenant-scoped `Identity` that later phases thread through every storage call.

**Architecture:** A new `internal/auth` package owns token verification and the context carrier. `internal/middleware` becomes a thin HTTP adapter over it. Verification runs in-process regardless of the API Gateway authorizer, because the gateway is not the only ingress and never sanitised request headers.

**Tech Stack:** Go 1.25, `github.com/golang-jwt/jwt/v5` (already an indirect dependency — promoted to direct), standard-library HTTP and RSA.

## Global Constraints

See `2026-08-07-chintan-v2-master.md`. Restated for this phase:

- `export PATH=/usr/local/go/bin:$PATH` before every Go command.
- Branch `feat/v2`. Never deploy. Never push to `main`.
- No test-only bypass may exist in the request path.

## The defect this phase closes

`backend/internal/middleware/auth.go:37-40` reads an unverified `X-User-ID` header and prefers it over the JWT `sub`. `backend/internal/middleware/cors.go:19` advertises that header to browsers. `auth.go:50-75` then base64-decodes the token payload with no signature, `iss`, `aud`, or `exp` check. Any holder of a valid token can act as any other user by setting one header.

**File structure**

| File | Responsibility |
|---|---|
| Create `internal/auth/identity.go` | `Identity` type; context put/get |
| Create `internal/auth/verifier.go` | `Verifier` interface; `CognitoVerifier` with claim validation |
| Create `internal/auth/jwks.go` | JWKS fetch, in-memory cache with TTL, `kid`-miss refresh |
| Create `internal/auth/verifier_test.go` | Signed-token table tests including the impersonation regression |
| Create `internal/auth/jwks_test.go` | Cache hit, TTL expiry, `kid`-miss refetch, fetch failure |
| Rewrite `internal/middleware/auth.go` | HTTP adapter only — no parsing, no header trust |
| Modify `internal/middleware/cors.go` | Drop `X-User-ID`; reject wildcard origin |
| Modify `internal/handler/router.go` | Router takes a `Verifier` |
| Modify `cmd/api/main.go` | Construct the verifier; fail closed if unconfigured |
| Create `internal/repository/isolation_test.go` | Cross-tenant test below the HTTP layer |

**Interfaces**

- Produces, consumed by every later phase:

```go
package auth

type Identity struct {
    UserID   string
    TenantID string
}

func WithIdentity(ctx context.Context, id Identity) context.Context
func FromContext(ctx context.Context) (Identity, bool)

type Verifier interface {
    Verify(ctx context.Context, rawToken string) (Identity, error)
}

func NewCognitoVerifier(issuer, clientID string, hc *http.Client) (*CognitoVerifier, error)
```

```go
package middleware

// Auth returns middleware that verifies the bearer token and stores the Identity.
func Auth(v auth.Verifier) func(http.Handler) http.Handler
```

- Consumes: nothing from earlier phases.

---

### Task 1: Identity type and context carrier

- [ ] **Step 1: Write the failing test** — `internal/auth/identity_test.go`

```go
func TestIdentityRoundTripsThroughContext(t *testing.T) {
	ctx := WithIdentity(context.Background(), Identity{UserID: "u1", TenantID: "u1"})
	got, ok := FromContext(ctx)
	if !ok || got.UserID != "u1" || got.TenantID != "u1" {
		t.Fatalf("got %+v ok=%v", got, ok)
	}
}

func TestFromContextReportsAbsence(t *testing.T) {
	if _, ok := FromContext(context.Background()); ok {
		t.Fatal("expected no identity in a bare context")
	}
}
```

- [ ] **Step 2: Run and watch it fail.** `go test ./internal/auth/ -run TestIdentity -v` → build failure, undefined `WithIdentity`.
- [ ] **Step 3: Implement `identity.go`** with an unexported context key type.
- [ ] **Step 4: Run and watch it pass.**
- [ ] **Step 5: Commit.**

### Task 2: JWKS cache

- [ ] **Step 1: Write failing tests** — `internal/auth/jwks_test.go`. Serve a JWKS from `httptest.Server` with a counter. Assert: first `Key(kid)` fetches once; second returns cached without refetch; an unknown `kid` triggers exactly one refetch; after TTL expiry a refetch occurs; a 500 from the endpoint returns an error and does not poison the cache.
- [ ] **Step 2: Run and watch them fail.**
- [ ] **Step 3: Implement `jwks.go`** — `keySet` with `sync.RWMutex`, `fetchedAt`, TTL, single-flight refresh, RSA public key construction from `n`/`e`.
- [ ] **Step 4: Run and watch them pass.**
- [ ] **Step 5: Commit.**

### Task 3: Cognito verifier

- [ ] **Step 1: Write failing tests** — `internal/auth/verifier_test.go`. Generate an RSA key in the test, serve its public half as JWKS, mint tokens with `golang-jwt`. Cases, each asserting a distinct error:

| Case | Expectation |
|---|---|
| Valid ID token (`token_use=id`, `aud=clientID`) | `Identity{UserID: sub, TenantID: sub}` |
| Valid access token (`token_use=access`, `client_id=clientID`) | accepted |
| `alg: none` | rejected |
| Signed by a different RSA key | rejected |
| `exp` in the past | rejected |
| `iss` mismatch | rejected |
| `aud` mismatch on an ID token | rejected |
| `client_id` mismatch on an access token | rejected |
| Missing `token_use` | rejected |
| Empty `sub` | rejected |
| Unknown `kid` not in JWKS | rejected |

- [ ] **Step 2: Run and watch them fail.**
- [ ] **Step 3: Implement `verifier.go`.** Restrict accepted algorithms to RS256 explicitly — do not rely on library defaults.
- [ ] **Step 4: Run and watch them pass.**
- [ ] **Step 5: Commit.**

### Task 4: Middleware rewrite and the impersonation regression test

- [ ] **Step 1: Write the failing regression test** — `internal/middleware/auth_test.go`, replacing the existing file:

```go
// The v1 build trusted X-User-ID over the token. This asserts it cannot come back.
func TestAuthIgnoresUserIDHeader(t *testing.T) {
	v := stubVerifier{id: auth.Identity{UserID: "victim-owner", TenantID: "victim-owner"}}
	var seen auth.Identity
	h := Auth(v)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = auth.FromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/v1/notes", nil)
	req.Header.Set("Authorization", "Bearer valid-token")
	req.Header.Set("X-User-ID", "attacker-target")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if seen.UserID != "victim-owner" {
		t.Fatalf("header overrode verified identity: %q", seen.UserID)
	}
}
```

  Plus: missing `Authorization` → 401; malformed scheme → 401; verifier error → 401 and handler never runs.

- [ ] **Step 2: Run and watch it fail.**
- [ ] **Step 3: Rewrite `middleware/auth.go`.** Delete `userIDFromBearer` entirely. Keep `WithUserID`/`GetUserID` only as thin delegates so the 30 existing handler-test call sites keep compiling; they inject through context, which no remote caller can reach.
- [ ] **Step 4: Run and watch it pass.** Then `go test ./...` — the whole suite must stay green.
- [ ] **Step 5: Commit.**

### Task 5: CORS hardening

- [ ] **Step 1: Write failing tests** — `internal/middleware/cors_test.go`: `X-User-ID` must not appear in `Access-Control-Allow-Headers`; a disallowed origin gets no `Access-Control-Allow-Origin`; the allowed origin gets exactly itself.
- [ ] **Step 2: Run and watch them fail.**
- [ ] **Step 3: Implement.** Remove `X-User-ID` from the header allowlist and remove the `allowedOrigin == "*"` reflection branch.
- [ ] **Step 4: Run and watch them pass.**
- [ ] **Step 5: Commit.**

### Task 6: Wiring and fail-closed configuration

- [ ] **Step 1:** Change `handler.NewRouter` to accept `auth.Verifier` and apply `middleware.Auth(v)`. Note the WebAuthn login routes stay unauthenticated by design.
- [ ] **Step 2:** In `cmd/api/main.go`, build the verifier from `COGNITO_ISSUER` (or `AWS_REGION` + `USER_POOL_ID`) and `USER_POOL_CLIENT_ID`. Missing configuration is fatal — auth may not silently degrade. Validate `ALLOWED_ORIGIN` is a concrete origin and reject `*` at startup.
- [ ] **Step 3:** Replace `service.subFromIDToken` in `service/webauthn.go` with the same verifier, so the KMS-sealed refresh token is no longer guarded by an unverified parse.
- [ ] **Step 4:** `go build ./... && go vet ./... && go test -race ./...` all green; `gofmt -l .` prints nothing.
- [ ] **Step 5: Commit.**

### Task 7: Cross-tenant isolation test

- [ ] **Step 1: Write the failing test** — `internal/repository/isolation_test.go`. Using `MemoryStore`, seed a note for tenant A, then assert every read, list, update, archive, and delete path invoked as tenant B returns not-found and leaves A's data intact. This is the test that would have caught the v1 defect.
- [ ] **Step 2: Run.** It should pass against the current store — its purpose is to lock the property down before Phase 2 rewrites the repository.
- [ ] **Step 3:** Add the same assertion at the HTTP layer in `handler_test.go` with two distinct identities.
- [ ] **Step 4:** Full suite green.
- [ ] **Step 5: Commit.**

## Self-review

- Spec §4.1 covered by Tasks 1–6; §9 isolation requirement by Task 7.
- No placeholders: every task names exact files and exact assertions.
- Type consistency: `Identity`, `Verifier`, `WithIdentity`, `FromContext`, `Auth(v)` used identically across tasks and in the master plan's Global Constraints.
