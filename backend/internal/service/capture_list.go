package service

import (
	"context"
	"encoding/base64"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// CaptureFilter selects which captures a list returns. The values are the
// `status` query parameter of GET /v1/captures.
type CaptureFilter string

const (
	// CaptureFilterAll returns every capture, newest first.
	CaptureFilterAll CaptureFilter = "all"
	// CaptureFilterPending returns captures still moving through the pipeline.
	// This is what backs the progress card, which must survive a reload.
	CaptureFilterPending CaptureFilter = "pending"
	// CaptureFilterFailed returns captures that stopped with a fault, including
	// the distinct spend-capped outcome.
	CaptureFilterFailed CaptureFilter = "failed"
	// CaptureFilterNeedsTarget returns captures waiting on the user to choose a
	// destination note.
	CaptureFilterNeedsTarget CaptureFilter = "needs_target"
)

// ParseCaptureFilter maps a query parameter to a filter, defaulting to all.
func ParseCaptureFilter(v string) (CaptureFilter, bool) {
	switch CaptureFilter(strings.TrimSpace(v)) {
	case "", CaptureFilterAll:
		return CaptureFilterAll, true
	case CaptureFilterPending:
		return CaptureFilterPending, true
	case CaptureFilterFailed:
		return CaptureFilterFailed, true
	case CaptureFilterNeedsTarget:
		return CaptureFilterNeedsTarget, true
	default:
		return "", false
	}
}

func (f CaptureFilter) keep(c model.CaptureIndex) bool {
	switch f {
	case CaptureFilterPending:
		return CaptureIsPending(c.Status)
	case CaptureFilterFailed:
		return c.Status == model.StatusFailed || c.Status == model.StatusSpendCapped
	case CaptureFilterNeedsTarget:
		return c.Status == model.StatusNeedsTarget
	default:
		return true
	}
}

// TenantCaptureLister is the store capability GET /v1/captures wants: one
// query over the tenant's whole capture partition.
//
// It is an optional interface rather than a method on repository.Store because
// the store is owned elsewhere and does not offer it yet. A store that
// implements it is used directly; a store that does not falls back to
// listCapturesByWalk below, which is correct but pays a query per note.
//
// This is the one piece of the API surface still waiting on the repository. The
// method that closes it is a Query on pk = USER#<tenant> with sk prefix
// CAPTURE#, which is the same shape as ListNotes.
type TenantCaptureLister interface {
	ListCaptures(ctx context.Context, tenantID string, opts repository.ListOptions) (repository.Page[model.CaptureIndex], error)
}

// maxCaptureWalkNotes bounds the fallback walk.
const maxCaptureWalkNotes = 500

// maxCaptureFilterRounds bounds how many store queries one filtered page may
// cost. Without it, a filter that matches almost nothing turns one request into
// a scan of the whole partition.
const maxCaptureFilterRounds = 10

// ListCaptures returns one page of the tenant's captures, newest first.
//
// The status filter runs here rather than in the store query, so the store
// paginates before the filter is applied and a page can come back with nothing
// kept. Keep querying until the page is full or the partition is exhausted:
// otherwise one needs_target capture behind a page of newer ones answers
// GET /v1/captures?status=needs_target with an empty list and a cursor, and the
// screen whose whole job is to ask about that capture shows nothing.
//
// Each round asks the store for only what is still missing, so a round can
// never return more kept captures than the page has room for. Discarding the
// overflow would put it behind a cursor that already points past it, which is
// the same defect one layer along.
//
// A filed capture whose note no longer has a row is dropped on the way out.
// Its row can outlive the note — a purge that could not see it (see
// ListUnindexedCaptures), a note deleted before purges cascaded at all — and
// then the capture is a receipt for a note the user cannot open: the library
// showed three "Filed · Open the note" cards under a count of zero notes. The
// other stopped statuses are kept without a lookup; needs_target, failed and
// no_content have no note by definition.
func (s *CaptureService) ListCaptures(ctx context.Context, userID string, filter CaptureFilter, opts repository.ListOptions) (repository.Page[model.CaptureIndex], error) {
	lister, ok := s.store.(TenantCaptureLister)
	if !ok {
		return s.listCapturesByWalk(ctx, userID, filter, opts)
	}

	limit := int(opts.Limit)
	if limit <= 0 {
		limit = int(repository.DefaultListLimit)
	}
	if limit > int(repository.MaxListLimit) {
		limit = int(repository.MaxListLimit)
	}
	kept := make([]model.CaptureIndex, 0, limit)
	cursor := opts.Cursor
	notes := s.noteExistence(userID)
	for round := 0; round < maxCaptureFilterRounds && len(kept) < limit; round++ {
		page, err := lister.ListCaptures(ctx, userID, repository.ListOptions{
			Limit:  int32(limit - len(kept)),
			Cursor: cursor,
		})
		if err != nil {
			return repository.Page[model.CaptureIndex]{}, err
		}
		for _, c := range page.Items {
			if !filter.keep(c) {
				continue
			}
			filed, err := notes.filedIntoLiveNote(ctx, c)
			if err != nil {
				return repository.Page[model.CaptureIndex]{}, err
			}
			if filed {
				kept = append(kept, c)
			}
		}
		cursor = page.Cursor
		if cursor == "" {
			break
		}
	}
	return repository.Page[model.CaptureIndex]{Items: kept, Cursor: cursor}, nil
}

// noteExistence remembers, for one request, which of the tenant's notes have a
// row. A page of twenty receipts names a handful of notes; each is asked about
// once.
type noteExistence struct {
	store  repository.Store
	userID string
	known  map[string]bool
}

func (s *CaptureService) noteExistence(userID string) *noteExistence {
	return &noteExistence{store: s.store, userID: userID, known: map[string]bool{}}
}

// filedIntoLiveNote is false only for an appended capture whose note has no
// row. Everything else passes: a capture still moving, or stopped on the user,
// is shown whatever its note_id says.
func (n *noteExistence) filedIntoLiveNote(ctx context.Context, c model.CaptureIndex) (bool, error) {
	if c.Status != model.StatusAppended || c.NoteID == "" {
		return true, nil
	}
	exists, ok := n.known[c.NoteID]
	if !ok {
		var err error
		exists, err = n.store.NoteExists(ctx, n.userID, c.NoteID)
		if err != nil {
			return false, err
		}
		n.known[c.NoteID] = exists
	}
	return exists, nil
}

// ListUnroutedCaptures returns captures that have no destination note yet.
func (s *CaptureService) ListUnroutedCaptures(ctx context.Context, userID string, opts repository.ListOptions) (repository.Page[model.CaptureIndex], error) {
	return s.store.ListCapturesByNote(ctx, userID, "", opts)
}

// listCapturesByWalk builds the list from the note index plus the unrouted
// captures, then pages it in memory.
//
// It is bounded and it is honest about being a fallback: the cursor it issues
// is its own offset token, not the store's, so it is only valid against this
// same walk.
func (s *CaptureService) listCapturesByWalk(ctx context.Context, userID string, filter CaptureFilter, opts repository.ListOptions) (repository.Page[model.CaptureIndex], error) {
	offset, err := decodeWalkCursor(opts.Cursor)
	if err != nil {
		return repository.Page[model.CaptureIndex]{}, err
	}

	notes, err := repository.DrainPages(ctx, maxCaptureWalkNotes, func(ctx context.Context, o repository.ListOptions) (repository.Page[model.NoteIndex], error) {
		return s.store.ListNotes(ctx, userID, o)
	})
	if err != nil {
		return repository.Page[model.CaptureIndex]{}, err
	}
	archived, err := repository.DrainPages(ctx, maxCaptureWalkNotes, func(ctx context.Context, o repository.ListOptions) (repository.Page[model.NoteIndex], error) {
		return s.store.ListArchivedNotes(ctx, userID, o)
	})
	if err != nil {
		return repository.Page[model.CaptureIndex]{}, err
	}
	notes = append(notes, archived...)

	seen := map[string]struct{}{}
	all := make([]model.CaptureIndex, 0, len(notes))

	add := func(captures []model.CaptureIndex) {
		for _, c := range captures {
			if _, dup := seen[c.ID]; dup {
				continue
			}
			seen[c.ID] = struct{}{}
			if filter.keep(c) {
				all = append(all, c)
			}
		}
	}

	unrouted, err := repository.DrainPages(ctx, maxCaptureWalkNotes, func(ctx context.Context, o repository.ListOptions) (repository.Page[model.CaptureIndex], error) {
		return s.store.ListCapturesByNote(ctx, userID, "", o)
	})
	if err != nil {
		return repository.Page[model.CaptureIndex]{}, err
	}
	add(unrouted)

	for _, note := range notes {
		captures, err := repository.DrainPages(ctx, maxCaptureWalkNotes, func(ctx context.Context, o repository.ListOptions) (repository.Page[model.CaptureIndex], error) {
			return s.store.ListCapturesByNote(ctx, userID, note.ID, o)
		})
		if err != nil {
			return repository.Page[model.CaptureIndex]{}, err
		}
		add(captures)
	}

	// CreatedAt is written with a fixed-width fraction, so a string comparison is
	// a valid reverse-chronological ordering. The id breaks ties.
	sort.Slice(all, func(i, j int) bool {
		if all[i].CreatedAt != all[j].CreatedAt {
			return all[i].CreatedAt > all[j].CreatedAt
		}
		return all[i].ID > all[j].ID
	})

	limit := int(opts.Limit)
	if limit <= 0 {
		limit = int(repository.DefaultListLimit)
	}
	if limit > int(repository.MaxListLimit) {
		limit = int(repository.MaxListLimit)
	}

	if offset > len(all) {
		offset = len(all)
	}
	end := offset + limit
	if end > len(all) {
		end = len(all)
	}
	page := repository.Page[model.CaptureIndex]{Items: all[offset:end]}
	if end < len(all) {
		page.Cursor = encodeWalkCursor(end)
	}
	return page, nil
}

const walkCursorPrefix = "walk:"

func encodeWalkCursor(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(walkCursorPrefix + strconv.Itoa(offset)))
}

func decodeWalkCursor(cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, fmt.Errorf("%w: malformed cursor", ErrInvalidCursor)
	}
	text, ok := strings.CutPrefix(string(raw), walkCursorPrefix)
	if !ok {
		return 0, fmt.Errorf("%w: cursor is not for this query", ErrInvalidCursor)
	}
	offset, err := strconv.Atoi(text)
	if err != nil || offset < 0 {
		return 0, fmt.Errorf("%w: malformed cursor", ErrInvalidCursor)
	}
	return offset, nil
}
