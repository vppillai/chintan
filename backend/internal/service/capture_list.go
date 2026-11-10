package service

import (
	"context"
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
		page, err := s.store.ListCaptures(ctx, userID, repository.ListOptions{
			Limit:  int32(limit - len(kept)),
			Cursor: cursor,
		})
		if err != nil {
			return repository.Page[model.CaptureIndex]{}, err
		}
		if err := notes.learn(ctx, page.Items); err != nil {
			return repository.Page[model.CaptureIndex]{}, err
		}
		for _, c := range page.Items {
			if filter.keep(c) && notes.filedIntoLiveNote(c) {
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
// row. A page of twenty receipts names a handful of notes; the page asks about
// all of them in one round trip (learn) and each is asked about once, however
// many pages the filter walks.
type noteExistence struct {
	store  repository.Store
	userID string
	known  map[string]bool
}

func (s *CaptureService) noteExistence(userID string) *noteExistence {
	return &noteExistence{store: s.store, userID: userID, known: map[string]bool{}}
}

// learn asks the store, once, about every note a page's receipts name that
// this request has not asked about yet.
func (n *noteExistence) learn(ctx context.Context, page []model.CaptureIndex) error {
	var ask []string
	for _, c := range page {
		if c.Status != model.StatusAppended || c.NoteID == "" {
			continue
		}
		if _, known := n.known[c.NoteID]; known {
			continue
		}
		n.known[c.NoteID] = false
		ask = append(ask, c.NoteID)
	}
	if len(ask) == 0 {
		return nil
	}
	found, err := n.store.NotesExist(ctx, n.userID, ask)
	if err != nil {
		for _, id := range ask {
			delete(n.known, id)
		}
		return err
	}
	for id, exists := range found {
		n.known[id] = exists
	}
	return nil
}

// filedIntoLiveNote is false only for an appended capture whose note has no
// row. Everything else passes: a capture still moving, or stopped on the user,
// is shown whatever its note_id says. The page must have been learned first.
func (n *noteExistence) filedIntoLiveNote(c model.CaptureIndex) bool {
	if c.Status != model.StatusAppended || c.NoteID == "" {
		return true
	}
	return n.known[c.NoteID]
}

// ListUnroutedCaptures returns captures that have no destination note yet.
func (s *CaptureService) ListUnroutedCaptures(ctx context.Context, userID string, opts repository.ListOptions) (repository.Page[model.CaptureIndex], error) {
	return s.store.ListCapturesByNote(ctx, userID, "", opts)
}
