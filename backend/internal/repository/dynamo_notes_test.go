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
	"github.com/vppillai/chintan/backend/internal/repository/dynamofake"
)

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
	api.PageSize = 4

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
	seedNotes(t, store, "tenant-a", int(repository.MaxListLimit)+25)

	// The clamp is visible in the page the store returns: the list is ordered
	// in Go, so the limit is applied to the sorted result rather than to the
	// query.
	page, err := store.ListNotes(ctx, "tenant-a", repository.ListOptions{Limit: 100000})
	if err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	if got := len(page.Items); got != int(repository.MaxListLimit) {
		t.Fatalf("page has %d notes, want the clamp %d", got, repository.MaxListLimit)
	}
	if page.Cursor == "" {
		t.Fatal("a clamped page with more notes behind it carried no cursor")
	}

	page, err = store.ListNotes(ctx, "tenant-a", repository.ListOptions{})
	if err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	if got := len(page.Items); got != int(repository.DefaultListLimit) {
		t.Fatalf("page has %d notes, want the default %d", got, repository.DefaultListLimit)
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
// The list reads the base table and orders in Go (there was an index for a
// year; see MaxNotesDrained for why there is not one now), so the guarantee is a
// ProjectionExpression that never names `data`.
func TestListNotesDoesNotTransferTheDataBlob(t *testing.T) {
	store, api := newTestStore(t)
	seedNotes(t, store, "tenant-a", 1)
	api.Queries = nil

	page, err := store.ListNotes(context.Background(), "tenant-a", repository.ListOptions{})
	if err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Title != "Note 0" {
		t.Fatalf("list did not reconstruct the note from projected attributes: %+v", page.Items)
	}

	if len(api.Queries) == 0 {
		t.Fatal("the list issued no query")
	}
	q := api.Queries[0]
	if q.IndexName != nil {
		t.Fatalf("the note list queried index %q; there is no notes index any more", *q.IndexName)
	}
	if q.ProjectionExpression == nil {
		t.Fatal("the note list issued no ProjectionExpression, so it transfers the record blob")
	}
	for _, name := range strings.Split(*q.ProjectionExpression, ",") {
		if strings.TrimSpace(name) == "data" {
			t.Fatalf("the list asks for the record blob: %q", *q.ProjectionExpression)
		}
	}
}

// TestListNotesCursorIsStableWhenANoteMovesAboveIt is the property the cursor
// exists for. The order is built in Go from a sorted slice, so a cursor cannot
// be a DynamoDB key; it is the position of the last note served, and resuming
// re-derives the offset by comparison. A note touched between two pages jumps
// to the top, above the cursor, and page two must neither repeat a note the
// client saw nor skip one it had not — a stored offset would do the latter.
func TestListNotesCursorIsStableWhenANoteMovesAboveIt(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	const total = 6
	for i := 0; i < total; i++ {
		if _, err := store.PutNote(ctx, "tenant-a", model.NoteIndex{
			ID: fmt.Sprintf("note_%03d", i), Title: fmt.Sprintf("Note %d", i),
			UpdatedAt: model.FormatTime(time.Date(2026, 8, 1, 0, 0, total-i, 0, time.UTC)),
		}); err != nil {
			t.Fatalf("seed note %d: %v", i, err)
		}
	}

	first, err := store.ListNotes(ctx, "tenant-a", repository.ListOptions{Limit: 2})
	if err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	if len(first.Items) != 2 || first.Items[0].ID != "note_000" || first.Items[1].ID != "note_001" {
		t.Fatalf("page one = %v, want note_000, note_001", ids(first.Items))
	}

	// The last note in the order is touched now and belongs at the top.
	last, err := store.GetNote(ctx, "tenant-a", "note_005")
	if err != nil {
		t.Fatalf("GetNote: %v", err)
	}
	last.UpdatedAt = model.FormatTime(time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC))
	if _, err := store.PutNote(ctx, "tenant-a", last); err != nil {
		t.Fatalf("PutNote: %v", err)
	}

	var rest []string
	cursor := first.Cursor
	for cursor != "" {
		page, err := store.ListNotes(ctx, "tenant-a", repository.ListOptions{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatalf("ListNotes(cursor): %v", err)
		}
		rest = append(rest, ids(page.Items)...)
		cursor = page.Cursor
	}
	want := []string{"note_002", "note_003", "note_004"}
	if fmt.Sprint(rest) != fmt.Sprint(want) {
		t.Fatalf("after the touch the remaining pages returned %v, want %v: nothing repeated, nothing skipped, "+
			"and the touched note is above the cursor where an index walk would also have left it", rest, want)
	}
}

// An archive cursor replayed against the active list would resume at a
// position in a different order. Refused, like another tenant's.
func TestListNotesRejectsTheArchiveListsCursor(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	purge := time.Now().Add(24 * time.Hour)
	for i := 0; i < 3; i++ {
		if _, err := store.PutNote(ctx, "tenant-a", model.NoteIndex{
			ID: fmt.Sprintf("note_%03d", i), Title: "t", UpdatedAt: model.Now(), DeletedAt: model.Now(),
			PurgeAfter: model.FormatTime(purge), PurgeAfterEpoch: purge.Unix(),
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	page, err := store.ListArchivedNotes(ctx, "tenant-a", repository.ListOptions{Limit: 1})
	if err != nil || page.Cursor == "" {
		t.Fatalf("ListArchivedNotes = %v, cursor %q", err, page.Cursor)
	}
	if _, err := store.ListNotes(ctx, "tenant-a", repository.ListOptions{Cursor: page.Cursor}); err == nil {
		t.Fatal("the active list accepted an archive cursor")
	}
}

// TestExpiredNotesFindsEveryPastDueArchivedNoteAcrossTenants is the sweep's
// input. It has to cross tenants — the worker knows no tenant list — and it has
// to leave alone both the archived note that is not yet due and the live note
// that carries no deadline at all.
func TestExpiredNotesFindsEveryPastDueArchivedNoteAcrossTenants(t *testing.T) {
	store, api := newTestStore(t)
	ctx := context.Background()
	now := time.Now()
	put := func(tenant, id string, deadline time.Time) {
		n := model.NoteIndex{ID: id, Title: "Note " + id, UpdatedAt: model.Now()}
		if !deadline.IsZero() {
			n.DeletedAt = model.Now()
			n.PurgeAfter = model.FormatTime(deadline)
			n.PurgeAfterEpoch = deadline.Unix()
		}
		if _, err := store.PutNote(ctx, tenant, n); err != nil {
			t.Fatalf("PutNote(%s/%s): %v", tenant, id, err)
		}
	}
	put("tenant-a", "live", time.Time{})
	put("tenant-a", "pending", now.Add(time.Hour))
	put("tenant-a", "due_a", now.Add(-time.Hour))
	put("tenant-b", "due_b", now.Add(-48*time.Hour))
	// Something that is not a note at all in the same table.
	if _, err := store.PutCapture(ctx, model.CaptureIndex{ID: "c_1", UserID: "tenant-a", Status: model.StatusAppended, CreatedAt: model.Now()}); err != nil {
		t.Fatalf("PutCapture: %v", err)
	}

	// Several pages, so the scan has to follow LastEvaluatedKey.
	api.PageSize = 2
	expired, err := store.ExpiredNotes(ctx, now.Unix())
	if err != nil {
		t.Fatalf("ExpiredNotes: %v", err)
	}
	got := map[string]string{}
	for _, tn := range expired {
		got[tn.Note.ID] = tn.TenantID
		if tn.Note.Title == "" {
			t.Errorf("expired note %s came back without its attributes; the sweep's cascade needs its keys", tn.Note.ID)
		}
	}
	want := map[string]string{"due_a": "tenant-a", "due_b": "tenant-b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expired = %v, want %v", got, want)
	}
	if api.Scans < 2 {
		t.Fatalf("the scan took %d round trip(s) with a page size of 2; it is not following LastEvaluatedKey", api.Scans)
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

// ----------------------------------------------------- conditional writes

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

// TestListNotesReadsItemsWrittenBeforeAttributesWerePromoted: a note written
// before attributes were promoted is nothing but a `data` blob, so the
// projected read returns only its key, and the list has to fetch it whole
// rather than drop it. No migration is needed to make it appear — which is the
// point of ordering in Go rather than through an index that only holds items
// carrying its key attributes.
func TestListNotesReadsItemsWrittenBeforeAttributesWerePromoted(t *testing.T) {
	api := dynamofake.New()
	store := repository.NewDynamoStore(api, tableName)

	legacy := `{"id":"note_legacy","title":"Written by v1","aliases":["old"],"updated_at":"2026-08-01T00:00:00Z","s3_markdown_key":"tenants/tenant-a/notes/note_legacy/note.md"}`
	api.Put(map[string]types.AttributeValue{
		"pk":   &types.AttributeValueMemberS{Value: "USER#tenant-a"},
		"sk":   &types.AttributeValueMemberS{Value: "NOTE#note_legacy"},
		"type": &types.AttributeValueMemberS{Value: "note"},
		"data": &types.AttributeValueMemberS{Value: legacy},
	})
	seedNotes(t, store, "tenant-a", 1)

	page, err := store.ListNotes(context.Background(), "tenant-a", repository.ListOptions{})
	if err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("got %d notes, want the legacy note beside the new one", len(page.Items))
	}
	// Touched in 2026-08-01, so it sorts after the note seeded now.
	if page.Items[1].Title != "Written by v1" || page.Items[1].ID != "note_legacy" {
		t.Fatalf("legacy note = %+v, want its title recovered from the blob", page.Items[1])
	}
}

// ------------------------------------------------------ field round-trips

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
// Checking ID, Title and Version alone would let `verbatim` and `created_at` be
// dropped on every single read without a test noticing: a store that rebuilds
// the model from promoted attributes loses any field it never promoted.
// `verbatim` in particular is the flag that says "do not reword this
// dictation", so losing it silently sends content through cleanup the user
// explicitly excluded.
func TestNoteRoundTripPreservesEveryField(t *testing.T) {
	store, _ := newTestStore(t)
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
}

// A listed note has to carry every field too. The list reads a projection
// rather than the record blob, so a field missing from the projection is a
// field the list renders as empty.
func TestListedNoteCarriesEveryField(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	want := populatedNote(t)

	if _, err := store.PutNote(ctx, "tenant-a", want); err != nil {
		t.Fatalf("PutNote: %v", err)
	}
	want.Version = 1

	// populatedNote sets DeletedAt, so the note is archived. SearchText and
	// CleanedBody are the two fields a list carries only on request (32 KB
	// and 200 KB per note respectively), so the list asks for both here and
	// the plain list is checked to omit exactly those two and nothing else.
	page, err := store.ListArchivedNotes(ctx, "tenant-a", repository.ListOptions{IncludeSearchText: true, IncludeCleanedBody: true})
	if err != nil {
		t.Fatalf("ListArchivedNotes: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("archived list = %v, want the one note just written", ids(page.Items))
	}
	if diffs := noteFieldDiffs(want, page.Items[0]); len(diffs) > 0 {
		t.Fatalf("the listed note lost fields:\n\t%s", strings.Join(diffs, "\n\t"))
	}

	plain, err := store.ListArchivedNotes(ctx, "tenant-a", repository.ListOptions{})
	if err != nil {
		t.Fatalf("ListArchivedNotes (plain): %v", err)
	}
	withoutOptIns := want
	withoutOptIns.SearchText = ""
	withoutOptIns.CleanedBody = ""
	if diffs := noteFieldDiffs(withoutOptIns, plain.Items[0]); len(diffs) > 0 {
		t.Fatalf("the plain list differs from the full one in more than search_text and cleaned_body:\n\t%s", strings.Join(diffs, "\n\t"))
	}
}

// An update has to preserve what the previous read did not surface. This is the
// mechanism that made the dropped fields permanent rather than merely invisible:
// read a note, write it back, and the fields the read blanked are now blanked in
// storage as well.
func TestUpdatingANoteDoesNotErasePreviouslyStoredFields(t *testing.T) {
	store, _ := newTestStore(t)
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
}

// ------------------------------------------------------------- list order

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

// ----------------------------------------------------- opt-in projections

// search_text is up to 32 KB per note and only search and the offline corpus
// read it, so the list projects it only when asked — and never puts it in the
// record blob, which would store every byte of it twice.
func TestListNotesProjectsSearchTextOnlyWhenAsked(t *testing.T) {
	store, api := newTestStore(t)
	if _, err := store.PutNote(context.Background(), "tenant-a", model.NoteIndex{
		ID: "note_1", Title: "Roof", UpdatedAt: model.Now(), SearchText: "the gutter is leaking",
	}); err != nil {
		t.Fatalf("PutNote: %v", err)
	}

	projectionOf := func(t *testing.T) string {
		t.Helper()
		if len(api.Queries) == 0 {
			t.Fatal("the list issued no query")
		}
		return *api.Queries[len(api.Queries)-1].ProjectionExpression
	}

	api.Queries = nil
	plain, err := store.ListNotes(context.Background(), "tenant-a", repository.ListOptions{})
	if err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	if strings.Contains(projectionOf(t), "search_text") {
		t.Errorf("a list that did not ask projected search_text: %q", projectionOf(t))
	}
	if plain.Items[0].SearchText != "" {
		t.Errorf("a list that did not ask carried search text %q", plain.Items[0].SearchText)
	}

	// Asked for, the field is fetched for the PAGE by BatchGetItem, never
	// projected onto the drain of the whole partition: a paged list re-drains
	// the partition once per page, and 32 KB per note for every note to
	// serve two hundred of them was the cost that made the offline corpus's
	// pages quadratic (S5).
	api.Queries, api.BatchGets = nil, nil
	with, err := store.ListNotes(context.Background(), "tenant-a", repository.ListOptions{IncludeSearchText: true})
	if err != nil {
		t.Fatalf("ListNotes(include): %v", err)
	}
	if strings.Contains(projectionOf(t), "search_text") {
		t.Errorf("the list projected search_text onto the partition drain: %q", projectionOf(t))
	}
	if len(api.BatchGets) != 1 || !strings.Contains(*api.BatchGets[0].RequestItems[tableName].ProjectionExpression, "search_text") {
		t.Errorf("the list did not fetch search_text for the page with one BatchGetItem: %+v", api.BatchGets)
	}
	if with.Items[0].SearchText != "the gutter is leaking" {
		t.Errorf("search text = %q, want the stored value", with.Items[0].SearchText)
	}

	got, err := store.GetNote(context.Background(), "tenant-a", "note_1")
	if err != nil {
		t.Fatalf("GetNote: %v", err)
	}
	if got.SearchText != "the gutter is leaking" {
		t.Errorf("GetNote search text = %q", got.SearchText)
	}

	// The blob must not carry it: json:"-" on the model is what keeps a 32 KB
	// field from being written twice per save.
	table := tableName
	raw, err := api.GetItem(context.Background(), &dynamodb.GetItemInput{
		TableName: &table,
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: "USER#tenant-a"},
			"sk": &types.AttributeValueMemberS{Value: "NOTE#note_1"},
		},
	})
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	blob, _ := raw.Item["data"].(*types.AttributeValueMemberS)
	if blob == nil || strings.Contains(blob.Value, "gutter") {
		t.Errorf("the record blob carries the search text: %v", raw.Item["data"])
	}
	if _, ok := raw.Item["search_text"].(*types.AttributeValueMemberS); !ok {
		t.Errorf("search_text was not promoted to its own attribute: %v", raw.Item)
	}
}

// cleaned_body is up to 200 KB per note and only the export and the note
// detail read it, so it is treated exactly as search_text is: projected only
// when asked, promoted to its own attribute, and never duplicated in the blob.
func TestListNotesProjectsCleanedBodyOnlyWhenAsked(t *testing.T) {
	store, api := newTestStore(t)
	if _, err := store.PutNote(context.Background(), "tenant-a", model.NoteIndex{
		ID: "note_1", Title: "Roof", UpdatedAt: model.Now(),
		CleanedBody: "# Roof\n\nThe gutter is leaking.", CleanedMode: model.NoteCleanStructured,
		CleanedAt: model.Now(), AutoClean: true,
	}); err != nil {
		t.Fatalf("PutNote: %v", err)
	}

	projectionOf := func(t *testing.T) string {
		t.Helper()
		if len(api.Queries) == 0 {
			t.Fatal("the list issued no query")
		}
		return *api.Queries[len(api.Queries)-1].ProjectionExpression
	}

	api.Queries = nil
	plain, err := store.ListNotes(context.Background(), "tenant-a", repository.ListOptions{})
	if err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	if strings.Contains(projectionOf(t), "cleaned_body") {
		t.Errorf("a list that did not ask projected cleaned_body: %q", projectionOf(t))
	}
	if plain.Items[0].CleanedBody != "" {
		t.Errorf("a list that did not ask carried a cleaned body %q", plain.Items[0].CleanedBody)
	}
	// The fields that describe the view are small and always listed, so a
	// listed note can say it has a view without carrying it.
	if !plain.Items[0].AutoClean || plain.Items[0].CleanedMode != model.NoteCleanStructured || plain.Items[0].CleanedAt == "" {
		t.Errorf("the plain list dropped the cleaned view's metadata: %+v", plain.Items[0])
	}

	// As for search_text: fetched for the page by BatchGetItem, not
	// projected onto the drain.
	api.Queries, api.BatchGets = nil, nil
	with, err := store.ListNotes(context.Background(), "tenant-a", repository.ListOptions{IncludeCleanedBody: true})
	if err != nil {
		t.Fatalf("ListNotes(include): %v", err)
	}
	if strings.Contains(projectionOf(t), "cleaned_body") {
		t.Errorf("the list projected cleaned_body onto the partition drain: %q", projectionOf(t))
	}
	if len(api.BatchGets) != 1 || !strings.Contains(*api.BatchGets[0].RequestItems[tableName].ProjectionExpression, "cleaned_body") {
		t.Errorf("the list did not fetch cleaned_body for the page with one BatchGetItem: %+v", api.BatchGets)
	}
	if with.Items[0].CleanedBody != "# Roof\n\nThe gutter is leaking." {
		t.Errorf("cleaned body = %q, want the stored value", with.Items[0].CleanedBody)
	}

	got, err := store.GetNote(context.Background(), "tenant-a", "note_1")
	if err != nil {
		t.Fatalf("GetNote: %v", err)
	}
	if got.CleanedBody != "# Roof\n\nThe gutter is leaking." {
		t.Errorf("GetNote cleaned body = %q", got.CleanedBody)
	}

	table := tableName
	raw, err := api.GetItem(context.Background(), &dynamodb.GetItemInput{
		TableName: &table,
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: "USER#tenant-a"},
			"sk": &types.AttributeValueMemberS{Value: "NOTE#note_1"},
		},
	})
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	blob, _ := raw.Item["data"].(*types.AttributeValueMemberS)
	if blob == nil || strings.Contains(blob.Value, "gutter") {
		t.Errorf("the record blob carries the cleaned body: %v", raw.Item["data"])
	}
	if _, ok := raw.Item["cleaned_body"].(*types.AttributeValueMemberS); !ok {
		t.Errorf("cleaned_body was not promoted to its own attribute: %v", raw.Item)
	}
}
