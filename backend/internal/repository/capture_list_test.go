package repository_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// GET /v1/captures was assembled by walking the tenant's notes and asking for
// each note's captures. That is correct on the in-memory store and wrong on
// DynamoDB: a capture with no destination note has no note partition, so GSI1
// cannot see it, so the walk cannot see it.
//
// The captures it could not see are the ones the user most needs: `needs_target`
// is the disambiguation flow, and pending captures back the progress card.
// Recorded, durable, and unreachable from the UI is indistinguishable from lost.
//
// So every assertion below runs against both backends. A test that only ran
// against the in-memory store would have passed against the broken walk, which
// is exactly how this got through.

func TestListCapturesIncludesACaptureWithNoNote(t *testing.T) {
	eachStore(t, func(t *testing.T) {
		store, ctx := newStore(), t.Context()

		// The router could not place this one. It has no note id.
		if _, err := store.PutCapture(ctx, model.CaptureIndex{
			ID: "c_unrouted", UserID: owner, NoteID: "",
			Status: model.StatusNeedsTarget, CreatedAt: model.Now(),
		}); err != nil {
			t.Fatalf("seed unrouted capture: %v", err)
		}
		// And one that was placed, so the list is not trivially right.
		if _, err := store.PutCapture(ctx, model.CaptureIndex{
			ID: "c_routed", UserID: owner, NoteID: "note_1",
			Status: model.StatusAppended, CreatedAt: model.Now(),
		}); err != nil {
			t.Fatalf("seed routed capture: %v", err)
		}

		page, err := store.ListCaptures(ctx, owner, repository.ListOptions{})
		if err != nil {
			t.Fatalf("ListCaptures: %v", err)
		}

		got := map[string]bool{}
		for _, c := range page.Items {
			got[c.ID] = true
		}
		if !got["c_unrouted"] {
			t.Error("a capture awaiting disambiguation is missing from the tenant's capture list; " +
				"it is durable and unreachable, which the user cannot tell apart from lost")
		}
		if !got["c_routed"] {
			t.Error("a routed capture is missing from the tenant's capture list")
		}
	})
}

// The same capture must also be reachable through the unrouted query the export
// path uses, or an export silently omits it.
func TestUnroutedCapturesAreReachableByEmptyNoteID(t *testing.T) {
	eachStore(t, func(t *testing.T) {
		store, ctx := newStore(), t.Context()

		if _, err := store.PutCapture(ctx, model.CaptureIndex{
			ID: "c_unrouted", UserID: owner, NoteID: "",
			Status: model.StatusNeedsTarget, CreatedAt: model.Now(),
		}); err != nil {
			t.Fatalf("seed unrouted capture: %v", err)
		}

		page, err := store.ListCapturesByNote(ctx, owner, "", repository.ListOptions{})
		if err != nil {
			t.Fatalf("ListCapturesByNote(\"\"): %v", err)
		}
		if len(page.Items) != 1 || page.Items[0].ID != "c_unrouted" {
			t.Fatalf("unrouted captures = %v, want the one capture with no note", captureIDs(page.Items))
		}
	})
}

func TestListCapturesIsTenantScoped(t *testing.T) {
	eachStore(t, func(t *testing.T) {
		store, ctx := newStore(), t.Context()

		if _, err := store.PutCapture(ctx, model.CaptureIndex{
			ID: "c_owned", UserID: owner, Status: model.StatusNeedsTarget, CreatedAt: model.Now(),
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}

		page, err := store.ListCaptures(ctx, intruder, repository.ListOptions{})
		if err != nil {
			t.Fatalf("ListCaptures: %v", err)
		}
		if len(page.Items) != 0 {
			t.Fatalf("intruder listed %d captures belonging to another tenant", len(page.Items))
		}
	})
}

func TestListCapturesPagesThroughEveryCapture(t *testing.T) {
	eachStore(t, func(t *testing.T) {
		store, ctx := newStore(), t.Context()

		const total = 25
		base := time.Now().UTC()
		for i := 0; i < total; i++ {
			// Ids carry their creation instant, as the service generates them.
			id := fmt.Sprintf("c_%016x_%04x", uint64(base.Add(time.Duration(i)*time.Second).UnixNano()), i)
			noteID := ""
			if i%2 == 0 {
				noteID = fmt.Sprintf("note_%d", i)
			}
			if _, err := store.PutCapture(ctx, model.CaptureIndex{
				ID: id, UserID: owner, NoteID: noteID,
				Status:    model.StatusNeedsTarget,
				CreatedAt: model.FormatTime(base.Add(time.Duration(i) * time.Second)),
			}); err != nil {
				t.Fatalf("seed %d: %v", i, err)
			}
		}

		seen := map[string]bool{}
		opts := repository.ListOptions{Limit: 4}
		for pages := 0; ; pages++ {
			if pages > 40 {
				t.Fatal("pagination did not terminate")
			}
			page, err := store.ListCaptures(ctx, owner, opts)
			if err != nil {
				t.Fatalf("ListCaptures: %v", err)
			}
			for _, c := range page.Items {
				if seen[c.ID] {
					t.Fatalf("capture %s returned on two pages", c.ID)
				}
				seen[c.ID] = true
			}
			if page.Cursor == "" {
				break
			}
			opts.Cursor = page.Cursor
		}

		if len(seen) != total {
			t.Fatalf("paged through %d captures, want %d", len(seen), total)
		}
	})
}

// Page one has to hold the newest captures, because that is the only page the
// progress card asks for.
func TestListCapturesReturnsNewestFirst(t *testing.T) {
	eachStore(t, func(t *testing.T) {
		store, ctx := newStore(), t.Context()

		base := time.Now().UTC()
		for i := 0; i < 6; i++ {
			at := base.Add(time.Duration(i) * time.Minute)
			if _, err := store.PutCapture(ctx, model.CaptureIndex{
				ID:        fmt.Sprintf("c_%016x_%04x", uint64(at.UnixNano()), i),
				UserID:    owner,
				Status:    model.StatusUploaded,
				CreatedAt: model.FormatTime(at),
			}); err != nil {
				t.Fatalf("seed %d: %v", i, err)
			}
		}

		page, err := store.ListCaptures(ctx, owner, repository.ListOptions{Limit: 3})
		if err != nil {
			t.Fatalf("ListCaptures: %v", err)
		}
		if len(page.Items) != 3 {
			t.Fatalf("got %d captures, want 3", len(page.Items))
		}
		for i := 1; i < len(page.Items); i++ {
			if page.Items[i-1].CreatedAt < page.Items[i].CreatedAt {
				t.Fatalf("page is not newest-first: %v", captureCreatedAt(page.Items))
			}
		}
		// The three newest, not three arbitrary ones.
		newest := model.FormatTime(base.Add(5 * time.Minute))
		if page.Items[0].CreatedAt != newest {
			t.Fatalf("first item created at %s, want the newest capture at %s",
				page.Items[0].CreatedAt, newest)
		}
	})
}

func TestListCapturesRejectsACursorFromAnotherQuery(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := t.Context()
	seedNotes(t, store, owner, 6)

	notesPage, err := store.ListNotes(ctx, owner, repository.ListOptions{Limit: 2})
	if err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	if notesPage.Cursor == "" {
		t.Fatal("expected a note cursor")
	}

	// Notes and captures share one partition, so the partition check alone would
	// accept this and return a wrong page rather than an error.
	if _, err := store.ListCaptures(ctx, owner, repository.ListOptions{Cursor: notesPage.Cursor}); err == nil {
		t.Fatal("a note cursor was accepted by the capture list")
	}
}

func captureCreatedAt(cs []model.CaptureIndex) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.CreatedAt)
	}
	return out
}
