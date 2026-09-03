package handler_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/handler"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/upload"
)

// beginCapture is the whole of POST /v1/captures: a row and two presigned PUTs.
func TestBeginCaptureReturnsBothUploads(t *testing.T) {
	h := newHarness(t)
	note := h.createNote(t, "user1", "Target", nil)

	w := h.do(t, http.MethodPost, "/v1/captures", "user1", map[string]any{
		"content_type": "audio/webm",
		"note_id":      note.ID,
		"duration_ms":  12000,
		"size_bytes":   4096,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}

	var created handler.CaptureCreated
	decodeInto(t, w, &created)
	if created.Capture.ID == "" {
		t.Fatal("no capture id")
	}
	if created.Capture.Status != string(model.StatusUploaded) {
		t.Errorf("status = %q", created.Capture.Status)
	}
	if created.Upload.URL == "" || created.Upload.ExpiresAt == "" {
		t.Fatalf("upload = %+v", created.Upload)
	}
	if created.Upload.MaxBytes != 4096 {
		t.Errorf("max_bytes = %d, want the declared size", created.Upload.MaxBytes)
	}
	if created.PeaksUpload == nil || created.PeaksUpload.URL == "" {
		t.Fatal("no peaks upload; the client computes the envelope and needs somewhere to put it")
	}
}

// The upload headers are part of the signature. Dropping x-amz-tagging turns
// every upload into a 403 and, if S3 accepted it, would silently lose the
// retention tag the lifecycle rule matches on.
func TestUploadHeadersReachTheClientVerbatim(t *testing.T) {
	h := newHarness(t)
	h.captures.WithUploads(taggingPresigner{})

	w := h.do(t, http.MethodPost, "/v1/captures", "user1", map[string]any{"content_type": "audio/webm"})
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var created handler.CaptureCreated
	decodeInto(t, w, &created)

	tag, ok := created.Upload.Headers[upload.TaggingHeader]
	if !ok {
		t.Fatalf("the signed tagging header did not reach the client: %+v", created.Upload.Headers)
	}
	if !strings.Contains(tag, upload.ArtifactTagKey) {
		t.Fatalf("%s = %q, want the retention tag", upload.TaggingHeader, tag)
	}
	if created.Upload.Headers["Content-Type"] != "audio/webm" {
		t.Errorf("Content-Type header = %q", created.Upload.Headers["Content-Type"])
	}
}

// taggingPresigner is the tag-aware shape cmd/api wires. The in-memory fallback
// deliberately does not advertise the header, so the assertion above needs the
// real behaviour.
type taggingPresigner struct{}

func (taggingPresigner) PresignPut(_ context.Context, key, contentType string, tags map[string]string, maxBytes int64, _ time.Duration) (upload.Presigned, error) {
	headers := map[string]string{}
	if contentType != "" {
		headers["Content-Type"] = contentType
	}
	if encoded := upload.EncodeTags(tags); encoded != "" {
		headers[upload.TaggingHeader] = encoded
	}
	return upload.Presigned{
		URL:      "https://example.invalid/" + key,
		MaxBytes: maxBytes,
		Headers:  headers,
	}, nil
}

func TestBeginCaptureRejectsWhatThePipelineCannotUse(t *testing.T) {
	h := newHarness(t)
	for name, payload := range map[string]map[string]any{
		"no content type":       {},
		"unsupported container": {"content_type": "application/pdf"},
		"negative size":         {"content_type": "audio/webm", "size_bytes": -1},
	} {
		w := h.do(t, http.MethodPost, "/v1/captures", "user1", payload)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (body=%s)", name, w.Code, w.Body.String())
		}
	}

	t.Run("oversize", func(t *testing.T) {
		w := h.do(t, http.MethodPost, "/v1/captures", "user1", map[string]any{
			"content_type": "audio/webm",
			"size_bytes":   int64(1) << 40,
		})
		if w.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want 413", w.Code)
		}
	})
}

// A capped tenant is told before it uploads, not after the capture stalls at a
// status nothing explains.
func TestSpendCapIs429(t *testing.T) {
	h := newHarness(t)
	h.spend.capped = true

	w := h.do(t, http.MethodPost, "/v1/captures", "user1", map[string]any{"content_type": "audio/webm"})
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", w.Code)
	}
	problemOf(t, w)
}

func TestRetryIsAcceptedNotPerformed(t *testing.T) {
	h := newHarness(t)
	note := h.createNote(t, "user1", "Retry target", nil)

	failed := h.putCapture(t, model.CaptureIndex{
		ID: "c_failed", UserID: "user1", NoteID: note.ID,
		Status: model.StatusFailed, Error: "provider timeout", CreatedAt: model.Now(),
	})

	w := h.do(t, http.MethodPost, "/v1/captures/"+failed.ID+"/retry", "user1", nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body=%s)", w.Code, w.Body.String())
	}
	if len(h.worker.calls) == 0 {
		t.Fatal("retry did not hand the capture to the worker; the work must not happen on the request path")
	}
	var capture handler.Capture
	decodeInto(t, w, &capture)
	if capture.Error != nil {
		t.Errorf("the previous error survived the retry: %v", *capture.Error)
	}
}

func TestRetryingAFinishedCaptureIsAConflict(t *testing.T) {
	h := newHarness(t)
	note := h.createNote(t, "user1", "Done", nil)
	done := h.putCapture(t, model.CaptureIndex{
		ID: "c_done", UserID: "user1", NoteID: note.ID,
		Status: model.StatusAppended, CreatedAt: model.Now(),
	})

	w := h.do(t, http.MethodPost, "/v1/captures/"+done.ID+"/retry", "user1", nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
	problemOf(t, w)
}

// The /complete route is gone. A client still calling it must get a routing
// error, not a second append.
func TestCompleteRouteNoLongerExists(t *testing.T) {
	h := newHarness(t)
	w := h.do(t, http.MethodPost, "/v1/captures/c_1/complete", "user1", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestSetCaptureTarget(t *testing.T) {
	h := newHarness(t)

	t.Run("an existing note", func(t *testing.T) {
		note := h.createNote(t, "user1", "Chosen", nil)
		capture := h.putCapture(t, model.CaptureIndex{
			ID: "c_target1", UserID: "user1", Status: model.StatusNeedsTarget, CreatedAt: model.Now(),
		})
		w := h.do(t, http.MethodPost, "/v1/captures/"+capture.ID+"/target", "user1",
			map[string]any{"note_id": note.ID})
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
		}
		var got handler.Capture
		decodeInto(t, w, &got)
		if got.NoteID == nil || *got.NoteID != note.ID {
			t.Fatalf("note_id = %v", got.NoteID)
		}
	})

	t.Run("a brand new note", func(t *testing.T) {
		capture := h.putCapture(t, model.CaptureIndex{
			ID: "c_target2", UserID: "user1", Status: model.StatusNeedsTarget, CreatedAt: model.Now(),
		})
		w := h.do(t, http.MethodPost, "/v1/captures/"+capture.ID+"/target", "user1",
			map[string]any{"new_note_title": "Named by the user"})
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
		}
	})

	t.Run("neither is a 400", func(t *testing.T) {
		capture := h.putCapture(t, model.CaptureIndex{
			ID: "c_target3", UserID: "user1", Status: model.StatusNeedsTarget, CreatedAt: model.Now(),
		})
		w := h.do(t, http.MethodPost, "/v1/captures/"+capture.ID+"/target", "user1", map[string]any{})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", w.Code)
		}
	})

	t.Run("already targeted is a 409", func(t *testing.T) {
		note := h.createNote(t, "user1", "Already", nil)
		capture := h.putCapture(t, model.CaptureIndex{
			ID: "c_target4", UserID: "user1", NoteID: note.ID,
			Status: model.StatusCleaned, CreatedAt: model.Now(),
		})
		w := h.do(t, http.MethodPost, "/v1/captures/"+capture.ID+"/target", "user1",
			map[string]any{"note_id": note.ID})
		if w.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409", w.Code)
		}
	})
}

// segments and peaks are the two kinds v1 could not serve, and they are what
// tap-to-seek and the waveform read.
func TestDownloadServesEveryKind(t *testing.T) {
	h := newHarness(t)
	note := h.createNote(t, "user1", "Playback", nil)
	capture := h.putCapture(t, model.CaptureIndex{
		ID: "c_media", UserID: "user1", NoteID: note.ID, Status: model.StatusAppended,
		CreatedAt:   model.Now(),
		AudioKey:    "tenants/user1/captures/c_media/audio.webm",
		RawKey:      "tenants/user1/captures/c_media/raw.txt",
		CleanKey:    "tenants/user1/captures/c_media/clean.txt",
		SegmentsKey: "tenants/user1/captures/c_media/segments.json",
		PeaksKey:    "tenants/user1/captures/c_media/peaks.json",
	})

	for _, kind := range []string{"audio", "raw", "clean", "segments", "peaks"} {
		w := h.do(t, http.MethodGet, "/v1/captures/"+capture.ID+"/download?kind="+kind, "user1", nil)
		if w.Code != http.StatusOK {
			t.Fatalf("kind=%s: status = %d body = %s", kind, w.Code, w.Body.String())
		}
		var out struct {
			URL       string `json:"url"`
			ExpiresAt string `json:"expires_at"`
		}
		decodeInto(t, w, &out)
		if out.URL == "" || out.ExpiresAt == "" {
			t.Errorf("kind=%s: %+v", kind, out)
		}
	}

	t.Run("an unknown kind is 400", func(t *testing.T) {
		w := h.do(t, http.MethodGet, "/v1/captures/"+capture.ID+"/download?kind=video", "user1", nil)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", w.Code)
		}
	})

	// A capture recorded before v2 has neither artifact. The client renders a
	// plain player rather than an empty waveform.
	t.Run("a pre-v2 capture reports the artifact absent", func(t *testing.T) {
		old := h.putCapture(t, model.CaptureIndex{
			ID: "c_old", UserID: "user1", NoteID: note.ID, Status: model.StatusAppended,
			CreatedAt: model.Now(), AudioKey: "tenants/user1/captures/c_old/audio.webm",
		})
		w := h.do(t, http.MethodGet, "/v1/captures/"+old.ID+"/download?kind=segments", "user1", nil)
		if w.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", w.Code)
		}
		var got handler.Capture
		detail := h.do(t, http.MethodGet, "/v1/captures/"+old.ID, "user1", nil)
		decodeInto(t, detail, &got)
		if got.HasSegments || got.HasPeaks {
			t.Errorf("a pre-v2 capture claims artifacts it has not got: %+v", got)
		}
	})
}

// The progress card reads this list, and it has to survive a reload.
func TestListCapturesFiltersByStatus(t *testing.T) {
	h := newHarness(t)
	note := h.createNote(t, "user1", "Progress", nil)

	h.putCapture(t, model.CaptureIndex{
		ID: "c_pending", UserID: "user1", NoteID: note.ID,
		Status: model.StatusTranscribing, CreatedAt: model.Now(),
	})
	h.putCapture(t, model.CaptureIndex{
		ID: "c_broken", UserID: "user1", NoteID: note.ID,
		Status: model.StatusFailed, CreatedAt: model.Now(),
	})
	h.putCapture(t, model.CaptureIndex{
		ID: "c_asking", UserID: "user1",
		Status: model.StatusNeedsTarget, CreatedAt: model.Now(),
	})

	for filter, want := range map[string]string{
		"pending":      "c_pending",
		"failed":       "c_broken",
		"needs_target": "c_asking",
	} {
		w := h.do(t, http.MethodGet, "/v1/captures?status="+filter, "user1", nil)
		if w.Code != http.StatusOK {
			t.Fatalf("status=%s: %d body=%s", filter, w.Code, w.Body.String())
		}
		var got handler.Page[handler.Capture]
		decodeInto(t, w, &got)
		if len(got.Items) != 1 || got.Items[0].ID != want {
			t.Errorf("status=%s returned %+v, want only %s", filter, ids(got.Items), want)
		}
	}

	t.Run("all", func(t *testing.T) {
		w := h.do(t, http.MethodGet, "/v1/captures", "user1", nil)
		var got handler.Page[handler.Capture]
		decodeInto(t, w, &got)
		if len(got.Items) != 3 {
			t.Fatalf("all returned %v, want three", ids(got.Items))
		}
	})

	t.Run("an unknown filter is refused", func(t *testing.T) {
		w := h.do(t, http.MethodGet, "/v1/captures?status=whatever", "user1", nil)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", w.Code)
		}
	})
}

func ids(items []handler.Capture) []string {
	out := make([]string, 0, len(items))
	for _, c := range items {
		out = append(out, c.ID)
	}
	return out
}
