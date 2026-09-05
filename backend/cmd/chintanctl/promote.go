package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	dynamotypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// hasAttr reports whether the row carries the attribute at all. Presence is
// the question, not value: a promoted empty string is a promoted attribute.
func hasAttr(it Item, name string) bool {
	_, ok := it[name]
	return ok
}

// promotedAttributes is what reconcile's --apply writes onto a row that
// predates attribute promotion: every attribute internal/repository would put
// on the item today, built by the store's own item builders from the record
// decoded off the row, minus the key and the blob. The blob is the record and
// stays as it was written; the version comes from it like every other
// attribute, and the write is conditioned on the row's version, so a row the
// API rewrote in the meantime is skipped rather than overwritten.
func promotedAttributes(tenantID string, it Item) (Item, error) {
	var (
		attrs map[string]dynamotypes.AttributeValue
		err   error
	)
	switch sk := it.SK(); {
	case strings.HasPrefix(sk, "NOTE#"):
		n, derr := noteFromItem(it)
		if derr != nil {
			return nil, derr
		}
		attrs, err = repository.NoteItemAttributes(tenantID, n)
	case strings.HasPrefix(sk, "CAPTURE#"):
		c, derr := captureFromItem(it)
		if derr != nil {
			return nil, derr
		}
		c.UserID = tenantID
		attrs, err = repository.CaptureItemAttributes(c)
	default:
		return nil, fmt.Errorf("promote %s: not a note or capture row", sk)
	}
	if err != nil {
		return nil, err
	}
	set := itemFromSDK(attrs)
	delete(set, "pk")
	delete(set, "sk")
	delete(set, "data")
	// search_text and cleaned_body are promoted-only and never in the blob;
	// the builders emit them from the decoded model, which reconcile filled
	// from the row itself, so what is written back is what was there.
	return set, nil
}

// describeNoteShelf says which list, if any, would show a note as decoded — the
// detail an operator needs to decide whether an unlisted note is missing from
// the library or was archived and forgotten by a deadline that was never set.
func describeNoteShelf(n model.NoteIndex) string {
	switch {
	case strings.TrimSpace(n.DeletedAt) == "" && n.UpdatedAt == "":
		return "active, but with no updated_at it sorts last in the library"
	case strings.TrimSpace(n.DeletedAt) == "":
		return "active"
	case n.PurgeAfterEpoch == 0:
		return "archived with no purge deadline, so neither shelf lists it and no sweep collects it"
	default:
		return "archived"
	}
}

// blobWithAudioBytes is the row's record blob with audio_bytes set to size and
// everything else as it was. The blob is handled as raw JSON — numbers kept as
// their lexemes, unknown fields kept — because the model this build was
// compiled with is not the authority on what an older or newer row may carry;
// only the one field is this repair's business.
func blobWithAudioBytes(it Item, size int64) (AttrValue, error) {
	blob := it.Str("data")
	if blob == "" {
		return AttrValue{}, errors.New("row has no record blob to write the size into")
	}
	dec := json.NewDecoder(strings.NewReader(blob))
	dec.UseNumber()
	var record map[string]any
	if err := dec.Decode(&record); err != nil {
		return AttrValue{}, fmt.Errorf("decode record blob of %s: %w", it.SK(), err)
	}
	record["audio_bytes"] = json.Number(strconv.FormatInt(size, 10))
	out, err := json.Marshal(record)
	if err != nil {
		return AttrValue{}, fmt.Errorf("encode record blob of %s: %w", it.SK(), err)
	}
	return StringAttr(string(out)), nil
}
