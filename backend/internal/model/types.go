package model

import (
	"time"
)

// TimeLayout stores timestamps with fixed-width fractional seconds.
//
// time.RFC3339Nano trims trailing zeros from the fraction, so "…:00Z" sorts
// above "…:00.1Z" ('Z' > '.') and lexicographic order stops being chronological
// order. Every timestamp written by the backend uses this layout instead, so a
// plain string comparison is a valid ordering.
const TimeLayout = "2006-01-02T15:04:05.000000000Z07:00"

// FormatTime renders t in UTC using TimeLayout.
func FormatTime(t time.Time) string {
	return t.UTC().Format(TimeLayout)
}

// Now is FormatTime(time.Now()).
func Now() string {
	return FormatTime(time.Now())
}

// ParseTime parses a timestamp written by FormatTime, and also the RFC3339 and
// RFC3339Nano values written by earlier versions.
func ParseTime(s string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, s)
}

type CleanupMode string

const (
	CleanupFaithful CleanupMode = "faithful"
	CleanupPolished CleanupMode = "polished"
)

// Theme is the rendering palette the client asks for. It is stored server-side
// so a second device opens in the theme the first one chose.
type Theme string

const (
	ThemeInk      Theme = "ink"
	ThemeNocturne Theme = "nocturne"
	ThemeSystem   Theme = "system"
)

// MaxRetentionDays bounds the audio retention setting. Beyond ten years the
// value is not a policy, it is a typo.
const MaxRetentionDays = 3650

// RetentionTiers are the audio retention periods this system can actually
// enforce, in days, shortest first.
//
// It is a fixed set rather than any number the user types, and that is a
// property of S3 rather than a shortcut. An expiry is performed by a lifecycle
// rule, a rule carries its own ExpirationInDays, and a rule cannot read a
// number out of a DynamoDB item — so the only thing an upload can vary per user
// is WHICH rule matches, by way of an object tag. One rule per tier is
// therefore one tier per rule, and the set has to be small enough to write down
// in the template.
//
// The alternative that was actually shipped is worse: a free-text number that
// is validated, stored, returned and read by nothing, so a user asking for
// thirty days keeps their audio forever and is told otherwise.
var RetentionTiers = []int{7, 30, 90, 365}

// RetentionTierFor maps a requested retention to the tier that will enforce it.
//
// 0 means keep indefinitely and stays 0. Anything else resolves to the longest
// tier that is no longer than what was asked for, so a retention setting is
// honoured no later than requested — it is a promise to delete, and rounding it
// up would break that promise silently. A value shorter than the shortest tier
// is the one exception: there is nothing briefer to offer, so it becomes the
// shortest tier and the caller is told, because the alternative is to answer a
// request for two days with "forever".
func RetentionTierFor(days int) int {
	if days <= 0 {
		return 0
	}
	tier := RetentionTiers[0]
	for _, t := range RetentionTiers {
		if t <= days {
			tier = t
		}
	}
	return tier
}

// Transcription language. Groq's Whisper endpoint takes an optional ISO-639-1
// code; supplying it "will improve accuracy and latency" (console.groq.com/docs
// /speech-to-text), and omitting it leaves the model to guess, which on a
// short clip is how English dictation comes back transliterated into another
// script. There is no mixed-language mode: auto-detection is the only option
// for code-switching, and it decides per request, not per segment.
const (
	// LanguageAuto asks the provider to detect the language: the code sends no
	// `language` field.
	LanguageAuto = "auto"
	// DefaultLanguage is what a tenant transcribes in until they say otherwise.
	DefaultLanguage = "en"
)

// ValidLanguage reports whether v is LanguageAuto or a lowercase two-letter
// ISO-639-1 code. It checks the shape, not the list: Whisper's supported set
// changes with the model and a code it does not know is answered by the
// provider, not silently mapped to English here.
func ValidLanguage(v string) bool {
	if v == LanguageAuto {
		return true
	}
	if len(v) != 2 {
		return false
	}
	for _, r := range v {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return true
}

type Settings struct {
	CleanupMode   CleanupMode `json:"cleanup_mode"`
	RetentionDays int         `json:"retention_days"` // 0 = indefinite
	// Theme is empty on records written before v2; readers substitute ThemeInk.
	Theme Theme `json:"theme,omitempty"`
	// DefaultLanguage is the transcription language for a capture whose
	// destination note sets none, or is not yet known when transcription runs
	// (a capture without note_id is transcribed before it is routed). Empty on
	// records written before 2026-09; readers substitute DefaultLanguage.
	DefaultLanguage string `json:"default_language,omitempty"`
	// There is no per-tenant spend cap. Records written before 2026-09 may
	// carry a daily_spend_cap_micros field; encoding/json drops it on read.
}

// MaxSearchTextBytes caps NoteIndex.SearchText. DynamoDB's item limit is
// 400 KB, the rest of a note row is well under 10 KB, and 32 KB of lowercased
// text is roughly 5,000 words — more than any dictation session produces
// before the note is split. The cap is on bytes because that is what the
// table charges for and enforces; the cut lands on a rune boundary.
const MaxSearchTextBytes = 32 << 10

type NoteIndex struct {
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	Aliases       []string `json:"aliases"`
	Tags          []string `json:"tags,omitempty"`
	Snippet       string   `json:"snippet,omitempty"` // first ~500 runes of note for light match
	CreatedAt     string   `json:"created_at,omitempty"`
	UpdatedAt     string   `json:"updated_at"`
	S3MarkdownKey string   `json:"s3_markdown_key"`
	S3MetaKey     string   `json:"s3_meta_key"`
	DeletedAt     string   `json:"deleted_at,omitempty"`
	PurgeAfter    string   `json:"purge_after,omitempty"`
	// Verbatim bypasses cleanup for this note entirely. Dictated content that
	// must not be reworded — a spec, a quote, a prompt — is otherwise silently
	// rewritten by polished mode.
	Verbatim bool `json:"verbatim,omitempty"`
	// Language is the transcription language for captures recorded INTO this
	// note (LanguageAuto or an ISO-639-1 code). Empty means the tenant's
	// Settings.DefaultLanguage. It only applies when the capture was started
	// with this note as its target: a capture that is routed afterwards was
	// transcribed before the destination was known.
	Language string `json:"language,omitempty"`
	// SearchText is the note body prepared for GET /v1/search: lowercased,
	// append markers stripped, capped at MaxSearchTextBytes. It lives on the
	// index row so a search over the tenant's notes is one partition query
	// rather than one S3 GET per note, and it is written wherever the body is
	// (service.NotesService.UpdateNote, the worker's index refresh, the
	// chintanctl backfill).
	//
	// It is deliberately kept out of the JSON blob the DynamoDB store keeps in
	// `data`: the blob duplicates every promoted attribute, and duplicating a
	// 32 KB field doubles the write units of every note save for nothing a
	// reader needs. The store promotes it as its own attribute and reads it back
	// only when a list asks for it (repository.ListOptions.IncludeSearchText).
	SearchText string `json:"-"`
	// PurgeAfterEpoch is the same instant as PurgeAfter as a Unix second count.
	// The archived list filters on it, and the weekly expiry sweep
	// (internal/purge) deletes the note's objects and row once it has passed.
	// The store also derives the DynamoDB TTL attribute from it, later by a
	// grace period, as the backstop for a sweep that did not run.
	PurgeAfterEpoch int64 `json:"purge_after_epoch,omitempty"`
	// Version is the optimistic-concurrency counter. A write carries the version
	// it read; the store rejects it if the stored version has moved on.
	Version int64 `json:"version"`
}

// CaptureStatus is where a capture sits in the pipeline. It is a string type, so
// promoting a constant from another package into this one changes no stored
// value and no wire representation.
type CaptureStatus string

const (
	StatusUploaded    CaptureStatus = "uploaded"
	StatusTranscribed CaptureStatus = "transcribed"
	StatusCleaned     CaptureStatus = "cleaned"
	StatusAppended    CaptureStatus = "appended"
	StatusFailed      CaptureStatus = "failed"
	// StatusNeedsTarget means the transcript was understood but the destination
	// note is uncertain, so the user has to confirm before anything is written.
	StatusNeedsTarget CaptureStatus = "needs_target"
	// StatusNoContent means the recording was nothing but an instruction to the app,
	// such as "create a note called test123", so there was no dictation to write.
	StatusNoContent CaptureStatus = "no_content"

	// The five below arrived with the asynchronous pipeline and lived in
	// internal/service until the API surface landed. They are the in-progress
	// stages the frontend's progress card polls, plus the distinct outcome a
	// spend cap produces.

	// StatusTranscribing means the recording is with the speech provider.
	StatusTranscribing CaptureStatus = "transcribing"
	// StatusRouting means the destination note is being decided.
	StatusRouting CaptureStatus = "routing"
	// StatusCleaning means the transcript is with the cleanup model.
	StatusCleaning CaptureStatus = "cleaning"
	// StatusAppending means the append claim is held and the text is going into
	// the note body.
	StatusAppending CaptureStatus = "appending"
	// StatusSpendCapped means the tenant's daily provider spend cap stopped the
	// call. It is deliberately distinct from failed so the UI can explain a
	// budget decision rather than report a fault.
	StatusSpendCapped CaptureStatus = "spend_capped"
)

type CaptureIndex struct {
	ID        string        `json:"id"`
	NoteID    string        `json:"note_id"`
	UserID    string        `json:"user_id"`
	Status    CaptureStatus `json:"status"`
	Mode      CleanupMode   `json:"cleanup_mode"`
	AudioKey  string        `json:"audio_key"`
	RawKey    string        `json:"raw_key"`
	RoutedKey string        `json:"routed_key,omitempty"`
	CleanKey  string        `json:"clean_key"`
	Error     string        `json:"error,omitempty"`
	CreatedAt string        `json:"created_at"`

	// Routing suggestion, set when the destination could not be decided confidently.
	SuggestedNoteID string  `json:"suggested_note_id,omitempty"`
	SuggestedTitle  string  `json:"suggested_title,omitempty"`
	RouteConfidence float64 `json:"route_confidence,omitempty"`

	// Version is the optimistic-concurrency counter.
	Version int64 `json:"version"`
	// AppendToken is claimed before the capture's text is written into the note
	// and is what makes the append idempotent: a retry that finds its own token
	// already recorded must not append again.
	AppendToken string `json:"append_token,omitempty"`
	// AppendClaimedAt is when AppendToken was claimed. A claim older than
	// AppendClaimLease is assumed abandoned so a dead worker cannot strand the
	// capture forever.
	AppendClaimedAt int64 `json:"append_claimed_at,omitempty"`
	// AppendedAt is set only once the text is durably in the note body.
	AppendedAt int64 `json:"appended_at,omitempty"`

	DurationMS  int64  `json:"duration_ms,omitempty"`
	SegmentsKey string `json:"segments_key,omitempty"`
	PeaksKey    string `json:"peaks_key,omitempty"`
}
