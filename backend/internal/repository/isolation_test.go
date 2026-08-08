package repository

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/keys"
	"github.com/vppillai/chintan/backend/internal/model"
)

// These tests assert the property the v1 build shipped without: one tenant's
// identifier must never reach another tenant's data. They sit below the HTTP
// layer deliberately — the impersonation defect lived in middleware, so a test
// that goes through the router could have been fooled by the same bug.
//
// They also pin the invariant before Phase 2 rewrites the repository.

const (
	owner     = "tenant-owner"
	intruder  = "tenant-intruder"
	ownedNote = "note_owned"
	ownedCap  = "cap_owned"
)

func seededStore(t *testing.T) (*MemoryStore, context.Context) {
	t.Helper()
	store := NewMemoryStore()
	ctx := context.Background()

	if err := store.PutNote(ctx, owner, model.NoteIndex{
		ID:        ownedNote,
		Title:     "Private note",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("seed note: %v", err)
	}
	if err := store.PutCapture(ctx, model.CaptureIndex{
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
	store, ctx := seededStore(t)

	if _, err := store.GetNote(ctx, intruder, ownedNote); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetNote as intruder returned %v, want ErrNotFound", err)
	}

	notes, err := store.ListNotes(ctx, intruder)
	if err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	if len(notes) != 0 {
		t.Fatalf("intruder listed %d notes belonging to another tenant", len(notes))
	}
}

func TestIntruderCannotReadAnotherTenantsCapture(t *testing.T) {
	store, ctx := seededStore(t)

	if _, err := store.GetCapture(ctx, intruder, ownedCap); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetCapture as intruder returned %v, want ErrNotFound", err)
	}

	caps, err := store.ListCapturesByNote(ctx, intruder, ownedNote)
	if err != nil {
		t.Fatalf("ListCapturesByNote: %v", err)
	}
	if len(caps) != 0 {
		t.Fatalf("intruder listed %d captures belonging to another tenant", len(caps))
	}
}

func TestIntruderCannotDeleteAnotherTenantsData(t *testing.T) {
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
}

func TestIntruderCannotOverwriteAnotherTenantsNote(t *testing.T) {
	store, ctx := seededStore(t)

	if err := store.PutNote(ctx, intruder, model.NoteIndex{
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
}

func TestIntruderCannotReadAnotherTenantsSettings(t *testing.T) {
	store, ctx := seededStore(t)

	got, err := store.GetSettings(ctx, intruder)
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if got.CleanupMode == model.CleanupPolished {
		t.Fatal("intruder received the owner's settings instead of defaults")
	}
}

func TestIntruderCannotReadAnotherTenantsVault(t *testing.T) {
	store, ctx := seededStore(t)
	if err := store.PutRefreshVault(ctx, model.RefreshVault{
		UserID: owner, Ciphertext: []byte("sealed"),
	}); err != nil {
		t.Fatalf("seed vault: %v", err)
	}

	if _, err := store.GetRefreshVault(ctx, intruder); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetRefreshVault as intruder returned %v, want ErrNotFound", err)
	}

	// Disabling biometrics for the intruder must not clear the owner's vault.
	if err := store.DeleteRefreshVault(ctx, intruder); err != nil && !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteRefreshVault: %v", err)
	}
	if _, err := store.GetRefreshVault(ctx, owner); err != nil {
		t.Fatalf("owner's vault was cleared by another tenant: %v", err)
	}
}

func TestIntruderCannotDeleteAnotherTenantsCredentials(t *testing.T) {
	store, ctx := seededStore(t)
	if err := store.PutWebAuthnCredential(ctx, model.WebAuthnCredential{
		UserID: owner, CredentialID: "cred-1", Credential: `{"ID":"Yw=="}`,
	}); err != nil {
		t.Fatalf("seed credential: %v", err)
	}

	if err := store.DeleteAllWebAuthnCredentials(ctx, intruder); err != nil {
		t.Fatalf("DeleteAllWebAuthnCredentials: %v", err)
	}

	creds, err := store.ListWebAuthnCredentialsByUser(ctx, owner)
	if err != nil {
		t.Fatalf("ListWebAuthnCredentialsByUser: %v", err)
	}
	if len(creds) != 1 {
		t.Fatalf("owner's credentials were deleted by another tenant: have %d", len(creds))
	}
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
