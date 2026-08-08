package repository_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// The v1 tests here asserted that userPK("user123") == "USER#user123" and that
// calling it twice returned the same string. Nothing they covered could break.
// These exercise the behaviour that actually loses data: pagination, cursors,
// conditional writes, index usage, and idempotency.

const tableName = "chintan-test"

func newTestStore(t *testing.T) (*repository.DynamoStore, *fakeDynamo) {
	t.Helper()
	api := newFakeDynamo()
	return repository.NewDynamoStore(api, tableName), api
}

func seedNotes(t *testing.T, store *repository.DynamoStore, tenantID string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		_, err := store.PutNote(context.Background(), tenantID, model.NoteIndex{
			ID:        fmt.Sprintf("note_%03d", i),
			Title:     fmt.Sprintf("Note %d", i),
			UpdatedAt: model.Now(),
		})
		if err != nil {
			t.Fatalf("seed note %d: %v", i, err)
		}
	}
}

// ------------------------------------------------------------- pagination

func TestListNotesPagesThroughEveryItem(t *testing.T) {
	store, api := newTestStore(t)
	ctx := context.Background()
	const total = 37
	seedNotes(t, store, "tenant-a", total)

	// Force the store to follow LastEvaluatedKey: no single Query can answer.
	api.pageSize = 4

	seen := map[string]bool{}
	opts := repository.ListOptions{Limit: 10}
	pages := 0
	for {
		page, err := store.ListNotes(ctx, "tenant-a", opts)
		if err != nil {
			t.Fatalf("ListNotes: %v", err)
		}
		pages++
		if pages > 50 {
			t.Fatal("pagination did not terminate")
		}
		for _, n := range page.Items {
			if seen[n.ID] {
				t.Fatalf("note %s returned twice", n.ID)
			}
			seen[n.ID] = true
		}
		if page.Cursor == "" {
			break
		}
		opts.Cursor = page.Cursor
	}

	if len(seen) != total {
		t.Fatalf("saw %d notes across %d pages, want %d — an unpaginated query silently truncates", len(seen), pages, total)
	}
	if pages < 2 {
		t.Fatalf("expected several pages, got %d", pages)
	}
}

func TestListNotesClampsLimit(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	seedNotes(t, store, "tenant-a", 5)

	if _, err := store.ListNotes(ctx, "tenant-a", repository.ListOptions{Limit: 100000}); err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	// The clamp is visible in the request the store issued.
	store2, api := newTestStore(t)
	seedNotes(t, store2, "tenant-a", 1)
	api.queries = nil
	if _, err := store2.ListNotes(ctx, "tenant-a", repository.ListOptions{Limit: 100000}); err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	if got := *api.queries[0].Limit; got != repository.MaxListLimit {
		t.Fatalf("Limit = %d, want clamp to %d", got, repository.MaxListLimit)
	}

	api.queries = nil
	if _, err := store2.ListNotes(ctx, "tenant-a", repository.ListOptions{}); err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	if got := *api.queries[0].Limit; got != repository.DefaultListLimit {
		t.Fatalf("Limit = %d, want default %d", got, repository.DefaultListLimit)
	}
}

func TestListNotesCursorRoundTrips(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	seedNotes(t, store, "tenant-a", 6)

	first, err := store.ListNotes(ctx, "tenant-a", repository.ListOptions{Limit: 2})
	if err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	if first.Cursor == "" {
		t.Fatal("expected a cursor with 6 notes and a limit of 2")
	}

	second, err := store.ListNotes(ctx, "tenant-a", repository.ListOptions{Limit: 2, Cursor: first.Cursor})
	if err != nil {
		t.Fatalf("ListNotes(page 2): %v", err)
	}
	if len(second.Items) == 0 {
		t.Fatal("second page is empty")
	}
	if second.Items[0].ID == first.Items[0].ID {
		t.Fatalf("cursor did not advance: page 2 starts at %s", second.Items[0].ID)
	}
}

// A cursor is scoped to the partition that produced it. Replaying tenant A's
// cursor against tenant B's list must be rejected, not honoured.
func TestListNotesRejectsAnotherTenantsCursor(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	seedNotes(t, store, "tenant-a", 6)
	seedNotes(t, store, "tenant-b", 6)

	page, err := store.ListNotes(ctx, "tenant-a", repository.ListOptions{Limit: 2})
	if err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	if page.Cursor == "" {
		t.Fatal("expected a cursor")
	}

	if _, err := store.ListNotes(ctx, "tenant-b", repository.ListOptions{Limit: 2, Cursor: page.Cursor}); err == nil {
		t.Fatal("another tenant's cursor was accepted")
	}
}

func TestListNotesRejectsGarbageCursor(t *testing.T) {
	store, _ := newTestStore(t)
	for _, bad := range []string{"!!!!", "Zm9v", "eyJwayI6Ik9USEVSIn0"} {
		if _, err := store.ListNotes(context.Background(), "tenant-a", repository.ListOptions{Cursor: bad}); err == nil {
			t.Fatalf("cursor %q was accepted", bad)
		}
	}
}

// TestListNotesDoesNotTransferTheDataBlob keeps the cost this projection work
// exists to remove: `data` is the full record as JSON and duplicates every
// attribute a list renders, so transferring it made listing notes to draw
// titles pay for the whole corpus.
//
// It used to be enforced by a ProjectionExpression on a base-table query. The
// list now reads GSI2, whose INCLUDE projection does not hold `data` at all, so
// the guarantee is structural rather than per-query — and this asserts the
// guarantee rather than the mechanism, which is why it survived the change of
// mechanism.
func TestListNotesDoesNotTransferTheDataBlob(t *testing.T) {
	store, api := newTestStore(t)
	seedNotes(t, store, "tenant-a", 1)
	api.queries = nil

	page, err := store.ListNotes(context.Background(), "tenant-a", repository.ListOptions{})
	if err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Title != "Note 0" {
		t.Fatalf("list did not reconstruct the note from projected attributes: %+v", page.Items)
	}

	if len(api.queries) == 0 {
		t.Fatal("the list issued no query")
	}
	q := api.queries[0]
	if q.IndexName == nil || *q.IndexName != "gsi2" {
		t.Fatalf("the note list queried %v, want the gsi2 index — the base table can only order by creation",
			q.IndexName)
	}
	// A ProjectionExpression naming an unprojected attribute on a GSI does not
	// fetch it, it returns nothing for it. Relying on the index projection is
	// the only way this can fail loudly rather than silently blank a field.
	if q.ProjectionExpression != nil && strings.Contains(*q.ProjectionExpression, "data") {
		t.Fatalf("the list asks for the record blob: %q", *q.ProjectionExpression)
	}
	if !*q.ScanIndexForward == false {
		t.Fatal("the note list is not walking the index backwards")
	}
}

func TestArchivedNotesAreSeparatedFromActiveOnes(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	if _, err := store.PutNote(ctx, "tenant-a", model.NoteIndex{ID: "n_active", Title: "Active"}); err != nil {
		t.Fatalf("PutNote: %v", err)
	}

	future := time.Now().Add(24 * time.Hour)
	if _, err := store.PutNote(ctx, "tenant-a", model.NoteIndex{
		ID: "n_archived", Title: "Archived",
		DeletedAt: model.Now(), PurgeAfter: model.FormatTime(future), PurgeAfterEpoch: future.Unix(),
	}); err != nil {
		t.Fatalf("PutNote(archived): %v", err)
	}
	past := time.Now().Add(-time.Hour)
	if _, err := store.PutNote(ctx, "tenant-a", model.NoteIndex{
		ID: "n_expired", Title: "Expired",
		DeletedAt: model.Now(), PurgeAfter: model.FormatTime(past), PurgeAfterEpoch: past.Unix(),
	}); err != nil {
		t.Fatalf("PutNote(expired): %v", err)
	}

	activePage, err := store.ListNotes(ctx, "tenant-a", repository.ListOptions{})
	if err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	if len(activePage.Items) != 1 || activePage.Items[0].ID != "n_active" {
		t.Fatalf("active list = %+v, want only n_active", ids(activePage.Items))
	}

	archivedPage, err := store.ListArchivedNotes(ctx, "tenant-a", repository.ListOptions{})
	if err != nil {
		t.Fatalf("ListArchivedNotes: %v", err)
	}
	if len(archivedPage.Items) != 1 || archivedPage.Items[0].ID != "n_archived" {
		t.Fatalf("archived list = %v, want only n_archived", ids(archivedPage.Items))
	}
}

func ids(notes []model.NoteIndex) []string {
	out := make([]string, 0, len(notes))
	for _, n := range notes {
		out = append(out, n.ID)
	}
	return out
}

// ------------------------------------------------------ conditional writes

func TestPutNoteRejectsAStaleWrite(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	v1, err := store.PutNote(ctx, "tenant-a", model.NoteIndex{ID: "n1", Title: "First"})
	if err != nil {
		t.Fatalf("PutNote: %v", err)
	}

	// Two readers hold the same version; the second write must not win silently.
	editorCopy := v1
	voiceCopy := v1

	editorCopy.Title = "Edited in the browser"
	if _, err := store.PutNote(ctx, "tenant-a", editorCopy); err != nil {
		t.Fatalf("first writer: %v", err)
	}

	voiceCopy.Title = "Appended by voice"
	_, err = store.PutNote(ctx, "tenant-a", voiceCopy)
	if !errors.Is(err, repository.ErrVersionConflict) {
		t.Fatalf("second writer err = %v, want ErrVersionConflict", err)
	}

	got, err := store.GetNote(ctx, "tenant-a", "n1")
	if err != nil {
		t.Fatalf("GetNote: %v", err)
	}
	if got.Title != "Edited in the browser" {
		t.Fatalf("title = %q, want the first writer's value", got.Title)
	}
}

func TestPutNoteVersionIncrements(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	n, err := store.PutNote(ctx, "tenant-a", model.NoteIndex{ID: "n1"})
	if err != nil {
		t.Fatalf("PutNote: %v", err)
	}
	if n.Version != 1 {
		t.Fatalf("first version = %d, want 1", n.Version)
	}
	n2, err := store.PutNote(ctx, "tenant-a", n)
	if err != nil {
		t.Fatalf("PutNote(2): %v", err)
	}
	if n2.Version != 2 {
		t.Fatalf("second version = %d, want 2", n2.Version)
	}
}

func TestPutCaptureRejectsAStaleWrite(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	c1, err := store.PutCapture(ctx, model.CaptureIndex{ID: "c1", UserID: "tenant-a", NoteID: "n1", CreatedAt: model.Now()})
	if err != nil {
		t.Fatalf("PutCapture: %v", err)
	}
	stale := c1
	if _, err := store.PutCapture(ctx, c1); err != nil {
		t.Fatalf("PutCapture(2): %v", err)
	}
	if _, err := store.PutCapture(ctx, stale); !errors.Is(err, repository.ErrVersionConflict) {
		t.Fatalf("stale capture write err = %v, want ErrVersionConflict", err)
	}
}

// ------------------------------------------------------------------ GSI1

func TestListCapturesByNoteQueriesGSI1(t *testing.T) {
	store, api := newTestStore(t)
	ctx := context.Background()

	base := time.Now().UTC()
	for i := 0; i < 3; i++ {
		if _, err := store.PutCapture(ctx, model.CaptureIndex{
			ID: fmt.Sprintf("c%d", i), UserID: "tenant-a", NoteID: "n1",
			CreatedAt: model.FormatTime(base.Add(time.Duration(i) * time.Second)),
		}); err != nil {
			t.Fatalf("PutCapture: %v", err)
		}
	}
	// A capture on another note in the same tenant partition must not appear.
	if _, err := store.PutCapture(ctx, model.CaptureIndex{
		ID: "other", UserID: "tenant-a", NoteID: "n2", CreatedAt: model.Now(),
	}); err != nil {
		t.Fatalf("PutCapture(other): %v", err)
	}

	api.queries = nil
	page, err := store.ListCapturesByNote(ctx, "tenant-a", "n1", repository.ListOptions{})
	if err != nil {
		t.Fatalf("ListCapturesByNote: %v", err)
	}
	if len(page.Items) != 3 {
		t.Fatalf("got %d captures, want 3", len(page.Items))
	}
	// Newest first.
	if page.Items[0].ID != "c2" || page.Items[2].ID != "c0" {
		t.Fatalf("order = %v, want newest first", captureIDs(page.Items))
	}

	q := api.queries[0]
	if q.IndexName == nil || *q.IndexName != "gsi1" {
		t.Fatalf("query did not use gsi1: IndexName=%v — a partition scan is the defect being fixed", q.IndexName)
	}
	if got := *q.KeyConditionExpression; !strings.Contains(got, "gsi1pk = :pk") {
		t.Fatalf("key condition = %q, want a gsi1pk equality", got)
	}
	if got := avOf(q.ExpressionAttributeValues[":pk"]); got != "TENANT#tenant-a#NOTE#n1" {
		t.Fatalf("gsi1pk = %q, want TENANT#tenant-a#NOTE#n1", got)
	}
	if got := avOf(q.ExpressionAttributeValues[":sk_prefix"]); got != "CAPTURE#" {
		t.Fatalf("gsi1sk prefix = %q, want CAPTURE#", got)
	}
	if q.FilterExpression != nil {
		t.Fatalf("index query still filters client-side: %q", *q.FilterExpression)
	}
}

// The index has to answer on its own. Hydrating each entry with a GetItem turns
// one read into N+1 and gives back most of what querying the index bought.
func TestListCapturesByNoteDoesNotHydratePerItem(t *testing.T) {
	store, api := newTestStore(t)
	ctx := context.Background()

	base := time.Now().UTC()
	for i := 0; i < 5; i++ {
		if _, err := store.PutCapture(ctx, model.CaptureIndex{
			ID: fmt.Sprintf("c%d", i), UserID: "tenant-a", NoteID: "n1",
			CreatedAt: model.FormatTime(base.Add(time.Duration(i) * time.Second)),
			AudioKey:  fmt.Sprintf("tenants/tenant-a/captures/c%d/audio.webm", i),
		}); err != nil {
			t.Fatalf("PutCapture: %v", err)
		}
	}

	api.gets = 0
	page, err := store.ListCapturesByNote(ctx, "tenant-a", "n1", repository.ListOptions{})
	if err != nil {
		t.Fatalf("ListCapturesByNote: %v", err)
	}
	if len(page.Items) != 5 {
		t.Fatalf("got %d captures, want 5", len(page.Items))
	}
	if api.gets != 0 {
		t.Fatalf("list issued %d GetItem calls; the index projection should answer without them", api.gets)
	}
}

// Every S3 artefact a cascade delete has to unlink must survive the index
// projection. One missing key here is one orphaned object per capture, and
// widening the projection later means rebuilding the index.
func TestListedCaptureCarriesEveryArtefactKeyTheCascadeDeleteNeeds(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	want := model.CaptureIndex{
		ID: "c1", UserID: "tenant-a", NoteID: "n1", CreatedAt: model.Now(),
		Status:      model.StatusAppended,
		AudioKey:    "tenants/tenant-a/captures/c1/audio.webm",
		RawKey:      "tenants/tenant-a/captures/c1/raw.txt",
		RoutedKey:   "tenants/tenant-a/captures/c1/routed.txt",
		CleanKey:    "tenants/tenant-a/captures/c1/clean.txt",
		SegmentsKey: "tenants/tenant-a/captures/c1/segments.json",
		PeaksKey:    "tenants/tenant-a/captures/c1/peaks.json",
		DurationMS:  1234,
	}
	if _, err := store.PutCapture(ctx, want); err != nil {
		t.Fatalf("PutCapture: %v", err)
	}

	page, err := store.ListCapturesByNote(ctx, "tenant-a", "n1", repository.ListOptions{})
	if err != nil {
		t.Fatalf("ListCapturesByNote: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("got %d captures, want 1", len(page.Items))
	}
	got := page.Items[0]

	for _, field := range []struct{ name, got, want string }{
		{"audio_key", got.AudioKey, want.AudioKey},
		{"raw_key", got.RawKey, want.RawKey},
		{"routed_key", got.RoutedKey, want.RoutedKey},
		{"clean_key", got.CleanKey, want.CleanKey},
		{"segments_key", got.SegmentsKey, want.SegmentsKey},
		{"peaks_key", got.PeaksKey, want.PeaksKey},
	} {
		if field.got != field.want {
			t.Errorf("%s = %q, want %q — the GSI1 projection does not carry it, so a cascade delete would orphan that object",
				field.name, field.got, field.want)
		}
	}

	// The rest of what a list is expected to render.
	if got.ID != want.ID || got.NoteID != want.NoteID || got.UserID != "tenant-a" {
		t.Errorf("identity = %+v, want id/note/tenant of %+v", got, want)
	}
	if got.Status != want.Status || got.CreatedAt != want.CreatedAt || got.DurationMS != want.DurationMS {
		t.Errorf("listed capture = %+v, want status/created_at/duration of %+v", got, want)
	}
}

// A capture whose index entry predates capture_id still has to come back whole,
// because a cascade delete that saw it without its S3 keys would skip them.
func TestListCapturesByNoteReadsPrePromotionIndexEntriesWhole(t *testing.T) {
	api := newFakeDynamo()
	store := repository.NewDynamoStore(api, tableName)

	legacy := `{"id":"c_legacy","note_id":"n1","user_id":"tenant-a","status":"appended",` +
		`"audio_key":"tenants/tenant-a/captures/c_legacy/audio.webm","created_at":"2026-08-01T00:00:00Z"}`
	api.put(map[string]types.AttributeValue{
		"pk":     &types.AttributeValueMemberS{Value: "USER#tenant-a"},
		"sk":     &types.AttributeValueMemberS{Value: "CAPTURE#c_legacy"},
		"type":   &types.AttributeValueMemberS{Value: "capture"},
		"gsi1pk": &types.AttributeValueMemberS{Value: "TENANT#tenant-a#NOTE#n1"},
		"gsi1sk": &types.AttributeValueMemberS{Value: "CAPTURE#2026-08-01T00:00:00Z"},
		"data":   &types.AttributeValueMemberS{Value: legacy},
	})

	page, err := store.ListCapturesByNote(context.Background(), "tenant-a", "n1", repository.ListOptions{})
	if err != nil {
		t.Fatalf("ListCapturesByNote: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("got %d captures, want the one legacy capture", len(page.Items))
	}
	if page.Items[0].AudioKey != "tenants/tenant-a/captures/c_legacy/audio.webm" {
		t.Fatalf("legacy capture = %+v, want its audio key recovered from the blob", page.Items[0])
	}
}

func avOf(v types.AttributeValue) string {
	if s, ok := v.(*types.AttributeValueMemberS); ok {
		return s.Value
	}
	return ""
}

func captureIDs(cs []model.CaptureIndex) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.ID)
	}
	return out
}

// ------------------------------------------------------------- append guard

func TestClaimCaptureAppendIsExclusive(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	if _, err := store.PutCapture(ctx, model.CaptureIndex{
		ID: "c1", UserID: "tenant-a", NoteID: "n1", CreatedAt: model.Now(), Status: model.StatusCleaned,
	}); err != nil {
		t.Fatalf("PutCapture: %v", err)
	}

	claimed, _, err := store.ClaimCaptureAppend(ctx, "tenant-a", "c1", "token-1")
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if !claimed {
		t.Fatal("first claim was refused")
	}

	// The same token again — a retry — must not be handed the append a second
	// time while the first is still in progress.
	claimed, current, err := store.ClaimCaptureAppend(ctx, "tenant-a", "c1", "token-1")
	if err != nil {
		t.Fatalf("retry claim: %v", err)
	}
	if claimed {
		t.Fatal("a retry re-claimed an append that is already owned")
	}
	if current.AppendedAt != 0 {
		t.Fatalf("AppendedAt = %d, want 0 while the append is unfinished", current.AppendedAt)
	}

	done, err := store.CompleteCaptureAppend(ctx, "tenant-a", "c1", "token-1")
	if err != nil {
		t.Fatalf("CompleteCaptureAppend: %v", err)
	}
	if done.Status != model.StatusAppended || done.AppendedAt == 0 {
		t.Fatalf("completed capture = %+v, want appended with a timestamp", done)
	}

	claimed, current, err = store.ClaimCaptureAppend(ctx, "tenant-a", "c1", "token-1")
	if err != nil {
		t.Fatalf("post-completion claim: %v", err)
	}
	if claimed {
		t.Fatal("append was claimed again after completion")
	}
	if current.AppendedAt == 0 {
		t.Fatal("a completed append does not report AppendedAt")
	}
}

func TestCompleteCaptureAppendRejectsAnotherHoldersToken(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	if _, err := store.PutCapture(ctx, model.CaptureIndex{ID: "c1", UserID: "tenant-a", NoteID: "n1", CreatedAt: model.Now()}); err != nil {
		t.Fatalf("PutCapture: %v", err)
	}
	if _, _, err := store.ClaimCaptureAppend(ctx, "tenant-a", "c1", "token-mine"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := store.CompleteCaptureAppend(ctx, "tenant-a", "c1", "token-theirs"); !errors.Is(err, repository.ErrVersionConflict) {
		t.Fatalf("err = %v, want ErrVersionConflict", err)
	}
}

// ------------------------------------------------------------ idempotency

func TestIdempotencyClaimReplayAndInFlight(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	// First caller owns the key.
	rec, err := store.BeginIdempotent(ctx, "tenant-a", "key-1", "fp-1")
	if err != nil {
		t.Fatalf("BeginIdempotent: %v", err)
	}
	if rec != nil {
		t.Fatalf("first caller got a record %+v, want nil so it performs the work", rec)
	}

	// A second, genuinely concurrent attempt is told to back off rather than
	// duplicating the work.
	if _, err := store.BeginIdempotent(ctx, "tenant-a", "key-1", "fp-1"); !errors.Is(err, repository.ErrIdempotencyInFlight) {
		t.Fatalf("concurrent attempt err = %v, want ErrIdempotencyInFlight", err)
	}

	if err := store.CompleteIdempotent(ctx, "tenant-a", "key-1", 201, []byte(`{"id":"note_1"}`)); err != nil {
		t.Fatalf("CompleteIdempotent: %v", err)
	}

	replay, err := store.BeginIdempotent(ctx, "tenant-a", "key-1", "fp-1")
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replay == nil {
		t.Fatal("replay returned no record; the original response is lost")
	}
	if replay.Status != 201 || string(replay.Response) != `{"id":"note_1"}` {
		t.Fatalf("replay = %+v, want the original 201 response", replay)
	}
}

func TestIdempotencyRejectsAReusedKeyWithADifferentBody(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	if _, err := store.BeginIdempotent(ctx, "tenant-a", "key-1", "fp-1"); err != nil {
		t.Fatalf("BeginIdempotent: %v", err)
	}
	if _, err := store.BeginIdempotent(ctx, "tenant-a", "key-1", "fp-DIFFERENT"); !errors.Is(err, repository.ErrIdempotencyKeyReused) {
		t.Fatalf("err = %v, want ErrIdempotencyKeyReused", err)
	}
}

func TestIdempotencyKeysAreTenantScoped(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	if _, err := store.BeginIdempotent(ctx, "tenant-a", "key-1", "fp-1"); err != nil {
		t.Fatalf("tenant-a: %v", err)
	}
	// The same key for a different tenant is a different key.
	rec, err := store.BeginIdempotent(ctx, "tenant-b", "key-1", "fp-1")
	if err != nil {
		t.Fatalf("tenant-b: %v", err)
	}
	if rec != nil {
		t.Fatal("tenant-b was handed tenant-a's idempotency record")
	}
}

// An SDK-level retry of a committed conditional PutItem comes back as
// ConditionalCheckFailed. The attempt token is what stops that from locking the
// original caller out of its own key for the whole TTL.
func TestIdempotencyOwnRetryIsNotTreatedAsADuplicate(t *testing.T) {
	api := newFakeDynamo()
	retrier := &putRetryingDynamo{fakeDynamo: api}
	store := repository.NewDynamoStore(retrier, tableName)

	rec, err := store.BeginIdempotent(context.Background(), "tenant-a", "key-1", "fp-1")
	if err != nil {
		t.Fatalf("BeginIdempotent under an SDK retry = %v, want the caller to own its own key", err)
	}
	if rec != nil {
		t.Fatalf("got record %+v, want nil so the caller proceeds", rec)
	}
	if !retrier.retried {
		t.Fatal("the test did not actually exercise a duplicated PutItem")
	}
}

// putRetryingDynamo commits the first PutItem twice, mimicking an SDK retry
// whose first response was lost.
type putRetryingDynamo struct {
	*fakeDynamo
	retried bool
}

func (d *putRetryingDynamo) PutItem(ctx context.Context, in *dynamodb.PutItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	out, err := d.fakeDynamo.PutItem(ctx, in, opts...)
	if err != nil || d.retried {
		return out, err
	}
	d.retried = true
	// Same request again: the item now exists, so the condition fails.
	return d.fakeDynamo.PutItem(ctx, in, opts...)
}

// Items written by v1 carry only the `data` blob, which the list projection
// deliberately does not fetch. They must still list, not vanish and not error.
func TestListNotesReadsItemsWrittenBeforeAttributesWerePromoted(t *testing.T) {
	api := newFakeDynamo()
	store := repository.NewDynamoStore(api, tableName)

	legacy := `{"id":"note_legacy","title":"Written by v1","aliases":["old"],"updated_at":"2026-08-01T00:00:00Z","s3_markdown_key":"tenants/tenant-a/notes/note_legacy/note.md"}`
	api.put(map[string]types.AttributeValue{
		"pk":   &types.AttributeValueMemberS{Value: "USER#tenant-a"},
		"sk":   &types.AttributeValueMemberS{Value: "NOTE#note_legacy"},
		"type": &types.AttributeValueMemberS{Value: "note"},
		"data": &types.AttributeValueMemberS{Value: legacy},
	})

	// A note written before GSI2 existed carries none of its key attributes, so
	// DynamoDB does not index it and the list cannot see it. That is the
	// migration this index needs, and leaving it implicit would mean shipping a
	// deploy that empties the notes list.
	before, err := store.ListNotes(context.Background(), "tenant-a", repository.ListOptions{})
	if err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	if len(before.Items) != 0 {
		t.Fatalf("got %d notes before reindexing; if this passes, the migration below is not the thing being tested",
			len(before.Items))
	}

	touched, err := store.ReindexNotes(context.Background(), "tenant-a")
	if err != nil {
		t.Fatalf("ReindexNotes: %v", err)
	}
	if touched != 1 {
		t.Fatalf("reindexed %d notes, want 1", touched)
	}

	page, err := store.ListNotes(context.Background(), "tenant-a", repository.ListOptions{})
	if err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("got %d notes after reindexing, want the one legacy note", len(page.Items))
	}
	if page.Items[0].Title != "Written by v1" {
		t.Fatalf("legacy note = %+v, want its title recovered from the blob", page.Items[0])
	}

	// Idempotent: running it again must not double-count or disturb anything.
	if touched, err := store.ReindexNotes(context.Background(), "tenant-a"); err != nil || touched != 1 {
		t.Fatalf("second ReindexNotes = %d, %v; want 1, nil", touched, err)
	}
}

// The store and the CloudFormation template have to agree about GSI1, and
// nothing else checks it: a projection that is missing an attribute does not
// error at runtime, it returns an empty field. Widening it later is not an
// update — the index is deleted and rebuilt.
func TestGSI1ProjectionCoversWhatTheCaptureListReads(t *testing.T) {
	projected := indexNonKeyAttributes("gsi1")
	if len(projected) == 0 {
		t.Fatalf("could not read the gsi1 projection from %s; the drift check is not running", templatePath)
	}

	// Every attribute ListCapturesByNote builds a capture out of.
	required := []string{
		"capture_id", "note_id", "status", "created_at", "version", "duration_ms",
		// Every artefact a cascade delete unlinks. A missing one orphans an
		// object that the UI has already reported as purged.
		"audio_key", "raw_key", "routed_key", "clean_key", "segments_key", "peaks_key",
	}
	for _, name := range required {
		if !projected[name] {
			t.Errorf("gsi1 does not project %q, so a listed capture cannot carry it", name)
		}
	}

	// ALL would carry `data`, which is the transfer cost the projection exists
	// to remove.
	if projected["data"] {
		t.Error("gsi1 projects the `data` blob; the index now duplicates every capture body")
	}
}

// ------------------------------------------------------- field round-trips

// populatedNote fills every exported field of model.NoteIndex with a
// distinctive non-zero value.
//
// It walks the struct by reflection rather than listing fields by hand, so a
// field added to the model later is covered by the round-trip tests without
// anybody remembering to extend them. An unhandled field kind is a hard failure
// here rather than a silently unasserted field.
func populatedNote(t *testing.T) model.NoteIndex {
	t.Helper()
	var n model.NoteIndex
	v := reflect.ValueOf(&n).Elem()
	typ := v.Type()
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if !f.IsExported() {
			t.Fatalf("model.NoteIndex.%s is unexported; the round-trip test cannot populate it", f.Name)
		}
		fv := v.Field(i)
		lower := strings.ToLower(f.Name)
		switch {
		case f.Type.Kind() == reflect.String:
			fv.SetString("rt-" + lower)
		case f.Type.Kind() == reflect.Bool:
			fv.SetBool(true)
		case f.Type.Kind() == reflect.Int64:
			fv.SetInt(int64(1_700_000_000 + i))
		case f.Type.Kind() == reflect.Slice && f.Type.Elem().Kind() == reflect.String:
			fv.Set(reflect.ValueOf([]string{lower + "-one", lower + "-two"}))
		default:
			t.Fatalf("model.NoteIndex.%s is a %s; teach populatedNote how to fill it", f.Name, f.Type.Kind())
		}
	}
	// Three fields carry store semantics rather than being opaque payload.
	// Version is the optimistic-concurrency counter the write conditions on, so
	// a first write has to carry 0; PurgeAfter/PurgeAfterEpoch have to name a
	// future instant or the archived list filters the note out before it can be
	// compared.
	n.Version = 0
	purge := time.Now().Add(24 * time.Hour).Truncate(time.Second)
	n.PurgeAfterEpoch = purge.Unix()
	n.PurgeAfter = model.FormatTime(purge)
	return n
}

// noteFieldDiffs names every exported field whose value did not survive.
func noteFieldDiffs(want, got model.NoteIndex) []string {
	wv, gv := reflect.ValueOf(want), reflect.ValueOf(got)
	var diffs []string
	for i := 0; i < wv.NumField(); i++ {
		f := wv.Type().Field(i)
		if !f.IsExported() {
			continue
		}
		a, b := wv.Field(i).Interface(), gv.Field(i).Interface()
		if !reflect.DeepEqual(a, b) {
			diffs = append(diffs, fmt.Sprintf("%s: want %#v, got %#v", f.Name, a, b))
		}
	}
	return diffs
}

// A note read back has to be the note that was written, field for field.
//
// The v1 assertions here checked ID, Title and Version only, which is why
// `verbatim` and `created_at` could be dropped on every single read without a
// test noticing: the store rebuilt the model from promoted attributes and those
// two were never promoted. `verbatim` in particular is the flag that says "do
// not reword this dictation", so losing it silently sends content through
// cleanup the user explicitly excluded.
func TestNoteRoundTripPreservesEveryField(t *testing.T) {
	eachStore(t, func(t *testing.T) {
		store := newStore()
		ctx := context.Background()
		want := populatedNote(t)

		stored, err := store.PutNote(ctx, "tenant-a", want)
		if err != nil {
			t.Fatalf("PutNote: %v", err)
		}
		want.Version = 1
		if diffs := noteFieldDiffs(want, stored); len(diffs) > 0 {
			t.Errorf("PutNote returned a different note:\n\t%s", strings.Join(diffs, "\n\t"))
		}

		got, err := store.GetNote(ctx, "tenant-a", want.ID)
		if err != nil {
			t.Fatalf("GetNote: %v", err)
		}
		if diffs := noteFieldDiffs(want, got); len(diffs) > 0 {
			t.Fatalf("GetNote lost fields on the round trip:\n\t%s", strings.Join(diffs, "\n\t"))
		}
	})
}

// A listed note has to carry every field too. The list reads a projection
// rather than the record blob, so a field missing from the projection is a
// field the list renders as empty.
func TestListedNoteCarriesEveryField(t *testing.T) {
	eachStore(t, func(t *testing.T) {
		store := newStore()
		ctx := context.Background()
		want := populatedNote(t)

		if _, err := store.PutNote(ctx, "tenant-a", want); err != nil {
			t.Fatalf("PutNote: %v", err)
		}
		want.Version = 1

		// populatedNote sets DeletedAt, so the note is archived.
		page, err := store.ListArchivedNotes(ctx, "tenant-a", repository.ListOptions{})
		if err != nil {
			t.Fatalf("ListArchivedNotes: %v", err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("archived list = %v, want the one note just written", ids(page.Items))
		}
		if diffs := noteFieldDiffs(want, page.Items[0]); len(diffs) > 0 {
			t.Fatalf("the listed note lost fields:\n\t%s", strings.Join(diffs, "\n\t"))
		}
	})
}

// An update has to preserve what the previous read did not surface. This is the
// mechanism that made the dropped fields permanent rather than merely invisible:
// read a note, write it back, and the fields the read blanked are now blanked in
// storage as well.
func TestUpdatingANoteDoesNotErasePreviouslyStoredFields(t *testing.T) {
	eachStore(t, func(t *testing.T) {
		store := newStore()
		ctx := context.Background()
		want := populatedNote(t)

		if _, err := store.PutNote(ctx, "tenant-a", want); err != nil {
			t.Fatalf("PutNote: %v", err)
		}
		want.Version = 1

		// The read-modify-write every caller performs.
		read, err := store.GetNote(ctx, "tenant-a", want.ID)
		if err != nil {
			t.Fatalf("GetNote: %v", err)
		}
		read.Title = "Retitled"
		if _, err := store.PutNote(ctx, "tenant-a", read); err != nil {
			t.Fatalf("PutNote(update): %v", err)
		}

		got, err := store.GetNote(ctx, "tenant-a", want.ID)
		if err != nil {
			t.Fatalf("GetNote(after update): %v", err)
		}
		want.Title = "Retitled"
		want.Version = 2
		if diffs := noteFieldDiffs(want, got); len(diffs) > 0 {
			t.Fatalf("an unrelated update erased stored fields:\n\t%s", strings.Join(diffs, "\n\t"))
		}
	})
}

// ---------------------------------------------------------------------------
// Shapes DynamoDB refuses
// ---------------------------------------------------------------------------

// tableNameValue is tableName as an addressable copy, for the inputs below that
// are built by hand rather than by the store.
var tableNameValue = tableName

// TestEveryWriteIsAnItemDynamoDBWouldAccept walks the store's write paths
// through a fake that applies the service's own AttributeValue shape rules.
//
// It exists because the absence of this check was itself the defect. Every
// repository test passed while BeginIdempotent wrote
// "idem_response": &types.AttributeValueMemberB{Value: nil} — an AttributeValue
// carrying no datatype — and real DynamoDB answered
//
//	ValidationException: Supplied AttributeValue is empty, must contain
//	exactly one of the supported datatypes
//
// to the whole PutItem. Since every POST that carries an Idempotency-Key begins
// with that write, and every capture carries one, nothing could be recorded at
// all. The fake stored what it was handed, so it agreed with the bug.
//
// The subtests below are the writes on the request path. A shape violation in
// any of them now fails here, with the message production produced.
func TestEveryWriteIsAnItemDynamoDBWouldAccept(t *testing.T) {
	ctx := context.Background()

	t.Run("claiming an idempotency key", func(t *testing.T) {
		store, _ := newTestStore(t)
		if _, err := store.BeginIdempotent(ctx, "tenant-a", "key-1", "fp-1"); err != nil {
			t.Fatalf("BeginIdempotent: %v", err)
		}
	})

	t.Run("completing one with a body", func(t *testing.T) {
		store, _ := newTestStore(t)
		if _, err := store.BeginIdempotent(ctx, "tenant-a", "key-1", "fp-1"); err != nil {
			t.Fatalf("BeginIdempotent: %v", err)
		}
		if err := store.CompleteIdempotent(ctx, "tenant-a", "key-1", 201, []byte(`{"id":"n1"}`)); err != nil {
			t.Fatalf("CompleteIdempotent: %v", err)
		}
	})

	// A 204, or any handler that writes no body. The response attribute cannot
	// be written empty, so it must not be written at all.
	t.Run("completing one with no body", func(t *testing.T) {
		store, _ := newTestStore(t)
		if _, err := store.BeginIdempotent(ctx, "tenant-a", "key-2", "fp-2"); err != nil {
			t.Fatalf("BeginIdempotent: %v", err)
		}
		if err := store.CompleteIdempotent(ctx, "tenant-a", "key-2", 204, nil); err != nil {
			t.Fatalf("CompleteIdempotent with no body: %v", err)
		}
		replay, err := store.BeginIdempotent(ctx, "tenant-a", "key-2", "fp-2")
		if err != nil {
			t.Fatalf("replay: %v", err)
		}
		if replay == nil || !replay.Done {
			t.Fatal("a bodyless completion did not read back as done")
		}
		if len(replay.Response) != 0 {
			t.Errorf("replayed response = %q, want empty", replay.Response)
		}
	})

	// A note and a capture carrying nothing but their required fields: every
	// optional string empty and every list nil, which is the shape most likely
	// to produce an attribute the service will not take.
	t.Run("a note with every optional field empty", func(t *testing.T) {
		store, _ := newTestStore(t)
		if _, err := store.PutNote(ctx, "tenant-a", model.NoteIndex{
			ID: "note_1", Title: "t", UpdatedAt: model.Now(),
		}); err != nil {
			t.Fatalf("PutNote: %v", err)
		}
	})

	t.Run("a capture with every optional field empty", func(t *testing.T) {
		store, _ := newTestStore(t)
		if _, err := store.PutCapture(ctx, model.CaptureIndex{
			ID: "c_1", UserID: "tenant-a", Status: model.StatusUploaded, CreatedAt: model.Now(),
		}); err != nil {
			t.Fatalf("PutCapture: %v", err)
		}
	})

	t.Run("settings", func(t *testing.T) {
		store, _ := newTestStore(t)
		if err := store.PutSettings(ctx, "tenant-a", model.Settings{}); err != nil {
			t.Fatalf("PutSettings: %v", err)
		}
	})

	t.Run("an append claim and its completion", func(t *testing.T) {
		store, _ := newTestStore(t)
		if _, err := store.PutCapture(ctx, model.CaptureIndex{
			ID: "c_1", UserID: "tenant-a", Status: model.StatusCleaned, CreatedAt: model.Now(),
			CleanKey: "tenants/tenant-a/captures/c_1/clean.txt",
		}); err != nil {
			t.Fatalf("PutCapture: %v", err)
		}
		claimed, _, err := store.ClaimCaptureAppend(ctx, "tenant-a", "c_1", "token-1")
		if err != nil {
			t.Fatalf("ClaimCaptureAppend: %v", err)
		}
		if !claimed {
			t.Fatal("claim was refused")
		}
		if _, err := store.CompleteCaptureAppend(ctx, "tenant-a", "c_1", "token-1"); err != nil {
			t.Fatalf("CompleteCaptureAppend: %v", err)
		}
	})

	// The expiry cascade's writes. internal/purge is new code driving these
	// against a table it has never met.
	t.Run("the deletes the expiry cascade performs", func(t *testing.T) {
		store, _ := newTestStore(t)
		if _, err := store.PutCapture(ctx, model.CaptureIndex{
			ID: "c_1", UserID: "tenant-a", Status: model.StatusAppended, CreatedAt: model.Now(),
		}); err != nil {
			t.Fatalf("PutCapture: %v", err)
		}
		if _, err := store.PutNote(ctx, "tenant-a", model.NoteIndex{
			ID: "note_1", Title: "t", UpdatedAt: model.Now(),
		}); err != nil {
			t.Fatalf("PutNote: %v", err)
		}
		if err := store.DeleteCapture(ctx, "tenant-a", "c_1"); err != nil {
			t.Fatalf("DeleteCapture: %v", err)
		}
		if err := store.DeleteNote(ctx, "tenant-a", "note_1"); err != nil {
			t.Fatalf("DeleteNote: %v", err)
		}
	})
}

// TestTheFakeRefusesTheShapesTheServiceRefuses is the check on the check. A
// validator nobody has watched reject anything is a validator nobody should
// trust — and this one exists precisely because the previous fake accepted
// everything.
func TestTheFakeRefusesTheShapesTheServiceRefuses(t *testing.T) {
	bad := map[string]types.AttributeValue{
		"a nil binary":                 &types.AttributeValueMemberB{Value: nil},
		"an empty binary":              &types.AttributeValueMemberB{Value: []byte{}},
		"a nil attribute":              nil,
		"an empty number":              &types.AttributeValueMemberN{Value: ""},
		"an empty string set":          &types.AttributeValueMemberSS{Value: []string{}},
		"a nil binary inside a list":   &types.AttributeValueMemberL{Value: []types.AttributeValue{&types.AttributeValueMemberB{}}},
		"a nil binary inside a map":    &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{"x": &types.AttributeValueMemberB{}}},
		"a member type nobody defined": &types.UnknownUnionMember{Tag: "Q"},
	}

	for name, value := range bad {
		t.Run(name, func(t *testing.T) {
			api := newFakeDynamo()
			_, err := api.PutItem(context.Background(), &dynamodb.PutItemInput{
				TableName: &tableNameValue,
				Item: map[string]types.AttributeValue{
					"pk":    &types.AttributeValueMemberS{Value: "USER#tenant-a"},
					"sk":    &types.AttributeValueMemberS{Value: "THING#1"},
					"thing": value,
				},
			})
			if err == nil {
				t.Fatal("the fake accepted an attribute DynamoDB refuses")
			}
			if !strings.Contains(err.Error(), "ValidationException") {
				t.Errorf("error = %v, want a ValidationException", err)
			}
		})
	}

	t.Run("an empty key attribute", func(t *testing.T) {
		api := newFakeDynamo()
		_, err := api.PutItem(context.Background(), &dynamodb.PutItemInput{
			TableName: &tableNameValue,
			Item: map[string]types.AttributeValue{
				"pk": &types.AttributeValueMemberS{Value: ""},
				"sk": &types.AttributeValueMemberS{Value: "THING#1"},
			},
		})
		if err == nil {
			t.Fatal("the fake accepted an empty partition key")
		}
	})

	// The legal shapes must still be legal, or the validator would fail every
	// write and prove nothing.
	t.Run("shapes the service accepts", func(t *testing.T) {
		api := newFakeDynamo()
		if _, err := api.PutItem(context.Background(), &dynamodb.PutItemInput{
			TableName: &tableNameValue,
			Item: map[string]types.AttributeValue{
				"pk":            &types.AttributeValueMemberS{Value: "USER#tenant-a"},
				"sk":            &types.AttributeValueMemberS{Value: "THING#1"},
				"an empty text": &types.AttributeValueMemberS{Value: ""},
				"an empty list": &types.AttributeValueMemberL{Value: nil},
				"an empty map":  &types.AttributeValueMemberM{Value: nil},
				"a null":        &types.AttributeValueMemberNULL{Value: true},
				"a bool":        &types.AttributeValueMemberBOOL{Value: false},
				"some bytes":    &types.AttributeValueMemberB{Value: []byte{0}},
			},
		}); err != nil {
			t.Fatalf("the fake refused a legal item: %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// List order
// ---------------------------------------------------------------------------

// pageThroughNotes walks every page of a note list and returns the ids in the
// order they were served, plus the number of pages it took.
//
// It asserts across pages deliberately. A list that is sorted after the fact,
// per page, passes any single-page assertion and still serves the whole
// collection in the wrong order — page one holds the oldest notes, correctly
// sorted among themselves.
func pageThroughNotes(
	t *testing.T,
	list func(context.Context, repository.ListOptions) (repository.Page[model.NoteIndex], error),
	limit int32,
) ([]string, int) {
	t.Helper()
	ctx := context.Background()

	var order []string
	seen := map[string]bool{}
	opts := repository.ListOptions{Limit: limit}
	pages := 0

	for {
		page, err := list(ctx, opts)
		if err != nil {
			t.Fatalf("page %d: %v", pages+1, err)
		}
		pages++
		for _, n := range page.Items {
			if seen[n.ID] {
				t.Fatalf("%s was served twice; pagination is repeating items", n.ID)
			}
			seen[n.ID] = true
			order = append(order, n.ID)
		}
		if page.Cursor == "" {
			break
		}
		if pages > 50 {
			t.Fatal("pagination did not terminate")
		}
		opts.Cursor = page.Cursor
	}
	return order, pages
}

// TestNotesComeBackMostRecentlyTouchedFirst is the order the owner asked for,
// and the reason it needs an index rather than a reversed query.
//
// The notes here are created in one order and updated in the opposite one, so
// creation order and update order disagree about every single note. That is not
// a contrived case: in a voice app every capture appends to a note, so the note
// somebody touched last is almost never the note they made last. A query that
// walks the base table backwards — NOTE#<id>, and a note id leads with its
// creation instant — passes a test where the two agree and fails this one.
//
// The walk spans three pages because a page sorted after it arrives also passes
// a single-page assertion while leaving the collection in the wrong order.
func TestNotesComeBackMostRecentlyTouchedFirst(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	const total = 7

	// note_000 is the oldest by creation and the newest by update; note_006 is
	// the reverse. Every pair disagrees.
	for i := 0; i < total; i++ {
		touched := time.Date(2026, 8, 1, 0, 0, total-i, 0, time.UTC)
		if _, err := store.PutNote(ctx, "tenant-a", model.NoteIndex{
			ID:        fmt.Sprintf("note_%03d", i),
			Title:     fmt.Sprintf("Note %d", i),
			UpdatedAt: model.FormatTime(touched),
		}); err != nil {
			t.Fatalf("seed note %d: %v", i, err)
		}
	}

	order, pages := pageThroughNotes(t, func(ctx context.Context, opts repository.ListOptions) (repository.Page[model.NoteIndex], error) {
		return store.ListNotes(ctx, "tenant-a", opts)
	}, 3)

	if pages < 2 {
		t.Fatalf("the walk took %d page(s); this test is meaningless on one page", pages)
	}
	if len(order) != total {
		t.Fatalf("saw %d notes over %d pages, want all %d", len(order), pages, total)
	}

	// Most recently touched first, which here is creation order forwards.
	want := make([]string, 0, total)
	for i := 0; i < total; i++ {
		want = append(want, fmt.Sprintf("note_%03d", i))
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("notes came back %v, want most recently touched first %v.\n"+
				"Reversing the base table would give the exact opposite, which is newest CREATED.",
				order, want)
		}
	}
}

// TestTouchingANoteMovesItToTheTop is the same property from the other side: an
// old note that receives a capture today belongs at the top afterwards, and the
// index has to be rewritten by the ordinary write path for that to happen.
func TestTouchingANoteMovesItToTheTop(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := store.PutNote(ctx, "tenant-a", model.NoteIndex{
			ID:        fmt.Sprintf("note_%03d", i),
			Title:     fmt.Sprintf("Note %d", i),
			UpdatedAt: model.FormatTime(time.Date(2026, 8, 1, 0, 0, i, 0, time.UTC)),
		}); err != nil {
			t.Fatalf("seed note %d: %v", i, err)
		}
	}

	oldest, err := store.GetNote(ctx, "tenant-a", "note_000")
	if err != nil {
		t.Fatalf("GetNote: %v", err)
	}
	oldest.UpdatedAt = model.FormatTime(time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC))
	if _, err := store.PutNote(ctx, "tenant-a", oldest); err != nil {
		t.Fatalf("PutNote: %v", err)
	}

	page, err := store.ListNotes(ctx, "tenant-a", repository.ListOptions{})
	if err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	if len(page.Items) == 0 || page.Items[0].ID != "note_000" {
		t.Fatalf("after touching the oldest note the list starts with %v, want note_000",
			func() string {
				if len(page.Items) == 0 {
					return "nothing"
				}
				return page.Items[0].ID
			}())
	}
}

// TestArchivedNotesComeBackNewestFirstAcrossPages holds the archive to the same
// order. It is the same query shape with a different filter, so a fix applied to
// one and not the other leaves the two lists disagreeing about what "first"
// means.
func TestArchivedNotesComeBackNewestFirstAcrossPages(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	const total = 7
	purge := time.Now().Add(24 * time.Hour)

	for i := 0; i < total; i++ {
		if _, err := store.PutNote(ctx, "tenant-a", model.NoteIndex{
			ID: fmt.Sprintf("note_%03d", i), Title: fmt.Sprintf("Note %d", i),
			UpdatedAt: model.Now(), DeletedAt: model.Now(),
			PurgeAfter: model.FormatTime(purge), PurgeAfterEpoch: purge.Unix(),
		}); err != nil {
			t.Fatalf("seed archived note %d: %v", i, err)
		}
	}

	order, pages := pageThroughNotes(t, func(ctx context.Context, opts repository.ListOptions) (repository.Page[model.NoteIndex], error) {
		return store.ListArchivedNotes(ctx, "tenant-a", opts)
	}, 3)

	if pages < 2 {
		t.Fatalf("the walk took %d page(s); this test is meaningless on one page", pages)
	}
	if len(order) != total {
		t.Fatalf("saw %d archived notes over %d pages, want all %d", len(order), pages, total)
	}
	if order[0] != fmt.Sprintf("note_%03d", total-1) {
		t.Fatalf("archived page one starts at %s, want the newest %s",
			order[0], fmt.Sprintf("note_%03d", total-1))
	}
}

// TestCapturesComeBackNewestFirstAcrossPages pins the order the capture ids
// were made time-sortable for. The progress card only ever asks for page one,
// so a capture that lands on page two is a capture the user watches as a stall.
//
// Both capture queries are already descending. This is here so that stays true.
func TestCapturesComeBackNewestFirstAcrossPages(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	const total = 7

	for i := 0; i < total; i++ {
		if _, err := store.PutCapture(ctx, model.CaptureIndex{
			ID: fmt.Sprintf("c_%03d", i), UserID: "tenant-a", NoteID: "note_1",
			Status: model.StatusAppended, CreatedAt: fmt.Sprintf("2026-08-0%dT00:00:00.000000000Z", i+1),
		}); err != nil {
			t.Fatalf("seed capture %d: %v", i, err)
		}
	}

	for _, tc := range []struct {
		name string
		list func(context.Context, repository.ListOptions) (repository.Page[model.CaptureIndex], error)
	}{
		{"the tenant list", func(ctx context.Context, o repository.ListOptions) (repository.Page[model.CaptureIndex], error) {
			return store.ListCaptures(ctx, "tenant-a", o)
		}},
		{"one note's captures", func(ctx context.Context, o repository.ListOptions) (repository.Page[model.CaptureIndex], error) {
			return store.ListCapturesByNote(ctx, "tenant-a", "note_1", o)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var order []string
			opts := repository.ListOptions{Limit: 3}
			pages := 0
			for {
				page, err := tc.list(ctx, opts)
				if err != nil {
					t.Fatalf("page %d: %v", pages+1, err)
				}
				pages++
				for _, c := range page.Items {
					order = append(order, c.ID)
				}
				if page.Cursor == "" {
					break
				}
				opts.Cursor = page.Cursor
			}
			if pages < 2 {
				t.Fatalf("the walk took %d page(s)", pages)
			}
			if len(order) != total {
				t.Fatalf("saw %d captures, want %d", len(order), total)
			}
			if order[0] != fmt.Sprintf("c_%03d", total-1) {
				t.Fatalf("captures came back %v, want newest (%s) first",
					order, fmt.Sprintf("c_%03d", total-1))
			}
		})
	}
}

// TestGSI2ProjectionCoversWhatTheNoteListReads holds the notes index to the
// same standard as GSI1, and for the same reason: a projection that is missing
// an attribute does not error, it returns an empty field — a note with no title
// rather than a failure. Widening it later is not an update; the index is
// deleted and rebuilt, which on a populated table is an outage of the notes
// list.
func TestGSI2ProjectionCoversWhatTheNoteListReads(t *testing.T) {
	projected := indexNonKeyAttributes("gsi2")
	if len(projected) == 0 {
		t.Fatalf("could not read the gsi2 projection from %s; the drift check is not running", templatePath)
	}

	required := []string{
		// Everything a note row renders.
		"note_id", "title", "snippet", "tags", "aliases", "created_at", "updated_at",
		// What the editor and the archive need without a second read.
		"s3_markdown_key", "s3_meta_key", "verbatim", "version",
		// What marks a note archived, and what the archived list filters on.
		"deleted_at", "purge_after", "purge_after_epoch",
	}
	for _, name := range required {
		if !projected[name] {
			t.Errorf("gsi2 does not project %q; the note list would read it back empty", name)
		}
	}

	// ALL would project `data`, the blob that duplicates every attribute above.
	// Carrying it is the transfer cost the projection exists to avoid.
	if projected["data"] {
		t.Error("gsi2 projects `data`; that is the cost this index exists to avoid")
	}
}
