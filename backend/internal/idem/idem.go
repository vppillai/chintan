// Package idem provides idempotency-key support for the mutating endpoints (§6.6).
//
// §2A.1 places this in Phase 0 rather than deferring it with the rest of the commercial
// scaffolding: "Once clients retry on your behalf, duplicate capture creation is a
// data-integrity bug. Cheap now, invasive later." The retries are not hypothetical. The
// primary client is a phone on a flaky mobile link, and a request can be replayed
// without the user doing anything — a service worker draining a queued POST, a fetch
// retried after a timeout, a user pressing send twice because the first attempt showed
// nothing. §6.6 therefore requires that "every POST and PATCH accepts an
// Idempotency-Key header, backed by a short-TTL DynamoDB item", and §Phase 0 acceptance
// states the property this package exists to hold: "Replaying an identical request with
// the same idempotency key creates exactly one capture."
//
// **The reservation is a conditional write, never a read-then-write.** repository.PutOnce
// exists for exactly this. A read-then-write has a race in which two concurrent retries
// both see "absent" and both proceed — which is the duplicate-capture bug this package
// exists to prevent, arriving through the mechanism meant to stop it. The race window is
// tens of milliseconds wide and is exactly the window a double-fire lands in, so it is
// the common case rather than the exotic one.
//
// **Three states, and they are not interchangeable:**
//
//   - StateNew — never seen (or seen only by this same attempt, see the attempt token
//     below). The caller owns the operation and must finish it with Complete or Abandon.
//   - StateInFlight — another attempt holds the reservation right now. The correct answer
//     is 409 Conflict: the caller must not do the work, and must not report success it
//     cannot substantiate. There is no result to hand back yet, and inventing one — an
//     empty 200, or a resource ID the client did not create — is worse than a conflict
//     the client can retry.
//   - StateCompleted — the operation already happened, and the stored Outcome names what
//     it produced. A client that retried because it never saw a response needs an answer
//     it can act on, so "already seen" alone is not acceptable — but see Outcome for what
//     the record does and does not contain, because for one route in §6.6 the handler has
//     to regenerate part of the answer rather than replay it.
//
// The record carries no user content (§9.2): a client key, a request digest, an attempt
// token, an HTTP status, and a resource identifier. The digest is a SHA-256, which is why
// key reuse can be detected without storing the request.
//
// **Every write path is guarded by the record it is about to overwrite.** Begin, Complete
// and Abandon all read the stored reservation and refuse when it is not theirs. That is
// not defensive decoration: an unguarded Abandon deletes the completed record that
// answers a legitimate client's retry, and an unguarded Complete rewrites the fingerprint
// the reservation was taken under, so the client that performed the operation can never
// learn what it created. Both turn this package into the source of the duplicate it
// exists to prevent. The guards are Get-then-write and therefore not atomic — the
// repository seam has no compare-and-set — so they catch caller mistakes, not concurrent
// contention; single ownership is what PutOnce provides.
//
// **The attempt token is what makes "is this record mine?" answerable.** Begin mints a
// random token per call and stores it. Two things follow, and neither is possible without
// it:
//
//   - A conditional PutItem is not retry-safe. The AWS SDK's default retryer re-sends on
//     a network error or a 5xx, so a reservation write that *committed* and lost its
//     response comes back as ConditionalCheckFailedException, which repository maps to
//     ErrAlreadyExists. Without a token Begin reads back its own record, matches its own
//     fingerprint, and reports StateInFlight — telling the creator that someone else
//     holds the key, with no self-heal, for the whole TTL. With one, a stored token equal
//     to mine means the record is mine and the state is StateNew.
//   - Only the attempt that claimed the key may release or complete it. Otherwise a
//     concurrent attempt that was correctly told 409 can delete the live reservation the
//     owner is working under (the `defer` cleanup pattern below does exactly that), which
//     re-permits the duplicate.
//
// **This record is deliberately mutable and deletable, and that is not an exception to
// anything.** I1 and I13 make L0 transcripts and audit records write-once, which is why
// the rest of this codebase reaches for PutOnce; a reservation is neither. It is
// short-lived request bookkeeping whose whole purpose is to transition in-flight →
// completed, so Complete uses Put and Abandon uses Delete. No transcript, item, or audit
// record is touched from here.
//
// **A completed outcome is not an upload confirmation** (I2). A client told 409 has had
// nothing persisted on its behalf, so it must keep its local audio buffer. And a
// *completed* outcome for §6.6's `POST /v1/uploads` means a presigned PUT was issued, not
// that any audio reached S3 — so the buffer may only be pruned once the PUT itself
// succeeded. Pruning on any non-error response is how a thought is lost to a
// duplicate-suppression path.
//
// Handler contract:
//
//	res, err := store.Begin(ctx, req)
//	switch {
//	case errors.Is(err, idem.ErrFingerprintMismatch):
//	    // 422. The client reused a key for a *different* request. This is not a replay,
//	    // and returning the original outcome would silently discard the new request.
//	    // Nothing was written for this attempt, so nothing needs releasing.
//	case errors.Is(err, idem.ErrVanished):
//	    // 409. The reservation disappeared mid-check; retry the whole request.
//	case err != nil:
//	    // 500 — and then release, because the reservation write may have committed before
//	    // the failure was reported:
//	    //     _ = store.Abandon(ctx, req, res.Token)
//	    // Begin populates res.Token on exactly the error returns where its own write may
//	    // be what is stranding the key, precisely so this is possible. Skipping it leaves
//	    // an in-flight record nothing can clear, so every retry gets 409 for the full TTL
//	    // and that capture cannot be created at all. An *unconditional* Abandon would be
//	    // the wrong fix — it would delete a live reservation another attempt legitimately
//	    // holds — which is why Abandon is keyed on this attempt's token and returns
//	    // ErrNotOwner (ignorable here) when the record belongs to someone else.
//	}
//	switch res.State {
//	case idem.StateNew:
//	    // Do the work, then store.Complete(ctx, req, res.Token, out) on success, or
//	    // store.Abandon(ctx, req, res.Token) if nothing was persisted. Pass the *same*
//	    // Request value: a Fingerprint recomputed over a normalised body is a different
//	    // fingerprint, and Complete refuses it rather than corrupting the record.
//	case idem.StateInFlight:
//	    // 409 Conflict. Do not Abandon — this attempt holds nothing.
//	case idem.StateCompleted:
//	    // Answer the retry from res.Outcome — and still write the audit record (I13). A
//	    // replay is an access to user content even though it did no work, and metering
//	    // still counts the request (I12) while emitting none of the original's provider
//	    // costs.
//	    //
//	    // res.Outcome names *what* the operation produced; it is not the original
//	    // response body. Where the response carries values that expire or bear
//	    // credentials — §6.6's `POST /v1/uploads` answers with a presigned PUT and an
//	    // upload token — the handler must regenerate those from res.Outcome.Resource on
//	    // the replay path. They must not be stored here: a presigned URL expires at
//	    // limits.presign_ttl_seconds (15 minutes, §7.4) while the reservation lives for
//	    // hours, so a stored URL would replay as a dead credential, and it would be a
//	    // credential at rest in a record that outlives it (§9.1). Outcome.validate
//	    // rejects a URL-shaped Resource to keep that shortcut from being taken quietly.
//	}
package idem

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/vppillai/chintan/backend/internal/clock"
	"github.com/vppillai/chintan/backend/internal/keys"
	"github.com/vppillai/chintan/backend/internal/logging"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// Sentinel errors. All of these are client-visible outcomes or caller defects rather than
// internal faults, so they are compared with errors.Is at the handler boundary and mapped
// to a status there — this package does not know about HTTP status codes.
var (
	// ErrFingerprintMismatch means the key was reused for a different request.
	//
	// This must never be treated as a replay. Returning the stored outcome would tell the
	// client "here is your capture" while silently discarding the request it actually
	// sent, which loses a thought — the one failure mode this whole system is built to
	// avoid. It is also the safety net for a client that generates weak keys: a key
	// colliding with the client's own earlier request becomes a loud error instead of a
	// response describing the wrong resource.
	//
	// Returned by Complete and Abandon too, where it means the caller is about to write
	// over a reservation taken under a different request.
	ErrFingerprintMismatch = errors.New("idem: idempotency key reused with a different request")

	// ErrVanished means the reservation existed for the conditional write and was gone by
	// the time it was read back — the record expired in between, or a concurrent attempt
	// abandoned it.
	//
	// Deliberately an error rather than a silent fall-through to StateNew. Proceeding
	// would mean two attempts both doing the work, which is the duplicate this package
	// prevents. Failing is self-healing: the client's next retry finds a clean slate and
	// proceeds normally.
	ErrVanished = errors.New("idem: reservation vanished between the conditional write and the read")

	// ErrNotOwner means the stored reservation was claimed by a different attempt, so this
	// caller may neither complete nor release it.
	//
	// The case that makes this necessary is ordinary rather than exotic: an attempt
	// correctly told StateInFlight still runs its own cleanup path, and a cleanup that
	// deletes the owner's live reservation lets the retry create the second capture.
	ErrNotOwner = errors.New("idem: the reservation is held by a different attempt")

	// ErrCompleted means the reservation already records a completed operation, so it
	// cannot be released.
	//
	// Deleting it is never correct: the operation happened, and that record is the only
	// thing that answers the legitimate client's retry with the resource it created.
	ErrCompleted = errors.New("idem: the reservation is completed and must not be released")

	// ErrOutcomeConflict means Complete was asked to record an outcome that disagrees with
	// one already stored.
	//
	// Refused rather than overwritten, because the stored answer may already be in the
	// client's hands; changing it would make a later replay contradict the response the
	// client actually received.
	ErrOutcomeConflict = errors.New("idem: the reservation already records a different outcome")
)

// State is how far along the operation behind an idempotency key is.
type State string

const (
	// StateNew means the caller just claimed the key and owns the operation.
	StateNew State = "new"

	// StateInFlight means another attempt claimed the key and has not finished.
	StateInFlight State = "in_flight"

	// StateCompleted means the operation finished and its Outcome is stored.
	StateCompleted State = "completed"
)

// Outcome is what the original request produced, kept so a retry can be answered
// consistently with it.
//
// Status and Resource are the minimum that makes a replay useful: a client that retried
// because it never received a response is asking "did it happen, and what did it make".
// A bare "already seen" does not answer that, and forces the client either to give up or
// to go hunting for a resource it cannot name.
//
// **It is not the original response body, and it deliberately cannot be.** Some responses
// contain values that must not be stored and could not be replayed if they were:
// §6.6's `POST /v1/uploads` answers with a presigned PUT URL and an upload token, which
// bear credentials and expire at limits.presign_ttl_seconds — an order of magnitude
// sooner than the reservation. A handler for such a route regenerates those values from
// Resource on the replay path; validate refuses a URL-shaped Resource so that storing
// them is not an available shortcut.
type Outcome struct {
	// Status is the HTTP status the original response carried.
	Status int

	// Resource identifies what the operation created or modified — a capture ID, an item
	// ID, a thread ID. Required, and an identifier rather than a payload.
	Resource string
}

// Request identifies one attempt at an idempotent operation.
type Request struct {
	// Tenant scopes the key (I11), and comes from the validated JWT claim only — never
	// from a path, query, or body (§6.6). Two tenants using the same client-generated key
	// must not collide: client keys are frequently UUIDs but nothing stops a client from
	// sending "1", and an unscoped key would let one tenant's retry return another
	// tenant's resource identifier.
	Tenant keys.TenantID

	// Key is the Idempotency-Key header value, verbatim. Validated by keys.Idempotency,
	// which rejects an empty value and the key delimiters. Never echoed in an error or a
	// log line — see validate.
	Key string

	// Fingerprint identifies the request itself — use Fingerprint or FingerprintRequest to
	// compute it.
	//
	// Required rather than optional. Without it, "same key" is taken to mean "same
	// request", and a client that reuses a key by accident gets a confident, wrong
	// answer. An optional check is one that is not performed on the path where it
	// matters.
	//
	// The *same* value must be passed to Begin, Complete and Abandon. Recomputing it from
	// a normalised or defaulted body between calls is a caller defect that Complete
	// refuses (ErrFingerprintMismatch) rather than silently writing through, because a
	// record carrying a fingerprint the client will never reproduce answers that client's
	// genuine retry with a 422 for the whole TTL.
	Fingerprint string
}

// Store reserves and resolves idempotency keys.
type Store struct {
	repo     repository.Repository
	clk      clock.Clock
	log      *slog.Logger
	ttlHours int
}

// TTL bounds. Not defaults — New refuses a value outside them rather than substituting
// one, because §7.4 requires that "a missing threshold must fail the deploy, never fall
// back to a hardcoded default".
const (
	// maxTTLHours is one week. Past that the record has stopped being a retry window and
	// has become a permanent uniqueness constraint on a client-supplied string, which is
	// a different feature with a different failure mode: a client that reuses a key by
	// accident is then blocked for as long as the record lives.
	maxTTLHours = 24 * 7
)

// New builds a Store.
//
// ttlHours is the retention window for a reservation. **There is no config key for it in
// §7.4.** §7.4 says a threshold with no key is a spec bug to be flagged rather than
// hardcoded, so it is a constructor parameter and the gap is reported — see the package
// report. The recommended value is 24 hours: long enough to cover a phone that was
// offline overnight, short enough that a reservation stuck by a hard Lambda kill (below)
// does not block a capture for days.
//
// Fails at construction rather than at the first request. An idempotency store with a
// zero TTL writes records that never expire, and one with a negative TTL writes records
// DynamoDB deletes immediately — both silently disable the protection, and neither is
// visible until a duplicate appears in the corpus.
func New(repo repository.Repository, clk clock.Clock, log *slog.Logger, ttlHours int) (*Store, error) {
	if repo == nil {
		return nil, fmt.Errorf("idem: repository is required")
	}
	if clk == nil {
		// Nothing in this codebase calls time.Now() outside internal/clock, so there is
		// no fallback to substitute here (see the clock package).
		return nil, fmt.Errorf("idem: clock is required")
	}
	if log == nil {
		// The key-reuse warning is the only server-side signal a client defect produces,
		// and it sits on the one path where a nil logger would panic — turning a 422 the
		// client could act on into a 500 that hides the cause.
		return nil, fmt.Errorf("idem: logger is required")
	}
	if ttlHours <= 0 {
		return nil, fmt.Errorf("idem: ttl_hours is %d; a non-positive TTL writes reservations that never expire or expire instantly, either of which silently disables idempotency (§6.6)", ttlHours)
	}
	if ttlHours > maxTTLHours {
		return nil, fmt.Errorf("idem: ttl_hours is %d, above the %d-hour ceiling; beyond a week the record is a permanent uniqueness constraint on a client-supplied string, not a retry window", ttlHours, maxTTLHours)
	}
	return &Store{repo: repo, clk: clk, log: log, ttlHours: ttlHours}, nil
}

// Reservation is the answer to Begin.
type Reservation struct {
	State State

	// Outcome is populated only for StateCompleted.
	Outcome Outcome

	// StartedAt is when the reservation was first claimed, RFC3339 UTC. Populated for
	// StateNew, StateInFlight and StateCompleted.
	//
	// Exposed so a handler can log how long a conflicting attempt has been running, and
	// how long a completed operation took. A reservation held for hours means a worker
	// died mid-operation, and without this the only symptom is a 409 that never clears —
	// which reads as a client bug rather than the server-side death it is. It is
	// therefore the *claim* time on every path: Complete carries it through rather than
	// stamping its own, so a replay does not report an age of zero for an operation that
	// took ninety minutes.
	//
	// Empty for a record written before this attribute existed, which is honest: for such
	// a record the claim time is unknown, and reporting the last-write time instead would
	// be the wrong value dressed as the right one.
	StartedAt string

	// Token identifies this attempt, and is the caller's proof of ownership when it calls
	// Complete or Abandon.
	//
	// Populated for StateNew, and on the error returns where this attempt's own
	// reservation write may have committed — that is what makes a stranded key releasable
	// after a 500 (see the handler contract). Empty for StateInFlight and StateCompleted,
	// where this attempt holds nothing and has nothing to release.
	Token string
}

// Attribute names on the stored record. Kept together so the writer and the reader
// cannot drift apart.
const (
	attrState       = "state"
	attrFingerprint = "fingerprint"
	attrStatus      = "status"
	attrResource    = "resource"
	attrTS          = "ts"
	attrTTL         = "ttl"

	// attrAttempt is the per-attempt token. See the package doc: without it a committed
	// reservation write whose response was lost is indistinguishable from another
	// attempt's reservation.
	attrAttempt = "attempt"

	// attrStarted is the claim time, distinct from attrTS.
	//
	// attrTS is when this record was last written, which Complete moves; the claim time
	// must not move, because it is the only thing that makes "this reservation has been
	// in flight for two hours" or "this operation took ninety minutes" observable.
	attrStarted = "started_at"
)

// Begin claims the key for this attempt, or reports who already has it.
//
// The claim is repository.PutOnce — one conditional write, no prior read. Under two
// concurrent retries exactly one gets StateNew and the other gets StateInFlight; there
// is no interleaving in which both proceed.
//
// The one case where a *single* attempt is told StateNew twice is its own SDK-level
// retry, recognised by the attempt token (package doc). That is not a second owner: it is
// the same owner, and refusing it is what dead-ends a key for the whole TTL.
func (s *Store) Begin(ctx context.Context, req Request) (Reservation, error) {
	key, err := req.validate()
	if err != nil {
		return Reservation{}, err
	}

	attempt, err := newAttemptToken()
	if err != nil {
		// Nothing has been written, so there is no token to hand back and nothing to
		// release. Failing closed here is required: a reservation written without a token
		// cannot be recognised by its own creator.
		return Reservation{}, err
	}

	now := s.clk.Now()
	nowStr := clock.RFC3339UTC(now)
	ttl := s.expiry(now)

	item := repository.Item{
		Key: key,
		Attrs: map[string]any{
			attrState:       string(StateInFlight),
			attrFingerprint: req.Fingerprint,
			attrAttempt:     attempt,
			attrStarted:     nowStr,
			attrTS:          nowStr,
			attrTTL:         ttl,
		},
		TTL: ttl,
		// No GSI1 attributes. These records are per-request and short-lived; projecting
		// them into the sparse time-ordered index would make it a second copy of the
		// write traffic for records nothing ever lists (§6.3).
	}

	err = s.repo.PutOnce(ctx, item)
	switch {
	case err == nil:
		return Reservation{State: StateNew, StartedAt: nowStr, Token: attempt}, nil
	case errors.Is(err, repository.ErrAlreadyExists):
		// Fall through to read the existing record. It may be another attempt's — or this
		// attempt's own committed write, re-reported because the SDK retried it.
	default:
		// Not swallowed into StateNew. A storage failure here is indistinguishable from
		// "absent" if the error is discarded, and treating it as absent is how the
		// duplicate gets created on the one request where storage was unhealthy. The
		// token comes back because the write may nevertheless have landed.
		return Reservation{Token: attempt}, fmt.Errorf("idem: reserving idempotency key: %w", err)
	}

	existing, err := s.repo.Get(ctx, key)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return Reservation{Token: attempt}, ErrVanished
		}
		return Reservation{Token: attempt}, fmt.Errorf("idem: reading an existing reservation: %w", err)
	}

	stored, err := decode(existing)
	if err != nil {
		return Reservation{Token: attempt}, err
	}

	if stored.fingerprint != req.Fingerprint {
		// Logged at WARN because it is a client defect that will otherwise be invisible:
		// the client sees a 422 it did not expect and the server sees nothing. Neither
		// fingerprint nor the key value is logged — the key is client-supplied free text
		// and any channel that can carry content eventually does (§9.2).
		logging.FromContext(ctx, s.log).Warn("idempotency key reused with a different request",
			slog.String("state", string(stored.state)),
		)
		// No token: the stored record carries a different fingerprint, so it is provably
		// not this attempt's write and this attempt has nothing to release.
		return Reservation{}, ErrFingerprintMismatch
	}

	switch stored.state {
	case StateCompleted:
		return Reservation{
			State:     StateCompleted,
			Outcome:   Outcome{Status: stored.status, Resource: stored.resource},
			StartedAt: stored.started,
		}, nil
	case StateInFlight:
		if stored.attempt != "" && stored.attempt == attempt {
			// This attempt's own committed write, reported as a conflict because the SDK
			// re-sent it after losing the response. Reporting StateInFlight here is the
			// 409-that-never-clears failure: the only party that could ever finish the
			// operation is this one, and it has just been told it may not.
			return Reservation{State: StateNew, StartedAt: stored.started, Token: attempt}, nil
		}
		return Reservation{State: StateInFlight, StartedAt: stored.started}, nil
	default:
		return Reservation{Token: attempt}, fmt.Errorf("idem: stored reservation has unknown state %q", stored.state)
	}
}

// Complete records the outcome so a later retry gets the original answer.
//
// token is Reservation.Token from the Begin that returned StateNew. It is checked against
// the stored record, and so is req.Fingerprint: an unguarded overwrite would either
// record this outcome against another attempt's reservation, or replace the fingerprint
// the reservation was taken under with a different one — after which the client's genuine
// retry, sending the bytes it actually sent, is answered 422 for the whole TTL for a
// defect it does not have.
//
// The check is Get-then-Put, which is not atomic; the repository seam offers a conditional
// *create* and an unconditional write, no compare-and-set. It is a guard against caller
// mistakes, which is where the demonstrated failures come from — single ownership is
// PutOnce's job, not this function's.
//
// The TTL is recomputed from now rather than inherited from Begin. The window that
// matters runs from the moment the response was — or was not — delivered, so a slow
// operation must not eat into the client's retry window. The *claim* time is carried
// through unchanged (see attrStarted).
func (s *Store) Complete(ctx context.Context, req Request, token string, out Outcome) error {
	key, err := req.validate()
	if err != nil {
		return err
	}
	if err := out.validate(); err != nil {
		return err
	}
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("idem: an attempt token is required; pass Reservation.Token from the Begin that returned StateNew, so this outcome cannot be recorded against a reservation another attempt holds")
	}

	now := s.clk.Now()
	nowStr := clock.RFC3339UTC(now)
	startedAt := nowStr

	existing, getErr := s.repo.Get(ctx, key)
	switch {
	case getErr == nil:
		stored, err := decode(existing)
		if err != nil {
			return err
		}
		if stored.fingerprint != req.Fingerprint {
			// The reservation was taken under a different request. Writing through would
			// leave a record the retrying client can never match. Loud and server-side
			// makes the caller's inconsistency deterministic in the handler's own tests;
			// silent repair would make it permanent and invisible. Nothing is echoed:
			// neither fingerprint nor key value may reach a log (§9.2).
			logging.FromContext(ctx, s.log).Warn("outcome recorded against a reservation taken under a different request",
				slog.String("state", string(stored.state)),
			)
			return ErrFingerprintMismatch
		}
		switch stored.state {
		case StateCompleted:
			if stored.status == out.Status && stored.resource == out.Resource {
				// A repeat of the same Complete — a retried call, or a handler that lost
				// the response to this one. Idempotent, so it is not an error.
				return nil
			}
			return fmt.Errorf("idem: the reservation already records a completed outcome with status %d, and this call carries status %d: %w",
				stored.status, out.Status, ErrOutcomeConflict)
		case StateInFlight:
			if stored.attempt != token {
				return fmt.Errorf("idem: recording this outcome would answer another attempt's retries with this resource: %w", ErrNotOwner)
			}
			startedAt = stored.started
		default:
			return fmt.Errorf("idem: stored reservation has unknown state %q", stored.state)
		}
	case errors.Is(getErr, repository.ErrNotFound):
		// The reservation expired (or was released) while the operation ran. Recorded
		// anyway, deliberately: the operation *happened*, and refusing would leave no
		// record at all, so the client's retry would be answered StateNew and create the
		// second capture — the exact bug this package exists to prevent. WARN because a
		// TTL shorter than the operation is a configuration fault worth seeing, and
		// because it is also what a Complete with no prior Begin looks like.
		logging.FromContext(ctx, s.log).Warn("recording an outcome for a reservation that is no longer held",
			slog.Int("status", out.Status),
		)
	default:
		// Cannot verify, so does not write. The permissive direction here is the dangerous
		// one: a blind overwrite on an unreadable record can bury another request's
		// reservation, whereas refusing leaves the record in flight and fails closed.
		return fmt.Errorf("idem: reading the reservation before recording its outcome: %w", getErr)
	}

	ttl := s.expiry(now)
	item := repository.Item{
		Key: key,
		Attrs: map[string]any{
			attrState:       string(StateCompleted),
			attrFingerprint: req.Fingerprint,
			attrAttempt:     token,
			attrStatus:      int64(out.Status),
			attrResource:    out.Resource,
			attrStarted:     startedAt,
			attrTS:          nowStr,
			attrTTL:         ttl,
		},
		TTL: ttl,
	}
	if err := s.repo.Put(ctx, item); err != nil {
		return fmt.Errorf("idem: recording the outcome: %w", err)
	}
	return nil
}

// Abandon releases the reservation so the client's retry can proceed.
//
// **Only call this when nothing was persisted.** Abandoning after a partial write invites
// the duplicate: the retry finds a clean slate and does the work again on top of whatever
// the first attempt left behind.
//
// It exists because the alternative is worse. A handler that fails after Begin and does
// not abandon leaves an in-flight record for the whole TTL, and every retry gets 409 —
// so a transient provider error would block that capture for a day. There is a limit to
// what it can rescue: a hard Lambda kill (timeout, OOM) runs no deferred code, so that
// reservation does stay stuck until it expires. That is the accepted cost of a
// fail-closed reservation, and it is an argument for a TTL measured in hours rather than
// days.
//
// It refuses three cases, and each of them is a way this function turns into the
// duplicate-capture bug rather than the cure for it:
//
//   - A completed record (ErrCompleted). The operation happened; that record is what
//     answers the legitimate client's retry, and deleting it lets the retry create a
//     second capture. Refused even for the attempt that completed it.
//   - A reservation taken under a different request (ErrFingerprintMismatch). This is the
//     reachable one: a handler whose Begin returned ErrFingerprintMismatch runs its
//     cleanup with the *mismatched* Request, and would otherwise delete the completed
//     record belonging to the request that used the key first.
//   - A reservation held by a different attempt (ErrNotOwner). An attempt told 409 still
//     runs its cleanup, and deleting the owner's live reservation re-permits the
//     duplicate.
//
// A record it cannot decode, or one whose state it does not recognise, is refused for the
// same reason: ownership it cannot check is ownership it must assume belongs to someone
// else. The completed check runs first, so a mismatched request aimed at a completed
// record is refused as completed rather than as a mismatch — both save the record, and the
// stronger reason is the more useful one to report.
//
// A key that is not held at all is not an error: that is the normal state after a Begin
// that failed before its write landed, and the handler contract calls Abandon there
// precisely because it cannot know whether the write landed. Nothing is destroyed, so
// there is nothing to report.
func (s *Store) Abandon(ctx context.Context, req Request, token string) error {
	key, err := req.validate()
	if err != nil {
		return err
	}
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("idem: an attempt token is required to release a reservation; pass Reservation.Token from Begin, so a caller cannot delete a reservation it does not hold")
	}

	existing, err := s.repo.Get(ctx, key)
	switch {
	case err == nil:
	case errors.Is(err, repository.ErrNotFound):
		return nil
	default:
		// Refuses rather than deleting blind. An unreadable record cannot be shown to be
		// this attempt's, and the cost of being wrong is asymmetric: leaving a reservation
		// in place delays one capture by the TTL, deleting a completed one loses the answer
		// that prevents a duplicate.
		return fmt.Errorf("idem: reading the reservation before releasing it: %w", err)
	}

	stored, err := decode(existing)
	if err != nil {
		return err
	}
	if stored.state == StateCompleted {
		return fmt.Errorf("idem: refusing to release a completed reservation with status %d: %w", stored.status, ErrCompleted)
	}
	if stored.state != StateInFlight {
		// A state this code does not recognise is a record it cannot reason about, and
		// therefore not one it may delete: the possibility it is protecting something
		// outweighs one capture waiting out the TTL.
		return fmt.Errorf("idem: stored reservation has unknown state %q; refusing to release it", stored.state)
	}
	if stored.fingerprint != req.Fingerprint {
		logging.FromContext(ctx, s.log).Warn("release attempted against a reservation taken under a different request",
			slog.String("state", string(stored.state)),
		)
		return ErrFingerprintMismatch
	}
	if stored.attempt != token {
		return fmt.Errorf("idem: refusing to release a reservation this attempt does not hold: %w", ErrNotOwner)
	}

	// Delete, not a state transition to "abandoned": a deleted key is indistinguishable
	// from one never used, which is exactly the semantics wanted — the retry should run
	// the operation, not be told about a previous failure.
	if err := s.repo.Delete(ctx, key); err != nil {
		return fmt.Errorf("idem: releasing a reservation: %w", err)
	}
	return nil
}

// expiry converts the configured window into the absolute epoch second DynamoDB's TTL
// attribute requires.
//
// **Expiry is a lower bound on retention, not an upper bound**: DynamoDB deletes expired
// items on its own schedule, typically within 48 hours. Nothing here may depend on the
// record being gone promptly — and nothing does, because clients generate a fresh key per
// operation, so a lingering record is never in the way.
//
// The consequence in the other direction is the one that matters: **a retry arriving
// after the record expired is indistinguishable from a new request, and will create a
// second capture.** The TTL is therefore a floor on how long protection lasts, and must
// exceed the longest plausible client retry window. For audio specifically there is a
// second line of defence — content-hash ingest dedupe (§5A.3.4) catches a re-submitted
// recording — but nothing catches a duplicated PATCH, so the window is chosen for the
// client, not for storage cost.
func (s *Store) expiry(now time.Time) int64 {
	return now.Add(time.Duration(s.ttlHours) * time.Hour).Unix()
}

// newAttemptToken mints the per-attempt ownership token.
//
// crypto/rand rather than a counter or a timestamp: two Lambda invocations for the same
// key run in different processes with no shared state, and a token that collides between
// attempts would let one attempt release or complete another's reservation — which is
// the failure the token exists to prevent. 128 bits, so a collision is not a case worth
// reasoning about.
//
// A generation failure is fatal to the call rather than falling back to a fixed value:
// every attempt sharing one token is worse than no token at all, because it makes the
// ownership checks silently pass for the wrong caller (§0.5A — "an untested check is
// worse than no check, because it is believed").
func newAttemptToken() (string, error) {
	var b [16]byte
	if _, err := randRead(b[:]); err != nil {
		return "", fmt.Errorf("idem: generating an attempt token: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// randRead is crypto/rand.Read behind an indirection, so the fail-closed path above is
// tested rather than believed (§0.5A). Replaced only by this package's own tests.
var randRead = rand.Read

// Fingerprint digests a request so key reuse is detectable without storing the request.
//
// requestTarget is the **full request target including the query string** — net/http's
// r.URL.RequestURI(), not r.URL.Path. Two POSTs to the same route differing only in a
// query parameter are different operations, and digesting only the path makes the second
// one a replay of the first, answered with the first one's resource ID: precisely the
// confident wrong answer the fingerprint exists to prevent. FingerprintRequest exists so
// a caller does not have to remember this.
//
// Method participates too, not only the target: an empty-bodied PATCH would otherwise
// fingerprint identically to every other empty-bodied PATCH the client sends.
//
// The body is bounded and cheap to digest because audio never transits an API Gateway
// request (I3) — every POST and PATCH body here is small JSON, and the upload endpoint
// carries metadata rather than bytes.
//
// SHA-256 over the raw bytes, deliberately without canonicalisation. Two JSON encodings
// of the same object fingerprint differently, so a client that re-serialises before
// retrying gets a mismatch rather than a replay. That is the safe direction to fail —
// a false mismatch surfaces as a 422 the client can act on, whereas a false match returns
// the wrong resource silently — and clients are required to retry with the bytes they
// sent, which is what a queued-request replay does naturally.
func Fingerprint(method, requestTarget string, body []byte) string {
	h := sha256.New()
	// Each field is length-prefixed rather than concatenated: without it, method "POST"
	// with target "/x" and method "POS" with target "T/x" digest identically, and that
	// collision is reachable by a caller choosing an unlucky path rather than by an
	// attacker doing anything clever.
	//
	// sha256's Write never returns an error, which is why the results are ignored.
	for _, field := range [][]byte{[]byte(method), []byte(requestTarget), body} {
		_, _ = fmt.Fprintf(h, "%d:", len(field))
		_, _ = h.Write(field)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// FingerprintRequest computes the fingerprint from the request itself, so the query
// string cannot be dropped by accident.
//
// The mistake it removes is a one-word one — r.URL.Path where r.URL.RequestURI() was
// meant — and its symptom is a request answered as a replay of a different request. body
// is passed separately because the handler has to buffer it anyway (r.Body is a
// single-use stream, and the same bytes are needed for decoding).
//
// Returns "" for a nil request, which Request.validate rejects as a missing fingerprint:
// a nil request must not silently produce the digest of an empty one, which is a real
// fingerprint that would collide with every other caller making the same mistake.
func FingerprintRequest(r *http.Request, body []byte) string {
	if r == nil || r.URL == nil {
		return ""
	}
	return Fingerprint(r.Method, r.URL.RequestURI(), body)
}

// validate checks the request and returns its key.
func (r Request) validate() (keys.DynamoKey, error) {
	// The tenant is validated on its own first, because keys.Tenant's error names only
	// the tenant — an identifier, and one the logging package already puts on every line
	// — so it is safe to propagate verbatim.
	if _, err := keys.Tenant(r.Tenant); err != nil {
		return keys.DynamoKey{}, fmt.Errorf("idem: %w", err)
	}

	// The key's rejection message is written here rather than propagated, and the value is
	// **not** quoted. keys.validateIdent formats the offending value with %q, and this
	// error travels to a handler that will log it or put it in a response body — an error
	// message is as much a leak as a log line (§9.2). The rejection path is also the one
	// where the header holds arbitrary characters, so it is the path most likely to carry
	// something that should not be recorded: a client sending
	// "Idempotency-Key: call the supplier about the invoice" would otherwise write that
	// sentence to CloudWatch. Building the key is still the validation — keys.Idempotency
	// remains the authority, this only restates its verdict without its evidence (I11).
	key, err := keys.Idempotency(r.Tenant, r.Key)
	if err != nil {
		if strings.TrimSpace(r.Key) == "" {
			return keys.DynamoKey{}, fmt.Errorf("idem: idempotency_key is empty")
		}
		return keys.DynamoKey{}, fmt.Errorf("idem: idempotency_key of %d bytes contains characters not permitted in a key segment; its value is deliberately not quoted here because it is client-supplied free text (§9.2)", len(r.Key))
	}

	if strings.TrimSpace(r.Fingerprint) == "" {
		return keys.DynamoKey{}, fmt.Errorf("idem: fingerprint is required; without it a key reused for a different request returns the wrong resource silently (use idem.Fingerprint)")
	}
	return key, nil
}

// resourceRe is the shape a resource identifier may take: an ID, not a payload.
//
// Deliberately narrow. It is what makes the tempting wrong fix for §6.6's
// `POST /v1/uploads` — storing the presigned PUT URL so the replay can return it —
// impossible to do quietly. See Outcome.
var resourceRe = regexp.MustCompile(`^[A-Za-z0-9_.:+=@/~-]{1,256}$`)

func (o Outcome) validate() error {
	// Success only. A 4xx will fail again deterministically on replay, at no provider
	// cost and with nothing written, so pinning it for the TTL buys nothing and costs
	// clarity; a 5xx must be released with Abandon so the client's retry can proceed,
	// because recording a transient failure freezes it for the whole window. Restricting
	// the record to successes keeps its meaning exact: "this operation happened, and here
	// is what it produced".
	if o.Status < 200 || o.Status > 299 {
		return fmt.Errorf("idem: outcome status %d is not a success; a failed operation is released with Abandon, not recorded as completed", o.Status)
	}
	if strings.TrimSpace(o.Resource) == "" {
		// A replay that reports a status but names nothing cannot answer the question the
		// retrying client is asking, which is *which* resource its request created.
		return fmt.Errorf("idem: outcome resource is empty; a replay must be able to name what the original request produced")
	}
	if strings.Contains(o.Resource, "://") || strings.Contains(o.Resource, "?") {
		return fmt.Errorf("idem: outcome resource looks like a URL; a presigned URL or token must never be stored in a reservation — it bears a credential and expires at limits.presign_ttl_seconds, long before the reservation does, so a replay would hand the client a dead credential. Store the resource identifier and regenerate the URL on the replay path (§6.6, §9.1)")
	}
	if !resourceRe.MatchString(o.Resource) {
		return fmt.Errorf("idem: outcome resource of %d bytes is not an identifier; the reservation records what the operation produced, not the response body it produced", len(o.Resource))
	}
	return nil
}

// record is the decoded stored reservation.
type record struct {
	state       State
	fingerprint string
	attempt     string
	status      int
	resource    string
	started     string
	ts          string
}

func decode(it *repository.Item) (record, error) {
	var r record
	s, ok := it.Attrs[attrState].(string)
	if !ok || s == "" {
		return r, fmt.Errorf("idem: stored reservation has no %s attribute", attrState)
	}
	r.state = State(s)
	r.fingerprint, _ = it.Attrs[attrFingerprint].(string)
	r.attempt, _ = it.Attrs[attrAttempt].(string)
	r.ts, _ = it.Attrs[attrTS].(string)
	r.started, _ = it.Attrs[attrStarted].(string)
	r.resource, _ = it.Attrs[attrResource].(string)

	if r.state == StateCompleted {
		// Strict for a completed record, because a missing or unreadable status would
		// otherwise replay as status 0 — a response the client cannot interpret, produced
		// from a record that looked fine. Loud beats plausible here.
		n, err := intAttr(it.Attrs[attrStatus])
		if err != nil {
			return r, fmt.Errorf("idem: stored reservation has an unreadable %s attribute: %w", attrStatus, err)
		}
		r.status = n
		if r.resource == "" {
			return r, fmt.Errorf("idem: completed reservation has no %s attribute", attrResource)
		}
	}
	return r, nil
}

// intAttr reads a numeric attribute.
//
// Accepts the three shapes a DynamoDB number can arrive as depending on the unmarshaller
// in front of it (int, int64, or float64 via a JSON round trip) and errors on anything
// else. Liberal about equivalent encodings, loud about nonsense — the storage adapter
// that will read these records in production does not exist yet, so assuming one
// encoding here would be a bet rather than a contract.
func intAttr(v any) (int, error) {
	switch n := v.(type) {
	case int:
		return n, nil
	case int64:
		return int(n), nil
	case float64:
		return int(n), nil
	default:
		return 0, fmt.Errorf("expected a number, got %T", v)
	}
}
