package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
)

// AttrValue is one DynamoDB attribute in its wire form ("DynamoDB JSON").
//
// chintanctl carries attributes verbatim rather than decoding them into Go
// values, because backup and restore have to reproduce an item exactly. A
// number is stored as its decimal string and may not survive a float64; a
// string set is not a list; binary is not text. Decoding through
// map[string]any would lose all three, and a backup that silently rewrites
// what it restores is worse than no backup.
type AttrValue struct {
	S    *string              `json:"S,omitempty"`
	N    *string              `json:"N,omitempty"`
	B    []byte               `json:"B,omitempty"`
	BOOL *bool                `json:"BOOL,omitempty"`
	NULL *bool                `json:"NULL,omitempty"`
	L    []AttrValue          `json:"L,omitempty"`
	M    map[string]AttrValue `json:"M,omitempty"`
	SS   []string             `json:"SS,omitempty"`
	NS   []string             `json:"NS,omitempty"`
	BS   [][]byte             `json:"BS,omitempty"`
}

// MarshalJSON emits exactly the one member that is set.
//
// The struct tags carry omitempty for documentation, but omitempty would also
// drop an empty-but-present list or map — `{"L":[]}` is a legal DynamoDB
// attribute and must round-trip as itself, not as an absent attribute. So the
// discriminator is "non-nil", not "non-empty".
func (a AttrValue) MarshalJSON() ([]byte, error) {
	var body any
	var name string
	switch {
	case a.S != nil:
		name, body = "S", a.S
	case a.N != nil:
		name, body = "N", a.N
	case a.BOOL != nil:
		name, body = "BOOL", a.BOOL
	case a.NULL != nil:
		name, body = "NULL", a.NULL
	case a.B != nil:
		name, body = "B", a.B
	case a.L != nil:
		name, body = "L", a.L
	case a.M != nil:
		name, body = "M", a.M
	case a.SS != nil:
		name, body = "SS", a.SS
	case a.NS != nil:
		name, body = "NS", a.NS
	case a.BS != nil:
		name, body = "BS", a.BS
	default:
		return []byte("{}"), nil
	}
	return json.Marshal(map[string]any{name: body})
}

// StringAttr builds a string attribute.
func StringAttr(v string) AttrValue { return AttrValue{S: &v} }

// NumberAttr builds a number attribute from an integer.
func NumberAttr(v int64) AttrValue {
	s := strconv.FormatInt(v, 10)
	return AttrValue{N: &s}
}

// Item is one DynamoDB item, keyed by attribute name.
type Item map[string]AttrValue

// Str returns a string attribute, or "" when it is absent or not a string.
func (it Item) Str(name string) string {
	if v, ok := it[name]; ok && v.S != nil {
		return *v.S
	}
	return ""
}

// Num returns a number attribute as an int64, or 0 when it is absent, not a
// number, or not an integer.
func (it Item) Num(name string) int64 {
	v, ok := it[name]
	if !ok || v.N == nil {
		return 0
	}
	n, err := strconv.ParseInt(*v.N, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// PK returns the item's partition key.
func (it Item) PK() string { return it.Str("pk") }

// SK returns the item's sort key.
func (it Item) SK() string { return it.Str("sk") }

// Partition is raw, kind-agnostic access to one DynamoDB partition.
//
// It deliberately exposes no per-entity method. Every caller in this command
// walks a whole partition and decides what to do with each item afterwards,
// which is what keeps a schema addition inside the export instead of outside
// it. See enumerate.go.
type Partition interface {
	// Scan streams every item whose pk matches and whose sk begins with
	// skPrefix, in sort-key order. An empty skPrefix means the whole
	// partition. It is internally paginated: nothing accumulates a partition
	// in memory.
	Scan(ctx context.Context, pk, skPrefix string, fn func(Item) error) error
	// Put writes an item verbatim, overwriting whatever is there.
	Put(ctx context.Context, it Item) error
	// Update sets the given attributes on one existing item, conditionally on
	// its `version` attribute still being expectVersion (0 means the item
	// carries no version). It touches nothing else on the item, so a derived
	// attribute can be filled in behind a live API without rewriting the
	// record blob or bumping the version the API's optimistic concurrency
	// compares. A missing item or a moved version returns ErrItemChanged.
	Update(ctx context.Context, pk, sk string, set Item, expectVersion int64) error
	// Delete removes one item. Deleting an absent item is not an error.
	Delete(ctx context.Context, pk, sk string) error
}

// ErrItemChanged is returned by Partition.Update when the item is gone or its
// version is no longer the one the caller read. The caller re-reads or skips;
// it never overwrites.
var ErrItemChanged = errors.New("chintanctl: item changed since it was read")

// ObjectInfo is what a listing reports about one object. It is metadata only —
// the body is never part of a listing.
type ObjectInfo struct {
	Key  string `json:"key"`
	Size int64  `json:"size"`
	ETag string `json:"etag,omitempty"`
}

// Blobs is streaming, kind-agnostic access to the content bucket.
//
// Every body crosses this interface as an io.Reader. A tenant's corpus is
// mostly audio and can be gigabytes; no method here returns or accepts a
// []byte body, so no caller can accidentally buffer one.
type Blobs interface {
	// List streams every object under prefix, in key order.
	List(ctx context.Context, prefix string, fn func(ObjectInfo) error) error
	// Prefixes returns the immediate child prefixes of prefix, as a delimited
	// listing does. It is how tenants are discovered without a table scan.
	Prefixes(ctx context.Context, prefix string) ([]string, error)
	// Open streams one object. The caller closes it.
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	// Put streams body of the given size to key. Pass an io.ReadSeeker where
	// possible so the transport does not have to buffer to compute a checksum.
	Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) error
	// Delete removes one object. Deleting an absent object is not an error.
	Delete(ctx context.Context, key string) error
}
