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

// NoteCleanMode is how the whole-note cleaned view is written. It is a
// different axis from CleanupMode, which governs each recording's transcript
// as it is appended: the cleaned view is one pass over the entire body after
// the fact, and its modes describe a document, not a paragraph.
type NoteCleanMode string

const (
	// NoteCleanPolished rewrites the body as coherent prose with a light touch
	// on wording — no headings, no lists.
	NoteCleanPolished NoteCleanMode = "polished"
	// NoteCleanStructured rewrites the body as an organised Markdown document:
	// short headings, lists for enumerations, filler and repetition removed.
	NoteCleanStructured NoteCleanMode = "structured"
	// DefaultNoteCleanMode is what a note cleans in until it says otherwise.
	DefaultNoteCleanMode = NoteCleanStructured
)

// ValidNoteCleanMode reports whether m names a whole-note cleanup mode.
func ValidNoteCleanMode(m NoteCleanMode) bool {
	return m == NoteCleanPolished || m == NoteCleanStructured
}

// Bounds on the whole-note cleaned view. Both exist to keep the note row
// under DynamoDB's 400 KB item limit by construction: the row already carries
// up to MaxSearchTextBytes of search text and a few KB of everything else, so
// a body the worker will clean is capped on the way in and the result is
// capped on the way out, and a note past either limit gets a stored error
// rather than a truncated document presented as the whole.
const (
	// MaxCleanNoteInputBytes is the largest note body (markers stripped) the
	// worker will hand to the cleanup model as one document.
	MaxCleanNoteInputBytes = 150 << 10
	// MaxCleanedBodyBytes is the largest cleaned view the row will store.
	MaxCleanedBodyBytes = 200 << 10
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
	// Theme is empty on records written before it existed; readers substitute
	// ThemeInk.
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
	// AutoClean asks the worker to regenerate the cleaned view after every
	// change to the body it makes or is told about: an append, a recording
	// moved in or out, a recording deleted. Off by default because each run is
	// one LLM call over the whole note.
	AutoClean bool `json:"auto_clean,omitempty"`
	// CleanMode is the mode an automatic or unspecified clean uses. Empty means
	// DefaultNoteCleanMode.
	CleanMode NoteCleanMode `json:"clean_mode,omitempty"`
	// CleanedBody is the whole-note cleaned view, generated by the worker's
	// clean-note task from the body as it stood at CleanedAt. Read-only from
	// the API: a user edit belongs in the body, and the view is regenerated
	// from it. Like SearchText it is a promoted attribute the record blob does
	// not duplicate, and a list carries it only when asked
	// (repository.ListOptions.IncludeCleanedBody).
	CleanedBody string `json:"-"`
	// CleanedMode is the mode CleanedBody was generated in.
	CleanedMode NoteCleanMode `json:"cleaned_mode,omitempty"`
	// CleanedAt is when CleanedBody was generated, or when the last attempt
	// failed if there is no body.
	CleanedAt string `json:"cleaned_at,omitempty"`
	// CleanedStale is set by every writer of the body once a cleaned view
	// exists, and cleared when the view is regenerated from the current body.
	CleanedStale bool `json:"cleaned_stale,omitempty"`
	// CleanedError is the fixed, user-facing reason the last clean-note run
	// produced no view. A successful run clears it. It never carries provider
	// text.
	CleanedError string `json:"cleaned_error,omitempty"`
	// CleanedRequestedAt and CleanedRequestedMode are the clean-note run most
	// recently handed to the worker: when, and in which mode. The request path
	// stamps them before it invokes (service.RecordCleanRequest) and answers a
	// repeat in the same mode without a second hand-off while the stamp is
	// younger than service.CleanNoteTimeout; the worker writes nothing once the
	// stamp is no longer the one it was invoked for, and clears both when its
	// run reaches a verdict. Promoted and projected like the fields above it,
	// and never on the wire.
	CleanedRequestedAt   string        `json:"cleaned_requested_at,omitempty"`
	CleanedRequestedMode NoteCleanMode `json:"cleaned_requested_mode,omitempty"`
	// AppendingCapture and AppendingAt say a capture's paragraph is on its way
	// into the body: the worker stamps them, together with a version bump,
	// after it takes the append claim and before it writes the body
	// (repository.Store.StampNoteAppend), and clears them when the index
	// refresh that follows the write lands or the claim is handed back. While
	// the stamp is younger than repository.AppendClaimLease, PATCH refuses a
	// body write with 409 `append_in_progress` (service.UpdateNote): the
	// version alone cannot witness a body write the worker has made but not yet
	// indexed, and an editor save landing in that window carried the marker
	// forward and dropped the paragraph. Promoted and projected like the stamp
	// above, and never on the wire.
	AppendingCapture string `json:"appending_capture,omitempty"`
	AppendingAt      string `json:"appending_at,omitempty"`
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

// TargetSource records who chose a capture's destination note. It exists so
// the client can tell a recording a person aimed — "Record into this", or a
// note picked for a needs_target capture — from one the router filed: the
// first is already visible under its note and the Home screen's filing rows
// show only the second. A capture written before the field existed reads as
// "", which the wire promotes to targeted=false; for such a capture whose
// note_id was set at creation there is no way to recover, after the fact,
// that a person chose it, and that is accepted.
type TargetSource string

const (
	// TargetSourceClient means the request that began the capture named the
	// note (POST /v1/captures with note_id).
	TargetSourceClient TargetSource = "client"
	// TargetSourceUser means a person chose the note for a capture that had
	// none (POST /v1/captures/{id}/target).
	TargetSourceUser TargetSource = "user"
	// TargetSourceRouter means the pipeline decided.
	TargetSourceRouter TargetSource = "router"
)

// Targeted reports whether a person, rather than the router, chose the
// destination.
func (t TargetSource) Targeted() bool {
	return t == TargetSourceClient || t == TargetSourceUser
}

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

	// TargetSource says who set NoteID. Empty on rows written before 2026-09
	// and on captures nothing has decided for yet.
	TargetSource TargetSource `json:"target_source,omitempty"`

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
	// AudioBytes is the uploaded object's size as S3 reported it in the
	// notification that started the pipeline — the only measurement of the
	// recording this system ever gets (the request-time size_bytes is the
	// client's claim). Zero on captures processed before 2026-09 and on ones
	// the worker has not yet seen; GET /v1/usage sums it as storage.
	AudioBytes int64 `json:"audio_bytes,omitempty"`
}

// ---------------------------------------------------------------------------
// Ask (backlog D5)
// ---------------------------------------------------------------------------

// AskStatus is where a question sits: written by the API and pending, then
// answered or failed by the worker.
type AskStatus string

const (
	AskPending  AskStatus = "pending"
	AskAnswered AskStatus = "answered"
	AskFailed   AskStatus = "failed"
)

// AskTurn is one earlier exchange of the conversation the question continues.
// It is context for the model so a follow-up ("and when was that?") resolves;
// retrieval never reads it.
type AskTurn struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// AskSource is one note the answer was drawn from: a note that was actually
// packed into the prompt AND that the model cited.
type AskSource struct {
	NoteID string `json:"note_id"`
	Title  string `json:"title"`
}

// AskTTL is how long an ask row lives. A question and its answer are a
// conversation turn, not a record: the client reads the answer within seconds
// and keeps its own history, so a day is generous.
const AskTTL = 24 * time.Hour

// Ask is one question over the tenant's notes and, once the worker has run,
// its answer. It is stored under the tenant's partition (sk ASK#<id>) with a
// TTL, so it is exported, backed up and erased with the tenant and expires on
// its own.
type Ask struct {
	ID       string    `json:"id"`
	UserID   string    `json:"user_id"`
	Status   AskStatus `json:"status"`
	Question string    `json:"question"`
	History  []AskTurn `json:"history,omitempty"`
	// Answer is plain text, possibly with simple Markdown, once answered.
	Answer string `json:"answer,omitempty"`
	// Grounded is true when the answer was drawn from the notes and false
	// when the honest answer was "that is not in your notes".
	Grounded bool `json:"grounded,omitempty"`
	// Sources is written as [] rather than omitted once the row is answered,
	// so a reader can tell "answered with no sources" from "not yet answered".
	Sources []AskSource `json:"sources"`
	// Error is the fixed user-facing sentence when Status is failed. It never
	// carries a provider's words.
	Error string `json:"error,omitempty"`
	// NotesConsidered is how many notes were in the retrieval window.
	NotesConsidered int    `json:"notes_considered"`
	CreatedAt       string `json:"created_at"`
	AnsweredAt      string `json:"answered_at,omitempty"`
	// ExpiresAt is the row's TTL as a Unix second count.
	ExpiresAt int64 `json:"expires_at"`
}
