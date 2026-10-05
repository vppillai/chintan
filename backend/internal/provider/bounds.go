package provider

import "time"

// The bounds of the retry inside each provider client (retry.go), in one
// place with why each is its number, held to the table in
// TestProviderBoundsAreRegistered the way routing/bounds.go is held to its
// rule table: a bound changed in one place fails there.
//
// The retry exists for the shape the live model showed while the replay set
// was recorded: HTTP 529 ("overloaded") in bursts of about ten consecutive
// calls, each answered in well under a second. A capture that landed in such
// a burst failed its stage on the first refusal and wore a Retry button for
// a fault that was gone seconds later. The retry runs under the stage's own
// deadline and never extends it (docs/design/pipeline-deadlines.md,
// "Retries inside a deadline").
const (
	// ProviderRetryAttempts is how many times one call is sent, the first
	// included. Three: a burst that outlasts two short waits is an outage,
	// and the stage's own retry (routing, ask) or the person's Retry button
	// is the right answer to an outage, not a fourth call from the same
	// context.
	ProviderRetryAttempts = 3

	// ProviderRetryAttemptsAsk is the attempts for the Ask call: two, one
	// retry. Ask is interactive — the person is watching a spinner and the
	// app gives up after sixty seconds — and the ask stage already asks a
	// second time under a fresh deadline (pipeline.askModel), so a third
	// client try inside the first attempt would only move the answer, or
	// the fixed "could not be produced", later.
	ProviderRetryAttemptsAsk = 2

	// ProviderRetryBaseWait and ProviderRetryMaxWait bound the wait before
	// a retried call when the provider sent no Retry-After: full jitter,
	// uniform between zero and min(MaxWait, BaseWait doubled per attempt),
	// so the first wait is under two seconds and the second under four.
	// Six seconds at most in all: inside the shortest attempt a call runs
	// under (routing's fifteen seconds, ask's retry at fifteen) with room
	// for the calls themselves, and short enough that a burst is waited
	// out rather than reasoned about. A Retry-After the provider sends is
	// honoured as sent, under the context's deadline alone.
	ProviderRetryBaseWait = 1 * time.Second
	ProviderRetryMaxWait  = 4 * time.Second
)
