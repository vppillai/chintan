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
// special-case an empty collection, and v1's bare array plus an X-Next-Cursor
// header meant a caller had to read the headers to know there was more.
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

// NoteDetail is the OpenAPI NoteDetail schema: a note, its body, and its
// captures.
type NoteDetail struct {
	Note
	Body     string    `json:"body"`
	Captures []Capture `json:"captures"`
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
		// A capture created before v2 has neither, and the client renders a
		// plain player rather than an empty waveform.
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
type Settings struct {
	CleanupMode         string `json:"cleanup_mode"`
	RetentionDays       int    `json:"retention_days"`
	Theme               string `json:"theme"`
	DailySpendCapMicros int64  `json:"daily_spend_cap_micros"`
}

func settingsOf(s model.Settings) Settings {
	return Settings{
		CleanupMode:         string(s.CleanupMode),
		RetentionDays:       s.RetentionDays,
		Theme:               string(s.Theme),
		DailySpendCapMicros: s.DailySpendCapMicros,
	}
}

// writeJSON writes a 2xx JSON body. Non-2xx bodies go through httperr and are
// problem+json without exception.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
