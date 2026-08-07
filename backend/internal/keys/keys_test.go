package keys_test

import (
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/keys"
)

func TestNotePathsAreUserScopedAndNavigable(t *testing.T) {
	md, err := keys.NoteMarkdown("u1", "n1")
	if err != nil {
		t.Fatal(err)
	}
	meta, err := keys.NoteMeta("u1", "n1")
	if err != nil {
		t.Fatal(err)
	}
	if md != "tenants/u1/notes/n1/note.md" {
		t.Fatalf("got %q", md)
	}
	if meta != "tenants/u1/notes/n1/meta.json" {
		t.Fatalf("got %q", meta)
	}
}

func TestCapturePaths(t *testing.T) {
	audio, err := keys.CaptureAudio("u1", "c1", "webm")
	if err != nil {
		t.Fatal(err)
	}
	if audio != "tenants/u1/captures/c1/audio.webm" {
		t.Fatalf("got %q", audio)
	}
	raw, err := keys.CaptureRaw("u1", "c1")
	if err != nil {
		t.Fatal(err)
	}
	if raw != "tenants/u1/captures/c1/raw.txt" {
		t.Fatalf("got %q", raw)
	}
	clean, err := keys.CaptureClean("u1", "c1")
	if err != nil {
		t.Fatal(err)
	}
	if clean != "tenants/u1/captures/c1/clean.txt" {
		t.Fatalf("got %q", clean)
	}
	meta, err := keys.CaptureMeta("u1", "c1")
	if err != nil {
		t.Fatal(err)
	}
	if meta != "tenants/u1/captures/c1/meta.json" {
		t.Fatalf("got %q", meta)
	}
}

func TestRejectsEmptyOrSlashIDs(t *testing.T) {
	_, err := keys.NoteMarkdown("../x", "n1")
	if err == nil {
		t.Fatal("expected error on bad userID")
	}
	_, err = keys.NoteMarkdown("u1", "")
	if err == nil {
		t.Fatal("expected error on empty noteID")
	}
	_, err = keys.NoteMarkdown("u1", "n/1")
	if err == nil {
		t.Fatal("expected error on slash in noteID")
	}
}

func TestNoLeadingSlash(t *testing.T) {
	p, err := keys.NoteMarkdown("u", "n")
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(p, "/") {
		t.Fatal(p)
	}
}
