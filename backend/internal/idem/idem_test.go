package idem

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/clock"
	"github.com/vppillai/chintan/backend/internal/keys"
	"github.com/vppillai/chintan/backend/internal/logging"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// The negative cases carry the weight here. A store that answers correctly for a
// well-behaved client but also answers *something* when a key is reused, when storage is
// unhealthy, or when the stored record is malformed is a store that creates the duplicate
// capture it was built to prevent — silently, and in the corpus rather than in a test.
// So each test asserts the reason, not merely that an error appeared.
//
// The write paths (Complete, Abandon) get the same treatment as the read path, because
// that is where the demonstrated failures were: an unguarded Abandon deleting a completed
// record, and a Complete rewriting the fingerprint the reservation was taken under. Both
// re-permit the duplicate, and neither had a test.

const testTTLHours = 24

var (
	fixedNow = time.Date(2026, 8, 5, 10, 30, 0, 0, time.UTC)
	errBoom  = errors.New("storage is unavailable")
)

func newStore(t *testing.T, repo repository.Repository, clk clock.Clock) *Store {
	t.Helper()
	s, err := New(repo, clk, logging.New(), testTTLHours)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func req(tenant, key, body string) Request {
	return Request{
		Tenant:      keys.TenantID(tenant),
		Key:         key,
		Fingerprint: Fingerprint("POST", "/v1/captures", []byte(body)),
	}
}

// begin claims a key and returns the attempt token, failing the test unless the caller
// was made the owner. Most tests need the token, and threading it by hand obscures what
// they are actually asserting.
func begin(t *testing.T, s *Store, r Request) string {
	t.Helper()
	res, err := s.Begin(context.Background(), r)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if res.State != StateNew {
		t.Fatalf("Begin gave state %q, want %q", res.State, StateNew)
	}
	if res.Token == "" {
		t.Fatal("Begin returned no attempt token; without one the caller cannot prove ownership to Complete or Abandon")
	}
	return res.Token
}

func storedRecord(t *testing.T, repo repository.Repository, r Request) record {
	t.Helper()
	key, err := keys.Idempotency(r.Tenant, r.Key)
	if err != nil {
		t.Fatalf("building the key: %v", err)
	}
	it, err := repo.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	rec, err := decode(it)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return rec
}

// movableClock is a clock a test can advance. clock.Fixed returns a new value from
// Advance rather than mutating, so a Store holding one cannot have its time moved; this
// wrapper is what lets a test observe that Complete recomputes the TTL from when the
// response was produced while leaving the claim time alone.
type movableClock struct{ f clock.Fixed }

func (m *movableClock) Now() time.Time          { return m.f.Now() }
func (m *movableClock) advance(d time.Duration) { m.f = m.f.Advance(d) }

// ---------------------------------------------------------------------------
// The property §Phase 0 acceptance names
// ---------------------------------------------------------------------------

func TestReplayReturnsTheOriginalOutcomeAndDoesTheWorkOnce(t *testing.T) {
	ctx := context.Background()
	repo := repository.NewMemory()
	s := newStore(t, repo, clock.Fixed{T: fixedNow})
	r := req("t1", "k-abc", `{"label":"x"}`)

	token := begin(t, s, r)

	// The operation ran and produced a capture.
	want := Outcome{Status: 201, Resource: "cap_01H"}
	if err := s.Complete(ctx, r, token, want); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	replay, err := s.Begin(ctx, r)
	if err != nil {
		t.Fatalf("replay Begin: %v", err)
	}
	if replay.State != StateCompleted {
		t.Fatalf("replay gave state %q, want %q", replay.State, StateCompleted)
	}
	// "Already seen" is not an acceptable answer: a client that retried because it never
	// got a response needs the response.
	if replay.Outcome != want {
		t.Errorf("replay outcome = %+v, want %+v; a retry must be answered with the original result", replay.Outcome, want)
	}
	if replay.StartedAt != clock.RFC3339UTC(fixedNow) {
		t.Errorf("replay StartedAt = %q, want the claim time %q", replay.StartedAt, clock.RFC3339UTC(fixedNow))
	}
	// A replay holds no reservation, so handing it a token would invite a handler to
	// abandon or re-complete an operation it did not perform.
	if replay.Token != "" {
		t.Errorf("replay carried an attempt token %q; it owns nothing", replay.Token)
	}

	// One record, not two. The acceptance criterion is "exactly one capture", and the
	// storage-level equivalent is that the replay wrote nothing new.
	if n := repo.Len(); n != 1 {
		t.Errorf("repository holds %d items after a replay, want 1", n)
	}
}

func TestAnInFlightDuplicateIsNotACompletedOne(t *testing.T) {
	ctx := context.Background()
	s := newStore(t, repository.NewMemory(), clock.Fixed{T: fixedNow})
	r := req("t1", "k-abc", "{}")

	begin(t, s, r)

	second, err := s.Begin(ctx, r)
	if err != nil {
		t.Fatalf("second Begin: %v", err)
	}
	if second.State != StateInFlight {
		t.Fatalf("second Begin gave state %q, want %q", second.State, StateInFlight)
	}
	// The distinction is the point of the test: an in-flight duplicate has no result, and
	// a caller handed a zero Outcome that it mistook for a completed one would answer 200
	// naming a resource that does not exist.
	if second.Outcome != (Outcome{}) {
		t.Errorf("in-flight reservation carried an outcome %+v; there is no result yet", second.Outcome)
	}
	// No token either: an attempt told 409 holds nothing, and the `defer` cleanup pattern
	// handlers write would otherwise delete the owner's live reservation.
	if second.Token != "" {
		t.Errorf("an in-flight conflict carried attempt token %q; it does not hold the reservation", second.Token)
	}
}

func TestConcurrentBeginsGiveExactlyOneOwner(t *testing.T) {
	// This is the race a read-then-write loses: both attempts see "absent" and both
	// create a capture. PutOnce is a conditional write, so exactly one attempt may win no
	// matter how the calls interleave.
	ctx := context.Background()
	s := newStore(t, repository.NewMemory(), clock.Fixed{T: fixedNow})
	r := req("t1", "k-race", "{}")

	const attempts = 32
	var (
		mu       sync.Mutex
		counts   = map[State]int{}
		tokens   = map[string]int{}
		firstErr error
		wg       sync.WaitGroup
	)
	wg.Add(attempts)
	for i := 0; i < attempts; i++ {
		go func() {
			defer wg.Done()
			res, err := s.Begin(ctx, r)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			counts[res.State]++
			if res.Token != "" {
				tokens[res.Token]++
			}
		}()
	}
	wg.Wait()

	if firstErr != nil {
		t.Fatalf("concurrent Begin returned an error: %v", firstErr)
	}
	if counts[StateNew] != 1 {
		t.Errorf("%d of %d concurrent attempts were told they own the operation, want exactly 1", counts[StateNew], attempts)
	}
	if counts[StateInFlight] != attempts-1 {
		t.Errorf("%d attempts saw in-flight, want %d", counts[StateInFlight], attempts-1)
	}
	// One token, held by the one owner. A token shared between attempts would make the
	// ownership checks on Complete and Abandon pass for the wrong caller.
	if len(tokens) != 1 {
		t.Errorf("%d distinct attempt tokens were handed out, want 1", len(tokens))
	}
}

// ---------------------------------------------------------------------------
// A committed reservation whose response was lost (the retry-safety defect)
// ---------------------------------------------------------------------------

// commitThenConflictRepo commits the conditional write and then reports
// ErrAlreadyExists.
//
// That is not a contrived failure: it is what the AWS SDK's default retryer produces
// when a PutItem with a condition expression commits and its response is lost to a
// network blip or a 5xx — the re-sent write finds the item it just created and returns
// ConditionalCheckFailedException, which repository maps to ErrAlreadyExists.
type commitThenConflictRepo struct {
	*repository.Memory
}

func (r commitThenConflictRepo) PutOnce(ctx context.Context, item repository.Item) error {
	if err := r.Memory.PutOnce(ctx, item); err != nil {
		return err
	}
	return fmt.Errorf("simulated SDK retry of a committed conditional write: %w", repository.ErrAlreadyExists)
}

func TestACommittedReservationIsRecognisedByTheAttemptThatWroteIt(t *testing.T) {
	// Without the attempt token this is a 409 the client can never clear: Begin reads back
	// its own record, matches its own fingerprint, and reports StateInFlight — so the only
	// party that could finish the operation is told it may not, for the whole 24h TTL,
	// while every other request works.
	ctx := context.Background()
	mem := repository.NewMemory()
	s := newStore(t, commitThenConflictRepo{Memory: mem}, clock.Fixed{T: fixedNow})
	r := req("t1", "k-lost-response", `{"label":"x"}`)

	res, err := s.Begin(ctx, r)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if res.State != StateNew {
		t.Fatalf("Begin gave state %q, want %q; the attempt that wrote the reservation must own it", res.State, StateNew)
	}
	if res.Token == "" {
		t.Fatal("Begin returned no attempt token on the self-recognised path")
	}
	if res.StartedAt != clock.RFC3339UTC(fixedNow) {
		t.Errorf("StartedAt = %q, want the claim time %q", res.StartedAt, clock.RFC3339UTC(fixedNow))
	}
	// The owner can then finish, which is the whole point: the key is not dead.
	if err := s.Complete(ctx, r, res.Token, Outcome{Status: 201, Resource: "cap_1"}); err != nil {
		t.Fatalf("Complete after a self-recognised reservation: %v", err)
	}

	// The self-heal must not extend to a *different* attempt: a genuine second caller still
	// has to be told the key is held, or the duplicate is back.
	other := newStore(t, mem, clock.Fixed{T: fixedNow})
	replay, err := other.Begin(ctx, r)
	if err != nil {
		t.Fatalf("second attempt Begin: %v", err)
	}
	if replay.State != StateCompleted {
		t.Fatalf("second attempt saw %q, want %q", replay.State, StateCompleted)
	}
}

func TestASecondAttemptIsNotMistakenForTheReservationsOwner(t *testing.T) {
	// The dangerous direction for the self-heal above: if the stored token were empty, or
	// compared loosely, every attempt would recognise the record as its own and every
	// concurrent retry would be told StateNew.
	ctx := context.Background()
	mem := repository.NewMemory()
	first := newStore(t, mem, clock.Fixed{T: fixedNow})
	begin(t, first, req("t1", "k1", "{}"))

	// A record with no attempt attribute at all — what a reservation written by a deploy
	// before the token existed looks like.
	key, err := keys.Idempotency(keys.TenantID("t2"), "k2")
	if err != nil {
		t.Fatalf("building the key: %v", err)
	}
	r2 := req("t2", "k2", "{}")
	if err := mem.Put(ctx, repository.Item{Key: key, Attrs: map[string]any{
		attrState:       string(StateInFlight),
		attrFingerprint: r2.Fingerprint,
	}}); err != nil {
		t.Fatalf("seeding a tokenless reservation: %v", err)
	}

	second := newStore(t, commitThenConflictRepo{Memory: mem}, clock.Fixed{T: fixedNow})
	for name, r := range map[string]Request{
		"another attempt's token": req("t1", "k1", "{}"),
		"no stored token":         r2,
	} {
		t.Run(name, func(t *testing.T) {
			res, err := second.Begin(ctx, r)
			if err != nil {
				t.Fatalf("Begin: %v", err)
			}
			if res.State != StateInFlight {
				t.Fatalf("Begin gave %q, want %q; a caller that does not hold the reservation must not be told it owns the operation", res.State, StateInFlight)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tenant scoping (I11)
// ---------------------------------------------------------------------------

func TestTheSameClientKeyInTwoTenantsDoesNotCollide(t *testing.T) {
	// Client keys are usually UUIDs, but nothing stops a client sending "1". An unscoped
	// record would let one tenant's retry be answered with another tenant's resource ID —
	// the one bug a multi-tenant product cannot survive (I11).
	ctx := context.Background()
	s := newStore(t, repository.NewMemory(), clock.Fixed{T: fixedNow})

	a := req("tenant-a", "1", "{}")
	b := req("tenant-b", "1", "{}")

	tokenA := begin(t, s, a)
	if err := s.Complete(ctx, a, tokenA, Outcome{Status: 201, Resource: "cap_a"}); err != nil {
		t.Fatalf("Complete for tenant-a: %v", err)
	}

	res, err := s.Begin(ctx, b)
	if err != nil {
		t.Fatalf("Begin for tenant-b: %v", err)
	}
	if res.State != StateNew {
		t.Fatalf("tenant-b saw state %q for its own first request; the key is not tenant-scoped (I11)", res.State)
	}
	if res.Outcome.Resource != "" {
		t.Errorf("tenant-b was handed resource %q, which belongs to tenant-a", res.Outcome.Resource)
	}
}

// ---------------------------------------------------------------------------
// Key reuse
// ---------------------------------------------------------------------------

func TestKeyReusedForADifferentRequestIsNotAReplay(t *testing.T) {
	ctx := context.Background()
	s := newStore(t, repository.NewMemory(), clock.Fixed{T: fixedNow})

	original := req("t1", "k-shared", `{"label":"first"}`)
	token := begin(t, s, original)
	if err := s.Complete(ctx, original, token, Outcome{Status: 201, Resource: "cap_first"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	different := req("t1", "k-shared", `{"label":"second"}`)
	res, err := s.Begin(ctx, different)
	if !errors.Is(err, ErrFingerprintMismatch) {
		t.Fatalf("Begin with a reused key gave (%+v, %v), want ErrFingerprintMismatch", res, err)
	}
	// Returning the stored outcome here would tell the client "here is your capture"
	// while discarding the request it actually sent — a lost thought, which is the failure
	// this system is built to avoid.
	if res.Outcome.Resource != "" {
		t.Errorf("a mismatched request was handed resource %q; it must not be treated as a replay", res.Outcome.Resource)
	}
	if res.State == StateCompleted {
		t.Error("a mismatched request was reported completed")
	}
	// And no token: the stored record provably belongs to another request, so the
	// mismatched attempt holds nothing it could release.
	if res.Token != "" {
		t.Errorf("a mismatched request was handed attempt token %q", res.Token)
	}
}

func TestFingerprintSeparatesMethodTargetAndBody(t *testing.T) {
	body := []byte(`{"text":"remember to call the supplier"}`)
	base := Fingerprint("POST", "/v1/captures", body)

	cases := map[string]string{
		"different method":       Fingerprint("PATCH", "/v1/captures", body),
		"different path":         Fingerprint("POST", "/v1/items", body),
		"different body":         Fingerprint("POST", "/v1/captures", []byte(`{"text":"something else"}`)),
		"field boundary shifted": Fingerprint("POS", "T/v1/captures", body),
		// The query string participates because it is part of the request target. A
		// caller that passes r.URL.Path drops it, and two POSTs differing only in a query
		// parameter then replay as one another — answered with the first one's resource ID.
		"different query": Fingerprint("POST", "/v1/captures?thread_id=B", body),
		"query dropped":   Fingerprint("POST", "/v1/captures?thread_id=A", body),
	}
	for name, got := range cases {
		if got == base {
			t.Errorf("%s produced the same fingerprint; two different operations would replay as one", name)
		}
	}
	if a, b := Fingerprint("POST", "/v1/items?thread_id=A", body), Fingerprint("POST", "/v1/items?thread_id=B", body); a == b {
		t.Error("two request targets differing only in a query parameter fingerprint identically; the second would be answered as a replay of the first")
	}
	if again := Fingerprint("POST", "/v1/captures", body); again != base {
		t.Error("Fingerprint is not deterministic; every retry would look like a reused key")
	}
	// The digest is what makes reuse detectable *without* storing the request. If the
	// spoken text could be recovered from it, this package would be holding user content
	// in a record nobody thinks of as content (§9.2).
	if strings.Contains(base, "supplier") {
		t.Error("the fingerprint contains request content verbatim")
	}
}

func TestFingerprintRequestKeepsTheQueryString(t *testing.T) {
	// The mistake this helper removes is one word wide — r.URL.Path where
	// r.URL.RequestURI() was meant — and its symptom is a request answered as a replay of
	// a different one.
	body := []byte(`{"label":"x"}`)
	a := httptest.NewRequest("POST", "/v1/items?thread_id=A", nil)
	b := httptest.NewRequest("POST", "/v1/items?thread_id=B", nil)

	if got, want := FingerprintRequest(a, body), Fingerprint("POST", "/v1/items?thread_id=A", body); got != want {
		t.Errorf("FingerprintRequest = %q, want the digest of the full request target %q", got, want)
	}
	if FingerprintRequest(a, body) == FingerprintRequest(b, body) {
		t.Error("two requests differing only in a query parameter fingerprinted identically")
	}
	if FingerprintRequest(a, body) == Fingerprint("POST", "/v1/items", body) {
		t.Error("FingerprintRequest dropped the query string, which is the defect it exists to prevent")
	}
	// A nil request must not produce the digest of an empty one: that is a real
	// fingerprint, and every caller making the mistake would collide on it. Empty is
	// refused by validate instead.
	if got := FingerprintRequest(nil, body); got != "" {
		t.Errorf("FingerprintRequest(nil) = %q, want an empty string that validate rejects", got)
	}
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

func TestBeginRefusesARequestItCannotScopeOrIdentify(t *testing.T) {
	ctx := context.Background()
	s := newStore(t, repository.NewMemory(), clock.Fixed{T: fixedNow})

	cases := []struct {
		name       string
		req        Request
		wantReason string
	}{
		{
			name:       "empty tenant",
			req:        Request{Tenant: "", Key: "k", Fingerprint: "fp"},
			wantReason: "tenant_id is empty",
		},
		{
			name:       "whitespace tenant",
			req:        Request{Tenant: " ", Key: "k", Fingerprint: "fp"},
			wantReason: "tenant_id is empty",
		},
		{
			name:       "empty key",
			req:        Request{Tenant: "t1", Key: "", Fingerprint: "fp"},
			wantReason: "idempotency_key is empty",
		},
		{
			// A key carrying the sort-key delimiter could smuggle a prefix through and
			// forge another entity's record. The entity prefixes cannot be spelled out
			// here — check-tenant-keys.sh fails the build on a key literal outside the
			// keys package — so the bare delimiter stands for the class.
			name:       "key containing the key delimiter",
			req:        Request{Tenant: "t1", Key: "k#1", Fingerprint: "fp"},
			wantReason: "not permitted in a key segment",
		},
		{
			name:       "missing fingerprint",
			req:        Request{Tenant: "t1", Key: "k", Fingerprint: ""},
			wantReason: "fingerprint is required",
		},
		{
			name:       "whitespace fingerprint",
			req:        Request{Tenant: "t1", Key: "k", Fingerprint: "  "},
			wantReason: "fingerprint is required",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.Begin(ctx, tc.req)
			assertReason(t, err, tc.wantReason)
			// Complete and Abandon share the validation, so they must refuse the same
			// input — an unscoped write is no safer at the end of an operation than at
			// the start (I11).
			assertReason(t, s.Complete(ctx, tc.req, "tok", Outcome{Status: 201, Resource: "r"}), tc.wantReason)
			assertReason(t, s.Abandon(ctx, tc.req, "tok"), tc.wantReason)
		})
	}
}

func TestARejectedIdempotencyKeyIsNeverEchoedBack(t *testing.T) {
	// §9.2: "Transcript and audio content never appears in logs, error messages,
	// exception traces, or third-party monitoring." An error returned to a caller is as
	// much a leak as a log line — the handler will log it, wrap it, or put it in a
	// response body. The rejection path is exactly where the header holds arbitrary
	// characters, so it is the path most likely to carry something that should not be
	// recorded, and keys.validateIdent formats the offending value with %q.
	ctx := context.Background()
	s := newStore(t, repository.NewMemory(), clock.Fixed{T: fixedNow})

	const secret = "call the supplier about the invoice"
	r := Request{Tenant: "t1", Key: secret, Fingerprint: "fp"}

	check := func(t *testing.T, err error) {
		t.Helper()
		assertReason(t, err, "idempotency_key")
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("the rejection echoes the client-supplied key verbatim: %v", err)
		}
		// A fragment is a leak too — the assertion has to be about the words, not the
		// whole string.
		for _, word := range []string{"supplier", "invoice"} {
			if strings.Contains(err.Error(), word) {
				t.Errorf("the rejection contains %q from the client-supplied key: %v", word, err)
			}
		}
	}

	_, err := s.Begin(ctx, r)
	t.Run("Begin", func(t *testing.T) { check(t, err) })
	t.Run("Complete", func(t *testing.T) {
		check(t, s.Complete(ctx, r, "tok", Outcome{Status: 201, Resource: "cap_1"}))
	})
	t.Run("Abandon", func(t *testing.T) { check(t, s.Abandon(ctx, r, "tok")) })
}

func TestCompleteRefusesAnOutcomeThatCannotAnswerARetry(t *testing.T) {
	ctx := context.Background()
	s := newStore(t, repository.NewMemory(), clock.Fixed{T: fixedNow})
	r := req("t1", "k1", "{}")

	cases := []struct {
		name       string
		out        Outcome
		wantReason string
	}{
		{
			// Pinning a transient failure would block the client's retry for the whole
			// TTL, so a 5xx must be released rather than recorded.
			name:       "server error",
			out:        Outcome{Status: 503, Resource: "cap_1"},
			wantReason: "released with Abandon",
		},
		{
			name:       "client error",
			out:        Outcome{Status: 422, Resource: "cap_1"},
			wantReason: "is not a success",
		},
		{
			name:       "unset status",
			out:        Outcome{Resource: "cap_1"},
			wantReason: "is not a success",
		},
		{
			name:       "no resource",
			out:        Outcome{Status: 201},
			wantReason: "must be able to name what the original request produced",
		},
		{
			name:       "whitespace resource",
			out:        Outcome{Status: 201, Resource: " "},
			wantReason: "must be able to name what the original request produced",
		},
		{
			// §6.6's POST /v1/uploads answers with a presigned PUT and an upload token.
			// Storing the URL so the replay can return it is the tempting wrong fix: it
			// expires at limits.presign_ttl_seconds — 15 minutes (§7.4) — while the
			// reservation lives for hours, so the replay would hand the client a dead
			// credential, and the record would hold a credential at rest that outlives it
			// (§9.1). The handler must regenerate it from the resource ID instead.
			name: "presigned URL as the resource",
			// The object prefix is elided from this literal deliberately:
			// check-tenant-keys.sh fails the build on an S3 key prefix appearing outside
			// the keys package, and a test fixture is not an exemption from I11.
			out:        Outcome{Status: 201, Resource: "https://bucket.s3.amazonaws.com/audio/x.opus?X-Amz-Signature=deadbeef"},
			wantReason: "regenerate the URL on the replay path",
		},
		{
			name:       "URL with no query",
			out:        Outcome{Status: 201, Resource: "https://bucket.s3.amazonaws.com/x.opus"},
			wantReason: "regenerate the URL on the replay path",
		},
		{
			name:       "a response body rather than an identifier",
			out:        Outcome{Status: 201, Resource: `{"upload_token":"abc"}`},
			wantReason: "is not an identifier",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertReason(t, s.Complete(ctx, r, "tok", tc.out), tc.wantReason)
		})
	}
}

func TestNewRefusesATTLThatWouldDisableProtection(t *testing.T) {
	repo := repository.NewMemory()
	clk := clock.Fixed{T: fixedNow}
	log := logging.New()

	cases := []struct {
		name       string
		ttlHours   int
		wantReason string
	}{
		{"zero", 0, "silently disables idempotency"},
		{"negative", -1, "silently disables idempotency"},
		{"beyond a week", maxTTLHours + 1, "not a retry window"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(repo, clk, log, tc.ttlHours)
			assertReason(t, err, tc.wantReason)
		})
	}

	t.Run("recommended value", func(t *testing.T) {
		if _, err := New(repo, clk, log, 24); err != nil {
			t.Fatalf("New rejected the recommended 24-hour TTL: %v", err)
		}
	})
	// A nil clock is refused rather than defaulted, because the only alternative would be
	// time.Now() — which nothing outside internal/clock may call.
	t.Run("nil clock", func(t *testing.T) {
		_, err := New(repo, nil, log, 24)
		assertReason(t, err, "clock is required")
	})
	t.Run("nil repository", func(t *testing.T) {
		_, err := New(nil, clk, log, 24)
		assertReason(t, err, "repository is required")
	})
	// The key-reuse warning would panic on a nil logger, on the one path where the client
	// needs a 422 rather than a 500.
	t.Run("nil logger", func(t *testing.T) {
		_, err := New(repo, clk, nil, 24)
		assertReason(t, err, "logger is required")
	})
}

// ---------------------------------------------------------------------------
// Expiry and timestamps
// ---------------------------------------------------------------------------

func TestReservationCarriesAnAbsoluteExpiryTakenFromTheClock(t *testing.T) {
	ctx := context.Background()
	repo := repository.NewMemory()
	clk := &movableClock{f: clock.Fixed{T: fixedNow}}
	s := newStore(t, repo, clk)
	r := req("t1", "k1", "{}")

	token := begin(t, s, r)

	key, err := keys.Idempotency(r.Tenant, r.Key)
	if err != nil {
		t.Fatalf("building the key: %v", err)
	}
	stored, err := repo.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	wantTTL := fixedNow.Add(testTTLHours * time.Hour).Unix()
	// Item.TTL is what DynamoDB expires on. A record with the attribute set but Item.TTL
	// zero never expires, so the reservation becomes a permanent uniqueness constraint —
	// invisible until a client reuses a key months later and is refused.
	if stored.TTL != wantTTL {
		t.Errorf("Item.TTL = %d, want %d (absolute epoch seconds, %dh from the clock)", stored.TTL, wantTTL, testTTLHours)
	}
	if got, _ := stored.Attrs[attrTTL].(int64); got != wantTTL {
		t.Errorf("ttl attribute = %d, want %d", got, wantTTL)
	}
	if got, _ := stored.Attrs[attrAttempt].(string); got != token {
		t.Errorf("attempt attribute = %q, want the token Begin returned %q", got, token)
	}
	if stored.GSI1PK != "" || stored.GSI1SK != "" {
		t.Error("reservation projects into GSI1; per-request records must never enter the sparse index (§6.3)")
	}

	// The retry window runs from when the response was produced, not from when the
	// reservation was taken — a slow operation must not eat into it.
	clk.advance(30 * time.Minute)
	if err := s.Complete(ctx, r, token, Outcome{Status: 201, Resource: "cap_1"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	stored, err = repo.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get after Complete: %v", err)
	}
	wantTTL = fixedNow.Add(30*time.Minute + testTTLHours*time.Hour).Unix()
	if stored.TTL != wantTTL {
		t.Errorf("Item.TTL after Complete = %d, want %d recomputed from the completion time", stored.TTL, wantTTL)
	}
	// The last-write timestamp moves with the completion; the claim time must not.
	if got, _ := stored.Attrs[attrTS].(string); got != clock.RFC3339UTC(fixedNow.Add(30*time.Minute)) {
		t.Errorf("ts attribute = %q, want the completion time", got)
	}
	if got, _ := stored.Attrs[attrStarted].(string); got != clock.RFC3339UTC(fixedNow) {
		t.Errorf("started_at = %q, want the claim time %q", got, clock.RFC3339UTC(fixedNow))
	}
}

func TestStartedAtIsTheClaimTimeNotTheCompletionTime(t *testing.T) {
	// StartedAt exists so a handler can log how long a reservation has been alive. Stamped
	// with the completion time it reports an age of ~0 for an operation that took ninety
	// minutes, which is the opposite of the diagnostic it was added for — and a
	// presence-only assertion (StartedAt != "") cannot catch a wrong value.
	ctx := context.Background()
	clk := &movableClock{f: clock.Fixed{T: fixedNow}}
	s := newStore(t, repository.NewMemory(), clk)
	r := req("t1", "k1", "{}")

	token := begin(t, s, r)

	clk.advance(90 * time.Minute)
	if err := s.Complete(ctx, r, token, Outcome{Status: 201, Resource: "cap_1"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	replay, err := s.Begin(ctx, r)
	if err != nil {
		t.Fatalf("replay Begin: %v", err)
	}
	if want := clock.RFC3339UTC(fixedNow); replay.StartedAt != want {
		t.Errorf("replay StartedAt = %q, want the claim time %q; a handler logging the reservation's age reports 0 for a 90-minute operation", replay.StartedAt, want)
	}
}

func TestARetryArrivingAfterExpiryRunsTheOperationAgain(t *testing.T) {
	// This documents the accepted consequence rather than a desirable behaviour: once the
	// record is gone, a retry is indistinguishable from a new request and a second capture
	// is created. The TTL is therefore a floor on how long protection lasts. Nothing in
	// this package can detect the case — which is why the window must exceed the longest
	// plausible client retry, and why audio has a second line of defence in content-hash
	// ingest dedupe (§5A.3.4).
	ctx := context.Background()
	repo := repository.NewMemory()
	s := newStore(t, repo, clock.Fixed{T: fixedNow})
	r := req("t1", "k1", "{}")

	token := begin(t, s, r)
	if err := s.Complete(ctx, r, token, Outcome{Status: 201, Resource: "cap_1"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// Memory has no TTL sweeper, so expiry is simulated by removing the record — which is
	// exactly what DynamoDB's sweeper does.
	key, err := keys.Idempotency(r.Tenant, r.Key)
	if err != nil {
		t.Fatalf("building the key: %v", err)
	}
	if err := repo.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	res, err := s.Begin(ctx, r)
	if err != nil {
		t.Fatalf("Begin after expiry: %v", err)
	}
	if res.State != StateNew {
		t.Fatalf("after expiry Begin gave %q, want %q", res.State, StateNew)
	}
}

// ---------------------------------------------------------------------------
// Complete guards its own write
// ---------------------------------------------------------------------------

func TestCompleteRefusesToRewriteTheFingerprintTheReservationWasTakenUnder(t *testing.T) {
	// The reachable caller mistake: the handler fingerprints the raw body for Begin and
	// then rebuilds the Request from the normalised/defaulted body for Complete. Writing
	// that fingerprint through leaves a record the client can never match, so the client
	// that performed the operation is told 422 — "you reused a key for a different
	// request" — on every retry for the whole TTL, describing a defect it does not have.
	ctx := context.Background()
	repo := repository.NewMemory()
	s := newStore(t, repo, clock.Fixed{T: fixedNow})

	asSent := req("t1", "k1", `{"label":"x"}`)
	normalised := req("t1", "k1", `{"label":"x","kind":"idea"}`)

	token := begin(t, s, asSent)

	err := s.Complete(ctx, normalised, token, Outcome{Status: 201, Resource: "cap_1"})
	if !errors.Is(err, ErrFingerprintMismatch) {
		t.Fatalf("Complete with a rebuilt fingerprint gave %v, want ErrFingerprintMismatch", err)
	}

	// The stored record still belongs to the request the client sent, which is what makes
	// the caller's mistake recoverable rather than permanent.
	rec := storedRecord(t, repo, asSent)
	if rec.fingerprint != asSent.Fingerprint {
		t.Error("the stored fingerprint was overwritten with the rebuilt one")
	}
	if rec.state != StateInFlight {
		t.Errorf("stored state = %q, want %q", rec.state, StateInFlight)
	}
	// The client's genuine retry is answered 409 (in flight), never 422.
	res, err := s.Begin(ctx, asSent)
	if err != nil {
		t.Fatalf("the client's retry gave %v, want an in-flight reservation", err)
	}
	if res.State != StateInFlight {
		t.Errorf("the client's retry saw %q, want %q", res.State, StateInFlight)
	}
}

func TestCompleteRefusesAReservationAnotherAttemptHolds(t *testing.T) {
	// An attempt correctly told 409 must not be able to record its own outcome against
	// the owner's reservation: the owner's retries would then be answered with a resource
	// the owner never created.
	ctx := context.Background()
	repo := repository.NewMemory()
	s := newStore(t, repo, clock.Fixed{T: fixedNow})
	r := req("t1", "k1", "{}")

	begin(t, s, r)

	err := s.Complete(ctx, r, "some-other-attempt", Outcome{Status: 201, Resource: "cap_intruder"})
	if !errors.Is(err, ErrNotOwner) {
		t.Fatalf("Complete with a foreign token gave %v, want ErrNotOwner", err)
	}
	if rec := storedRecord(t, repo, r); rec.state != StateInFlight || rec.resource != "" {
		t.Errorf("the reservation was modified by a non-owner: %+v", rec)
	}

	t.Run("no token at all", func(t *testing.T) {
		// A handler that forgot to thread the token must fail loudly here rather than get
		// an unguarded write, which is what made the fingerprint defect above possible.
		assertReason(t, s.Complete(ctx, r, "  ", Outcome{Status: 201, Resource: "cap_1"}), "attempt token is required")
	})
}

func TestCompleteOnAnExpiredReservationStillRecordsTheOutcome(t *testing.T) {
	// The permissive direction is the right one here, and it is the only place in this
	// package where it is: the operation happened, and refusing to record it would leave
	// nothing for the client's retry to find, so the retry would be told StateNew and
	// create the second capture.
	ctx := context.Background()
	repo := repository.NewMemory()
	s := newStore(t, repo, clock.Fixed{T: fixedNow})
	r := req("t1", "k1", "{}")

	token := begin(t, s, r)

	key, err := keys.Idempotency(r.Tenant, r.Key)
	if err != nil {
		t.Fatalf("building the key: %v", err)
	}
	if err := repo.Delete(ctx, key); err != nil {
		t.Fatalf("simulating expiry: %v", err)
	}

	if err := s.Complete(ctx, r, token, Outcome{Status: 201, Resource: "cap_1"}); err != nil {
		t.Fatalf("Complete after the reservation expired: %v", err)
	}
	res, err := s.Begin(ctx, r)
	if err != nil {
		t.Fatalf("Begin after a recovered Complete: %v", err)
	}
	if res.State != StateCompleted || res.Outcome.Resource != "cap_1" {
		t.Errorf("the client's retry saw %q/%q, want the recorded outcome", res.State, res.Outcome.Resource)
	}
}

func TestARepeatedCompleteIsIdempotentAndAConflictingOneIsRefused(t *testing.T) {
	ctx := context.Background()
	repo := repository.NewMemory()
	s := newStore(t, repo, clock.Fixed{T: fixedNow})
	r := req("t1", "k1", "{}")

	token := begin(t, s, r)
	out := Outcome{Status: 201, Resource: "cap_1"}
	if err := s.Complete(ctx, r, token, out); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// A handler whose Complete lost its response retries it. Same answer, so this is a
	// no-op rather than an error.
	if err := s.Complete(ctx, r, token, out); err != nil {
		t.Fatalf("repeating the same Complete: %v", err)
	}

	// A *different* answer is refused: the first one may already be in the client's
	// hands, and a later replay must not contradict the response the client received.
	err := s.Complete(ctx, r, token, Outcome{Status: 200, Resource: "cap_2"})
	if !errors.Is(err, ErrOutcomeConflict) {
		t.Fatalf("Complete with a conflicting outcome gave %v, want ErrOutcomeConflict", err)
	}
	if rec := storedRecord(t, repo, r); rec.resource != "cap_1" || rec.status != 201 {
		t.Errorf("the stored outcome was overwritten: %+v", rec)
	}
}

// ---------------------------------------------------------------------------
// Abandon guards its own delete
// ---------------------------------------------------------------------------

func TestAbandonLetsTheClientsRetryProceed(t *testing.T) {
	// Without this path a handler that failed after Begin would leave the reservation
	// in place for the whole TTL, so a transient provider error would answer every retry
	// with 409 and block that capture for a day.
	ctx := context.Background()
	repo := repository.NewMemory()
	s := newStore(t, repo, clock.Fixed{T: fixedNow})
	r := req("t1", "k1", "{}")

	token := begin(t, s, r)
	if err := s.Abandon(ctx, r, token); err != nil {
		t.Fatalf("Abandon: %v", err)
	}
	if n := repo.Len(); n != 0 {
		t.Errorf("repository holds %d items after Abandon, want 0; an abandoned key must be indistinguishable from an unused one", n)
	}

	res, err := s.Begin(ctx, r)
	if err != nil {
		t.Fatalf("Begin after Abandon: %v", err)
	}
	if res.State != StateNew {
		t.Errorf("Begin after Abandon gave %q, want %q", res.State, StateNew)
	}
}

func TestAbandonRefusesToDestroyACompletedReservation(t *testing.T) {
	// This is the demonstrated path to the duplicate capture the package exists to
	// prevent, and it needs no concurrency to reach:
	//
	//   1. Request #1 completes under key K.
	//   2. Request #2 reuses K with a different body. Begin returns ErrFingerprintMismatch
	//      and the handler answers 422.
	//   3. The handler's cleanup — `defer { if !committed { store.Abandon(...) } }`, which
	//      the doc's own "Abandon if nothing was persisted" wording endorses — calls
	//      Abandon with the *mismatched* Request.
	//
	// An unguarded delete removes request #1's completed record, so request #1's genuine
	// retry is told StateNew and creates a second capture.
	ctx := context.Background()
	repo := repository.NewMemory()
	s := newStore(t, repo, clock.Fixed{T: fixedNow})

	first := req("t1", "k-shared", `{"label":"first"}`)
	token := begin(t, s, first)
	if err := s.Complete(ctx, first, token, Outcome{Status: 201, Resource: "cap_first"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	second := req("t1", "k-shared", `{"label":"second"}`)
	if _, err := s.Begin(ctx, second); !errors.Is(err, ErrFingerprintMismatch) {
		t.Fatalf("Begin for the reused key gave %v, want ErrFingerprintMismatch", err)
	}

	t.Run("mismatched request", func(t *testing.T) {
		// Refused as completed rather than as a mismatch: the completed check runs first
		// because it is the stronger reason. What the test is about is that the record
		// survives, which the assertions after this subtest establish.
		err := s.Abandon(ctx, second, "any-token")
		if !errors.Is(err, ErrCompleted) {
			t.Fatalf("Abandon with a mismatched request gave %v, want ErrCompleted", err)
		}
	})

	t.Run("the completing attempt's own token", func(t *testing.T) {
		// Refused even for the attempt that completed it: the operation happened, and that
		// record is the only thing that answers the client's retry.
		err := s.Abandon(ctx, first, token)
		if !errors.Is(err, ErrCompleted) {
			t.Fatalf("Abandon of a completed reservation gave %v, want ErrCompleted", err)
		}
	})

	// The record survived both, so the genuine retry of request #1 is still answered with
	// what it created.
	if n := repo.Len(); n != 1 {
		t.Fatalf("repository holds %d items, want the completed record to survive", n)
	}
	res, err := s.Begin(ctx, first)
	if err != nil {
		t.Fatalf("the original request's retry gave %v", err)
	}
	if res.State != StateCompleted || res.Outcome.Resource != "cap_first" {
		t.Errorf("the original request's retry saw %q/%q, want its completed outcome; a second capture would be created", res.State, res.Outcome.Resource)
	}
}

func TestAbandonRefusesToReleaseAReservationTakenUnderADifferentRequest(t *testing.T) {
	// The in-flight version of the case above, where the fingerprint guard is what fires:
	// request #2 reuses the key while request #1 is still working, is told 422, and its
	// cleanup would otherwise delete the reservation request #1 is operating under.
	ctx := context.Background()
	repo := repository.NewMemory()
	s := newStore(t, repo, clock.Fixed{T: fixedNow})

	first := req("t1", "k-shared", `{"label":"first"}`)
	begin(t, s, first)

	second := req("t1", "k-shared", `{"label":"second"}`)
	if _, err := s.Begin(ctx, second); !errors.Is(err, ErrFingerprintMismatch) {
		t.Fatalf("Begin for the reused key gave %v, want ErrFingerprintMismatch", err)
	}
	if err := s.Abandon(ctx, second, "any-token"); !errors.Is(err, ErrFingerprintMismatch) {
		t.Fatalf("Abandon with a mismatched request gave %v, want ErrFingerprintMismatch", err)
	}
	if rec := storedRecord(t, repo, first); rec.state != StateInFlight || rec.fingerprint != first.Fingerprint {
		t.Errorf("the original request's reservation was disturbed: %+v", rec)
	}
}

func TestAbandonRefusesToReleaseAReservationAnotherAttemptHolds(t *testing.T) {
	// The concurrency version of the same hazard: attempt B is correctly told 409, and its
	// cleanup path then deletes attempt A's live reservation, so the next retry does the
	// work again on top of whatever A persisted.
	ctx := context.Background()
	repo := repository.NewMemory()
	s := newStore(t, repo, clock.Fixed{T: fixedNow})
	r := req("t1", "k1", "{}")

	begin(t, s, r)
	conflicted, err := s.Begin(ctx, r)
	if err != nil {
		t.Fatalf("second Begin: %v", err)
	}

	// A handler holding the in-flight reservation reaches for its token and finds none,
	// which is the first refusal.
	assertReason(t, s.Abandon(ctx, r, conflicted.Token), "attempt token is required")
	// And inventing one does not help.
	if err := s.Abandon(ctx, r, "guessed-token"); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("Abandon with a foreign token gave %v, want ErrNotOwner", err)
	}
	if n := repo.Len(); n != 1 {
		t.Errorf("repository holds %d items, want the owner's reservation to survive", n)
	}
	if rec := storedRecord(t, repo, r); rec.state != StateInFlight {
		t.Errorf("stored state = %q, want the owner's reservation intact", rec.state)
	}
}

func TestAbandonOnAKeyThatIsNotHeldIsNotAnError(t *testing.T) {
	// The handler contract calls Abandon after a Begin that failed, precisely because it
	// cannot know whether the reservation write landed. When it did not, there is nothing
	// to release and nothing was destroyed, so there is nothing to report.
	ctx := context.Background()
	repo := repository.NewMemory()
	s := newStore(t, repo, clock.Fixed{T: fixedNow})

	if err := s.Abandon(ctx, req("t1", "never-claimed", "{}"), "some-token"); err != nil {
		t.Fatalf("Abandon of an unheld key: %v", err)
	}
	if n := repo.Len(); n != 0 {
		t.Errorf("Abandon of an unheld key wrote %d items", n)
	}
}

// ---------------------------------------------------------------------------
// A Begin that failed after its write may have committed
// ---------------------------------------------------------------------------

// commitThenFailRepo commits the reservation write and then reports a transport failure —
// a PutItem that landed and whose response never arrived, with the SDK's retries
// exhausted.
type commitThenFailRepo struct {
	*repository.Memory
}

func (r commitThenFailRepo) PutOnce(ctx context.Context, item repository.Item) error {
	if err := r.Memory.PutOnce(ctx, item); err != nil {
		return err
	}
	return errBoom
}

func TestAStrandedReservationFromAFailedBeginIsReleasable(t *testing.T) {
	// Without a token on the error return there is no way to clear this: the record exists,
	// the handler returned 500, and every retry is answered 409 for the full TTL, so the
	// capture cannot be created at all. An unconditional Abandon is not the alternative —
	// it would delete a live reservation another attempt holds — which is why the token
	// comes back on exactly the error paths where this attempt's own write may be the
	// thing stranding the key.
	ctx := context.Background()
	mem := repository.NewMemory()
	s := newStore(t, commitThenFailRepo{Memory: mem}, clock.Fixed{T: fixedNow})
	r := req("t1", "k1", "{}")

	res, err := s.Begin(ctx, r)
	assertReason(t, err, "reserving idempotency key")
	if res.State == StateNew {
		t.Fatal("a failed reservation was reported as owning the operation")
	}
	if res.Token == "" {
		t.Fatal("Begin returned no token on a failure that may have committed its write; the key cannot be released")
	}
	if mem.Len() != 1 {
		t.Fatalf("the fake did not commit the write this test is about (%d items)", mem.Len())
	}

	// The handler answers 500 and releases what it may have stranded.
	release := newStore(t, mem, clock.Fixed{T: fixedNow})
	if err := release.Abandon(ctx, r, res.Token); err != nil {
		t.Fatalf("releasing the stranded reservation: %v", err)
	}
	if mem.Len() != 0 {
		t.Errorf("the stranded reservation survived the release (%d items)", mem.Len())
	}
	next, err := release.Begin(ctx, r)
	if err != nil {
		t.Fatalf("Begin after the release: %v", err)
	}
	if next.State != StateNew {
		t.Errorf("the client's retry saw %q, want %q; the key is still dead", next.State, StateNew)
	}
}

func TestATokenlessAttemptClaimsNothing(t *testing.T) {
	// The token is what makes ownership checkable, so an attempt that cannot mint one must
	// not write a reservation at all. Falling back to a fixed value would be worse than
	// having no token: every attempt would then recognise every record as its own, and the
	// ownership checks would silently pass for the wrong caller.
	ctx := context.Background()
	repo := repository.NewMemory()
	s := newStore(t, repo, clock.Fixed{T: fixedNow})
	r := req("t1", "k1", "{}")

	original := randRead
	randRead = func([]byte) (int, error) { return 0, errBoom }
	defer func() { randRead = original }()

	res, err := s.Begin(ctx, r)
	assertReason(t, err, "generating an attempt token")
	if !errors.Is(err, errBoom) {
		t.Errorf("error does not wrap the cause with %%w: %v", err)
	}
	if res.State != "" || res.Token != "" {
		t.Errorf("Begin reported %+v after failing to mint a token", res)
	}
	if repo.Len() != 0 {
		t.Errorf("a reservation was written without a token (%d items)", repo.Len())
	}
}

// ---------------------------------------------------------------------------
// Unhealthy storage and malformed records
// ---------------------------------------------------------------------------

// stubRepo overrides one method of Memory.
//
// Memory.FailNext fails whichever call comes next, and the paths below need a specific
// method to misbehave while the others work — a PutOnce reporting ErrAlreadyExists
// followed by a Get that fails, or a Get that succeeds followed by a Delete that does
// not. Neither is expressible with a single injected failure.
type stubRepo struct {
	*repository.Memory
	getErr    error
	putErr    error
	deleteErr error
}

func (s stubRepo) Get(ctx context.Context, key keys.DynamoKey) (*repository.Item, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.Memory.Get(ctx, key)
}

func (s stubRepo) Put(ctx context.Context, item repository.Item) error {
	if s.putErr != nil {
		return s.putErr
	}
	return s.Memory.Put(ctx, item)
}

func (s stubRepo) Delete(ctx context.Context, key keys.DynamoKey) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	return s.Memory.Delete(ctx, key)
}

func TestUnhealthyStorageIsNeverMistakenForAnUnusedKey(t *testing.T) {
	ctx := context.Background()
	r := req("t1", "k1", "{}")

	t.Run("reservation write fails", func(t *testing.T) {
		repo := repository.NewMemory()
		s := newStore(t, repo, clock.Fixed{T: fixedNow})
		repo.FailNext(errBoom)

		res, err := s.Begin(ctx, r)
		// Discarding this error and reporting StateNew is how the duplicate gets created
		// on the one request where storage was unhealthy.
		assertReason(t, err, "reserving idempotency key")
		if !errors.Is(err, errBoom) {
			t.Errorf("error does not wrap the storage failure with %%w: %v", err)
		}
		if res.State == StateNew {
			t.Error("a failed reservation was reported as owning the operation")
		}
	})

	t.Run("read-back fails", func(t *testing.T) {
		mem := repository.NewMemory()
		s := newStore(t, stubRepo{Memory: mem}, clock.Fixed{T: fixedNow})
		begin(t, s, r)

		s2 := newStore(t, stubRepo{Memory: mem, getErr: errBoom}, clock.Fixed{T: fixedNow})
		res, err := s2.Begin(ctx, r)
		assertReason(t, err, "reading an existing reservation")
		if res.State != "" {
			t.Errorf("state %q reported despite an unreadable reservation", res.State)
		}
	})

	t.Run("reservation vanished", func(t *testing.T) {
		// The record existed for the conditional write and was gone by the read — it
		// expired in between, or a concurrent attempt abandoned it. Falling through to
		// StateNew would let both attempts do the work, so this fails and the client's
		// next retry finds a clean slate.
		mem := repository.NewMemory()
		s := newStore(t, stubRepo{Memory: mem}, clock.Fixed{T: fixedNow})
		begin(t, s, r)

		s2 := newStore(t, stubRepo{Memory: mem, getErr: fmt.Errorf("wrapped: %w", repository.ErrNotFound)}, clock.Fixed{T: fixedNow})
		res, err := s2.Begin(ctx, r)
		if !errors.Is(err, ErrVanished) {
			t.Fatalf("Begin gave (%+v, %v), want ErrVanished", res, err)
		}
		if res.State == StateNew {
			t.Error("a vanished reservation was reported as an unused key, which permits the duplicate")
		}
	})
}

func TestAFailedOutcomeWriteAndAFailedReleaseAreReported(t *testing.T) {
	// Neither may be swallowed. A dropped Complete leaves the reservation in flight, so
	// every retry gets a conflict for a result that exists — and the handler is the only
	// place that still holds the outcome, so it has to learn that storing it failed. A
	// dropped Abandon leaves the key held after a failure the client is expected to retry.
	//
	// Both now read the record before writing it, so the read is a failure surface too and
	// each has to be exercised separately.
	ctx := context.Background()
	r := req("t1", "k1", "{}")
	out := Outcome{Status: 201, Resource: "cap_1"}

	t.Run("Complete: the guard read fails", func(t *testing.T) {
		mem := repository.NewMemory()
		s := newStore(t, stubRepo{Memory: mem}, clock.Fixed{T: fixedNow})
		token := begin(t, s, r)

		s2 := newStore(t, stubRepo{Memory: mem, getErr: errBoom}, clock.Fixed{T: fixedNow})
		err := s2.Complete(ctx, r, token, out)
		// Cannot verify, so does not write: a blind overwrite on an unreadable record can
		// bury another request's reservation.
		assertReason(t, err, "reading the reservation before recording its outcome")
		if !errors.Is(err, errBoom) {
			t.Errorf("error does not wrap the storage failure with %%w: %v", err)
		}
		if rec := storedRecord(t, mem, r); rec.state != StateInFlight {
			t.Errorf("stored state = %q, want the reservation left untouched", rec.state)
		}
	})

	t.Run("Complete: the write fails", func(t *testing.T) {
		mem := repository.NewMemory()
		s := newStore(t, stubRepo{Memory: mem}, clock.Fixed{T: fixedNow})
		token := begin(t, s, r)

		s2 := newStore(t, stubRepo{Memory: mem, putErr: errBoom}, clock.Fixed{T: fixedNow})
		err := s2.Complete(ctx, r, token, out)
		assertReason(t, err, "recording the outcome")
		if !errors.Is(err, errBoom) {
			t.Errorf("error does not wrap the storage failure with %%w: %v", err)
		}
	})

	t.Run("Abandon: the guard read fails", func(t *testing.T) {
		mem := repository.NewMemory()
		s := newStore(t, stubRepo{Memory: mem}, clock.Fixed{T: fixedNow})
		token := begin(t, s, r)

		s2 := newStore(t, stubRepo{Memory: mem, getErr: errBoom}, clock.Fixed{T: fixedNow})
		err := s2.Abandon(ctx, r, token)
		assertReason(t, err, "reading the reservation before releasing it")
		if !errors.Is(err, errBoom) {
			t.Errorf("error does not wrap the storage failure with %%w: %v", err)
		}
		if mem.Len() != 1 {
			t.Error("an unreadable record was deleted anyway")
		}
	})

	t.Run("Abandon: the delete fails", func(t *testing.T) {
		mem := repository.NewMemory()
		s := newStore(t, stubRepo{Memory: mem}, clock.Fixed{T: fixedNow})
		token := begin(t, s, r)

		s2 := newStore(t, stubRepo{Memory: mem, deleteErr: errBoom}, clock.Fixed{T: fixedNow})
		err := s2.Abandon(ctx, r, token)
		assertReason(t, err, "releasing a reservation")
		if !errors.Is(err, errBoom) {
			t.Errorf("error does not wrap the storage failure with %%w: %v", err)
		}
	})
}

func TestAMalformedRecordFailsLoudlyRatherThanReplayingNonsense(t *testing.T) {
	ctx := context.Background()
	r := req("t1", "k1", "{}")
	key, err := keys.Idempotency(r.Tenant, r.Key)
	if err != nil {
		t.Fatalf("building the key: %v", err)
	}

	cases := []struct {
		name       string
		attrs      map[string]any
		wantReason string
	}{
		{
			name:       "no state",
			attrs:      map[string]any{attrFingerprint: r.Fingerprint},
			wantReason: "no state attribute",
		},
		{
			name:       "unknown state",
			attrs:      map[string]any{attrState: "finished", attrFingerprint: r.Fingerprint},
			wantReason: "unknown state",
		},
		{
			// Replaying this as status 0 would hand the client a response it cannot
			// interpret, produced from a record that looked fine.
			name:       "completed with no status",
			attrs:      map[string]any{attrState: string(StateCompleted), attrFingerprint: r.Fingerprint, attrResource: "cap_1"},
			wantReason: "unreadable status attribute",
		},
		{
			name:       "completed with a non-numeric status",
			attrs:      map[string]any{attrState: string(StateCompleted), attrFingerprint: r.Fingerprint, attrResource: "cap_1", attrStatus: "201"},
			wantReason: "unreadable status attribute",
		},
		{
			name:       "completed with no resource",
			attrs:      map[string]any{attrState: string(StateCompleted), attrFingerprint: r.Fingerprint, attrStatus: int64(201)},
			wantReason: "no resource attribute",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := repository.NewMemory()
			if err := repo.Put(ctx, repository.Item{Key: key, Attrs: tc.attrs}); err != nil {
				t.Fatalf("seeding the record: %v", err)
			}
			s := newStore(t, repo, clock.Fixed{T: fixedNow})

			res, err := s.Begin(ctx, r)
			assertReason(t, err, tc.wantReason)
			if res.State == StateNew {
				t.Error("a malformed record was reported as an unused key")
			}
			// The write paths read the same record, and a record they cannot decode is one
			// whose ownership they cannot check — so they refuse rather than overwrite or
			// delete it.
			assertReason(t, s.Complete(ctx, r, "tok", Outcome{Status: 201, Resource: "cap_1"}), tc.wantReason)
			assertReason(t, s.Abandon(ctx, r, "tok"), tc.wantReason)
			if repo.Len() != 1 {
				t.Error("a record that could not be decoded was deleted anyway")
			}
		})
	}
}

func TestCompletedRecordsAreReadableWhicheverNumericShapeTheyArriveIn(t *testing.T) {
	// The DynamoDB adapter that will read these records in production does not exist yet,
	// and a number can arrive as int, int64, or float64 depending on the unmarshaller in
	// front of it. Assuming one encoding would be a bet; erroring on a genuinely
	// non-numeric value is covered above.
	ctx := context.Background()
	r := req("t1", "k1", "{}")
	key, err := keys.Idempotency(r.Tenant, r.Key)
	if err != nil {
		t.Fatalf("building the key: %v", err)
	}

	for name, status := range map[string]any{"int": 201, "int64": int64(201), "float64": float64(201)} {
		t.Run(name, func(t *testing.T) {
			repo := repository.NewMemory()
			err := repo.Put(ctx, repository.Item{Key: key, Attrs: map[string]any{
				attrState:       string(StateCompleted),
				attrFingerprint: r.Fingerprint,
				attrStatus:      status,
				attrResource:    "cap_1",
			}})
			if err != nil {
				t.Fatalf("seeding the record: %v", err)
			}
			s := newStore(t, repo, clock.Fixed{T: fixedNow})
			res, err := s.Begin(ctx, r)
			if err != nil {
				t.Fatalf("Begin: %v", err)
			}
			if res.Outcome.Status != 201 {
				t.Errorf("status = %d, want 201", res.Outcome.Status)
			}
		})
	}
}

// assertReason fails unless err is non-nil and its message names the expected cause.
// Asserting the reason rather than "an error occurred" is what stops a test passing on a
// failure that happened for the wrong reason — an input rejected by the wrong check
// leaves the constraint under test unverified.
func assertReason(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("no error returned; expected one mentioning %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not mention %q", err.Error(), want)
	}
}
