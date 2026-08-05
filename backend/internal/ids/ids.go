// Package ids generates the sortable unique identifiers this system's keys depend on.
//
// ULIDs, not UUIDs, and the choice is load-bearing rather than aesthetic. §6.1 requires
// that a transcription run id be "monotonic so runs sort by recency", and §6.3 uses a
// ulid as the unique component of usage, audit, and metric sort keys. A UUIDv4 is
// random, so those keys would sort arbitrarily and a range read over "the most recent
// audit records" would be impossible without a secondary index — which §6.3 forbids
// projecting them into.
//
// Monotonicity within a millisecond matters too: two usage records written in the same
// millisecond must still order deterministically, or a test that writes several and
// asserts their order is flaky.
package ids

import (
	"crypto/rand"
	"fmt"
	"sync"

	"github.com/oklog/ulid/v2"
	"github.com/vppillai/chintan/backend/internal/clock"
)

// Generator produces ULIDs.
//
// Holds a lock because the monotonic entropy source is stateful: it remembers the last
// value issued in the current millisecond in order to increment rather than re-randomise.
type Generator struct {
	mu      sync.Mutex
	clk     clock.Clock
	entropy *ulid.MonotonicEntropy
}

// NewGenerator builds a generator over the given clock.
//
// crypto/rand rather than math/rand: these identifiers appear in S3 object keys, and a
// predictable key is a guessable key. The presigned-URL design (I3) means a key alone
// does not grant access, so this is defence in depth rather than the control — but the
// cost of using a CSPRNG here is nil.
func NewGenerator(clk clock.Clock) *Generator {
	return &Generator{
		clk:     clk,
		entropy: ulid.Monotonic(rand.Reader, 0),
	}
}

// NewID returns a new ULID as a string.
func (g *Generator) NewID() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return ulid.MustNew(ulid.Timestamp(g.clk.Now()), g.entropy).String()
}

// NewRunID returns a transcription run identifier: {provider_key}-{ulid} (§6.1).
//
// Provider-identifying so a run is attributable without a lookup, and monotonic so runs
// sort by recency. **A path without a run id makes the second transcription of any
// capture an I1 violation**, which is why this exists from Phase 1 even though Phase 1
// produces exactly one run.
func (g *Generator) NewRunID(providerKey string) (string, error) {
	if providerKey == "" {
		return "", fmt.Errorf("ids: provider key is required; a run id must be attributable without a lookup (§6.1)")
	}
	// The key helper rejects '#' and '/' in identifiers, and a provider key containing
	// one would produce a run id that could forge a different path. Checked here so the
	// failure names the cause rather than surfacing later as an opaque key error.
	for _, r := range providerKey {
		if r == '#' || r == '/' {
			return "", fmt.Errorf("ids: provider key %q contains a key delimiter", providerKey)
		}
	}
	return providerKey + "-" + g.NewID(), nil
}
