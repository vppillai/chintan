package repository_test

import (
	"context"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/dynamofake"
)

// These exercise the behaviour that actually loses data: pagination, cursors,
// conditional writes, index usage, and idempotency. Asserting key formats
// (userPK("user123") == "USER#user123") covers nothing that can break. The
// cases live beside the store file they exercise (dynamo_notes_test.go,
// dynamo_captures_test.go, dynamo_misc_test.go); this file keeps the shared
// fixture and the one test that walks every entity's write path.

const tableName = "chintan-test"

func newTestStore(t *testing.T) (*repository.DynamoStore, *dynamofake.Fake) {
	t.Helper()
	api := dynamofake.New()
	return repository.NewDynamoStore(api, tableName), api
}

// ------------------------------------------------ shapes DynamoDB refuses

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
		if err := store.CompleteIdempotent(ctx, "tenant-a", "key-1", 201, "application/json", []byte(`{"id":"n1"}`)); err != nil {
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
		if err := store.CompleteIdempotent(ctx, "tenant-a", "key-2", 204, "", nil); err != nil {
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
