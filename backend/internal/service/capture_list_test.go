package service

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
)

// captureFixture writes capture rows straight to the store, which is how a test
// reaches a pipeline state the API alone cannot produce. Every note a capture
// names gets a row too: a filed capture whose note is gone is dropped from the
// list on purpose, and a test about something else must not trip over that.
func captureFixture(t *testing.T, captures ...model.CaptureIndex) (*memory.Store, *CaptureService) {
	t.Helper()
	store := memory.NewStore()
	ctx := context.Background()
	for _, c := range captures {
		if _, err := store.PutCapture(ctx, c); err != nil {
			t.Fatalf("PutCapture(%s): %v", c.ID, err)
		}
		if c.NoteID == "" {
			continue
		}
		if _, err := store.GetNote(ctx, c.UserID, c.NoteID); errors.Is(err, repository.ErrNotFound) {
			if _, err := store.PutNote(ctx, c.UserID, model.NoteIndex{ID: c.NoteID, Title: "Note " + c.NoteID}); err != nil {
				t.Fatalf("PutNote(%s): %v", c.NoteID, err)
			}
		}
	}
	return store, NewCaptureService(store, memory.NewObjects())
}

func captureIDs(page repository.Page[model.CaptureIndex]) []string {
	out := make([]string, 0, len(page.Items))
	for _, c := range page.Items {
		out = append(out, c.ID)
	}
	return out
}

// A capture awaiting disambiguation has no destination note, so it has no note
// partition to be indexed under. The list must still find it: needs_target *is*
// the disambiguation flow, and a capture that is recorded, durable and
// unreachable from the UI is not distinguishable, from where the user sits,
// from lost. This is the defect e61e043 fixed and it stays pinned here.
func TestListCapturesIncludesACaptureWithNowhereToGo(t *testing.T) {
	_, svc := captureFixture(t,
		model.CaptureIndex{
			ID: "c_00000000000000010_a", UserID: "user1", NoteID: "n1",
			Status: model.StatusAppended, CreatedAt: "2026-08-08T10:00:00.000000000Z",
		},
		model.CaptureIndex{
			ID: "c_00000000000000020_b", UserID: "user1", NoteID: "",
			Status: model.StatusNeedsTarget, CreatedAt: "2026-08-08T11:00:00.000000000Z",
		},
	)
	ctx := context.Background()

	all, err := svc.ListCaptures(ctx, "user1", CaptureFilterAll, repository.ListOptions{})
	if err != nil {
		t.Fatalf("ListCaptures: %v", err)
	}
	if got := captureIDs(all); !slices.Contains(got, "c_00000000000000020_b") {
		t.Fatalf("captures = %v, want the unrouted needs_target capture c_00000000000000020_b among them", got)
	}
	if len(all.Items) != 2 {
		t.Fatalf("captures = %v, want both the routed and the unrouted capture", captureIDs(all))
	}

	needsTarget, err := svc.ListCaptures(ctx, "user1", CaptureFilterNeedsTarget, repository.ListOptions{})
	if err != nil {
		t.Fatalf("ListCaptures(needs_target): %v", err)
	}
	if got := captureIDs(needsTarget); len(got) != 1 || got[0] != "c_00000000000000020_b" {
		t.Fatalf("needs_target captures = %v, want only c_00000000000000020_b", got)
	}
}

// The tenant-wide list reads the base table, whose sort key leads with the
// capture id, and capture ids lead with a fixed-width creation instant. The
// progress card only ever asks for the first page, so newest-first is what
// makes an in-flight capture visible at all.
func TestListCapturesReturnsTheNewestCaptureFirst(t *testing.T) {
	_, svc := captureFixture(t,
		model.CaptureIndex{ID: "c_00000000000000010_a", UserID: "user1", Status: model.StatusAppended, CreatedAt: "2026-08-08T10:00:00.000000000Z"},
		model.CaptureIndex{ID: "c_00000000000000030_c", UserID: "user1", Status: model.StatusUploaded, CreatedAt: "2026-08-08T12:00:00.000000000Z"},
		model.CaptureIndex{ID: "c_00000000000000020_b", UserID: "user1", Status: model.StatusFailed, CreatedAt: "2026-08-08T11:00:00.000000000Z"},
	)

	page, err := svc.ListCaptures(context.Background(), "user1", CaptureFilterAll, repository.ListOptions{})
	if err != nil {
		t.Fatalf("ListCaptures: %v", err)
	}

	want := []string{"c_00000000000000030_c", "c_00000000000000020_b", "c_00000000000000010_a"}
	if got := captureIDs(page); !slices.Equal(got, want) {
		t.Fatalf("captures = %v, want %v (newest first)", got, want)
	}
}

func TestListCapturesFiltersToWhatEachStatusGroupMeans(t *testing.T) {
	_, svc := captureFixture(t,
		model.CaptureIndex{ID: "c_uploaded", UserID: "user1", Status: model.StatusUploaded},
		model.CaptureIndex{ID: "c_cleaning", UserID: "user1", Status: model.StatusCleaning},
		model.CaptureIndex{ID: "c_failed", UserID: "user1", Status: model.StatusFailed},
		model.CaptureIndex{ID: "c_capped", UserID: "user1", Status: model.StatusSpendCapped},
		model.CaptureIndex{ID: "c_target", UserID: "user1", Status: model.StatusNeedsTarget},
		model.CaptureIndex{ID: "c_appended", UserID: "user1", Status: model.StatusAppended},
	)
	ctx := context.Background()

	cases := []struct {
		filter CaptureFilter
		want   []string
	}{
		{CaptureFilterAll, []string{"c_appended", "c_capped", "c_cleaning", "c_failed", "c_target", "c_uploaded"}},
		{CaptureFilterPending, []string{"c_cleaning", "c_uploaded"}},
		// spend_capped is a distinct outcome but it is still a capture that
		// stopped, so the failed filter has to surface it or a spend-capped
		// capture is invisible everywhere.
		{CaptureFilterFailed, []string{"c_capped", "c_failed"}},
		{CaptureFilterNeedsTarget, []string{"c_target"}},
	}
	for _, tc := range cases {
		t.Run(string(tc.filter), func(t *testing.T) {
			page, err := svc.ListCaptures(ctx, "user1", tc.filter, repository.ListOptions{})
			if err != nil {
				t.Fatalf("ListCaptures(%s): %v", tc.filter, err)
			}
			got := captureIDs(page)
			slices.Sort(got)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("ListCaptures(%s) = %v, want %v", tc.filter, got, tc.want)
			}
		})
	}
}

// The status filter runs in Go, after the store has already paginated, and the
// store query carries no FilterExpression. One needs_target capture sitting
// behind a page of newer ones therefore answers
// GET /v1/captures?status=needs_target with an empty list and a cursor — the
// screen that exists to ask "which note should this go in?" shows nothing at
// all, and the capture is stuck.
func TestListCapturesFillsThePageFromBehindAFullPageOfNonMatches(t *testing.T) {
	const ahead = 60
	captures := make([]model.CaptureIndex, 0, ahead+1)
	captures = append(captures, model.CaptureIndex{
		ID: "c_0000000000000000_match", UserID: "user1", Status: model.StatusNeedsTarget,
		CreatedAt: "2026-08-08T09:00:00.000000000Z",
	})
	for i := 1; i <= ahead; i++ {
		captures = append(captures, model.CaptureIndex{
			ID: fmt.Sprintf("c_%016d_x", i), UserID: "user1", Status: model.StatusAppended,
			CreatedAt: fmt.Sprintf("2026-08-08T10:%02d:00.000000000Z", i%60),
		})
	}
	_, svc := captureFixture(t, captures...)

	page, err := svc.ListCaptures(context.Background(), "user1", CaptureFilterNeedsTarget, repository.ListOptions{})
	if err != nil {
		t.Fatalf("ListCaptures(needs_target): %v", err)
	}
	if got := captureIDs(page); !slices.Equal(got, []string{"c_0000000000000000_match"}) {
		t.Fatalf("first page = %v (cursor %q), want the one needs_target capture: it is %d captures deep, which is behind the store's first page",
			got, page.Cursor, ahead)
	}
	if page.Cursor != "" {
		t.Errorf("first page returned cursor %q with nothing left behind it", page.Cursor)
	}
}

// Filling the page must not go the other way and over-read: a kept capture that
// does not fit is only reachable if the cursor resumes before it.
func TestListCapturesPagesAFilterToExhaustionExactlyOnce(t *testing.T) {
	const total = 120
	captures := make([]model.CaptureIndex, 0, total)
	wantIDs := map[string]int{}
	for i := range total {
		c := model.CaptureIndex{
			ID: fmt.Sprintf("c_%016d_x", i), UserID: "user1", Status: model.StatusAppended,
			CreatedAt: fmt.Sprintf("2026-08-08T10:%02d:00.000000000Z", i%60),
		}
		if i%3 == 0 {
			c.Status = model.StatusNeedsTarget
			wantIDs[c.ID] = 0
		}
		captures = append(captures, c)
	}
	_, svc := captureFixture(t, captures...)
	ctx := context.Background()

	const limit = 25
	cursor := ""
	for request := 0; ; request++ {
		if request > total {
			t.Fatalf("paging did not terminate after %d requests", request)
		}
		page, err := svc.ListCaptures(ctx, "user1", CaptureFilterNeedsTarget, repository.ListOptions{Limit: limit, Cursor: cursor})
		if err != nil {
			t.Fatalf("ListCaptures(request %d): %v", request, err)
		}
		if len(page.Items) > limit {
			t.Fatalf("request %d returned %d captures, more than the requested limit %d", request, len(page.Items), limit)
		}
		for _, c := range page.Items {
			if c.Status != model.StatusNeedsTarget {
				t.Fatalf("capture %s has status %s, which the needs_target filter must not return", c.ID, c.Status)
			}
			wantIDs[c.ID]++
		}
		cursor = page.Cursor
		if cursor == "" {
			break
		}
	}

	for id, seen := range wantIDs {
		if seen != 1 {
			t.Errorf("needs_target capture %s was returned %d times across the whole traversal, want exactly 1", id, seen)
		}
	}
}

func TestParseCaptureFilterAcceptsTheDeclaredValues(t *testing.T) {
	cases := map[string]CaptureFilter{
		"":             CaptureFilterAll,
		"all":          CaptureFilterAll,
		"  pending  ":  CaptureFilterPending,
		"failed":       CaptureFilterFailed,
		"needs_target": CaptureFilterNeedsTarget,
		"\tall\n":      CaptureFilterAll,
	}
	for in, want := range cases {
		got, ok := ParseCaptureFilter(in)
		if !ok {
			t.Errorf("ParseCaptureFilter(%q) was rejected, want %s", in, want)
			continue
		}
		if got != want {
			t.Errorf("ParseCaptureFilter(%q) = %s, want %s", in, got, want)
		}
	}
}

// An unknown status is a client mistake, not a request for everything.
// Defaulting it to "all" would hand back the whole list for a typo.
func TestParseCaptureFilterRejectsAValueTheAPIDoesNotDeclare(t *testing.T) {
	for _, in := range []string{"appended", "PENDING", "needs-target", "done", "'; drop"} {
		got, ok := ParseCaptureFilter(in)
		if ok {
			t.Errorf("ParseCaptureFilter(%q) = %s, ok — want it rejected", in, got)
		}
		if got != "" {
			t.Errorf("ParseCaptureFilter(%q) returned filter %q alongside the rejection", in, got)
		}
	}
}

func TestListUnroutedCapturesReturnsOnlyTheCapturesWithNoNote(t *testing.T) {
	_, svc := captureFixture(t,
		model.CaptureIndex{ID: "c_routed", UserID: "user1", NoteID: "n1", Status: model.StatusAppended, CreatedAt: "2026-08-08T10:00:00.000000000Z"},
		model.CaptureIndex{ID: "c_orphan", UserID: "user1", NoteID: "", Status: model.StatusNeedsTarget, CreatedAt: "2026-08-08T11:00:00.000000000Z"},
	)

	page, err := svc.ListUnroutedCaptures(context.Background(), "user1", repository.ListOptions{})
	if err != nil {
		t.Fatalf("ListUnroutedCaptures: %v", err)
	}
	if got := captureIDs(page); !slices.Equal(got, []string{"c_orphan"}) {
		t.Fatalf("unrouted captures = %v, want only c_orphan", got)
	}
}

// The walk is the fallback for a store with no tenant-wide capture query. Every
// repository.Store now declares ListCaptures, so nothing reaches it through
// ListCaptures any more; it is exercised directly so that the property it was
// fixed for cannot rot unnoticed while it is still in the tree.
func TestTheCaptureWalkFallbackAlsoFindsAnUnroutedCapture(t *testing.T) {
	store, svc := captureFixture(t,
		model.CaptureIndex{ID: "c_routed", UserID: "user1", NoteID: "n1", Status: model.StatusAppended, CreatedAt: "2026-08-08T10:00:00.000000000Z"},
		model.CaptureIndex{ID: "c_orphan", UserID: "user1", NoteID: "", Status: model.StatusNeedsTarget, CreatedAt: "2026-08-08T11:00:00.000000000Z"},
	)
	ctx := context.Background()
	// The fixture gave n1 its row, which is what the walk lists through.
	if ok, _ := store.NoteExists(ctx, "user1", "n1"); !ok {
		t.Fatal("fixture: n1 has no row")
	}

	page, err := svc.listCapturesByWalk(ctx, "user1", CaptureFilterAll, repository.ListOptions{})
	if err != nil {
		t.Fatalf("listCapturesByWalk: %v", err)
	}

	want := []string{"c_orphan", "c_routed"}
	if got := captureIDs(page); !slices.Equal(got, want) {
		t.Fatalf("walked captures = %v, want %v (newest first, unrouted included)", got, want)
	}
}

// The walk's cursor is its own offset token, not the store's, so it is only
// valid against this same walk. It is honest about that rather than passing off
// an offset as a continuation key.
func TestTheCaptureWalkPagesWithItsOwnCursor(t *testing.T) {
	store, svc := captureFixture(t,
		model.CaptureIndex{ID: "c_1", UserID: "user1", CreatedAt: "2026-08-08T10:00:00.000000000Z"},
		model.CaptureIndex{ID: "c_2", UserID: "user1", CreatedAt: "2026-08-08T11:00:00.000000000Z"},
		model.CaptureIndex{ID: "c_3", UserID: "user1", CreatedAt: "2026-08-08T12:00:00.000000000Z"},
	)
	ctx := context.Background()
	if _, err := store.PutNote(ctx, "user1", model.NoteIndex{ID: "n1", Title: "Roof"}); err != nil {
		t.Fatalf("PutNote: %v", err)
	}

	first, err := svc.listCapturesByWalk(ctx, "user1", CaptureFilterAll, repository.ListOptions{Limit: 2})
	if err != nil {
		t.Fatalf("listCapturesByWalk: %v", err)
	}
	if got := captureIDs(first); !slices.Equal(got, []string{"c_3", "c_2"}) {
		t.Fatalf("first page = %v, want [c_3 c_2]", got)
	}
	if first.Cursor == "" {
		t.Fatal("first page returned no cursor with a third capture behind it")
	}

	second, err := svc.listCapturesByWalk(ctx, "user1", CaptureFilterAll, repository.ListOptions{Limit: 2, Cursor: first.Cursor})
	if err != nil {
		t.Fatalf("listCapturesByWalk(cursor): %v", err)
	}
	if got := captureIDs(second); !slices.Equal(got, []string{"c_1"}) {
		t.Fatalf("second page = %v, want [c_1]", got)
	}
	if second.Cursor != "" {
		t.Errorf("second page returned cursor %q with nothing behind it", second.Cursor)
	}
}

func TestWalkCursorRoundTripsItsOffset(t *testing.T) {
	for _, offset := range []int{0, 1, 50, 4096} {
		got, err := decodeWalkCursor(encodeWalkCursor(offset))
		if err != nil {
			t.Errorf("decodeWalkCursor(encodeWalkCursor(%d)): %v", offset, err)
			continue
		}
		if got != offset {
			t.Errorf("offset round-tripped to %d, want %d", got, offset)
		}
	}
	if got, err := decodeWalkCursor(""); err != nil || got != 0 {
		t.Fatalf("decodeWalkCursor(\"\") = %d, %v; want 0 and no error for a first page", got, err)
	}
}

// A malformed cursor is a client mistake. Typing it as ErrInvalidCursor is what
// lets the handler answer 400 rather than 500, which is the difference between
// the client fixing its request and the client retrying the same broken one.
func TestWalkCursorRejectsAForeignOrMalformedToken(t *testing.T) {
	cases := map[string]string{
		"not base64":            "!!!not base64!!!",
		"another query's token": base64.RawURLEncoding.EncodeToString([]byte("notes:12")),
		"a store continuation":  base64.RawURLEncoding.EncodeToString([]byte("user1\x00note_007")),
		"not a number":          base64.RawURLEncoding.EncodeToString([]byte(walkCursorPrefix + "seven")),
		"negative offset":       base64.RawURLEncoding.EncodeToString([]byte(walkCursorPrefix + "-1")),
	}
	for name, cursor := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := decodeWalkCursor(cursor)
			if !errors.Is(err, ErrInvalidCursor) {
				t.Fatalf("decodeWalkCursor(%q) err = %v, want ErrInvalidCursor", cursor, err)
			}
			if got != 0 {
				t.Fatalf("decodeWalkCursor(%q) = %d alongside the rejection, want 0", cursor, got)
			}
		})
	}
}

func TestListCapturesSurfacesAStoreFailure(t *testing.T) {
	boom := errors.New("dynamodb: dial tcp: connection refused")
	svc := NewCaptureService(captureListErrStore{Store: memory.NewStore(), err: boom}, memory.NewObjects())

	if _, err := svc.ListCaptures(context.Background(), "user1", CaptureFilterAll, repository.ListOptions{}); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the store's failure rather than an empty capture list", err)
	}
}

func TestCaptureStatusGroupsCoverEveryDeclaredStatus(t *testing.T) {
	pending := []model.CaptureStatus{
		model.StatusUploaded, model.StatusTranscribed, model.StatusCleaned,
		model.StatusTranscribing, model.StatusRouting, model.StatusCleaning, model.StatusAppending,
	}
	terminal := []model.CaptureStatus{
		model.StatusAppended, model.StatusNoContent, model.StatusFailed, model.StatusSpendCapped,
		model.StatusNeedsTarget,
	}
	for _, s := range pending {
		if !CaptureIsPending(s) {
			t.Errorf("CaptureIsPending(%s) = false; the progress card will lose a capture mid-pipeline", s)
		}
		if CaptureIsTerminal(s) {
			t.Errorf("CaptureIsTerminal(%s) = true for a stage the pipeline still moves on from", s)
		}
	}
	for _, s := range terminal {
		if !CaptureIsTerminal(s) {
			t.Errorf("CaptureIsTerminal(%s) = false; the pipeline will not leave this state on its own", s)
		}
		if CaptureIsPending(s) {
			t.Errorf("CaptureIsPending(%s) = true for a capture that has stopped", s)
		}
	}
	// needs_target is terminal for the pipeline — it will not move the capture
	// on by itself — but not pending: calling it pending would put it in the
	// progress card forever. One definition for terminal (model.IsTerminalStatus)
	// is what keeps the worker, the API's retry, reconcile and the frontend's
	// list from drifting apart again.
	if CaptureIsPending(model.StatusNeedsTarget) {
		t.Error("needs_target is pending; it waits on the user, and calling it pending puts it in the progress card forever")
	}
	if CaptureIsTerminal(model.StatusNeedsTarget) != model.IsTerminalStatus(model.StatusNeedsTarget) {
		t.Error("the service and the model disagree about needs_target")
	}
}

// captureListErrStore fails the tenant-wide capture query.
type captureListErrStore struct {
	repository.Store
	err error
}

func (s captureListErrStore) ListCaptures(context.Context, string, repository.ListOptions) (repository.Page[model.CaptureIndex], error) {
	return repository.Page[model.CaptureIndex]{}, s.err
}

// TestListCapturesDropsAReceiptForANoteThatIsGone is what the owner saw on
// 2026-09-05: every note deleted, the count at zero, and three "Filed · Open
// the note" cards for captures whose notes no longer had a row. A filed
// capture is a receipt for its note; without the note it is noise. The
// statuses that have no note by definition are kept, whatever note_id says.
func TestListCapturesDropsAReceiptForANoteThatIsGone(t *testing.T) {
	store, svc := captureFixture(t,
		model.CaptureIndex{ID: "c_00000000000000010_kept", UserID: "user1", NoteID: "n_live", Status: model.StatusAppended, CreatedAt: "2026-08-08T10:00:00.000000000Z"},
		model.CaptureIndex{ID: "c_00000000000000020_gone", UserID: "user1", NoteID: "n_gone", Status: model.StatusAppended, CreatedAt: "2026-08-08T11:00:00.000000000Z"},
		model.CaptureIndex{ID: "c_00000000000000030_empty", UserID: "user1", NoteID: "n_gone", Status: model.StatusNoContent, CreatedAt: "2026-08-08T12:00:00.000000000Z"},
		model.CaptureIndex{ID: "c_00000000000000040_failed", UserID: "user1", NoteID: "n_gone", Status: model.StatusFailed, CreatedAt: "2026-08-08T13:00:00.000000000Z"},
		model.CaptureIndex{ID: "c_00000000000000050_asking", UserID: "user1", NoteID: "", Status: model.StatusNeedsTarget, CreatedAt: "2026-08-08T14:00:00.000000000Z"},
	)
	ctx := context.Background()
	if err := store.DeleteNote(ctx, "user1", "n_gone"); err != nil {
		t.Fatalf("DeleteNote: %v", err)
	}

	page, err := svc.ListCaptures(ctx, "user1", CaptureFilterAll, repository.ListOptions{})
	if err != nil {
		t.Fatalf("ListCaptures: %v", err)
	}
	want := []string{"c_00000000000000050_asking", "c_00000000000000040_failed", "c_00000000000000030_empty", "c_00000000000000010_kept"}
	if got := captureIDs(page); !slices.Equal(got, want) {
		t.Fatalf("captures = %v, want %v (the filed capture of the deleted note dropped, nothing else)", got, want)
	}

	// The drop happens before the page is counted full, so a page asked for
	// one capture is not answered with nothing and a cursor.
	one, err := svc.ListCaptures(ctx, "user1", CaptureFilterAll, repository.ListOptions{Limit: 1})
	if err != nil {
		t.Fatalf("ListCaptures(limit 1): %v", err)
	}
	if got := captureIDs(one); len(got) != 1 {
		t.Fatalf("captures = %v, want one", got)
	}

	// The walk cannot reach a capture of a note with no row in the first
	// place: it lists captures through their notes.
	walked, err := svc.listCapturesByWalk(ctx, "user1", CaptureFilterAll, repository.ListOptions{})
	if err != nil {
		t.Fatalf("listCapturesByWalk: %v", err)
	}
	if got := captureIDs(walked); slices.Contains(got, "c_00000000000000020_gone") {
		t.Fatalf("walked captures = %v, must not include the filed capture of the deleted note", got)
	}
}

// noteExistsErrStore fails the note lookup the list makes for a receipt.
type noteExistsErrStore struct {
	repository.Store
	err error
}

func (s noteExistsErrStore) NoteExists(context.Context, string, string) (bool, error) {
	return false, s.err
}

func TestListCapturesSurfacesANoteLookupFailure(t *testing.T) {
	store, _ := captureFixture(t,
		model.CaptureIndex{ID: "c_1", UserID: "user1", NoteID: "n1", Status: model.StatusAppended, CreatedAt: "2026-08-08T10:00:00.000000000Z"},
	)
	boom := errors.New("dynamodb: throttled")
	svc := NewCaptureService(noteExistsErrStore{Store: store, err: boom}, memory.NewObjects())

	if _, err := svc.ListCaptures(context.Background(), "user1", CaptureFilterAll, repository.ListOptions{}); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the lookup's failure rather than a receipt silently kept or dropped", err)
	}
}
