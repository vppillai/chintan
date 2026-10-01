// Enumeration is by S3 prefix and DynamoDB partition, NEVER by entity type.
//
// This is the one property worth copying from the archived implementation, and
// it is the difference between a backup and a backup-shaped hole. If this file
// iterated a hardcoded list of known kinds — notes, captures, settings — then
// the day somebody adds a new item kind or a new per-capture artifact, that
// data would stop being exported, stop being backed up, and stop being erased,
// and nothing would say so. The first anybody would learn of it is a restore
// that came back short.
//
// So:
//
//   - Objects are found by listing the tenant's S3 prefix and taking every key
//     under it, whatever its shape.
//   - Items are found by querying the tenant's DynamoDB partition and taking
//     every item, whatever its sort key.
//
// Classification happens strictly afterwards, and only to decide how something
// is *presented*. Anything unrecognised is still carried: an unknown object is
// copied to a mirrored path, an unknown item is written out verbatim. Losing
// the pretty rendering of a new kind is a cosmetic regression; losing the data
// is not. TestExportCapturesUnknownKinds pins this.
//
// The rule is a property of this whole file, so it is written as a file comment
// rather than pinned to a declaration. The blank line below keeps it out of the
// package doc, which main.go owns.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/vppillai/chintan/backend/internal/model"
)

// tenantsPrefix is the root every tenant's objects live under. It matches
// internal/keys, which builds every key as tenants/<tenantId>/...
const tenantsPrefix = "tenants/"

// tenantIDRe is the charset internal/keys enforces on every identifier it
// interpolates into a key. It is repeated here because a tenant id is
// concatenated into an S3 prefix and a DynamoDB partition key, and a '#' or a
// '/' in one would address somebody else's data.
var tenantIDRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func checkTenantID(id string) error {
	if !tenantIDRe.MatchString(id) {
		return fmt.Errorf("invalid tenant id %q: expected %s", id, tenantIDRe.String())
	}
	return nil
}

// tenantPrefix is the S3 prefix holding everything belonging to one tenant.
func tenantPrefix(tenantID string) string {
	return tenantsPrefix + tenantID + "/"
}

// tenantPK is the DynamoDB partition key holding one tenant's items. It
// mirrors internal/repository's userPK.
func tenantPK(tenantID string) string {
	return "USER#" + tenantID
}

// discoverTenants lists the tenant ids that have objects in the bucket.
//
// Tenants are discovered from S3 rather than from DynamoDB because finding
// every partition key in a single-table design needs a Scan, and the
// infrastructure deliberately grants no dynamodb:Scan. A tenant that has index
// rows but no objects at all is therefore invisible here; pass --tenant to
// address one explicitly.
func discoverTenants(ctx context.Context, blobs Blobs) ([]string, error) {
	prefixes, err := blobs.Prefixes(ctx, tenantsPrefix)
	if err != nil {
		return nil, fmt.Errorf("list tenant prefixes: %w", err)
	}
	out := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		id := strings.TrimSuffix(strings.TrimPrefix(p, tenantsPrefix), "/")
		if id == "" {
			continue
		}
		if err := checkTenantID(id); err != nil {
			// A key that does not fit the layout is reported, never silently
			// skipped, and never turned into a prefix we would then delete.
			return nil, fmt.Errorf("bucket contains an unexpected tenant prefix %q: %w", p, err)
		}
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

// resolveTenants returns the tenants a command should act on: the explicit
// list if one was given, otherwise everything discoverable.
func resolveTenants(ctx context.Context, blobs Blobs, explicit []string) ([]string, error) {
	if len(explicit) > 0 {
		for _, t := range explicit {
			if err := checkTenantID(t); err != nil {
				return nil, err
			}
		}
		out := append([]string(nil), explicit...)
		sort.Strings(out)
		return out, nil
	}
	return discoverTenants(ctx, blobs)
}

// objectRef is the structure chintanctl can recognise in a key. Everything
// else keeps Group == "" and is handled as opaque content.
type objectRef struct {
	TenantID string
	Group    string // "notes", "captures", or "" when the shape is unrecognised
	EntityID string
	File     string
	Rest     string // key with the tenant prefix removed
}

// parseObjectKey splits a key of the form
// tenants/<tenantId>/<group>/<entityId>/<file>. A key that does not fit
// returns Group == "" with Rest still populated, so the caller can carry it
// without understanding it.
func parseObjectKey(key string) objectRef {
	ref := objectRef{}
	rest, ok := strings.CutPrefix(key, tenantsPrefix)
	if !ok {
		ref.Rest = key
		return ref
	}
	tenant, after, ok := strings.Cut(rest, "/")
	if !ok {
		ref.Rest = rest
		return ref
	}
	ref.TenantID = tenant
	ref.Rest = after
	parts := strings.Split(after, "/")
	if len(parts) != 3 {
		return ref
	}
	switch parts[0] {
	case "notes", "captures":
		ref.Group = parts[0]
		ref.EntityID = parts[1]
		ref.File = parts[2]
	}
	return ref
}

// tenantIndex is the metadata half of one tenant's state: enough to render an
// export and to reconcile pointers, and nothing else. Bodies never land here.
type tenantIndex struct {
	TenantID string
	Notes    map[string]model.NoteIndex
	Captures map[string]model.CaptureIndex
	// NoteCaptures maps a note id to its captures, newest last.
	NoteCaptures map[string][]string
	// SortKeys is every sk seen in the partition, in order.
	SortKeys []string
	// ItemCount is every item seen, including the ones no field above
	// describes.
	ItemCount int
	// UnknownSKs is the sort keys this build of chintanctl has no renderer
	// for. They are still exported and still backed up; this only drives the
	// summary.
	UnknownSKs []string
}

// itemVisitor is called once per item in the partition walk, after the index
// has recorded it. It is how export writes unknown kinds out as it goes rather
// than accumulating them.
type itemVisitor func(it Item) error

// buildIndex walks a tenant's whole DynamoDB partition.
//
// Note the shape: the walk takes every item, and the switch below only decides
// whether this build knows how to render one. An sk this code has never heard
// of still increments ItemCount, still reaches the visitor, and therefore still
// reaches the export.
func buildIndex(ctx context.Context, part Partition, tenantID string, visit itemVisitor) (*tenantIndex, error) {
	idx := &tenantIndex{
		TenantID:     tenantID,
		Notes:        map[string]model.NoteIndex{},
		Captures:     map[string]model.CaptureIndex{},
		NoteCaptures: map[string][]string{},
	}
	err := part.Scan(ctx, tenantPK(tenantID), "", func(it Item) error {
		idx.ItemCount++
		sk := it.SK()
		idx.SortKeys = append(idx.SortKeys, sk)

		switch {
		case strings.HasPrefix(sk, "NOTE#"):
			n, err := noteFromItem(it)
			if err != nil {
				return err
			}
			idx.Notes[n.ID] = n
		case strings.HasPrefix(sk, "CAPTURE#"):
			c, err := captureFromItem(it)
			if err != nil {
				return err
			}
			idx.Captures[c.ID] = c
			if c.NoteID != "" {
				idx.NoteCaptures[c.NoteID] = append(idx.NoteCaptures[c.NoteID], c.ID)
			}
		default:
			idx.UnknownSKs = append(idx.UnknownSKs, sk)
		}

		if visit != nil {
			return visit(it)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for noteID := range idx.NoteCaptures {
		ids := idx.NoteCaptures[noteID]
		sort.Slice(ids, func(i, j int) bool {
			return idx.Captures[ids[i]].CreatedAt < idx.Captures[ids[j]].CreatedAt
		})
	}
	return idx, nil
}

// noteFromItem decodes the note record from the `data` blob, which every
// writer in internal/repository fills with the whole model. The promoted
// attributes beside it are for queries, not for reading the record back: the
// only two it reads are the ones the blob deliberately omits (json:"-"). A row
// without the blob, or whose blob names no id, fails the run: the error goes
// up through buildIndex, backfill, latency and reconcile --apply, and nothing
// is half-reconstructed from the sort key. Until 2026-10-01 a 21-key
// fallback rebuilt a note or capture from the promoted attributes; a scan of
// both tables that day found no NOTE# or CAPTURE# row without `data` (0 of 225),
// so the fallback had nothing to decode and was deleted (PR9-50, PR10-5).
func noteFromItem(it Item) (model.NoteIndex, error) {
	var n model.NoteIndex
	blob := it.Str("data")
	if blob == "" {
		return model.NoteIndex{}, fmt.Errorf("decode note %s: no data blob", it.SK())
	}
	if err := json.Unmarshal([]byte(blob), &n); err != nil {
		return model.NoteIndex{}, fmt.Errorf("decode note %s: %w", it.SK(), err)
	}
	if n.ID == "" {
		return model.NoteIndex{}, fmt.Errorf("decode note %s: the data blob names no id", it.SK())
	}
	n.SearchText = it.Str("search_text")
	n.CleanedBody = it.Str("cleaned_body")
	return n, nil
}

// captureFromItem decodes the capture record, same contract as noteFromItem:
// the blob is the record. What the blob says about note_id is what reconcile
// judges a capture by.
func captureFromItem(it Item) (model.CaptureIndex, error) {
	var c model.CaptureIndex
	blob := it.Str("data")
	if blob == "" {
		return model.CaptureIndex{}, fmt.Errorf("decode capture %s: no data blob", it.SK())
	}
	if err := json.Unmarshal([]byte(blob), &c); err != nil {
		return model.CaptureIndex{}, fmt.Errorf("decode capture %s: %w", it.SK(), err)
	}
	if c.ID == "" {
		return model.CaptureIndex{}, fmt.Errorf("decode capture %s: the data blob names no id", it.SK())
	}
	return c, nil
}

// referencedKeys returns every S3 key one tenant's index rows point at. It is
// one half of reconcile: an index row whose object is missing.
func referencedKeys(idx *tenantIndex) map[string]string {
	refs := map[string]string{}
	add := func(key, owner string) {
		if key != "" {
			refs[key] = owner
		}
	}
	for id, n := range idx.Notes {
		add(n.S3MarkdownKey, "NOTE#"+id)
		add(n.S3MetaKey, "NOTE#"+id)
	}
	for id, c := range idx.Captures {
		owner := "CAPTURE#" + id
		add(c.AudioKey, owner)
		add(c.RawKey, owner)
		add(c.RoutedKey, owner)
		add(c.CleanKey, owner)
		add(c.SegmentsKey, owner)
		add(c.PeaksKey, owner)
	}
	return refs
}
