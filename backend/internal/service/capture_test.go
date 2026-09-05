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

// stubInvoker records every hand-off to the worker and runs nothing.
type stubInvoker struct {
	calls []string
}

func (w *stubInvoker) InvokeCapture(_ context.Context, tenantID, captureID, reason string) error {
	w.calls = append(w.calls, tenantID+"/"+captureID+"/"+reason)
	return nil
}

func (w *stubInvoker) InvokeCleanNote(_ context.Context, tenantID, noteID string, mode model.NoteCleanMode, _ string) error {
	w.calls = append(w.calls, "clean-note/"+tenantID+"/"+noteID+"/"+string(mode))
	return nil
}

func (w *stubInvoker) InvokeAsk(_ context.Context, tenantID, askID string) error {
	w.calls = append(w.calls, "ask/"+tenantID+"/"+askID)
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

// The presigned audio PUT must carry the retention tag. Without it
// RetentionDays is stored, returned, shown in the UI, and read by nothing: an
// S3 lifecycle filter takes one prefix and one suffix with no wildcards, so
// tenants/*/captures/ cannot be expressed and the rule matches this tag
// instead.
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

// Retry hands the capture back to the worker. Running the whole pipeline inline
// turns a gateway timeout into duplicated note content.
func TestRetryCaptureInvokesTheWorkerRatherThanRunningTheWorkInline(t *testing.T) {
	store := memory.NewStore()
	objects := memory.NewObjects()
	worker := &stubInvoker{}
	svc := NewCaptureService(store, objects).WithInvoker(worker)

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
	if len(worker.calls) != 1 || worker.calls[0] != "user1/c_1/retry" {
		t.Fatalf("worker invocations = %v, want one retry", worker.calls)
	}
}

func TestRetryCaptureRefusesAFinishedCapture(t *testing.T) {
	store := memory.NewStore()
	worker := &stubInvoker{}
	svc := NewCaptureService(store, memory.NewObjects()).WithInvoker(worker)

	ctx := context.Background()
	if _, err := store.PutCapture(ctx, model.CaptureIndex{
		ID: "c_1", UserID: "user1", NoteID: "n1", Status: model.StatusAppended,
	}); err != nil {
		t.Fatalf("PutCapture: %v", err)
	}

	if _, err := svc.RetryCapture(ctx, "user1", "c_1"); !errors.Is(err, ErrCaptureTerminal) {
		t.Fatalf("err = %v, want ErrCaptureTerminal", err)
	}
	if len(worker.calls) != 0 {
		t.Fatalf("an appended capture was handed to the worker: %v", worker.calls)
	}
}

// Without a worker there is nowhere for the slow work to go. Failing loudly
// beats quietly running the pipeline on the request path, which is the defect
// this phase removes.
func TestRetryCaptureFailsLoudlyWithNoWorker(t *testing.T) {
	store := memory.NewStore()
	svc := NewCaptureService(store, memory.NewObjects())

	ctx := context.Background()
	if _, err := store.PutCapture(ctx, model.CaptureIndex{
		ID: "c_1", UserID: "user1", NoteID: "n1", Status: model.StatusUploaded,
	}); err != nil {
		t.Fatalf("PutCapture: %v", err)
	}

	if _, err := svc.RetryCapture(ctx, "user1", "c_1"); !errors.Is(err, ErrCaptureWorkerUnavailable) {
		t.Fatalf("err = %v, want ErrCaptureWorkerUnavailable", err)
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

// TestTheUploadCarriesTheTenantsOwnRetention is the request-path half of making
// `retention_days` mean something.
//
// A setting that is validated, stored, returned and rendered in the UI while
// nothing on the way to S3 reads it — every recording tagged with the same
// constant, the expiry period from a CloudFormation parameter — is a control
// that does nothing. This is the only line of code that can make it real,
// because the presigned PUT is the last moment a per-tenant value can reach the
// object.
func TestTheUploadCarriesTheTenantsOwnRetention(t *testing.T) {
	cases := map[string]struct {
		saved int
		want  string // "" means no retention tag at all
	}{
		"a tenant who chose thirty days":        {30, "30"},
		"a tenant who chose the longest tier":   {365, "365"},
		"a tenant between tiers":                {45, "30"},
		"a tenant who chose to keep everything": {0, ""},
		"a tenant who has saved nothing at all": {0, ""},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store := memory.NewStore()
			presigner := &recordingPresigner{}
			svc := NewCaptureService(store, memory.NewObjects()).WithUploads(presigner)

			if tc.saved != 0 {
				if err := store.PutSettings(ctx, "user1", model.Settings{
					CleanupMode: model.CleanupFaithful, RetentionDays: tc.saved,
				}); err != nil {
					t.Fatalf("PutSettings: %v", err)
				}
			}

			if _, err := svc.BeginCapture(ctx, "user1", CaptureRequest{ContentType: "audio/webm"}); err != nil {
				t.Fatalf("BeginCapture: %v", err)
			}
			if len(presigner.calls) == 0 {
				t.Fatal("nothing was presigned")
			}

			tags := presigner.calls[0].tags
			if tags[upload.ArtifactTagKey] != upload.ArtifactCaptureAudio {
				t.Fatalf("artifact tag = %q, want %q", tags[upload.ArtifactTagKey], upload.ArtifactCaptureAudio)
			}
			got, present := tags[upload.RetentionTagKey]
			if tc.want == "" {
				if present {
					t.Errorf("retention tag = %q; a retention of 0 must carry no tag, so no rule matches and the audio is kept", got)
				}
				return
			}
			if got != tc.want {
				t.Errorf("retention tag = %q, want %q — the object expires on whatever day this names, and nothing else reads the setting",
					got, tc.want)
			}
		})
	}
}

// A retry is for a capture nobody is working on. One still in flight is
// refused until the worker's maximum lifetime has passed since it last wrote
// the row — a retry before that starts a second delivery beside a live one —
// and one in `appending` is refused until the claim lease has run out, because
// a retry inside the lease cannot take the claim and only dead-letters. Every
// hand-off stamps the row, so two taps are one delivery.
func TestRetryCaptureRefusesAnInFlightCaptureUntilNoWorkerCanBeAlive(t *testing.T) {
	now := time.Date(2026, 9, 5, 17, 0, 0, 0, time.UTC)
	at := func(ago time.Duration) string { return model.FormatTime(now.Add(-ago)) }

	for _, tc := range []struct {
		name    string
		capture model.CaptureIndex
		wantErr error
	}{
		{"transcribing, worker wrote the row a minute ago", model.CaptureIndex{
			Status: model.StatusTranscribing, CreatedAt: at(time.Hour), LastProgressAt: at(time.Minute),
		}, ErrCaptureInFlight},
		{"transcribing, nothing written for 15 minutes", model.CaptureIndex{
			Status: model.StatusTranscribing, CreatedAt: at(time.Hour), LastProgressAt: at(CaptureStuckAfter),
		}, nil},
		{"cleaning, a row from before the stamp existed, created 20 minutes ago", model.CaptureIndex{
			Status: model.StatusCleaning, CreatedAt: at(20 * time.Minute),
		}, nil},
		{"cleaning, a row from before the stamp existed, created 5 minutes ago", model.CaptureIndex{
			Status: model.StatusCleaning, CreatedAt: at(5 * time.Minute),
		}, ErrCaptureInFlight},
		{"appending, claim 12 minutes old: inside the lease, the retry could not take it", model.CaptureIndex{
			Status: model.StatusAppending, CreatedAt: at(time.Hour), LastProgressAt: at(16 * time.Minute),
			AppendToken: "t", AppendClaimedAt: now.Add(-12 * time.Minute).Unix(),
		}, ErrCaptureInFlight},
		{"appending, claim 21 minutes old: the lease has run out", model.CaptureIndex{
			Status: model.StatusAppending, CreatedAt: at(time.Hour), LastProgressAt: at(21 * time.Minute),
			AppendToken: "t", AppendClaimedAt: now.Add(-21 * time.Minute).Unix(),
		}, nil},
		{"failed a minute ago: retried at once", model.CaptureIndex{
			Status: model.StatusFailed, Error: "the cleanup provider failed; try again", CreatedAt: at(time.Hour), LastProgressAt: at(time.Minute),
		}, nil},
		{"spend capped a minute ago: retried at once", model.CaptureIndex{
			Status: model.StatusSpendCapped, CreatedAt: at(time.Hour), LastProgressAt: at(time.Minute),
		}, nil},
		{"needs_target: the person decides, not a retry", model.CaptureIndex{
			Status: model.StatusNeedsTarget, CreatedAt: at(time.Hour),
		}, ErrCaptureTerminal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := memory.NewStore()
			worker := &stubInvoker{}
			svc := NewCaptureService(store, memory.NewObjects()).WithInvoker(worker).WithClock(func() time.Time { return now })
			c := tc.capture
			c.ID, c.UserID, c.NoteID, c.RawKey = "c_1", "user1", "n1", "tenants/user1/captures/c_1/raw.txt"
			if _, err := store.PutCapture(context.Background(), c); err != nil {
				t.Fatalf("PutCapture: %v", err)
			}

			got, err := svc.RetryCapture(context.Background(), "user1", "c_1")
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr != nil {
				if len(worker.calls) != 0 {
					t.Fatalf("a refused retry handed the capture to the worker: %v", worker.calls)
				}
				return
			}
			if len(worker.calls) != 1 {
				t.Fatalf("worker calls = %v, want one retry", worker.calls)
			}
			if got.LastProgressAt != model.FormatTime(now) {
				t.Fatalf("the hand-off did not stamp the row: last_progress_at = %q", got.LastProgressAt)
			}
			// The same tap again, a moment later, is one delivery.
			if _, err := svc.RetryCapture(context.Background(), "user1", "c_1"); !errors.Is(err, ErrCaptureInFlight) {
				t.Fatalf("a second retry right after the first returned %v, want ErrCaptureInFlight", err)
			}
			if len(worker.calls) != 1 {
				t.Fatalf("worker calls after the second tap = %v, want still one", worker.calls)
			}
		})
	}
}
