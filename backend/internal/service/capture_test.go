package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
	"github.com/vppillai/chintan/backend/internal/upload"
)

// recordingPresigner captures what the service asked to be signed, which is
// where the retention tag either is or is not.
type recordingPresigner struct {
	calls []presignCall
}

type presignCall struct {
	key         string
	contentType string
	tags        map[string]string
	maxBytes    int64
}

func (p *recordingPresigner) PresignPut(_ context.Context, key, contentType string, tags map[string]string, maxBytes int64, ttl time.Duration) (upload.Presigned, error) {
	p.calls = append(p.calls, presignCall{key: key, contentType: contentType, tags: tags, maxBytes: maxBytes})
	headers := map[string]string{"Content-Type": contentType}
	if tagging := upload.EncodeTags(tags); tagging != "" {
		headers[upload.TaggingHeader] = tagging
	}
	return upload.Presigned{
		URL:       "https://example.invalid/" + key,
		ExpiresAt: time.Now().Add(ttl),
		MaxBytes:  maxBytes,
		Headers:   headers,
	}, nil
}

type stubQueue struct {
	calls []string
}

func (q *stubQueue) EnqueueCapture(_ context.Context, tenantID, captureID, reason string) error {
	q.calls = append(q.calls, tenantID+"/"+captureID+"/"+reason)
	return nil
}

func TestCaptureService_BeginCapture(t *testing.T) {
	store := memory.NewStore()
	objects := memory.NewObjects()
	svc := NewCaptureService(store, objects)

	note := model.NoteIndex{ID: "note1", Title: "Test Note"}
	if _, err := store.PutNote(context.Background(), "user1", note); err != nil {
		t.Fatalf("PutNote: %v", err)
	}

	ctx := context.Background()
	created, err := svc.BeginCapture(ctx, "user1", CaptureRequest{NoteID: "note1", ContentType: "audio/wav"})
	if err != nil {
		t.Fatalf("BeginCapture failed: %v", err)
	}
	capture, uploadURL := created.Capture, created.Audio.URL
	if capture.ID == "" {
		t.Error("Expected capture ID to be set")
	}
	if capture.Status != model.StatusUploaded {
		t.Errorf("Expected status %v, got %v", model.StatusUploaded, capture.Status)
	}
	if capture.UserID != "user1" {
		t.Errorf("Expected UserID user1, got %v", capture.UserID)
	}
	if capture.NoteID != "note1" {
		t.Errorf("Expected NoteID note1, got %v", capture.NoteID)
	}
	if uploadURL == "" {
		t.Error("Expected upload URL to be set")
	}
}

// The presigned audio PUT must carry the retention tag. In v1 RetentionDays was
// stored, returned, shown in the UI, and read by nothing; an S3 lifecycle filter
// takes one prefix and one suffix with no wildcards, so tenants/*/captures/
// cannot be expressed and the rule matches this tag instead.
func TestBeginCaptureRequiresTheRetentionTagOnTheAudioUpload(t *testing.T) {
	presigner := &recordingPresigner{}
	svc := NewCaptureService(memory.NewStore(), memory.NewObjects()).WithUploads(presigner)

	created, err := svc.BeginCapture(context.Background(), "user1", CaptureRequest{
		ContentType: "audio/webm",
		SizeBytes:   4 << 20,
	})
	if err != nil {
		t.Fatalf("BeginCapture: %v", err)
	}

	if len(presigner.calls) != 2 {
		t.Fatalf("presigned %d uploads, want audio and peaks", len(presigner.calls))
	}
	audio := presigner.calls[0]
	if got := audio.tags[upload.ArtifactTagKey]; got != upload.ArtifactCaptureAudio {
		t.Fatalf("audio presign tags = %v, want %s=%s",
			audio.tags, upload.ArtifactTagKey, upload.ArtifactCaptureAudio)
	}
	if audio.maxBytes != 4<<20 {
		t.Errorf("audio presign max bytes = %d, want the declared size", audio.maxBytes)
	}
	if got := created.Audio.Headers[upload.TaggingHeader]; got != "chintan-artifact=capture-audio" {
		t.Fatalf("audio upload headers = %v, want the tagging header the client must send", created.Audio.Headers)
	}

	// The peaks object is the client's waveform envelope, not source audio, and
	// must not be expired with it.
	peaks := presigner.calls[1]
	if _, tagged := peaks.tags[upload.ArtifactTagKey]; tagged {
		t.Errorf("peaks presign carries the capture-audio retention tag: %v", peaks.tags)
	}
	if created.Peaks.URL == "" {
		t.Error("no peaks upload URL was returned; the client computes the envelope and needs somewhere to PUT it")
	}
	if created.Capture.PeaksKey == "" {
		t.Error("the capture row does not record where peaks will land")
	}
}

func TestBeginCaptureRejectsUnsupportedAudio(t *testing.T) {
	svc := NewCaptureService(memory.NewStore(), memory.NewObjects())

	_, err := svc.BeginCapture(context.Background(), "user1", CaptureRequest{ContentType: "application/pdf"})
	if !errors.Is(err, ErrUnsupportedContentType) {
		t.Fatalf("err = %v, want ErrUnsupportedContentType", err)
	}
}

func TestBeginCaptureRejectsAnOversizeUpload(t *testing.T) {
	svc := NewCaptureService(memory.NewStore(), memory.NewObjects())

	_, err := svc.BeginCapture(context.Background(), "user1", CaptureRequest{
		ContentType: "audio/webm",
		SizeBytes:   MaxCaptureBytes + 1,
	})
	if !errors.Is(err, ErrCaptureTooLarge) {
		t.Fatalf("err = %v, want ErrCaptureTooLarge", err)
	}
}

// The extension decides whether the bucket ever tells the worker the object
// exists: the notification filters are a fixed list of suffixes.
func TestBeginCaptureWritesAKeyTheBucketNotifiesOn(t *testing.T) {
	svc := NewCaptureService(memory.NewStore(), memory.NewObjects())

	for contentType, wantSuffix := range map[string]string{
		"audio/webm;codecs=opus": "/audio.webm",
		"audio/mp4":              "/audio.m4a",
		"audio/ogg":              "/audio.ogg",
		"audio/wav":              "/audio.wav",
		"audio/mpeg":             "/audio.mp3",
	} {
		created, err := svc.BeginCapture(context.Background(), "user1", CaptureRequest{ContentType: contentType})
		if err != nil {
			t.Fatalf("BeginCapture(%s): %v", contentType, err)
		}
		if got := created.Capture.AudioKey; len(got) < len(wantSuffix) || got[len(got)-len(wantSuffix):] != wantSuffix {
			t.Errorf("%s produced audio key %q, want it to end in %q", contentType, got, wantSuffix)
		}
	}
}

// Retry hands the capture back to the worker. v1 ran the whole pipeline inline,
// which is what turned a gateway timeout into duplicated note content.
func TestRetryCaptureEnqueuesRatherThanRunningTheWorkInline(t *testing.T) {
	store := memory.NewStore()
	objects := memory.NewObjects()
	queue := &stubQueue{}
	svc := NewCaptureService(store, objects).WithQueue(queue)

	ctx := context.Background()
	if _, err := store.PutCapture(ctx, model.CaptureIndex{
		ID: "c_1", UserID: "user1", NoteID: "n1", Status: model.StatusFailed,
		Error: "provider outage", RawKey: "tenants/user1/captures/c_1/raw.txt",
	}); err != nil {
		t.Fatalf("PutCapture: %v", err)
	}

	capture, err := svc.RetryCapture(ctx, "user1", "c_1")
	if err != nil {
		t.Fatalf("RetryCapture: %v", err)
	}
	if capture.Status != model.StatusTranscribed {
		t.Fatalf("status = %s, want the last good stage (transcribed)", capture.Status)
	}
	if capture.Error != "" {
		t.Errorf("error = %q, want it cleared on retry", capture.Error)
	}
	if len(queue.calls) != 1 || queue.calls[0] != "user1/c_1/retry" {
		t.Fatalf("queue calls = %v, want one retry enqueue", queue.calls)
	}
}

func TestRetryCaptureRefusesAFinishedCapture(t *testing.T) {
	store := memory.NewStore()
	queue := &stubQueue{}
	svc := NewCaptureService(store, memory.NewObjects()).WithQueue(queue)

	ctx := context.Background()
	if _, err := store.PutCapture(ctx, model.CaptureIndex{
		ID: "c_1", UserID: "user1", NoteID: "n1", Status: model.StatusAppended,
	}); err != nil {
		t.Fatalf("PutCapture: %v", err)
	}

	if _, err := svc.RetryCapture(ctx, "user1", "c_1"); !errors.Is(err, ErrCaptureTerminal) {
		t.Fatalf("err = %v, want ErrCaptureTerminal", err)
	}
	if len(queue.calls) != 0 {
		t.Fatalf("an appended capture was re-enqueued: %v", queue.calls)
	}
}

// Without a queue there is nowhere for the slow work to go. Failing loudly beats
// quietly running the pipeline on the request path, which is the defect this
// phase removes.
func TestRetryCaptureFailsLoudlyWithNoQueue(t *testing.T) {
	store := memory.NewStore()
	svc := NewCaptureService(store, memory.NewObjects())

	ctx := context.Background()
	if _, err := store.PutCapture(ctx, model.CaptureIndex{
		ID: "c_1", UserID: "user1", NoteID: "n1", Status: model.StatusUploaded,
	}); err != nil {
		t.Fatalf("PutCapture: %v", err)
	}

	if _, err := svc.RetryCapture(ctx, "user1", "c_1"); !errors.Is(err, ErrCaptureQueueUnavailable) {
		t.Fatalf("err = %v, want ErrCaptureQueueUnavailable", err)
	}
}

func TestGetDownloadURLServesTimestampsAndPeaks(t *testing.T) {
	store := memory.NewStore()
	objects := memory.NewObjects()
	svc := NewCaptureService(store, objects)

	ctx := context.Background()
	if _, err := store.PutCapture(ctx, model.CaptureIndex{
		ID: "c_1", UserID: "user1", Status: model.StatusAppended,
		SegmentsKey: "tenants/user1/captures/c_1/segments.json",
		PeaksKey:    "tenants/user1/captures/c_1/peaks.json",
	}); err != nil {
		t.Fatalf("PutCapture: %v", err)
	}

	for _, kind := range []string{"segments", "peaks"} {
		url, err := svc.GetDownloadURL(ctx, "user1", "c_1", kind)
		if err != nil {
			t.Fatalf("GetDownloadURL(%s): %v", kind, err)
		}
		if url == "" {
			t.Errorf("GetDownloadURL(%s) returned an empty URL", kind)
		}
	}
}
