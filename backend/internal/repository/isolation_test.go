package repository_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/keys"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
)

// These tests assert tenant isolation: one tenant's identifier must never reach
// another tenant's data. They sit below the HTTP layer deliberately — an
// impersonation defect in middleware would fool a test that goes through the
// router, because the same bug would grant the access the test checks.
//
// They run against every Store implementation rather than only the in-memory
// double — the property has to hold in the code that actually talks to
// DynamoDB.

const (
	owner     = "tenant-owner"
	intruder  = "tenant-intruder"
	ownedNote = "note_owned"
	ownedCap  = "cap_owned"
)

// storeFactories is every implementation the isolation property must hold for.
var storeFactories = map[string]func() repository.Store{
	"memory": func() repository.Store { return memory.NewStore() },
	"dynamo": func() repository.Store { return repository.NewDynamoStore(newFakeDynamo(), "chintan-isolation") },
}

// eachStore runs fn against every Store implementation.
func eachStore(t *testing.T, fn func(t *testing.T)) {
	t.Helper()
	for name, factory := range storeFactories {
		newStore = factory
		t.Run(name, fn)
	}
}

// newStore is the factory the current subtest is exercising.
var newStore = func() repository.Store { return memory.NewStore() }

func seededStore(t *testing.T) (repository.Store, context.Context) {
	t.Helper()
	store := newStore()
	ctx := context.Background()

	if _, err := store.PutNote(ctx, owner, model.NoteIndex{
		ID:        ownedNote,
		Title:     "Private note",
		UpdatedAt: model.Now(),
	}); err != nil {
		t.Fatalf("seed note: %v", err)
	}
	if _, err := store.PutCapture(ctx, model.CaptureIndex{
		ID:     ownedCap,
		UserID: owner,
		NoteID: ownedNote,
		Status: model.StatusAppended,
	}); err != nil {
		t.Fatalf("seed capture: %v", err)
	}
	if err := store.PutSettings(ctx, owner, model.Settings{CleanupMode: model.CleanupPolished}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	return store, ctx
}

func TestIntruderCannotReadAnotherTenantsNote(t *testing.T) {
	eachStore(t, func(t *testing.T) {
		store, ctx := seededStore(t)

		if _, err := store.GetNote(ctx, intruder, ownedNote); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("GetNote as intruder returned %v, want ErrNotFound", err)
		}

		notes, err := store.ListNotes(ctx, intruder, repository.ListOptions{})
		if err != nil {
			t.Fatalf("ListNotes: %v", err)
		}
		if len(notes.Items) != 0 {
			t.Fatalf("intruder listed %d notes belonging to another tenant", len(notes.Items))
		}
	})
}

func TestIntruderCannotReadAnotherTenantsCapture(t *testing.T) {
	eachStore(t, func(t *testing.T) {
		store, ctx := seededStore(t)

		if _, err := store.GetCapture(ctx, intruder, ownedCap); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("GetCapture as intruder returned %v, want ErrNotFound", err)
		}

		caps, err := store.ListCapturesByNote(ctx, intruder, ownedNote, repository.ListOptions{})
		if err != nil {
			t.Fatalf("ListCapturesByNote: %v", err)
		}
		if len(caps.Items) != 0 {
			t.Fatalf("intruder listed %d captures belonging to another tenant", len(caps.Items))
		}
	})
}

func TestIntruderCannotDeleteAnotherTenantsData(t *testing.T) {
	eachStore(t, func(t *testing.T) {
		store, ctx := seededStore(t)

		// Delete may report success or not-found; what must not happen is the
		// owner's data disappearing.
		_ = store.DeleteNote(ctx, intruder, ownedNote)
		_ = store.DeleteCapture(ctx, intruder, ownedCap)

		if _, err := store.GetNote(ctx, owner, ownedNote); err != nil {
			t.Fatalf("owner's note was destroyed by another tenant's delete: %v", err)
		}
		if _, err := store.GetCapture(ctx, owner, ownedCap); err != nil {
			t.Fatalf("owner's capture was destroyed by another tenant's delete: %v", err)
		}
	})
}

func TestIntruderCannotOverwriteAnotherTenantsNote(t *testing.T) {
	eachStore(t, func(t *testing.T) {
		store, ctx := seededStore(t)

		if _, err := store.PutNote(ctx, intruder, model.NoteIndex{
			ID:    ownedNote, // same id, different tenant
			Title: "Overwritten",
		}); err != nil {
			t.Fatalf("PutNote: %v", err)
		}

		got, err := store.GetNote(ctx, owner, ownedNote)
		if err != nil {
			t.Fatalf("GetNote as owner: %v", err)
		}
		if got.Title != "Private note" {
			t.Fatalf("another tenant overwrote the owner's note: title=%q", got.Title)
		}
	})
}

func TestIntruderCannotReadAnotherTenantsSettings(t *testing.T) {
	eachStore(t, func(t *testing.T) {
		store, ctx := seededStore(t)

		got, err := store.GetSettings(ctx, intruder)
		if err != nil {
			t.Fatalf("GetSettings: %v", err)
		}
		if got.CleanupMode == model.CleanupPolished {
			t.Fatal("intruder received the owner's settings instead of defaults")
		}
	})
}

// Object keys are the other half of isolation: even a correct store is
// worthless if two tenants can be made to share an S3 prefix.
func TestObjectKeysAreTenantScoped(t *testing.T) {
	ownerKey, err := keys.NoteMarkdown(owner, ownedNote)
	if err != nil {
		t.Fatalf("NoteMarkdown(owner): %v", err)
	}
	intruderKey, err := keys.NoteMarkdown(intruder, ownedNote)
	if err != nil {
		t.Fatalf("NoteMarkdown(intruder): %v", err)
	}
	if ownerKey == intruderKey {
		t.Fatalf("two tenants derived the same object key: %q", ownerKey)
	}
	if !strings.Contains(ownerKey, owner) {
		t.Fatalf("key %q is not scoped to its tenant", ownerKey)
	}
}

// A tenant id that escapes its path segment would let one tenant address
// another's prefix. keys must reject rather than sanitise.
func TestObjectKeysRejectTraversalInTenantID(t *testing.T) {
	for _, bad := range []string{"../other", "a/b", "a#b", "", "a b"} {
		if _, err := keys.NoteMarkdown(bad, ownedNote); err == nil {
			t.Fatalf("tenant id %q was accepted into an object key", bad)
		}
	}
}
