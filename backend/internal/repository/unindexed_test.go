package repository_test

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// legacyCaptureItem is a capture row as the August 2026 code wrote it: the key,
// the type and the record blob, nothing promoted — and so no gsi1pk, which is
// what keeps it out of GSI1 entirely.
func legacyCaptureItem(tenantID, captureID, noteID string) map[string]types.AttributeValue {
	blob := `{"id":"` + captureID + `","note_id":"` + noteID + `","user_id":"` + tenantID + `","status":"appended",` +
		`"audio_key":"tenants/` + tenantID + `/captures/` + captureID + `/audio.webm","created_at":"2026-08-07T09:00:00Z"}`
	return map[string]types.AttributeValue{
		"pk":   &types.AttributeValueMemberS{Value: "USER#" + tenantID},
		"sk":   &types.AttributeValueMemberS{Value: "CAPTURE#" + captureID},
		"type": &types.AttributeValueMemberS{Value: "capture"},
		"data": &types.AttributeValueMemberS{Value: blob},
	}
}

// TestListUnindexedCapturesFindsWhatTheNoteIndexCannot is the production
// defect: a note's captures were listed through GSI1, a legacy row is not in
// GSI1, so a purge that trusted the index left the row and its audio behind.
func TestListUnindexedCapturesFindsWhatTheNoteIndexCannot(t *testing.T) {
	store, api := newTestStore(t)
	ctx := context.Background()

	api.put(legacyCaptureItem("tenant-a", "c_legacy", "n1"))
	if _, err := store.PutCapture(ctx, model.CaptureIndex{
		ID: "c_modern", UserID: "tenant-a", NoteID: "n1", Status: model.StatusAppended,
		CreatedAt: "2026-09-01T00:00:00.000000000Z",
	}); err != nil {
		t.Fatalf("PutCapture: %v", err)
	}
	// Another tenant's legacy row must not be found under this tenant.
	api.put(legacyCaptureItem("tenant-b", "c_other", "n9"))

	byNote, err := store.ListCapturesByNote(ctx, "tenant-a", "n1", repository.ListOptions{})
	if err != nil {
		t.Fatalf("ListCapturesByNote: %v", err)
	}
	if len(byNote.Items) != 1 || byNote.Items[0].ID != "c_modern" {
		t.Fatalf("the index answered %+v; a legacy row must be invisible to it, or this test proves nothing", byNote.Items)
	}

	unindexed, err := store.ListUnindexedCaptures(ctx, "tenant-a")
	if err != nil {
		t.Fatalf("ListUnindexedCaptures: %v", err)
	}
	if len(unindexed) != 1 {
		t.Fatalf("unindexed = %+v, want exactly the legacy row", unindexed)
	}
	got := unindexed[0]
	if got.ID != "c_legacy" || got.NoteID != "n1" || got.UserID != "tenant-a" {
		t.Errorf("identity = %+v", got)
	}
	if got.AudioKey != "tenants/tenant-a/captures/c_legacy/audio.webm" {
		t.Errorf("audio key = %q, want it recovered from the blob so the cascade can unlink it", got.AudioKey)
	}
}

// The read follows LastEvaluatedKey: a tenant with many captures and a few
// legacy rows among them must not lose the ones past the first page.
func TestListUnindexedCapturesFollowsEveryPage(t *testing.T) {
	store, api := newTestStore(t)
	api.pageSize = 3
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if _, err := store.PutCapture(ctx, model.CaptureIndex{
			ID: "c_modern_" + string(rune('a'+i)), UserID: "tenant-a", NoteID: "n1",
			Status: model.StatusAppended, CreatedAt: "2026-09-01T00:00:00.000000000Z",
		}); err != nil {
			t.Fatalf("PutCapture: %v", err)
		}
	}
	api.put(legacyCaptureItem("tenant-a", "c_a_legacy", "n1"))
	api.put(legacyCaptureItem("tenant-a", "c_z_legacy", "n1"))

	unindexed, err := store.ListUnindexedCaptures(ctx, "tenant-a")
	if err != nil {
		t.Fatalf("ListUnindexedCaptures: %v", err)
	}
	if len(unindexed) != 2 {
		t.Fatalf("unindexed = %d rows, want both legacy rows across the pages", len(unindexed))
	}
}

func TestNoteExistsAnswersWithoutReadingTheNote(t *testing.T) {
	store, api := newTestStore(t)
	ctx := context.Background()
	if _, err := store.PutNote(ctx, "tenant-a", model.NoteIndex{ID: "n1", Title: "Roof"}); err != nil {
		t.Fatalf("PutNote: %v", err)
	}

	cases := []struct {
		tenant, note string
		want         bool
	}{
		{"tenant-a", "n1", true},
		{"tenant-a", "n2", false},
		// The row is another tenant's; it does not exist for this one.
		{"tenant-b", "n1", false},
	}
	for _, tc := range cases {
		got, err := store.NoteExists(ctx, tc.tenant, tc.note)
		if err != nil {
			t.Fatalf("NoteExists(%s, %s): %v", tc.tenant, tc.note, err)
		}
		if got != tc.want {
			t.Errorf("NoteExists(%s, %s) = %v, want %v", tc.tenant, tc.note, got, tc.want)
		}
	}
	if api.gets != len(cases) {
		t.Errorf("gets = %d, want one GetItem per question", api.gets)
	}
}
