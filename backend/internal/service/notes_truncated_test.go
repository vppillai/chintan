package service

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
)

// truncatedListStore answers every notes list the way the DynamoDB store does
// when a tenant has more notes than repository.MaxNotesDrained.
type truncatedListStore struct{ repository.Store }

func (s truncatedListStore) ListNotes(ctx context.Context, tenantID string, opts repository.ListOptions) (repository.Page[model.NoteIndex], error) {
	page, err := s.Store.ListNotes(ctx, tenantID, opts)
	page.Truncated = true
	return page, err
}

func (s truncatedListStore) ListArchivedNotes(ctx context.Context, tenantID string, opts repository.ListOptions) (repository.Page[model.NoteIndex], error) {
	page, err := s.Store.ListArchivedNotes(ctx, tenantID, opts)
	page.Truncated = true
	return page, err
}

func (s truncatedListStore) DrainNotes(ctx context.Context, tenantID string, opts repository.DrainOptions) ([]model.NoteIndex, bool, error) {
	notes, _, err := s.Store.DrainNotes(ctx, tenantID, opts)
	return notes, true, err
}

// A list that hit the drain ceiling is counted, once per list, so the missing
// note has a metric an operator can find. The store cannot emit it — it has no
// metrics of its own — so this layer must.
func TestATruncatedNotesListIsCounted(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		list func(*NotesService) error
	}{
		{"active", func(s *NotesService) error {
			_, err := s.ListNotes(ctx, "u1", repository.ListOptions{})
			return err
		}},
		{"archived", func(s *NotesService) error {
			_, err := s.ListArchivedNotes(ctx, "u1", repository.ListOptions{})
			return err
		}},
		{"drain", func(s *NotesService) error {
			_, err := s.DrainNotes(ctx, "u1", repository.DrainOptions{})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var metrics bytes.Buffer
			restore := obs.SetMetricOutput(&metrics)
			defer restore()

			svc := NewNotesService(truncatedListStore{memory.NewStore()}, memory.NewObjects())
			if err := tc.list(svc); err != nil {
				t.Fatalf("list: %v", err)
			}
			// One EMF record per emission; the name appears once in each
			// record's metric definitions.
			if got := strings.Count(metrics.String(), `"Name":"NotesListTruncated"`); got != 1 {
				t.Fatalf("NotesListTruncated was emitted %d times for one truncated list, want 1:\n%s", got, metrics.String())
			}

			metrics.Reset()
			plain := NewNotesService(memory.NewStore(), memory.NewObjects())
			if err := tc.list(plain); err != nil {
				t.Fatalf("list: %v", err)
			}
			if strings.Contains(metrics.String(), "NotesListTruncated") {
				t.Fatal("a complete list was counted as truncated")
			}
		})
	}
}
