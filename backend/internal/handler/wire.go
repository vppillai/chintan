package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/service"
	"github.com/vppillai/chintan/backend/internal/upload"
)

// The wire types are declared here rather than serialising the storage structs
// directly, for two reasons.
//
// The storage structs carry fields no client may see: a note index holds its S3
// keys, and putting those on the wire hands out the bucket layout. And the
// contract in docs/api/openapi.yaml is what a frontend was written against —
// deriving the JSON from whatever a struct happens to hold today makes every
// storage change a silent API change.

// Page is the envelope every collection returns.
//
// Items is never null: a client iterating the response should not have to
// special-case an empty collection. The cursor is in the body rather than a
// header, so a caller never has to read headers to know there is more.
type Page[T any] struct {
	Items  []T    `json:"items"`
	Cursor string `json:"cursor,omitempty"`
}

func page[T any](items []T, cursor string) Page[T] {
	if items == nil {
		items = []T{}
	}
	return Page[T]{Items: items, Cursor: cursor}
}

// Note is the OpenAPI Note schema.
type Note struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Aliases    []string `json:"aliases"`
	Tags       []string `json:"tags"`
	Snippet    string   `json:"snippet,omitempty"`
	CreatedAt  string   `json:"created_at,omitempty"`
	UpdatedAt  string   `json:"updated_at"`
	Version    int64    `json:"version"`
	Archived   bool     `json:"archived"`
	PurgeAfter *string  `json:"purge_after"`
	Verbatim   bool     `json:"verbatim,omitempty"`
	// Language is the transcription language for captures recorded into this
	// note: "auto" or an ISO-639-1 code. Absent means the tenant's
	// default_language applies.
	Language string `json:"language,omitempty"`
	// SearchText is the lowercased, marker-stripped body the server searches
	// (capped at 32 KB). Sent only by GET /v1/notes?include=search_text, so
	// the client's offline corpus can search the same text the server does
	// without fetching every body; every other Note response omits it.
	SearchText string `json:"search_text,omitempty"`
	// AutoClean says the worker regenerates the whole-note cleaned view after
	// every change it makes or is told about (append, move, delete). Always
	// present so a toggle renders the state it is in.
	AutoClean bool `json:"auto_clean"`
	// CleanedMode is the mode an automatic or unspecified clean runs in:
	// polished or structured. Absent means structured, the default.
	CleanedMode string `json:"cleaned_mode,omitempty"`
}

// NoteCleaned is the OpenAPI NoteCleaned schema: the whole-note cleaned view.
//
// Read-only. The view is derived from the body by the worker's clean-note
// task, so there is no request that writes it; an edit belongs in the body
// and the view is regenerated (POST /v1/notes/{id}/clean, or auto_clean).
type NoteCleaned struct {
	// Body is the cleaned view in Markdown; empty when the last run failed
	// before any view existed.
	Body string `json:"body"`
	// Mode is the mode Body was generated in, or the mode of the failed
	// attempt when there is no body.
	Mode string `json:"mode"`
	// GeneratedAt is when Body was generated, or when the attempt failed when
	// there is no body.
	GeneratedAt string `json:"generated_at"`
	// Stale is true when the body changed — an append, an edit, a recording
	// moved in or out or deleted — after GeneratedAt.
	Stale bool `json:"stale"`
	// Error is the fixed reason the most recent run produced no view. Present
	// only after a failure; the next successful run clears it. Beside a Body,
	// it means the previous view was kept.
	Error *string `json:"error,omitempty"`
}

// cleanedOf renders the view, or nil for a note that has never been cleaned
// and never failed to be.
func cleanedOf(n model.NoteIndex) *NoteCleaned {
	if n.CleanedBody == "" && n.CleanedError == "" {
		return nil
	}
	out := &NoteCleaned{
		Body:        n.CleanedBody,
		Mode:        string(service.EffectiveCleanMode(model.NoteIndex{CleanMode: n.CleanedMode})),
		GeneratedAt: n.CleanedAt,
		Stale:       n.CleanedStale,
	}
	if n.CleanedError != "" {
		v := n.CleanedError
		out.Error = &v
	}
	return out
}

// NoteCleanQueued is the OpenAPI NoteCleanQueued schema, the 202 body of
// POST /v1/notes/{id}/clean.
type NoteCleanQueued struct {
	Status string `json:"status"`
	// Mode is the mode that was queued: the request's, else the note's, else
	// structured.
	Mode string `json:"mode"`
}

func noteOf(n model.NoteIndex) Note {
	out := Note{
		ID:        n.ID,
		Title:     n.Title,
		Aliases:   n.Aliases,
		Tags:      n.Tags,
		Snippet:   n.Snippet,
		CreatedAt: n.CreatedAt,
		UpdatedAt: n.UpdatedAt,
		Version:   n.Version,
		Archived:  !service.NoteIsActive(n),
		Verbatim:  n.Verbatim,
		Language:  n.Language,
		AutoClean: n.AutoClean,
	}
	if model.ValidNoteCleanMode(n.CleanMode) {
		out.CleanedMode = string(n.CleanMode)
	}
	if out.Aliases == nil {
		out.Aliases = []string{}
	}
	if out.Tags == nil {
		out.Tags = []string{}
	}
	if n.PurgeAfter != "" {
		v := n.PurgeAfter
		out.PurgeAfter = &v
	}
	return out
}

func notesOf(in []model.NoteIndex) []Note {
	out := make([]Note, 0, len(in))
	for _, n := range in {
		out = append(out, noteOf(n))
	}
	return out
}

// NotePurgeRequest is the body of POST /v1/notes/purge.
//
// Explicit identifiers only. There is deliberately no "all" flag and no filter:
// "clear all" is the client listing its archive and sending the ids in batches,
// and a server-side delete-everything switch is one malformed request away from
// emptying an account.
type NotePurgeRequest struct {
	NoteIDs []string `json:"note_ids"`
}

// NotePurgeResult is one note's outcome. Status is one of purged, not_found or
// failed; detail is safe to display.
type NotePurgeResult struct {
	NoteID string `json:"note_id"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// NotePurgeResponse reports every note separately, because a batch that
// reported one verdict would be claiming an atomicity no transaction provides
// across DynamoDB and S3.
type NotePurgeResponse struct {
	Results []NotePurgeResult `json:"results"`
}

// NoteDetail is the OpenAPI NoteDetail schema: a note, its body, and its
// captures.
type NoteDetail struct {
	Note
	Body     string    `json:"body"`
	Captures []Capture `json:"captures"`
	// Cleaned is the whole-note cleaned view, null until the note has been
	// cleaned (or a clean has failed) at least once.
	Cleaned *NoteCleaned `json:"cleaned"`
}

// Capture is the OpenAPI Capture schema.
type Capture struct {
	ID        string  `json:"id"`
	NoteID    *string `json:"note_id"`
	Status    string  `json:"status"`
	Error     *string `json:"error"`
	CreatedAt string  `json:"created_at"`
	// SuggestedNoteID and SuggestedTitle are the router's answer to "where does
	// this belong", and they are the whole reason a `needs_target` capture is
	// different from an unroutable one.
	//
	// They were computed and then dropped here. The pipeline pays an LLM call to
	// produce them (pipeline.route), stores them on the capture, and this struct
	// left them out — so a client asking why a capture is waiting could only
	// offer an unranked list of every note the user has, which is the question
	// the inference had already answered. Paying for a routing decision and
	// discarding it is worse than not routing at all.
	//
	// Exactly one is ever set. SuggestedNoteID names an existing note the router
	// was confident enough to propose but not confident enough to write to
	// unasked; SuggestedTitle is the title it would give a new note when there
	// was no plausible destination.
	SuggestedNoteID *string `json:"suggested_note_id"`
	SuggestedTitle  *string `json:"suggested_title"`
	AppendedAt      *string `json:"appended_at"`
	DurationMS      *int64  `json:"duration_ms"`
	HasSegments     bool    `json:"has_segments"`
	HasPeaks        bool    `json:"has_peaks"`
	Version         int64   `json:"version"`
}

func captureOf(c model.CaptureIndex) Capture {
	out := Capture{
		ID:        c.ID,
		Status:    string(c.Status),
		CreatedAt: c.CreatedAt,
		// A capture recorded before segments and peaks were stored has neither,
		// and the client renders a plain player rather than an empty waveform.
		//
		// Both keys are set only once the object is known to exist. The worker
		// writes segments.json itself and records the key after the PUT; peaks
		// are uploaded by the client, so the API records the key when it issues
		// the presigned PUT and the worker clears it if the bucket has no such
		// object once the pipeline is done (pipeline.verifyPeaks). Without that
		// check has_peaks would mean "a URL was issued", and the note screen
		// would 404 asking for a waveform nobody uploaded.
		HasSegments: c.SegmentsKey != "",
		HasPeaks:    c.PeaksKey != "",
		Version:     c.Version,
	}
	if c.NoteID != "" {
		v := c.NoteID
		out.NoteID = &v
	}
	if c.Error != "" {
		v := c.Error
		out.Error = &v
	}
	if c.SuggestedNoteID != "" {
		v := c.SuggestedNoteID
		out.SuggestedNoteID = &v
	}
	if c.SuggestedTitle != "" {
		v := c.SuggestedTitle
		out.SuggestedTitle = &v
	}
	if c.AppendedAt != 0 {
		v := model.FormatTime(time.Unix(c.AppendedAt, 0))
		out.AppendedAt = &v
	}
	if c.DurationMS != 0 {
		v := c.DurationMS
		out.DurationMS = &v
	}
	return out
}

func capturesOf(in []model.CaptureIndex) []Capture {
	out := make([]Capture, 0, len(in))
	for _, c := range in {
		out = append(out, captureOf(c))
	}
	return out
}

// RecordingURL is one entry of the OpenAPI RecordingURLs schema: a presigned
// GET for one recording's audio and the filename to save it under.
type RecordingURL struct {
	CaptureID string `json:"capture_id"`
	Filename  string `json:"filename"`
	URL       string `json:"url"`
	ExpiresAt string `json:"expires_at"`
}

// RecordingURLs is the OpenAPI RecordingURLs schema: every recording of a note
// that still has its audio, oldest first, in one response.
type RecordingURLs struct {
	Items []RecordingURL `json:"items"`
}

func recordingURLsOf(in []service.RecordingURL) RecordingURLs {
	out := RecordingURLs{Items: make([]RecordingURL, 0, len(in))}
	for _, u := range in {
		out.Items = append(out.Items, RecordingURL{
			CaptureID: u.CaptureID,
			Filename:  u.Filename,
			URL:       u.URL,
			ExpiresAt: model.FormatTime(u.ExpiresAt),
		})
	}
	return out
}

// Upload is one presigned PUT the client must perform verbatim.
//
// Headers reaches the client unmodified. Every entry is inside the signature —
// x-amz-tagging in particular, which is what binds the retention tag — so
// dropping one produces a 403 rather than an untagged object.
type Upload struct {
	URL       string            `json:"url"`
	ExpiresAt string            `json:"expires_at"`
	MaxBytes  int64             `json:"max_bytes,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
}

func uploadOf(p upload.Presigned) Upload {
	return Upload{
		URL:       p.URL,
		ExpiresAt: model.FormatTime(p.ExpiresAt),
		MaxBytes:  p.MaxBytes,
		Headers:   p.Headers,
	}
}

// CaptureCreated is the OpenAPI CaptureCreated schema.
type CaptureCreated struct {
	Capture     Capture `json:"capture"`
	Upload      Upload  `json:"upload"`
	PeaksUpload *Upload `json:"peaks_upload,omitempty"`
}

// Settings is the OpenAPI Settings schema.
//
// DailySpendCapMicros is the one field that is not the tenant's: it is the
// instance-wide daily provider budget from the template, reported so the UI can
// show the ceiling that is actually enforced. There is no per-tenant cap: one
// instance-wide counter is the whole enforcement.
type Settings struct {
	CleanupMode         string `json:"cleanup_mode"`
	RetentionDays       int    `json:"retention_days"`
	Theme               string `json:"theme"`
	DefaultLanguage     string `json:"default_language"`
	DailySpendCapMicros int64  `json:"daily_spend_cap_micros"`
}

// SettingsUpdate is the PUT /v1/settings request body.
//
// It accepts daily_spend_cap_micros and ignores it. decodeJSON refuses unknown
// fields, so dropping it from the schema would turn every save from a client
// built against the previous contract into a 400 — and the settings screen
// still sends it. The response says what was stored, which is how the client
// learns the value did not take.
type SettingsUpdate struct {
	CleanupMode         string `json:"cleanup_mode"`
	RetentionDays       int    `json:"retention_days"`
	Theme               string `json:"theme"`
	DefaultLanguage     string `json:"default_language"`
	DailySpendCapMicros int64  `json:"daily_spend_cap_micros"`
}

func (u SettingsUpdate) settings() model.Settings {
	return model.Settings{
		CleanupMode:     model.CleanupMode(u.CleanupMode),
		RetentionDays:   u.RetentionDays,
		Theme:           model.Theme(u.Theme),
		DefaultLanguage: u.DefaultLanguage,
	}
}

func settingsOf(s model.Settings, spendCapMicros int64) Settings {
	return Settings{
		CleanupMode:         string(s.CleanupMode),
		RetentionDays:       s.RetentionDays,
		Theme:               string(s.Theme),
		DefaultLanguage:     s.DefaultLanguage,
		DailySpendCapMicros: spendCapMicros,
	}
}

// writeJSON writes a 2xx JSON body. Non-2xx bodies go through httperr and are
// problem+json without exception.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
