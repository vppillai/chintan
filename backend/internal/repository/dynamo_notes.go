package repository

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/vppillai/chintan/backend/internal/model"
)

// MaxNotesDrained is the safety ceiling on how many of a tenant's notes one
// list reads from the base table before ordering them. It is a ceiling, not a
// window: a list is meant to see every note.
//
// The notes list is ordered by when each note was last touched, and the base
// table cannot answer that: its sort key is NOTE#<id> and a note id leads with
// its CREATION instant. Until 2026-09 a second index (gsi2) carried updated_at
// as its sort key so DynamoDB could do the ordering; it cost a backfill
// (`chintanctl reindex`), a two-step deploy procedure and an INCLUDE projection
// that had to be kept in step with the model, all to order a few hundred notes.
// The v3 decision to drop it stands, so the partition is drained and sorted in
// Go, and the drain has to reach every note for the order to be right: a
// bound of 500 here once made the list "the 500 most recently CREATED notes,
// ordered by touch", which silently dropped an old note dictated into today.
//
// What a full drain costs: the list projection is about 1 KB a row, so a
// tenant at this ceiling reads about 5 MB per page served, and a caller that
// pages through the whole list (DrainPages) pays that once per page. At the
// scale this serves — one person, hundreds of notes — that is a few hundred
// KB. If a tenant ever approaches the ceiling the remedy is the index, not a
// smaller number here; the drain reports Page.Truncated when it hits the
// ceiling so the service can log it and count NotesListTruncated.
const MaxNotesDrained = 5000

// noteTouchedSortKey renders a note's update time as a fixed-width instant, so
// that comparing two as strings compares them chronologically.
//
// The width is load-bearing. model.TimeLayout pads the fraction to nine digits
// precisely so a string comparison is a chronological comparison; RFC3339Nano
// trims trailing zeros, so "…:00Z" sorts ABOVE "…:00.1Z" because 'Z' > '.'.
// That defect was found and fixed in the capture router, where it handed the
// model the wrong fifty notes. A value written before the fixed-width layout
// existed is re-rendered here; an unparseable one is carried through as-is, so
// it sorts oddly, which is visible, rather than being dropped, which is not.
func noteTouchedSortKey(updatedAt string) string {
	if t, err := model.ParseTime(updatedAt); err == nil {
		return model.FormatTime(t)
	}
	return updatedAt
}

// listNotes drains the tenant's notes from the base table, keeps the ones on the
// requested shelf, orders them most recently touched first and pages the result.
//
// Ordering in Go is the whole design: see MaxNotesDrained for why there is no
// index. The page cursor is not a DynamoDB key — the walk it resumes is over a
// sorted slice this store built — so it carries the position in that order:
// the touch instant and id of the last note served. Resuming re-derives the
// offset by comparison rather than trusting a stored count, which is what keeps
// the cursor stable when a note is touched or created between two pages: the
// second page begins after the last note the client saw, whatever moved above
// it.
//
// The drain is always the light projection. search_text and cleaned_body,
// when asked for, are fetched afterwards for the page alone (hydrateNotes):
// every page of a paged list costs one drain, and a drain that carried 32 KB
// of search text per note for the whole partition to serve 200 of them was
// what made the offline corpus's ⌈N/200⌉ pages cost ⌈N/200⌉ × N × 32 KB.
func (s *DynamoStore) listNotes(ctx context.Context, tenantID, shelf string, keep func(model.NoteIndex) bool, opts ListOptions) (Page[model.NoteIndex], error) {
	if err := ctx.Err(); err != nil {
		return Page[model.NoteIndex]{}, err
	}
	after, err := decodeNoteCursor(opts.Cursor, tenantID, shelf)
	if err != nil {
		return Page[model.NoteIndex]{}, err
	}

	all, truncated, err := s.drainNotes(ctx, tenantID, ListOptions{})
	if err != nil {
		return Page[model.NoteIndex]{}, err
	}
	kept := make([]model.NoteIndex, 0, len(all))
	for _, n := range all {
		if keep(n) && opts.Keeps(n) {
			kept = append(kept, n)
		}
	}
	sortNotesMostRecentlyTouchedFirst(kept)

	start := 0
	if after != nil {
		// The first note that sorts after the cursor's position. A note touched
		// since page one moved above it and is skipped, exactly as it would
		// have been on an index walk; nothing below it is skipped or repeated.
		start = len(kept)
		for i, n := range kept {
			if NoteOrderKey(n) < after.key() {
				start = i
				break
			}
		}
	}
	end := min(start+int(opts.limit()), len(kept))
	page := Page[model.NoteIndex]{Items: kept[start:end], Truncated: truncated}
	if page.Items == nil {
		page.Items = []model.NoteIndex{}
	}
	if err := s.hydrateNotes(ctx, tenantID, page.Items, opts.IncludeSearchText, opts.IncludeCleanedBody); err != nil {
		return Page[model.NoteIndex]{}, err
	}
	if end < len(kept) {
		page.Cursor, err = encodeNoteCursor(tenantID, shelf, kept[end-1])
		if err != nil {
			return Page[model.NoteIndex]{}, err
		}
	}
	return page, nil
}

// DrainNotes is listNotes without the paging: one read of the partition, the
// shelf's notes in order, cut to what the caller asked for. The large opt-in
// fields ride the drain itself here, since the caller wants them for every
// row and a second read per row would be the cost this method exists to avoid.
func (s *DynamoStore) DrainNotes(ctx context.Context, tenantID string, opts DrainOptions) ([]model.NoteIndex, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	keep := noteShelfFilter(opts.Shelf, time.Now().Unix())
	all, truncated, err := s.drainNotes(ctx, tenantID, ListOptions{
		IncludeSearchText:  opts.IncludeSearchText,
		IncludeCleanedBody: opts.IncludeCleanedBody,
	})
	if err != nil {
		return nil, false, err
	}
	kept := make([]model.NoteIndex, 0, len(all))
	for _, n := range all {
		if keep(n) {
			kept = append(kept, n)
		}
	}
	sortNotesMostRecentlyTouchedFirst(kept)
	if opts.MaxItems > 0 && len(kept) > opts.MaxItems {
		kept = kept[:opts.MaxItems]
	}
	return kept, truncated, nil
}

// noteShelfFilter is the one definition of which notes are on which shelf,
// shared by the paged lists and the drain.
func noteShelfFilter(shelf NoteShelf, now int64) func(model.NoteIndex) bool {
	if shelf == NoteShelfArchived {
		return func(n model.NoteIndex) bool {
			return strings.TrimSpace(n.DeletedAt) != "" && n.PurgeAfterEpoch > now
		}
	}
	return func(n model.NoteIndex) bool {
		return strings.TrimSpace(n.DeletedAt) == ""
	}
}

// hydrateNotes fetches search_text and/or cleaned_body for exactly these notes
// with BatchGetItem and overlays them, so a paged list pays for the large
// fields of its page alone. Unprocessed keys — DynamoDB's answer to a batch
// over its 16 MB response cap, which two hundred 32 KB search texts can reach
// — are asked for again until every key has answered.
func (s *DynamoStore) hydrateNotes(ctx context.Context, tenantID string, notes []model.NoteIndex, searchText, cleanedBody bool) error {
	if len(notes) == 0 || (!searchText && !cleanedBody) {
		return nil
	}
	projection := "sk"
	if searchText {
		projection += ", " + searchTextAttr
	}
	if cleanedBody {
		projection += ", " + cleanedBodyAttr
	}
	byID := make(map[string]int, len(notes))
	keys := make([]map[string]types.AttributeValue, 0, len(notes))
	for i, n := range notes {
		byID[n.ID] = i
		keys = append(keys, map[string]types.AttributeValue{
			"pk": strAttr(userPK(tenantID)),
			"sk": strAttr(noteSK(n.ID)),
		})
	}
	items, err := s.batchGet(ctx, keys, projection, nil)
	if err != nil {
		return fmt.Errorf("dynamo hydrate notes: %w", err)
	}
	for _, item := range items {
		i, ok := byID[trimPrefix(readString(item, "sk"), "NOTE#")]
		if !ok {
			continue
		}
		if searchText {
			notes[i].SearchText = readString(item, searchTextAttr)
		}
		if cleanedBody {
			notes[i].CleanedBody = readString(item, cleanedBodyAttr)
		}
	}
	return nil
}

// drainNotes reads the tenant's note items, following LastEvaluatedKey (an
// unpaginated Query silently truncates at ~1 MB) until the partition is
// exhausted or MaxNotesDrained is reached. The bool reports the latter: the
// table had more notes than the list was allowed to read, and the order the
// caller builds is over an incomplete set.
func (s *DynamoStore) drainNotes(ctx context.Context, tenantID string, opts ListOptions) ([]model.NoteIndex, bool, error) {
	pk := userPK(tenantID)
	var start map[string]types.AttributeValue
	notes := make([]model.NoteIndex, 0, 64)
	projection := noteListProjection
	if opts.IncludeSearchText {
		projection += ", " + searchTextAttr
	}
	if opts.IncludeCleanedBody {
		projection += ", " + cleanedBodyAttr
	}

	for len(notes) < MaxNotesDrained {
		out, err := s.client.Query(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(s.tableName),
			KeyConditionExpression: aws.String("pk = :pk AND begins_with(sk, :sk_prefix)"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":pk":        strAttr(pk),
				":sk_prefix": strAttr("NOTE#"),
			},
			ProjectionExpression:     aws.String(projection),
			ExpressionAttributeNames: map[string]string{"#lang": languageAttr},
			// Newest CREATED first, so on the day the ceiling ever bites it
			// drops the oldest notes rather than the newest.
			ScanIndexForward:  aws.Bool(false),
			ExclusiveStartKey: start,
			Limit:             aws.Int32(int32(MaxNotesDrained - len(notes))),
		})
		if err != nil {
			return nil, false, fmt.Errorf("dynamo query notes: %w", err)
		}

		for _, raw := range out.Items {
			if readString(raw, "note_id") == "" {
				// An item written before the attributes were promoted carries
				// only the `data` blob, which the projection does not name.
				// Read it whole rather than dropping the note.
				n, err := s.GetNote(ctx, tenantID, trimPrefix(readString(raw, "sk"), "NOTE#"))
				if errors.Is(err, ErrNotFound) {
					continue
				}
				if err != nil {
					return nil, false, err
				}
				notes = append(notes, n)
				continue
			}
			n, err := noteFromItem(raw)
			if err != nil {
				return nil, false, err
			}
			notes = append(notes, n)
		}

		start = out.LastEvaluatedKey
		if len(start) == 0 {
			return notes, false, nil
		}
	}
	// The loop ended on the ceiling with a page still to read. (A tenant with
	// exactly MaxNotesDrained notes lands here too, because DynamoDB hands back
	// a LastEvaluatedKey whenever Limit is met; the false positive costs one
	// warning on an account that is a note away from the real thing.)
	return notes, true, nil
}

// maxPinRank bounds the rank NoteOrderKey can write in twelve digits. Fifty
// pins a step of a thousand apart never come near it; a rank past it is
// clamped, which only ties it with the last pinned note.
const maxPinRank = 999_999_999_999

// NoteOrderKey is the position of a note in the list: the pinned tier first,
// in pin_rank order with the more recently pinned note ahead on a tie, then
// the rest most recently touched first; the id breaks every remaining tie so
// the order is total and a cursor unambiguous. Larger sorts earlier, so the
// pinned tier leads with '1' and its rank is written inverted. It is exported
// so the repository tests can assert the order on the key itself.
func NoteOrderKey(n model.NoteIndex) string {
	if n.PinnedAt == "" {
		return "0" + noteTouchedSortKey(n.UpdatedAt) + "\x00" + n.ID
	}
	rank := min(max(n.PinRank, 0), maxPinRank)
	return "1" + fmt.Sprintf("%012d", maxPinRank-rank) + "\x00" + noteTouchedSortKey(n.PinnedAt) + "\x00" + n.ID
}

func sortNotesMostRecentlyTouchedFirst(notes []model.NoteIndex) {
	sort.SliceStable(notes, func(i, j int) bool {
		return NoteOrderKey(notes[i]) > NoteOrderKey(notes[j])
	})
}

// Note shelves. Archived is "has a deletion stamp"; whether it has also passed
// its purge deadline is a question about time, so the archived list also
// filters on purge_after_epoch — a note past its deadline is one the expiry
// sweep has not collected yet, and it must not appear in the archive the user
// is looking at.
const (
	noteShelfActive   = "ACTIVE"
	noteShelfArchived = "ARCHIVED"
)

func (s *DynamoStore) ListNotes(ctx context.Context, tenantID string, opts ListOptions) (Page[model.NoteIndex], error) {
	return s.listNotes(ctx, tenantID, noteShelfActive, noteShelfFilter(NoteShelfActive, 0), opts)
}

func (s *DynamoStore) ListArchivedNotes(ctx context.Context, tenantID string, opts ListOptions) (Page[model.NoteIndex], error) {
	return s.listNotes(ctx, tenantID, noteShelfArchived, noteShelfFilter(NoteShelfArchived, time.Now().Unix()), opts)
}

// ExpiredNotes returns every archived note, across every tenant, whose purge
// deadline had passed at asOf. It is the expiry sweep's input.
//
// This is the one Scan in the store, and it reads across tenants on purpose:
// the sweep is an instance job and the worker knows no tenant list. It is
// bounded by the table, which holds one tenant's few hundred notes plus their
// captures, and it runs weekly.
func (s *DynamoStore) ExpiredNotes(ctx context.Context, asOf int64) ([]TenantNote, error) {
	var start map[string]types.AttributeValue
	var out []TenantNote
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		res, err := s.client.Scan(ctx, &dynamodb.ScanInput{
			TableName: aws.String(s.tableName),
			// Only an archived note carries purge_after_epoch: PutNote deletes
			// it on restore, and no other item type writes it.
			FilterExpression:          aws.String("purge_after_epoch < :now"),
			ExpressionAttributeValues: map[string]types.AttributeValue{":now": numAttr(asOf)},
			ExpressionAttributeNames:  map[string]string{"#lang": languageAttr},
			ProjectionExpression:      aws.String("pk, " + noteListProjection),
			ExclusiveStartKey:         start,
		})
		if err != nil {
			return nil, fmt.Errorf("dynamo scan expired notes: %w", err)
		}
		for _, raw := range res.Items {
			n, err := noteFromItem(raw)
			if err != nil {
				return nil, err
			}
			if n.ID == "" {
				n.ID = trimPrefix(readString(raw, "sk"), "NOTE#")
			}
			out = append(out, TenantNote{
				TenantID: trimPrefix(readString(raw, "pk"), "USER#"),
				Note:     n,
			})
		}
		start = res.LastEvaluatedKey
		if len(start) == 0 {
			return out, nil
		}
	}
}

func (s *DynamoStore) GetNote(ctx context.Context, tenantID, noteID string) (model.NoteIndex, error) {
	if err := ctx.Err(); err != nil {
		return model.NoteIndex{}, err
	}

	result, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": strAttr(userPK(tenantID)),
			"sk": strAttr(noteSK(noteID)),
		},
		// Strongly consistent: NotesService.CreateNoteOnce decides on this
		// read whether to write a note's empty body, and a stale "not found"
		// for a note the crashed attempt just made would blank a body another
		// recording may already be in (R7-21).
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return model.NoteIndex{}, fmt.Errorf("dynamo get note: %w", err)
	}
	if result.Item == nil {
		return model.NoteIndex{}, ErrNotFound
	}
	return noteFromItem(result.Item)
}

// NoteExists is a GetItem projected to the sort key alone: DynamoDB charges
// the read by the item's full size either way, but nothing but the key crosses
// the wire, which is the cost that matters when a poll asks this for every
// receipt on the library screen.
func (s *DynamoStore) NoteExists(ctx context.Context, tenantID, noteID string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	result, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": strAttr(userPK(tenantID)),
			"sk": strAttr(noteSK(noteID)),
		},
		ProjectionExpression: aws.String("sk"),
	})
	if err != nil {
		return false, fmt.Errorf("dynamo note exists: %w", err)
	}
	return len(result.Item) > 0, nil
}

// NotesExist is NoteExists for a set: one BatchGetItem projected to the sort
// key, so a page of twenty receipts naming five notes costs one call, not five.
func (s *DynamoStore) NotesExist(ctx context.Context, tenantID string, noteIDs []string) (map[string]bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(noteIDs))
	keys := make([]map[string]types.AttributeValue, 0, len(noteIDs))
	for _, id := range noteIDs {
		if _, dup := out[id]; dup {
			continue
		}
		out[id] = false
		keys = append(keys, map[string]types.AttributeValue{
			"pk": strAttr(userPK(tenantID)),
			"sk": strAttr(noteSK(id)),
		})
	}
	if len(keys) == 0 {
		return out, nil
	}
	items, err := s.batchGet(ctx, keys, "sk", nil)
	if err != nil {
		return nil, fmt.Errorf("dynamo notes exist: %w", err)
	}
	for _, item := range items {
		out[trimPrefix(readString(item, "sk"), "NOTE#")] = true
	}
	return out, nil
}

// PutNote writes conditionally on the version the caller read. An unconditional
// PutItem loses a voice append that lands while the editor is open.
//
// The clean stamp is pinned too. StampCleanRequest deliberately leaves the
// version alone, so a whole-row write from a copy read before the tap lands
// under an unchanged version and, because noteItemAttrs writes every promoted
// attribute from that copy, puts the stamp back to what the caller saw —
// erasing the request; the worker then reads an unstamped row, judges itself
// superseded and writes nothing, and the Cleaned tab polls for a view that
// never arrives (review 2026-09-21, T12). Requiring the stored stamp to equal
// the one the caller read turns that into ErrVersionConflict, which every
// caller already answers by re-reading. attribute_not_exists admits a row
// written before the stamp was promoted.
func (s *DynamoStore) PutNote(ctx context.Context, tenantID string, note model.NoteIndex) (model.NoteIndex, error) {
	if err := ctx.Err(); err != nil {
		return model.NoteIndex{}, err
	}

	expected := note.Version
	next := note
	next.Version = expected + 1

	item, err := noteItemAttrs(tenantID, next)
	if err != nil {
		return model.NoteIndex{}, err
	}
	if next.PurgeAfterEpoch == 0 {
		// Restoring a note must clear the expiry, not leave the table poised to
		// delete it.
		delete(item, "purge_after_epoch")
		delete(item, "ttl")
	}

	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(s.tableName),
		Item:      item,
		ConditionExpression: aws.String("(" + versionCondition(expected) + ") AND " +
			"(attribute_not_exists(cleaned_requested_at) OR cleaned_requested_at = :stamp)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":expected": numAttr(expected),
			":stamp":    strAttr(note.CleanedRequestedAt),
		},
	})
	if err != nil {
		if isConditionalCheckFailed(err) {
			return model.NoteIndex{}, ErrVersionConflict
		}
		return model.NoteIndex{}, fmt.Errorf("dynamo put note: %w", err)
	}
	return next, nil
}

// StampNoteAppend is one conditional UpdateItem on the row's version. The blob
// is left as it was — its version and stamp fields go stale until the next
// PutNote rewrites it — which noteFromItem allows for by taking version and the
// stamp from the promoted attributes.
func (s *DynamoStore) StampNoteAppend(ctx context.Context, tenantID, noteID, captureID string, expectedVersion int64, at time.Time) (model.NoteIndex, error) {
	if err := ctx.Err(); err != nil {
		return model.NoteIndex{}, err
	}
	if captureID == "" {
		return model.NoteIndex{}, errors.New("repository: empty capture id for the append stamp")
	}
	out, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": strAttr(userPK(tenantID)),
			"sk": strAttr(noteSK(noteID)),
		},
		UpdateExpression:    aws.String("SET version = :next, appending_capture = :capture, appending_at = :at"),
		ConditionExpression: aws.String("attribute_exists(pk) AND (" + versionCondition(expectedVersion) + ")"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":expected": numAttr(expectedVersion),
			":next":     numAttr(expectedVersion + 1),
			":capture":  strAttr(captureID),
			":at":       strAttr(model.FormatTime(at)),
		},
		ReturnValues: types.ReturnValueAllNew,
	})
	if err != nil {
		if isConditionalCheckFailed(err) {
			// The condition says nothing about which clause failed, and the
			// caller's response differs: a missing note ends the append, a
			// moved version re-reads and stamps again.
			if _, getErr := s.GetNote(ctx, tenantID, noteID); errors.Is(getErr, ErrNotFound) {
				return model.NoteIndex{}, ErrNotFound
			} else if getErr != nil {
				return model.NoteIndex{}, getErr
			}
			return model.NoteIndex{}, ErrVersionConflict
		}
		return model.NoteIndex{}, fmt.Errorf("dynamo stamp note append: %w", err)
	}
	return noteFromItem(out.Attributes)
}

// ClearNoteAppend REMOVEs the two stamp attributes while they name captureID.
// A failed condition is not an error: either the row is gone, the stamp was
// already cleared, or a later append has stamped it for itself.
func (s *DynamoStore) ClearNoteAppend(ctx context.Context, tenantID, noteID, captureID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": strAttr(userPK(tenantID)),
			"sk": strAttr(noteSK(noteID)),
		},
		UpdateExpression:          aws.String("REMOVE appending_capture, appending_at"),
		ConditionExpression:       aws.String("attribute_exists(pk) AND appending_capture = :capture"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":capture": strAttr(captureID)},
	})
	if err != nil && !isConditionalCheckFailed(err) {
		return fmt.Errorf("dynamo clear note append: %w", err)
	}
	return nil
}

// StampCleanRequest is one conditional UpdateItem on the two stamp attributes.
// Like StampNoteAppend it leaves the blob behind; noteFromItem overlays the
// promoted attributes when the read projected them, and every read of this
// row does.
func (s *DynamoStore) StampCleanRequest(ctx context.Context, tenantID, noteID string, mode model.NoteCleanMode, at string, expectedVersion int64) (model.NoteIndex, error) {
	if err := ctx.Err(); err != nil {
		return model.NoteIndex{}, err
	}
	if at == "" {
		return model.NoteIndex{}, errors.New("repository: empty clean request stamp")
	}
	out, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": strAttr(userPK(tenantID)),
			"sk": strAttr(noteSK(noteID)),
		},
		UpdateExpression:    aws.String("SET cleaned_requested_at = :at, cleaned_requested_mode = :mode"),
		ConditionExpression: aws.String("attribute_exists(pk) AND (" + versionCondition(expectedVersion) + ")"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":expected": numAttr(expectedVersion),
			":at":       strAttr(at),
			":mode":     strAttr(string(mode)),
		},
		ReturnValues: types.ReturnValueAllNew,
	})
	if err != nil {
		if isConditionalCheckFailed(err) {
			if _, getErr := s.GetNote(ctx, tenantID, noteID); errors.Is(getErr, ErrNotFound) {
				return model.NoteIndex{}, ErrNotFound
			} else if getErr != nil {
				return model.NoteIndex{}, getErr
			}
			return model.NoteIndex{}, ErrVersionConflict
		}
		return model.NoteIndex{}, fmt.Errorf("dynamo stamp clean request: %w", err)
	}
	return noteFromItem(out.Attributes)
}

// ClearCleanStamp writes the two attributes empty rather than REMOVEing them:
// noteFromItem falls back to the blob for an attribute a read did not carry,
// and the blob may still hold the stamp from a whole-row write that carried
// it forward. A failed condition is not an error: the stamp is already gone
// or belongs to a later request.
func (s *DynamoStore) ClearCleanStamp(ctx context.Context, tenantID, noteID, at string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": strAttr(userPK(tenantID)),
			"sk": strAttr(noteSK(noteID)),
		},
		UpdateExpression:    aws.String("SET cleaned_requested_at = :empty, cleaned_requested_mode = :empty"),
		ConditionExpression: aws.String("attribute_exists(pk) AND cleaned_requested_at = :at"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":at":    strAttr(at),
			":empty": strAttr(""),
		},
	})
	if err != nil && !isConditionalCheckFailed(err) {
		return fmt.Errorf("dynamo clear clean stamp: %w", err)
	}
	return nil
}

func (s *DynamoStore) DeleteNote(ctx context.Context, tenantID, noteID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	_, err := s.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": strAttr(userPK(tenantID)),
			"sk": strAttr(noteSK(noteID)),
		},
		ConditionExpression: aws.String("attribute_exists(pk)"),
	})
	if err != nil {
		if isConditionalCheckFailed(err) {
			return ErrNotFound
		}
		return fmt.Errorf("dynamo delete note: %w", err)
	}
	return nil
}
