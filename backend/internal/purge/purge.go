// Package purge deletes what an archived note leaves behind once its purge
// deadline has passed: the S3 objects it and its captures name, and then the
// row itself.
//
// Archiving a note stamps it with a deadline thirty days out. Before this
// existed the row was left to DynamoDB TTL, and TTL deletes the index row and
// nothing else — so an archived note reaching its deadline lost its row and
// kept every object it named: audio, raw transcript, routed transcript, cleaned
// text, segments and peaks, billed monthly, referenced by nothing and reachable
// by nothing. The first fix was a second Lambda on the table's stream, acting
// on the REMOVE record TTL produced; the 2026-09-03 review found that a
// function, an event-source mapping, a dead-letter queue, two alarms and a
// stream to keep for what is one scan and one cascade a week.
//
// This is that scan. An EventBridge rule invokes the worker weekly with
// {"task":"sweep-expired"}; Sweep asks the store for every note past its
// deadline, runs the same cascade a user's "delete forever" runs, and deletes
// the row. Because the row is deleted here, after its objects, TTL is only the
// backstop for a sweep that did not run — and the store sets it a fortnight
// after the deadline so the sweep gets two chances first (see
// repository.ttlGraceSeconds).
//
// Nothing here derives a prefix, lists a bucket, or infers a sibling object: the
// cascade unlinks exactly the keys the index rows carry. A sweep that deleted by
// prefix would take a second tenant's objects with it the first time a key
// layout changed.
package purge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// Task is the payload field value the EventBridge rule sends, and cmd/worker
// dispatches on. The template's rule and this constant must agree.
const Task = "sweep-expired"

// NoteCascade is the half of service.NotesService this sweep needs. It is an
// interface so the cascade is the production one — the same code a user's
// "delete forever" runs — rather than a second implementation that can drift
// from it.
type NoteCascade interface {
	PurgeNoteArtifacts(ctx context.Context, userID, noteID string, note model.NoteIndex) error
}

// Store is the slice of repository.Store the sweep reads and writes.
type Store interface {
	ExpiredNotes(ctx context.Context, asOf int64) ([]repository.TenantNote, error)
	DeleteNote(ctx context.Context, tenantID, noteID string) error
}

// Sweeper runs one expiry sweep.
type Sweeper struct {
	store Store
	notes NoteCascade
	now   func() time.Time
}

// New builds the sweeper. Both dependencies are required: without the store
// there is nothing to find, and without the cascade an expired note keeps its
// captures' audio.
func New(store Store, notes NoteCascade) (*Sweeper, error) {
	switch {
	case store == nil:
		return nil, fmt.Errorf("purge: store is required")
	case notes == nil:
		return nil, fmt.Errorf("purge: note cascade is required")
	}
	return &Sweeper{store: store, notes: notes, now: time.Now}, nil
}

// Report is what one sweep did.
type Report struct {
	// Expired is how many notes were past their deadline.
	Expired int
	// Purged is how many of them are now gone, objects and row.
	Purged int
	// Failed is how many are still there for the next sweep.
	Failed int
}

// Sweep purges every note past its deadline and reports the count.
//
// One note failing does not stop the others: each is attempted, and the error
// returned at the end names how many are left. Returning an error is what makes
// Lambda retry the invocation — twice, minutes apart — and then dead-letter it,
// which raises the alarm on the queue. The sweep is idempotent, so a retry
// re-issues deletes for objects that are already gone, and the cascade treats a
// missing object or row as success.
func (s *Sweeper) Sweep(ctx context.Context) (Report, error) {
	var report Report
	asOf := s.now().UTC().Unix()

	expired, err := s.store.ExpiredNotes(ctx, asOf)
	if err != nil {
		return report, fmt.Errorf("purge: list expired notes: %w", err)
	}
	report.Expired = len(expired)

	for _, tn := range expired {
		noteCtx := obs.WithTenant(ctx, tn.TenantID)
		if err := s.purge(noteCtx, tn); err != nil {
			report.Failed++
			obs.Log(noteCtx).Error("could not purge an expired note; the next sweep will retry it",
				slog.String("note_id", tn.Note.ID),
				slog.String("error", err.Error()))
			continue
		}
		report.Purged++
	}

	obs.Log(ctx).Info("expiry sweep finished",
		slog.Int("expired", report.Expired),
		slog.Int("purged", report.Purged),
		slog.Int("failed", report.Failed))
	obs.Emit(ctx, nil,
		obs.Metric{Name: "ExpiredNotesPurged", Value: float64(report.Purged), Unit: obs.UnitCount},
		obs.Metric{Name: "ExpiredNotesFailed", Value: float64(report.Failed), Unit: obs.UnitCount},
	)
	if report.Failed > 0 {
		return report, fmt.Errorf("purge: %d of %d expired notes could not be purged", report.Failed, report.Expired)
	}
	return report, nil
}

// purge runs the cascade for one note and then deletes its row.
//
// Objects first, row second, always. The row is the only record of which
// objects the note owns; deleting it first would make a failure between the two
// steps permanent, which is the leak this package exists to close.
func (s *Sweeper) purge(ctx context.Context, tn repository.TenantNote) error {
	if err := s.notes.PurgeNoteArtifacts(ctx, tn.TenantID, tn.Note.ID, tn.Note); err != nil {
		return fmt.Errorf("purge: expired note %s: %w", tn.Note.ID, err)
	}
	if err := s.store.DeleteNote(ctx, tn.TenantID, tn.Note.ID); err != nil && !errors.Is(err, repository.ErrNotFound) {
		return fmt.Errorf("purge: expired note %s index: %w", tn.Note.ID, err)
	}
	obs.Log(ctx).Info("purged an expired note, its captures and their objects",
		slog.String("note_id", tn.Note.ID))
	return nil
}
