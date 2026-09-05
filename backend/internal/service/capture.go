package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/vppillai/chintan/backend/internal/keys"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/upload"
)

// uploadTTL bounds how long a presigned PUT stays usable. Long enough to absorb
// a bad cellular handover on the drive home, short enough that a leaked URL is
// not a standing grant.
//
// It is a bound on time, not on writes. Anyone holding the URL — from browser
// history, a HAR file, a proxy log — can PUT to it repeatedly until it expires,
// and the bucket keeps every version. See MaxCaptureBytes for what does and
// does not bound the size of each of those writes.
const uploadTTL = 30 * time.Minute

// DownloadTTL bounds a presigned GET handed to the client. It is exported so
// the handler can report the same expiry it signed, rather than a second
// constant that can drift from this one.
const DownloadTTL = 15 * time.Minute

// MaxCaptureBytes bounds an audio upload. Twenty minutes of 16 kHz mono WAV is
// about 38 MB; this leaves headroom for an uncompressed container.
//
// Where it is actually enforced, and where it is not:
//
//   - Here, against req.SizeBytes, it rejects a client that declares an
//     oversized upload. That is a courtesy: the number is the client's claim.
//   - The presigned PUT enforces nothing. SigV4 cannot sign Content-Length on a
//     PUT, so a URL issued for a one-kilobyte clip accepts five gigabytes, and
//     with bucket versioning it accepts five gigabytes again on every replay
//     inside the URL's lifetime. max_bytes in the response is advice.
//   - pipeline.Worker.Handle is the real bound. The S3 notification carries the
//     size that was written, and an object over this limit is deleted and its
//     capture failed before any provider sees it.
//
// The only construction that refuses the bytes at the edge is a presigned POST
// carrying a Content-Length-Range policy condition, which changes the upload
// contract for every client. That trade has not been made.
const MaxCaptureBytes int64 = 256 << 20

// MaxPeaksBytes bounds the client-computed waveform envelope. It is a few
// thousand numbers, not a media file.
const MaxPeaksBytes int64 = 2 << 20

var (
	// ErrCaptureTerminal means a capture has finished and cannot be re-run. It is
	// distinct so a retry of an already-appended capture is a 409 rather than a
	// second append.
	ErrCaptureTerminal = errors.New("capture is already complete")
	// ErrCaptureNotRoutable means a capture already has a destination note, so a
	// target cannot be chosen for it.
	ErrCaptureAlreadyTargeted = errors.New("capture already targets a note")
	// ErrCaptureTargetRequired means neither an existing note nor a new title was
	// supplied.
	ErrCaptureTargetRequired = errors.New("note_id or new_note_title is required")
	// ErrNoteCreationUnavailable means the service was built without a note
	// creator, so a capture cannot be given a brand new note.
	ErrNoteCreationUnavailable = errors.New("note creation is unavailable")
	// ErrCaptureWorkerUnavailable means no worker invoker is wired, so the slow
	// half of the pipeline cannot be reached. It is an error rather than a silent
	// inline run: running the pipeline on the request path is the defect this
	// phase removes.
	ErrCaptureWorkerUnavailable = errors.New("capture worker is not configured")
	// ErrUnsupportedContentType rejects a container the pipeline cannot route to
	// a transcription provider.
	ErrUnsupportedContentType = errors.New("unsupported audio content type")
	// ErrCaptureTooLarge rejects a declared upload size past MaxCaptureBytes.
	ErrCaptureTooLarge = errors.New("capture exceeds the maximum upload size")
	// ErrDownloadKindUnknown rejects a download kind outside the fixed set.
	ErrDownloadKindUnknown = errors.New("unknown download kind")
)

// NoteCreator creates the destination note for a capture that has none.
type NoteCreator interface {
	CreateNote(ctx context.Context, userID, title string, aliases []string) (model.NoteIndex, error)
}

// Invoker hands a capture to the worker Lambda.
//
// The interface is here and the implementation is in internal/pipeline, so the
// API binary depends on the shape of the hand-off and not on the pipeline.
type Invoker interface {
	InvokeCapture(ctx context.Context, tenantID, captureID, reason string) error
	// InvokeCleanNote asks the worker to regenerate the whole-note cleaned
	// view of noteID in mode. See note_clean.go.
	InvokeCleanNote(ctx context.Context, tenantID, noteID string, mode model.NoteCleanMode) error
	// InvokeAsk asks the worker to answer the question stored under askID.
	// See ask.go.
	InvokeAsk(ctx context.Context, tenantID, askID string) error
}

// CaptureRequest is what a client asks for when it begins a capture.
type CaptureRequest struct {
	NoteID      string
	ContentType string
	// SizeBytes is the recording's declared length, used to bound the upload.
	SizeBytes int64
	// DurationMS is what the recorder measured. The worker overwrites it with the
	// provider's figure once the audio is transcribed.
	DurationMS int64
}

// CaptureCreated is the response to beginning a capture: the row, and the two
// presigned PUTs the client needs.
type CaptureCreated struct {
	Capture model.CaptureIndex
	Audio   upload.Presigned
	// Peaks is where the client PUTs the amplitude envelope it computed while
	// recording. The browser already holds the decoded signal from its
	// AnalyserNode; recovering it in the worker would mean shipping an Opus
	// decoder into Lambda.
	Peaks upload.Presigned
}

// CaptureService owns the fast half of the capture lifecycle.
//
// Everything slow — transcribe, route, clean, append — belongs to
// internal/pipeline and runs in the worker Lambda. API Gateway's HTTP API caps
// an integration at 30 seconds and the cap is not adjustable, so a request that
// waits for a provider returns 504 to the user while the Lambda keeps running
// and billing. Nothing here contacts a provider.
type CaptureService struct {
	store   repository.Store
	objects repository.Objects
	uploads upload.Presigner
	worker  Invoker
	notes   NoteCreator
}

// NewCaptureService creates a new capture service.
//
// It takes no providers. Nothing here contacts one: transcribe, route and clean
// all belong to the worker.
func NewCaptureService(store repository.Store, objects repository.Objects) *CaptureService {
	return &CaptureService{
		store:   store,
		objects: objects,
		uploads: upload.NewObjects(objects),
	}
}

// WithNoteCreator sets the note creator.
func (s *CaptureService) WithNoteCreator(notes NoteCreator) *CaptureService {
	s.notes = notes
	return s
}

// WithUploads installs the presigner that binds the retention tag into the
// signature. Without it the fallback signs untagged PUTs and the lifecycle rule
// never sees the object.
func (s *CaptureService) WithUploads(p upload.Presigner) *CaptureService {
	if p != nil {
		s.uploads = p
	}
	return s
}

// WithInvoker installs the hand-off to the worker Lambda.
func (s *CaptureService) WithInvoker(w Invoker) *CaptureService {
	s.worker = w
	return s
}

// BeginCapture validates, writes the capture row, and returns the presigned
// PUTs. It performs no provider call and no object read: this is the whole of
// what POST /v1/captures does.
func (s *CaptureService) BeginCapture(ctx context.Context, userID string, req CaptureRequest) (CaptureCreated, error) {
	contentType, ext, err := normalizeAudioContentType(req.ContentType)
	if err != nil {
		return CaptureCreated{}, err
	}
	if req.SizeBytes > MaxCaptureBytes {
		return CaptureCreated{}, ErrCaptureTooLarge
	}

	if req.NoteID != "" {
		note, err := s.store.GetNote(ctx, userID, req.NoteID)
		if err != nil {
			return CaptureCreated{}, fmt.Errorf("failed to get note: %w", err)
		}
		if !NoteIsActive(note) {
			return CaptureCreated{}, ErrNoteArchived
		}
	}

	settings, err := s.store.GetSettings(ctx, userID)
	if err != nil {
		return CaptureCreated{}, fmt.Errorf("failed to get settings: %w", err)
	}

	captureIDBytes := make([]byte, 8)
	if _, err := rand.Read(captureIDBytes); err != nil {
		return CaptureCreated{}, fmt.Errorf("failed to generate capture ID: %w", err)
	}
	// The id leads with a fixed-width creation instant, so capture ids sort
	// chronologically. The tenant-wide capture list reads the base table, whose
	// sort key is CAPTURE#<id>, so the id is what decides whether page one holds
	// the newest captures. A purely random id scatters an in-flight capture
	// uniformly across every page, and the progress card only ever asks for the
	// first one. The random suffix keeps ids unguessable and collision-free.
	captureID := fmt.Sprintf("c_%016x_%s",
		uint64(time.Now().UTC().UnixNano()), hex.EncodeToString(captureIDBytes))

	audioKey, err := keys.CaptureAudio(userID, captureID, ext)
	if err != nil {
		return CaptureCreated{}, fmt.Errorf("failed to generate audio key: %w", err)
	}
	peaksKey, err := keys.CapturePeaks(userID, captureID)
	if err != nil {
		return CaptureCreated{}, fmt.Errorf("failed to generate peaks key: %w", err)
	}

	capture := model.CaptureIndex{
		ID:         captureID,
		UserID:     userID,
		NoteID:     req.NoteID,
		Status:     model.StatusUploaded,
		Mode:       settings.CleanupMode,
		AudioKey:   audioKey,
		PeaksKey:   peaksKey,
		DurationMS: req.DurationMS,
		CreatedAt:  model.Now(),
	}
	if req.NoteID != "" {
		// The client named the note — "Record into this" — so a person chose
		// the destination and the Home screen need not offer to file it.
		capture.TargetSource = model.TargetSourceClient
	}

	stored, err := s.store.PutCapture(ctx, capture)
	if err != nil {
		return CaptureCreated{}, fmt.Errorf("failed to store capture: %w", err)
	}

	// Advisory: it is echoed to the client so a recorder can stop before it
	// wastes an upload, and it constrains nothing on the wire. The enforcement
	// is in the worker, on the size S3 reports after the fact.
	maxBytes := req.SizeBytes
	if maxBytes <= 0 {
		maxBytes = MaxCaptureBytes
	}

	// The tags are the whole point: an S3 lifecycle filter takes one prefix and
	// one suffix with no wildcards, so tenants/*/captures/ cannot be expressed
	// and the retention rules match on these tags instead. Signing them means an
	// upload cannot quietly omit them and escape expiry.
	//
	// The tenant's own retention is what decides the second tag, and this is the
	// only place in the request path that reads it. Without it the setting is
	// validated, stored and returned while nothing acts on it — a control that
	// does nothing.
	audioUpload, err := s.uploads.PresignPut(ctx, audioKey, contentType, upload.CaptureAudioTags(settings.RetentionDays), maxBytes, uploadTTL)
	if err != nil {
		return CaptureCreated{}, fmt.Errorf("failed to generate upload URL: %w", err)
	}
	peaksUpload, err := s.uploads.PresignPut(ctx, peaksKey, "application/json", nil, MaxPeaksBytes, uploadTTL)
	if err != nil {
		return CaptureCreated{}, fmt.Errorf("failed to generate peaks upload URL: %w", err)
	}

	obs.Log(ctx).Info("capture created",
		slog.String("capture_id", stored.ID),
		slog.String("note_id", stored.NoteID),
		slog.String("content_type", contentType))
	obs.Count(ctx, "CapturesCreated", map[string]string{"Stage": "created"})

	return CaptureCreated{Capture: stored, Audio: audioUpload, Peaks: peaksUpload}, nil
}

// RetryCapture hands a capture back to the worker so it resumes from its last
// good stage. It does no work inline: running the whole pipeline on the request
// path turns a gateway timeout into duplicated note content.
func (s *CaptureService) RetryCapture(ctx context.Context, userID, captureID string) (*model.CaptureIndex, error) {
	capture, err := s.store.GetCapture(ctx, userID, captureID)
	if err != nil {
		return nil, fmt.Errorf("failed to get capture: %w", err)
	}

	switch capture.Status {
	case model.StatusAppended, model.StatusNoContent:
		return &capture, ErrCaptureTerminal
	case model.StatusNeedsTarget:
		// The destination is the user's to choose; re-running would only ask again.
		return &capture, ErrCaptureTerminal
	}

	if capture.Error != "" || capture.Status == model.StatusFailed || capture.Status == StatusSpendCapped {
		capture.Error = ""
		capture.Status = resumeStatusFor(capture)
		updated, err := s.store.PutCapture(ctx, capture)
		if err != nil {
			return nil, fmt.Errorf("failed to reset capture: %w", err)
		}
		capture = updated
	}

	if err := s.invokeWorker(ctx, userID, captureID, "retry"); err != nil {
		return nil, err
	}
	return &capture, nil
}

// resumeStatusFor picks the stage a failed capture restarts from, so a retry
// never redoes work whose artifact already exists.
func resumeStatusFor(c model.CaptureIndex) model.CaptureStatus {
	switch {
	case c.CleanKey != "":
		return model.StatusCleaned
	case c.RawKey != "":
		return model.StatusTranscribed
	default:
		return model.StatusUploaded
	}
}

// SetCaptureTarget records a user-chosen destination and hands the capture back
// to the worker. It writes the row and returns; it does not append.
func (s *CaptureService) SetCaptureTarget(ctx context.Context, userID, captureID, noteID, newNoteTitle string) (*model.CaptureIndex, error) {
	capture, err := s.store.GetCapture(ctx, userID, captureID)
	if err != nil {
		return nil, fmt.Errorf("failed to get capture: %w", err)
	}
	if capture.NoteID != "" {
		return nil, ErrCaptureAlreadyTargeted
	}

	newNoteTitle = sanitizeNoteTitle(newNoteTitle)

	switch {
	case noteID != "":
		note, err := s.store.GetNote(ctx, userID, noteID)
		if err != nil {
			return nil, fmt.Errorf("failed to get note: %w", err)
		}
		if !NoteIsActive(note) {
			return nil, ErrNoteArchived
		}
		capture.NoteID = noteID
	case newNoteTitle != "":
		if s.notes == nil {
			return nil, ErrNoteCreationUnavailable
		}
		note, err := s.notes.CreateNote(ctx, userID, newNoteTitle, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create note: %w", err)
		}
		capture.NoteID = note.ID
	default:
		return nil, ErrCaptureTargetRequired
	}

	// Either way a person chose: an existing note by id, or a new one by
	// title. The router's suggestion, if there was one, is superseded.
	capture.TargetSource = model.TargetSourceUser
	capture.Status = resumeStatusFor(capture)
	capture.SuggestedNoteID = ""
	capture.SuggestedTitle = ""
	capture.Error = ""
	updated, err := s.store.PutCapture(ctx, capture)
	if err != nil {
		return nil, fmt.Errorf("failed to update capture: %w", err)
	}

	if err := s.invokeWorker(ctx, userID, captureID, "target"); err != nil {
		return nil, err
	}
	return &updated, nil
}

func (s *CaptureService) invokeWorker(ctx context.Context, userID, captureID, reason string) error {
	if s.worker == nil {
		return ErrCaptureWorkerUnavailable
	}
	if err := s.worker.InvokeCapture(ctx, userID, captureID, reason); err != nil {
		return fmt.Errorf("failed to hand capture to the worker: %w", err)
	}
	obs.Log(ctx).Info("capture handed to the worker",
		slog.String("capture_id", captureID),
		slog.String("reason", reason))
	return nil
}

// GetCapture returns a capture by ID.
func (s *CaptureService) GetCapture(ctx context.Context, userID, captureID string) (*model.CaptureIndex, error) {
	capture, err := s.store.GetCapture(ctx, userID, captureID)
	if err != nil {
		return nil, err
	}
	return &capture, nil
}

// ListCapturesForNote returns one page of a note's captures, newest first.
func (s *CaptureService) ListCapturesForNote(ctx context.Context, userID, noteID string, opts repository.ListOptions) (repository.Page[model.CaptureIndex], error) {
	if _, err := s.store.GetNote(ctx, userID, noteID); err != nil {
		return repository.Page[model.CaptureIndex]{}, err
	}
	return s.store.ListCapturesByNote(ctx, userID, noteID, opts)
}

// GetDownloadURL returns a presigned download URL for capture artifacts.
func (s *CaptureService) GetDownloadURL(ctx context.Context, userID, captureID, kind string) (string, error) {
	capture, err := s.store.GetCapture(ctx, userID, captureID)
	if err != nil {
		return "", fmt.Errorf("failed to get capture: %w", err)
	}

	var key string
	switch kind {
	case "audio":
		key = capture.AudioKey
	case "raw":
		key = capture.RawKey
	case "clean":
		key = capture.CleanKey
	case "segments":
		key = capture.SegmentsKey
	case "peaks":
		key = capture.PeaksKey
	default:
		return "", fmt.Errorf("%w: %q", ErrDownloadKindUnknown, kind)
	}

	if key == "" {
		// A capture recorded before segments and peaks were stored has neither.
		// The artifact genuinely does not exist: a 404, not a fault.
		return "", fmt.Errorf("%w: %s", repository.ErrNotFound, kind)
	}

	url, err := s.objects.PresignGet(ctx, key, DownloadTTL)
	if err != nil {
		return "", fmt.Errorf("failed to generate download URL: %w", err)
	}
	return url, nil
}

// normalizeAudioContentType maps a declared content type to the canonical type
// and the file extension the S3 event notification filters on.
//
// The extension is not cosmetic: the bucket publishes ObjectCreated for a fixed
// list of suffixes, so an extension outside that list produces an object no
// worker is ever told about.
func normalizeAudioContentType(contentType string) (canonical, ext string, err error) {
	base := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	switch base {
	case "audio/wav", "audio/wave", "audio/x-wav":
		return "audio/wav", "wav", nil
	case "audio/mp3", "audio/mpeg":
		return "audio/mpeg", "mp3", nil
	case "audio/mp4", "audio/m4a", "audio/x-m4a":
		return "audio/mp4", "m4a", nil
	case "audio/ogg":
		return "audio/ogg", "ogg", nil
	case "audio/webm":
		return "audio/webm", "webm", nil
	}
	switch {
	case strings.Contains(base, "webm"):
		return "audio/webm", "webm", nil
	case strings.Contains(base, "ogg"):
		return "audio/ogg", "ogg", nil
	}
	return "", "", fmt.Errorf("%w: %q", ErrUnsupportedContentType, base)
}
