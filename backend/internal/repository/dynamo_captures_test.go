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
	"github.com/vppillai/chintan/backend/internal/repository/dynamofake"
)

// ----------------------------------------------------- conditional writes

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

// ------------------------------------------------------------------- GSI1

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

	api.Queries = nil
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

	q := api.Queries[0]
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

	api.Gets = 0
	page, err := store.ListCapturesByNote(ctx, "tenant-a", "n1", repository.ListOptions{})
	if err != nil {
		t.Fatalf("ListCapturesByNote: %v", err)
	}
	if len(page.Items) != 5 {
		t.Fatalf("got %d captures, want 5", len(page.Items))
	}
	if api.Gets != 0 {
		t.Fatalf("list issued %d GetItem calls; the index projection should answer without them", api.Gets)
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
	api := dynamofake.New()
	store := repository.NewDynamoStore(api, tableName)

	legacy := `{"id":"c_legacy","note_id":"n1","user_id":"tenant-a","status":"appended",` +
		`"audio_key":"tenants/tenant-a/captures/c_legacy/audio.webm","created_at":"2026-08-01T00:00:00Z"}`
	api.Put(map[string]types.AttributeValue{
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

// The store and the CloudFormation template have to agree about GSI1, and
// nothing else checks it: a projection that is missing an attribute does not
// error at runtime, it returns an empty field. Widening it later is not an
// update — the index is deleted and rebuilt.
func TestGSI1ProjectionCoversWhatTheCaptureListReads(t *testing.T) {
	projected := dynamofake.IndexNonKeyAttributes("gsi1")
	if len(projected) == 0 {
		t.Fatalf("could not read the gsi1 projection from %s; the drift check is not running", dynamofake.TemplatePath)
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

// ----------------------------------------------------------- append guard

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

// ------------------------------------------------------------- list order

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

// ----------------------------------------------------------------- status

func TestUpdateCaptureStatus(t *testing.T) {
	cases := []struct {
		name    string
		seed    bool
		status  model.CaptureStatus
		errMsg  string
		wantErr error
	}{
		{name: "writes the status and the error under a version bump", seed: true, status: model.StatusFailed, errMsg: "the speech provider is unavailable"},
		{name: "clears an earlier error when the status moves on", seed: true, status: model.StatusAppended},
		{name: "a missing capture is ErrNotFound", status: model.StatusFailed, wantErr: repository.ErrNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, _ := newTestStore(t)
			ctx := context.Background()
			if tc.seed {
				if _, err := store.PutCapture(ctx, model.CaptureIndex{ID: "cap1", UserID: "tenant-a", Status: model.StatusCleaning, Error: "an earlier fault"}); err != nil {
					t.Fatalf("seed: %v", err)
				}
			}

			err := store.UpdateCaptureStatus(ctx, "tenant-a", "cap1", tc.status, tc.errMsg)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("UpdateCaptureStatus err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr != nil {
				return
			}
			got, err := store.GetCapture(ctx, "tenant-a", "cap1")
			if err != nil {
				t.Fatalf("GetCapture: %v", err)
			}
			if got.Status != tc.status || got.Error != tc.errMsg {
				t.Fatalf("stored (status %q, error %q), want (%q, %q)", got.Status, got.Error, tc.status, tc.errMsg)
			}
			if got.Version != 2 {
				t.Fatalf("version = %d after seed and update, want 2: the write must go through the versioned PutCapture so a concurrent writer is not overwritten", got.Version)
			}
		})
	}
}

// A conflicting write between the read and the write is the one thing a
// read-modify-write can get wrong, so it is pinned here: the version check
// refuses, and nothing of the stale copy lands.
func TestUpdateCaptureStatusLosesToAConcurrentWriter(t *testing.T) {
	api := dynamofake.New()
	racer := &captureRacingDynamo{Fake: api}
	store := repository.NewDynamoStore(racer, tableName)
	racer.store = store
	ctx := context.Background()
	if _, err := store.PutCapture(ctx, model.CaptureIndex{ID: "cap1", UserID: "tenant-a", Status: model.StatusCleaning}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	racer.armed = true
	err := store.UpdateCaptureStatus(ctx, "tenant-a", "cap1", model.StatusFailed, "late")
	if !errors.Is(err, repository.ErrVersionConflict) {
		t.Fatalf("err = %v, want ErrVersionConflict", err)
	}
	got, err := store.GetCapture(ctx, "tenant-a", "cap1")
	if err != nil {
		t.Fatalf("GetCapture: %v", err)
	}
	if got.Status != model.StatusAppended || got.Error != "" {
		t.Fatalf("stored (status %q, error %q); the concurrent writer's appended row was overwritten", got.Status, got.Error)
	}
}

// captureRacingDynamo slips another writer's PutCapture in between the
// store's GetItem and its conditional PutItem, once armed.
type captureRacingDynamo struct {
	*dynamofake.Fake
	store *repository.DynamoStore
	armed bool
}

func (d *captureRacingDynamo) GetItem(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	out, err := d.Fake.GetItem(ctx, in, opts...)
	if err != nil || !d.armed {
		return out, err
	}
	d.armed = false
	other, err := d.store.GetCapture(ctx, "tenant-a", "cap1")
	if err != nil {
		return nil, err
	}
	other.Status = model.StatusAppended
	if _, err := d.store.PutCapture(ctx, other); err != nil {
		return nil, err
	}
	return out, nil
}
