package repository

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/vppillai/chintan/backend/internal/keys"
)

// Memory is an in-memory Repository for tests and the fake-AWS harness (§11.5).
//
// It is deliberately strict where DynamoDB is strict, because a fake that is more
// permissive than the real thing lets a test pass on code that would fail in
// production — which is worse than no fake. Specifically:
//
//   - PutOnce enforces the conditional write, so idempotency and write-once behaviour
//     are exercised rather than assumed.
//   - QueryPrefix returns items in sort-key order, which is what makes the zero-padded
//     segment sequence observable (see keys.Segment).
//   - An item over 400KB is rejected, because that ceiling is what forces the S3
//     overflow path for long verbatim prompt bodies (§3A.4) — a fake with no ceiling
//     would let that path go untested until a real prompt hit it. The size is measured
//     from the marshalled form, so a body hidden inside a list, a map, or a []byte is
//     seen; an estimate that counted a fixed 8 bytes for those was a ceiling in name only.
//   - A value DynamoDB cannot store faithfully is rejected here too, by asking the
//     production marshaller rather than by keeping a second list of the rules — see
//     validateItem.
type Memory struct {
	mu    sync.RWMutex
	items map[string]Item

	// failNext lets a test inject a failure at a chosen call, so error paths are
	// covered. I2's "audio is never lost to a software bug" and the patch-rejection
	// path in §Phase 3 both need a way to make storage fail on demand.
	failNext error
}

// dynamoItemLimit is DynamoDB's maximum item size. Enforced by the fake so the S3
// overflow path for oversized item bodies is exercised in tests (§3A.4).
const dynamoItemLimit = 400 * 1024

// NewMemory returns an empty in-memory repository.
func NewMemory() *Memory {
	return &Memory{items: make(map[string]Item)}
}

// FailNext makes the next operation return err.
func (m *Memory) FailNext(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failNext = err
}

func (m *Memory) takeFailure() error {
	if m.failNext != nil {
		err := m.failNext
		m.failNext = nil
		return err
	}
	return nil
}

// compositeKey joins PK and SK with a separator that cannot occur in either.
//
// The key helper rejects the delimiter in every identifier, so a NUL byte here cannot
// collide with content — and using a character the helper permits would let two
// distinct keys map to one map entry, which is the fake silently losing a record.
func compositeKey(k keys.DynamoKey) string { return k.PK + "\x00" + k.SK }

// Get returns one item, or ErrNotFound.
func (m *Memory) Get(_ context.Context, key keys.DynamoKey) (*Item, error) {
	m.mu.Lock()
	if err := m.takeFailure(); err != nil {
		m.mu.Unlock()
		return nil, err
	}
	m.mu.Unlock()

	m.mu.RLock()
	defer m.mu.RUnlock()
	it, ok := m.items[compositeKey(key)]
	if !ok {
		return nil, fmt.Errorf("%w: %s / %s", ErrNotFound, key.PK, key.SK)
	}
	// Return a copy: a caller mutating the returned map must not reach into storage.
	// DynamoDB cannot be mutated that way, so a fake that can would hide the bug.
	return copyItem(&it), nil
}

// Put writes or replaces one item.
func (m *Memory) Put(_ context.Context, item Item) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.takeFailure(); err != nil {
		return err
	}
	if err := validateItem(item); err != nil {
		return err
	}
	m.items[compositeKey(item.Key)] = *copyItem(&item)
	return nil
}

// PutOnce writes one item, failing if the key exists.
func (m *Memory) PutOnce(_ context.Context, item Item) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.takeFailure(); err != nil {
		return err
	}
	if err := validateItem(item); err != nil {
		return err
	}
	ck := compositeKey(item.Key)
	if _, exists := m.items[ck]; exists {
		return fmt.Errorf("%w: %s / %s", ErrAlreadyExists, item.Key.PK, item.Key.SK)
	}
	m.items[ck] = *copyItem(&item)
	return nil
}

// QueryPrefix returns items in one partition matching a sort-key prefix, ordered.
func (m *Memory) QueryPrefix(_ context.Context, pk, skPrefix string, limit int) ([]Item, error) {
	m.mu.Lock()
	if err := m.takeFailure(); err != nil {
		m.mu.Unlock()
		return nil, err
	}
	m.mu.Unlock()

	m.mu.RLock()
	defer m.mu.RUnlock()

	var out []Item
	for _, it := range m.items {
		if it.Key.PK != pk {
			continue
		}
		if skPrefix != "" && !strings.HasPrefix(it.Key.SK, skPrefix) {
			continue
		}
		out = append(out, *copyItem(&it))
	}
	// Sort-key order, as DynamoDB returns. Without this the fake's iteration order is
	// Go's map order, which is randomised — so a test asserting segment order would
	// pass or fail at random and be labelled flaky.
	sort.Slice(out, func(i, j int) bool { return out[i].Key.SK < out[j].Key.SK })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Delete removes one item. Absent is not an error, matching DynamoDB.
func (m *Memory) Delete(_ context.Context, key keys.DynamoKey) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.takeFailure(); err != nil {
		return err
	}
	delete(m.items, compositeKey(key))
	return nil
}

// Len reports how many items are stored, for test assertions.
func (m *Memory) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.items)
}

// Keys returns every stored key, sorted. Used by verify.sh's fixture mode and by tests
// that assert on what was written rather than on a return value.
func (m *Memory) Keys() []keys.DynamoKey {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]keys.DynamoKey, 0, len(m.items))
	for _, it := range m.items {
		out = append(out, it.Key)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PK != out[j].PK {
			return out[i].PK < out[j].PK
		}
		return out[i].SK < out[j].SK
	})
	return out
}

func validateItem(item Item) error {
	if item.Key.PK == "" || item.Key.SK == "" {
		// An empty key component means the caller bypassed the key helper, which is
		// the I11 violation check-tenant-keys.sh exists to catch statically. Catching
		// it at runtime too means a path the static check missed still fails.
		return fmt.Errorf("repository: item has an empty key component (PK=%q SK=%q); keys must come from the keys package (I11)", item.Key.PK, item.Key.SK)
	}
	// measureAttrs lives with the DynamoDB marshaller, and the fake calling into it is
	// deliberate rather than a layering slip: it is what makes "the fake refuses exactly
	// the items production refuses" true by construction. A separate size estimate and a
	// separate list of representable values in this file would drift, and the drift is
	// invisible — every test runs here and only production runs there. The first version
	// of this file estimated the size itself, counted 8 bytes for a []byte or a nested map,
	// and so let a 600KB body through a 400KB ceiling.
	n, err := measureAttrs(item)
	if err != nil {
		return err
	}
	if n > dynamoItemLimit {
		return fmt.Errorf("repository: item is ~%d bytes, over the %d-byte DynamoDB ceiling; oversized bodies go to S3 and are referenced by text_key (§3A.4)", n, dynamoItemLimit)
	}
	return nil
}

func copyItem(in *Item) *Item {
	out := *in
	// len rather than a nil check: a record with no attributes must read back as a nil map
	// from both implementations. DynamoDB stores no attribute for an empty map and cannot
	// tell an empty map from an absent one, so the adapter has no way to return a non-nil
	// empty map faithfully (see unmarshalItem) — and preserving one here would make a
	// caller's `if item.Attrs == nil` take one branch in every test and the other in
	// production.
	if len(in.Attrs) > 0 {
		out.Attrs = make(map[string]any, len(in.Attrs))
		for k, v := range in.Attrs {
			out.Attrs[k] = v
		}
	} else {
		out.Attrs = nil
	}
	return &out
}
