package repository_test

import (
	"context"
	"errors"
	"fmt"
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

func TestListNotesUsesProjectionWithoutTheDataBlob(t *testing.T) {
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

	projection := api.queries[0].ProjectionExpression
	if projection == nil {
		t.Fatal("list query has no ProjectionExpression")
	}
	if strings.Contains(*projection, "data") {
		t.Fatalf("list projection still transfers the full record blob: %q", *projection)
	}
	for _, want := range []string{"title", "updated_at", "tags", "version", "note_id"} {
		if !strings.Contains(*projection, want) {
			t.Fatalf("projection %q is missing %q", *projection, want)
		}
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

	page, err := store.ListNotes(context.Background(), "tenant-a", repository.ListOptions{})
	if err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("got %d notes, want the one legacy note", len(page.Items))
	}
	if page.Items[0].Title != "Written by v1" {
		t.Fatalf("legacy note = %+v, want its title recovered from the blob", page.Items[0])
	}
}
