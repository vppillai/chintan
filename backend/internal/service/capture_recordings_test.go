package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

func TestRecordingURLsListsEveryRecordingWithAudioOldestFirst(t *testing.T) {
	h := newEditHarness(t)
	note := h.note("u1", "Kitchen rebuild: phase 2!", "")
	h.appended("u1", note.ID, "c_new", "2026-03-04T15:06:00.000000000Z")
	h.appended("u1", note.ID, "c_old", "2026-03-04T09:30:00.000000000Z")
	// Same minute as c_old: the archive has to hold both.
	h.appended("u1", note.ID, "c_twin", "2026-03-04T09:30:45.000000000Z")
	// A capture whose audio the retention rule has expired.
	expired := h.appended("u1", note.ID, "c_expired", "2026-03-04T12:00:00.000000000Z")
	if err := h.objects.Delete(h.ctx, expired.AudioKey); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// A row with no audio key at all.
	if _, err := h.store.PutCapture(h.ctx, model.CaptureIndex{
		ID: "c_noaudio", UserID: "u1", NoteID: note.ID, Status: model.StatusFailed, CreatedAt: "2026-03-04T13:00:00.000000000Z",
	}); err != nil {
		t.Fatalf("PutCapture: %v", err)
	}

	before := time.Now()
	urls, err := h.captures.RecordingURLs(h.ctx, "u1", note.ID)
	if err != nil {
		t.Fatalf("RecordingURLs: %v", err)
	}

	var ids, names []string
	for _, u := range urls {
		ids = append(ids, u.CaptureID)
		names = append(names, u.Filename)
		if u.URL == "" {
			t.Errorf("%s: no URL", u.CaptureID)
		}
		if ttl := u.ExpiresAt.Sub(before); ttl < DownloadTTL-time.Minute || ttl > DownloadTTL+time.Minute {
			t.Errorf("%s: expires in %s, want about %s", u.CaptureID, ttl, DownloadTTL)
		}
	}
	if got := strings.Join(ids, ","); got != "c_old,c_twin,c_new" {
		t.Errorf("order = %s, want oldest first without the expired or audio-less rows", got)
	}
	want := "kitchen-rebuild-phase-2-20260304-0930.webm,kitchen-rebuild-phase-2-20260304-0930-2.webm,kitchen-rebuild-phase-2-20260304-1506.webm"
	if got := strings.Join(names, ","); got != want {
		t.Errorf("filenames = %s\nwant        %s", got, want)
	}
}

func TestRecordingURLsIsCappedAndScoped(t *testing.T) {
	h := newEditHarness(t)
	note := h.note("u1", "Long", "")
	for i := 0; i < MaxRecordingURLs+5; i++ {
		h.appended("u1", note.ID, fmt.Sprintf("c_%03d", i), fmt.Sprintf("2026-01-01T00:%02d:%02d.000000000Z", i/60, i%60))
	}
	urls, err := h.captures.RecordingURLs(h.ctx, "u1", note.ID)
	if err != nil {
		t.Fatalf("RecordingURLs: %v", err)
	}
	if len(urls) != MaxRecordingURLs {
		t.Errorf("got %d urls, want the cap of %d", len(urls), MaxRecordingURLs)
	}

	if _, err := h.captures.RecordingURLs(h.ctx, "intruder", note.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("another tenant's manifest = %v, want ErrNotFound", err)
	}
	if _, err := h.captures.RecordingURLs(h.ctx, "u1", "note_missing"); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("missing note = %v, want ErrNotFound", err)
	}
}

func TestRecordingURLsReportsAnObjectStoreFault(t *testing.T) {
	h := newEditHarness(t)
	note := h.note("u1", "Faulty", "")
	h.appended("u1", note.ID, "c_1", "2026-01-01T00:00:00.000000000Z")
	broken := NewCaptureService(h.store, failingExists{Objects: h.objects})
	if _, err := broken.RecordingURLs(h.ctx, "u1", note.ID); err == nil {
		t.Fatal("a manifest built over a failing object store reported success")
	}
}

func TestFilenameSlug(t *testing.T) {
	cases := map[string]string{
		"Kitchen rebuild":       "kitchen-rebuild",
		"  Quotes: in / out!  ": "quotes-in-out",
		"":                      "recording",
		"---":                   "recording",
		"വീട് പണി":              "വീട്-പണി",
		"Mixed CASE 2026":       "mixed-case-2026",
		strings.Repeat("a", 80): strings.Repeat("a", maxFilenameSlugRunes),
	}
	for in, want := range cases {
		if got := filenameSlug(in); got != want {
			t.Errorf("filenameSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

type failingExists struct{ repository.Objects }

func (failingExists) Exists(context.Context, string) (bool, error) {
	return false, errors.New("s3: 503 SlowDown")
}
