package service

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
)

type exportHarness struct {
	store    *memory.Store
	objects  *memory.Objects
	notes    *NotesService
	captures *CaptureService
	export   *ExportService
}

func newExportHarness(t *testing.T) *exportHarness {
	t.Helper()
	h := &exportHarness{store: memory.NewStore(), objects: memory.NewObjects()}
	h.notes = NewNotesService(h.store, h.objects)
	h.captures = NewCaptureService(h.store, h.objects)
	h.export = NewExportService(h.notes, h.captures, NewSettingsService(h.store), h.objects)
	return h
}

// exportedDocument reads back the payload the export actually wrote, rather
// than trusting the job record's byte count.
func (h *exportHarness) exportedDocument(t *testing.T, userID, exportID string) exportedDoc {
	t.Helper()
	key, err := exportDataKey(userID, exportID)
	if err != nil {
		t.Fatalf("exportDataKey: %v", err)
	}
	raw, err := h.objects.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("read export payload at %s: %v", key, err)
	}
	var doc exportedDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode export payload: %v (raw=%s)", err, raw)
	}
	return doc
}

// exportedDoc mirrors the on-disk shape from a reader's side, so the test
// asserts against the JSON a restore would actually see.
type exportedDoc struct {
	Version  int    `json:"version"`
	TenantID string `json:"tenant_id"`
	Notes    []struct {
		ID       string               `json:"id"`
		Title    string               `json:"title"`
		Body     string               `json:"body"`
		Captures []model.CaptureIndex `json:"captures"`
	} `json:"notes"`
	Unrouted  []model.CaptureIndex         `json:"unrouted_captures"`
	Artifacts map[string]map[string]string `json:"artifact_keys"`
}

func (d exportedDoc) noteIDs() []string {
	out := make([]string, 0, len(d.Notes))
	for _, n := range d.Notes {
		out = append(out, n.ID)
	}
	return out
}

// The export enumerates the tenant's partition and follows the keys it finds
// there. It is not a hand-written list of the kinds somebody remembered, so a
// note in a state nobody thought about — archived, or with no body object at
// all — still comes out.
func TestExportEnumeratesNotesRatherThanAnAllowlistOfKinds(t *testing.T) {
	h := newExportHarness(t)
	ctx := context.Background()

	active, err := h.notes.CreateNote(ctx, "user1", "Active note", nil)
	if err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	if _, err := h.notes.UpdateNote(ctx, "user1", active.ID, NoteUpdates{Body: strPtr("the roof leaks")}); err != nil {
		t.Fatalf("UpdateNote: %v", err)
	}

	archived, err := h.notes.CreateNote(ctx, "user1", "Archived note", nil)
	if err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	if _, err := h.notes.ArchiveNote(ctx, "user1", archived.ID); err != nil {
		t.Fatalf("ArchiveNote: %v", err)
	}

	// A note whose body object is missing: the index row is the record of
	// truth, and dropping the note because S3 lost the markdown would export a
	// corpus quietly missing a note.
	bodyless := model.NoteIndex{
		ID: "note_bodyless", Title: "No body in the bucket",
		S3MarkdownKey: "tenants/user1/notes/note_bodyless/note.md",
		S3MetaKey:     "tenants/user1/notes/note_bodyless/meta.json",
	}
	if _, err := h.store.PutNote(ctx, "user1", bodyless); err != nil {
		t.Fatalf("PutNote: %v", err)
	}

	job, err := h.export.Start(ctx, "user1")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	doc := h.exportedDocument(t, "user1", job.ID)

	for _, want := range []string{active.ID, archived.ID, "note_bodyless"} {
		if !slices.Contains(doc.noteIDs(), want) {
			t.Errorf("note %s is missing from the export %v", want, doc.noteIDs())
		}
	}
	if doc.TenantID != "user1" {
		t.Errorf("tenant_id = %q, want user1", doc.TenantID)
	}
	// Version leads the document so a future reader can branch on it before
	// parsing anything else.
	if doc.Version == 0 {
		t.Error("the export carries no version, so a future reader cannot tell which shape it is holding")
	}
	for _, n := range doc.Notes {
		if n.ID == active.ID && n.Body != "the roof leaks" {
			t.Errorf("body of %s = %q, want the markdown the note actually holds", n.ID, n.Body)
		}
		if n.ID == "note_bodyless" && n.Body != "" {
			t.Errorf("body of the bodyless note = %q, want empty rather than a failure", n.Body)
		}
	}
}

// A capture that never reached a note — a needs_target waiting on the user, or
// one that failed before routing — belongs in an export. Losing it is exactly
// the "silently falls out" failure the enumeration order exists to prevent.
func TestExportIncludesACaptureThatNeverReachedANote(t *testing.T) {
	h := newExportHarness(t)
	ctx := context.Background()

	if _, err := h.store.PutCapture(ctx, model.CaptureIndex{
		ID: "c_orphan", UserID: "user1", NoteID: "", Status: model.StatusNeedsTarget,
		AudioKey: "tenants/user1/captures/c_orphan/audio.webm",
		RawKey:   "tenants/user1/captures/c_orphan/raw.txt",
	}); err != nil {
		t.Fatalf("PutCapture: %v", err)
	}

	job, err := h.export.Start(ctx, "user1")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	doc := h.exportedDocument(t, "user1", job.ID)

	if len(doc.Unrouted) != 1 || doc.Unrouted[0].ID != "c_orphan" {
		t.Fatalf("unrouted captures = %+v, want the one capture with no destination note", doc.Unrouted)
	}
	// The JSON deliberately does not inline audio, so it has to say where the
	// audio is or a restore cannot find it.
	keys, ok := doc.Artifacts["c_orphan"]
	if !ok {
		t.Fatalf("no artifact keys recorded for c_orphan; artifact_keys = %+v", doc.Artifacts)
	}
	if keys["audio"] != "tenants/user1/captures/c_orphan/audio.webm" {
		t.Errorf("audio key = %q, want the object the restore has to fetch", keys["audio"])
	}
}

func TestExportCarriesANotesOwnCaptures(t *testing.T) {
	h := newExportHarness(t)
	ctx := context.Background()

	note, err := h.notes.CreateNote(ctx, "user1", "Roof", nil)
	if err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	if _, err := h.store.PutCapture(ctx, model.CaptureIndex{
		ID: "c_routed", UserID: "user1", NoteID: note.ID, Status: model.StatusAppended,
		CleanKey: "tenants/user1/captures/c_routed/clean.txt",
	}); err != nil {
		t.Fatalf("PutCapture: %v", err)
	}

	job, err := h.export.Start(ctx, "user1")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	doc := h.exportedDocument(t, "user1", job.ID)

	if len(doc.Notes) != 1 {
		t.Fatalf("notes = %v, want one", doc.noteIDs())
	}
	if len(doc.Notes[0].Captures) != 1 || doc.Notes[0].Captures[0].ID != "c_routed" {
		t.Fatalf("captures on %s = %+v, want c_routed", note.ID, doc.Notes[0].Captures)
	}
	if doc.Artifacts["c_routed"]["clean"] != "tenants/user1/captures/c_routed/clean.txt" {
		t.Errorf("clean key = %q, want the routed capture's cleaned transcript key", doc.Artifacts["c_routed"]["clean"])
	}
}

// The URL and its expiry are minted per read and never stored: a persisted
// presigned URL outlives the request that was authorised to hold it, and the
// job record is an object anybody who can read the prefix can fetch.
func TestStartDoesNotPersistThePresignedURLItReturns(t *testing.T) {
	h := newExportHarness(t)
	ctx := context.Background()

	job, err := h.export.Start(ctx, "user1")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if job.URL == "" || job.ExpiresAt == "" {
		t.Fatalf("Start returned job %+v, want a usable download URL and expiry", job)
	}
	if job.Status != ExportReady {
		t.Fatalf("status = %q, want %q: the work is done inline", job.Status, ExportReady)
	}

	key, err := exportJobKey("user1", job.ID)
	if err != nil {
		t.Fatalf("exportJobKey: %v", err)
	}
	raw, err := h.objects.Get(ctx, key)
	if err != nil {
		t.Fatalf("read stored job at %s: %v", key, err)
	}
	if strings.Contains(string(raw), job.URL) {
		t.Fatalf("the stored job record holds the presigned URL: %s", raw)
	}

	var stored ExportJob
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatalf("decode stored job: %v", err)
	}
	if stored.URL != "" || stored.ExpiresAt != "" {
		t.Fatalf("stored job = %+v, want no URL and no expiry persisted", stored)
	}
	if stored.ID != job.ID || stored.Status != job.Status || stored.Bytes != job.Bytes {
		t.Fatalf("stored job = %+v, want the id, status and size of %+v", stored, job)
	}
}

func TestGetMintsAFreshURLForAReadyExport(t *testing.T) {
	h := newExportHarness(t)
	ctx := context.Background()

	started, err := h.export.Start(ctx, "user1")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	got, err := h.export.Get(ctx, "user1", started.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != started.ID {
		t.Fatalf("id = %q, want %q", got.ID, started.ID)
	}
	if got.URL == "" || got.ExpiresAt == "" {
		t.Fatalf("Get returned %+v, want a freshly minted URL and expiry", got)
	}
	if got.Bytes != started.Bytes {
		t.Errorf("bytes = %d, want the stored %d", got.Bytes, started.Bytes)
	}
}

func TestGetOnAnExportThatWasNeverIssuedIsNotFound(t *testing.T) {
	h := newExportHarness(t)

	if _, err := h.export.Get(context.Background(), "user1", "e_0123456789abcdef"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("err = %v, want repository.ErrNotFound for an id nothing was ever written under", err)
	}
}

// The id becomes part of an object key, so it is validated rather than trusted.
// An unvalidated id in a key is a path traversal into another tenant's prefix.
func TestGetRefusesAnExportIDThatCouldNotHaveBeenIssuedHere(t *testing.T) {
	h := newExportHarness(t)
	ctx := context.Background()

	for _, id := range []string{
		"../../tenants/user2/exports/e_1",
		"e_1/../../../etc/passwd",
		"e 1",
		"",
		strings.Repeat("e", 65),
	} {
		got, err := h.export.Get(ctx, "user1", id)
		if !errors.Is(err, ErrInvalidExportID) {
			t.Errorf("Get(%q) err = %v, want ErrInvalidExportID", id, err)
		}
		// It is also a not-found, so the handler answers 404 rather than
		// telling a prober that the id shape was interesting.
		if !errors.Is(err, repository.ErrNotFound) {
			t.Errorf("Get(%q) err = %v, want it to also be a repository.ErrNotFound", id, err)
		}
		if got != (ExportJob{}) {
			t.Errorf("Get(%q) = %+v alongside the rejection, want the zero job", id, got)
		}
	}
}

func TestExportKeyRefusesATenantIDThatWouldEscapeItsPrefix(t *testing.T) {
	if _, err := exportKey("../user2", "e_1", "export.json"); err == nil {
		t.Fatal("exportKey accepted a tenant id containing a traversal")
	}
	if _, err := exportKey("user1", "../e_1", "export.json"); !errors.Is(err, ErrInvalidExportID) {
		t.Fatal("exportKey accepted an export id containing a traversal")
	}

	got, err := exportKey("user1", "e_1", "export.json")
	if err != nil {
		t.Fatalf("exportKey: %v", err)
	}
	if got != "tenants/user1/exports/e_1/export.json" {
		t.Fatalf("key = %q, want it under the tenant's own prefix", got)
	}
}

func TestExportSurfacesAFailureToReadANoteBody(t *testing.T) {
	store := memory.NewStore()
	objects := memory.NewObjects()
	boom := errors.New("s3: AccessDenied on chintan-content-bucket")
	notes := NewNotesService(store, objects)
	ctx := context.Background()

	if _, err := notes.CreateNote(ctx, "user1", "Roof", nil); err != nil {
		t.Fatalf("CreateNote: %v", err)
	}

	broken := errGetObjects{Objects: objects, err: boom}
	svc := NewExportService(notes, NewCaptureService(store, objects), NewSettingsService(store), broken)

	if _, err := svc.Start(ctx, "user1"); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the object read failure: an export that silently drops a body is worse than one that fails", err)
	}
}

func strPtr(s string) *string { return &s }
