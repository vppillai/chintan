package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// Export job states, matching the OpenAPI ExportJob enum.
const (
	ExportPending = "pending"
	ExportRunning = "running"
	ExportReady   = "ready"
	ExportFailed  = "failed"
)

// exportURLTTL bounds how long an export download link stays usable.
const exportURLTTL = 15 * time.Minute

// maxExportNotes bounds one export. An export that walks an unbounded corpus
// inside a request is the same defect as an unbounded list.
const maxExportNotes = 2000

// maxExportCapturesPerNote bounds the capture list carried per note.
const maxExportCapturesPerNote = 500

// ExportJob is the state of one export, as the API reports it.
type ExportJob struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	URL       string `json:"url,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
	Bytes     int64  `json:"bytes,omitempty"`
}

// exportIDRe bounds an export id to what the OpenAPI path parameter declares.
// The id becomes part of an object key, so it is validated rather than trusted.
var exportIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ErrInvalidExportID rejects an id that could not have been issued here.
var ErrInvalidExportID = errors.New("invalid export id")

// ExportService produces a self-contained copy of a tenant's data.
//
// It enumerates the tenant's DynamoDB partition and follows every S3 key it
// finds there, rather than walking a hand-written list of entity types — so a
// schema addition cannot silently fall out of the export. It is the in-app
// counterpart to `chintanctl export`, which additionally enumerates raw S3
// prefixes and is the tool for a full disaster recovery.
type ExportService struct {
	notes    *NotesService
	captures *CaptureService
	settings *SettingsService
	objects  repository.Objects
	now      func() time.Time
}

// NewExportService builds the export service.
func NewExportService(notes *NotesService, captures *CaptureService, settings *SettingsService, objects repository.Objects) *ExportService {
	return &ExportService{
		notes:    notes,
		captures: captures,
		settings: settings,
		objects:  objects,
		now:      time.Now,
	}
}

// exportDocument is the on-disk shape. Version is first so a future reader can
// branch on it before parsing anything else.
type exportDocument struct {
	Version    int                   `json:"version"`
	TenantID   string                `json:"tenant_id"`
	ExportedAt string                `json:"exported_at"`
	Settings   model.Settings        `json:"settings"`
	Notes      []exportNote          `json:"notes"`
	Truncated  bool                  `json:"truncated,omitempty"`
	Captures   []model.CaptureIndex  `json:"unrouted_captures,omitempty"`
	Artifacts  map[string]exportKeys `json:"artifact_keys,omitempty"`
}

type exportNote struct {
	model.NoteIndex
	Body     string               `json:"body"`
	Captures []model.CaptureIndex `json:"captures,omitempty"`
	// Cleaned is the whole-note cleaned view when the note has one. It is
	// spelled out because the index row keeps the body out of its JSON
	// (model.NoteIndex.CleanedBody is json:"-"), and an export that carried
	// the view's mode and time but not the view would look complete.
	Cleaned *exportCleaned `json:"cleaned,omitempty"`
}

// exportCleaned mirrors the API's NoteCleaned: the view, the mode and time it
// was generated in, and whether the body has moved on since.
type exportCleaned struct {
	Body        string `json:"body"`
	Mode        string `json:"mode"`
	GeneratedAt string `json:"generated_at"`
	Stale       bool   `json:"stale"`
}

// exportKeys records where a capture's binary artifacts live, so a restore can
// find the audio this JSON deliberately does not inline.
type exportKeys struct {
	Audio    string `json:"audio,omitempty"`
	Raw      string `json:"raw,omitempty"`
	Routed   string `json:"routed,omitempty"`
	Clean    string `json:"clean,omitempty"`
	Segments string `json:"segments,omitempty"`
	Peaks    string `json:"peaks,omitempty"`
}

// Start produces an export and returns its job record.
//
// The work is done inline. At personal scale a whole corpus is a few hundred
// index reads and it finishes well inside the gateway's ceiling; making it
// asynchronous would mean a second queue and a second worker for a feature used
// a handful of times a year. The job record exists anyway, so the endpoint can
// become asynchronous without the client changing.
func (s *ExportService) Start(ctx context.Context, userID string) (ExportJob, error) {
	idBytes := make([]byte, 8)
	if _, err := rand.Read(idBytes); err != nil {
		return ExportJob{}, fmt.Errorf("export: generate id: %w", err)
	}
	exportID := "e_" + hex.EncodeToString(idBytes)

	doc, err := s.build(ctx, userID)
	if err != nil {
		return ExportJob{}, err
	}
	payload, err := json.Marshal(doc)
	if err != nil {
		return ExportJob{}, fmt.Errorf("export: encode: %w", err)
	}

	dataKey, err := exportDataKey(userID, exportID)
	if err != nil {
		return ExportJob{}, err
	}
	if err := s.objects.Put(ctx, dataKey, payload, "application/json"); err != nil {
		return ExportJob{}, fmt.Errorf("export: write payload: %w", err)
	}

	job := ExportJob{ID: exportID, Status: ExportReady, Bytes: int64(len(payload))}
	if err := s.writeJob(ctx, userID, job); err != nil {
		return ExportJob{}, err
	}

	obs.Log(ctx).Info("export produced",
		slog.String("export_id", exportID),
		slog.Int("notes", len(doc.Notes)),
		slog.Int64("bytes", job.Bytes))

	return s.withURL(ctx, userID, job)
}

// Get returns an export's state, with a fresh download URL when it is ready.
func (s *ExportService) Get(ctx context.Context, userID, exportID string) (ExportJob, error) {
	if !exportIDRe.MatchString(exportID) {
		return ExportJob{}, fmt.Errorf("%w: %w", repository.ErrNotFound, ErrInvalidExportID)
	}
	key, err := exportJobKey(userID, exportID)
	if err != nil {
		return ExportJob{}, err
	}
	raw, err := s.objects.Get(ctx, key)
	if err != nil {
		return ExportJob{}, err
	}
	var job ExportJob
	if err := json.Unmarshal(raw, &job); err != nil {
		return ExportJob{}, fmt.Errorf("export: decode job: %w", err)
	}
	if job.Status != ExportReady {
		return job, nil
	}
	return s.withURL(ctx, userID, job)
}

func (s *ExportService) withURL(ctx context.Context, userID string, job ExportJob) (ExportJob, error) {
	dataKey, err := exportDataKey(userID, job.ID)
	if err != nil {
		return ExportJob{}, err
	}
	url, err := s.objects.PresignGet(ctx, dataKey, exportURLTTL)
	if err != nil {
		return ExportJob{}, fmt.Errorf("export: presign: %w", err)
	}
	job.URL = url
	job.ExpiresAt = model.FormatTime(s.now().Add(exportURLTTL))
	return job, nil
}

func (s *ExportService) writeJob(ctx context.Context, userID string, job ExportJob) error {
	key, err := exportJobKey(userID, job.ID)
	if err != nil {
		return err
	}
	// The URL and its expiry are minted per read, never stored: a persisted
	// presigned URL outlives the request that was authorised to hold it.
	stored := ExportJob{ID: job.ID, Status: job.Status, Bytes: job.Bytes}
	body, err := json.Marshal(stored)
	if err != nil {
		return fmt.Errorf("export: encode job: %w", err)
	}
	if err := s.objects.Put(ctx, key, body, "application/json"); err != nil {
		return fmt.Errorf("export: write job: %w", err)
	}
	return nil
}

func (s *ExportService) build(ctx context.Context, userID string) (exportDocument, error) {
	settings, err := s.settings.GetSettings(ctx, userID)
	if err != nil {
		return exportDocument{}, err
	}

	// The list carries the cleaned view only on request; an export wants it
	// for every note that has one.
	active, err := repository.DrainPages(ctx, maxExportNotes, func(ctx context.Context, opts repository.ListOptions) (repository.Page[model.NoteIndex], error) {
		opts.IncludeCleanedBody = true
		return s.notes.ListNotes(ctx, userID, opts)
	})
	if err != nil {
		return exportDocument{}, err
	}
	archived, err := repository.DrainPages(ctx, maxExportNotes, func(ctx context.Context, opts repository.ListOptions) (repository.Page[model.NoteIndex], error) {
		opts.IncludeCleanedBody = true
		return s.notes.ListArchivedNotes(ctx, userID, opts)
	})
	if err != nil {
		return exportDocument{}, err
	}

	doc := exportDocument{
		Version:    2,
		TenantID:   userID,
		ExportedAt: model.FormatTime(s.now()),
		Settings:   settings,
		Artifacts:  map[string]exportKeys{},
		Truncated:  len(active)+len(archived) >= maxExportNotes,
	}

	for _, note := range append(append([]model.NoteIndex{}, active...), archived...) {
		entry := exportNote{NoteIndex: note}
		body, err := s.objects.Get(ctx, note.S3MarkdownKey)
		if err != nil && !errors.Is(err, repository.ErrNotFound) {
			return exportDocument{}, fmt.Errorf("export: read note body: %w", err)
		}
		// Strip the worker's append markers as GET /v1/notes/{id} does: the
		// export is the note as the user knows it, not the object as stored.
		entry.Body = StripCaptureMarkers(string(body))
		if note.CleanedBody != "" {
			entry.Cleaned = &exportCleaned{
				Body:        note.CleanedBody,
				Mode:        string(EffectiveCleanMode(model.NoteIndex{CleanMode: note.CleanedMode})),
				GeneratedAt: note.CleanedAt,
				Stale:       note.CleanedStale,
			}
		}

		captures, err := repository.DrainPages(ctx, maxExportCapturesPerNote, func(ctx context.Context, opts repository.ListOptions) (repository.Page[model.CaptureIndex], error) {
			return s.captures.ListCapturesForNote(ctx, userID, note.ID, opts)
		})
		if err != nil {
			return exportDocument{}, fmt.Errorf("export: list captures: %w", err)
		}
		entry.Captures = captures
		for _, c := range captures {
			doc.Artifacts[c.ID] = exportKeys{
				Audio:    c.AudioKey,
				Raw:      c.RawKey,
				Routed:   c.RoutedKey,
				Clean:    c.CleanKey,
				Segments: c.SegmentsKey,
				Peaks:    c.PeaksKey,
			}
		}
		doc.Notes = append(doc.Notes, entry)
	}

	// Captures that never reached a note — a needs_target waiting on the user, or
	// one that failed before routing — belong in an export too. Losing them is
	// exactly the "silently falls out" failure the enumeration order exists to
	// prevent.
	unrouted, err := repository.DrainPages(ctx, maxExportCapturesPerNote, func(ctx context.Context, opts repository.ListOptions) (repository.Page[model.CaptureIndex], error) {
		return s.captures.ListUnroutedCaptures(ctx, userID, opts)
	})
	if err != nil {
		return exportDocument{}, fmt.Errorf("export: list unrouted captures: %w", err)
	}
	doc.Captures = unrouted
	for _, c := range unrouted {
		doc.Artifacts[c.ID] = exportKeys{
			Audio:    c.AudioKey,
			Raw:      c.RawKey,
			Routed:   c.RoutedKey,
			Clean:    c.CleanKey,
			Segments: c.SegmentsKey,
			Peaks:    c.PeaksKey,
		}
	}

	return doc, nil
}

func exportDataKey(userID, exportID string) (string, error) {
	return exportKey(userID, exportID, "export.json")
}

func exportJobKey(userID, exportID string) (string, error) {
	return exportKey(userID, exportID, "job.json")
}

// exportKey builds a tenant-scoped key, validating both identifiers. An
// unvalidated id in a key is a path traversal into another tenant's prefix.
func exportKey(userID, exportID, name string) (string, error) {
	if !exportIDRe.MatchString(userID) {
		return "", fmt.Errorf("export: invalid tenant id")
	}
	if !exportIDRe.MatchString(exportID) {
		return "", fmt.Errorf("%w: %w", ErrInvalidExportID, repository.ErrNotFound)
	}
	return fmt.Sprintf("tenants/%s/exports/%s/%s", userID, exportID, name), nil
}
