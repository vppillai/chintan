package model

import (
	"strings"
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
	// NoteCleanTasks rewrites a checklist as granular, actionable tasks: an
	// item holding several actions becomes one item per action, done items are
	// kept verbatim and in place, order is otherwise kept, nothing is invented
	// or merged. It is the only mode a checklist cleans in and means nothing
	// for a plain note (service.CheckCleanMode).
	NoteCleanTasks NoteCleanMode = "tasks"
	// DefaultNoteCleanMode is what a plain note cleans in until it says
	// otherwise.
	DefaultNoteCleanMode = NoteCleanStructured
)

// ValidNoteCleanMode reports whether m names a whole-note cleanup mode. Which
// modes a given note may use is service.CheckCleanMode's question.
func ValidNoteCleanMode(m NoteCleanMode) bool {
	return m == NoteCleanPolished || m == NoteCleanStructured || m == NoteCleanTasks
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
// script. There is no mixed-language mode, and auto-detection is not one: it
// decides per request, not per segment, so on code-switched speech it keeps
// the language it picked and drops or re-scripts the rest — real Malayalam
// came back in Tamil script, and the Malayalam sentence of an
// English–Malayalam clip was dropped (review 2026-09-21, T3). For mixed speech
// the better choice is the code of the language that matters most: forcing
// `ml` kept both languages and transcribed pure English verbatim.
const (
	// LanguageAuto asks the provider to detect the language: the code sends no
	// `language` field.
	LanguageAuto = "auto"
	// DefaultLanguage is what a tenant transcribes in until they say otherwise.
	DefaultLanguage = "en"
)

// languageNames maps the ISO-639-1 codes the settings screen offers to the
// name Whisper reports a detected language under (its `language` field is a
// lowercase English name, "english" or "malayalam", never a code). The list is
// the frontend's curated one (features/settings/languages.ts), so the two
// sides name the same languages; Whisper's own spelling is used where it
// differs ("chinese" for zh). A code outside the list is still valid
// (ValidLanguage checks the shape), it just has no name here.
var languageNames = map[string]string{
	"en": "english", "ml": "malayalam", "hi": "hindi", "ta": "tamil", "te": "telugu",
	"kn": "kannada", "mr": "marathi", "bn": "bengali", "gu": "gujarati", "pa": "punjabi",
	"ur": "urdu", "ar": "arabic", "es": "spanish", "fr": "french", "de": "german",
	"pt": "portuguese", "it": "italian", "nl": "dutch", "ru": "russian", "ja": "japanese",
	"ko": "korean", "zh": "chinese", "id": "indonesian", "tr": "turkish", "vi": "vietnamese",
	"th": "thai", "sv": "swedish", "pl": "polish",
}

var languageCodes = func() map[string]string {
	out := make(map[string]string, len(languageNames))
	for code, name := range languageNames {
		out[name] = code
	}
	return out
}()

// LanguageName is the English name for an ISO-639-1 code, capitalised, or ""
// for a code the table does not know.
func LanguageName(code string) string {
	name, ok := languageNames[code]
	if !ok {
		return ""
	}
	return strings.ToUpper(name[:1]) + name[1:]
}

// LanguageCode is the ISO-639-1 code for the language name Whisper reported,
// or "" when the name is not one the table knows.
func LanguageCode(whisperName string) string {
	return languageCodes[strings.ToLower(strings.TrimSpace(whisperName))]
}

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
	// destination note sets none, and the first language every routed
	// capture is transcribed in (routing reads the transcript, so it runs
	// after transcription; the worker transcribes again in the destination's
	// language when that differs). Empty on records written before 2026-09;
	// readers substitute DefaultLanguage.
	DefaultLanguage string `json:"default_language,omitempty"`
	// There is no per-tenant spend cap. Records written before 2026-09 may
	// carry a daily_spend_cap_micros field; encoding/json drops it on read.
}

// NoteKindChecklist is the one non-default NoteIndex.Kind. A checklist body is
// GitHub task-list syntax, one item per line: "- [ ] text" open, "- [x] text"
// done; blank lines are ignored by every reader. The worker appends each
// recording as the open items it named, one line each under the recording's
// marker, and the cleaned view runs in NoteCleanTasks.
const NoteKindChecklist = "checklist"

// ValidNoteKind reports whether k is a stored note kind.
func ValidNoteKind(k string) bool {
	return k == "" || k == NoteKindChecklist
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
	// Language is the transcription language for this note's captures
	// (LanguageAuto or an ISO-639-1 code). Empty means the tenant's
	// Settings.DefaultLanguage. A capture recorded into the note is
	// transcribed in it from the start; one the router files here afterwards
	// was transcribed in the default first, and the worker transcribes it
	// once more in this language when the two differ (pipeline.run).
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
	// Kind says what the body is: "" for a plain note, NoteKindChecklist for a
	// checklist whose body is one task-list item per line (see
	// docs/design/checklists.md). Promoted like Language, and absent on rows
	// written before 2026-09-21, which are plain notes; the wire maps "" to
	// "note" so a client never sees the storage default.
	Kind string `json:"kind,omitempty"`
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
	// PinnedAt and PinRank put the note in the Pinned group at the top of Home.
	// PinnedAt is when a person pinned it (empty means not pinned); PinRank is
	// its place among the pinned notes, PinRankStep apart, so a drag rewrites
	// the ranks of the moved notes alone. Both are promoted attributes, written
	// only when set, so a row from before 2026-09-24 needs no backfill and
	// reads as unpinned. Archiving clears both: the archive is never pinned.
	PinnedAt string `json:"pinned_at,omitempty"`
	PinRank  int64  `json:"pin_rank,omitempty"`
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

// Pinned reports whether the note is in the Pinned group.
func (n NoteIndex) Pinned() bool { return n.PinnedAt != "" }

// Pin bounds. Fifty pins is a shelf, not a second list; the step leaves room
// between two ranks so a future insert-between needs no renumbering.
const (
	MaxPinnedNotes = 50
	PinRankStep    = 1000
)

// CaptureStatus is where a capture sits in the pipeline. It is a string type, so
// promoting a constant from another package into this one changes no stored
// value and no wire representation.
type CaptureStatus string

// IsTerminalStatus reports whether the pipeline will not move a capture on
// from s by itself: it finished (appended, no_content), it stopped on a
// verdict (failed, spend_capped), or it is waiting on a person (needs_target).
// This is the one definition; the worker, the API's retry and `chintanctl
// reconcile` all read it, and the frontend's TERMINAL_CAPTURE_STATUSES lists
// the same five. Until 2026-09-05 there were three lists that disagreed, and
// reconcile reported every spend_capped capture as stuck.
func IsTerminalStatus(s CaptureStatus) bool {
	switch s {
	case StatusAppended, StatusNoContent, StatusFailed, StatusSpendCapped, StatusNeedsTarget:
		return true
	default:
		return false
	}
}

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
	// LastProgressAt is when the pipeline last wrote this row, or when the API
	// last handed the capture to the worker. It is what says whether a worker
	// can still be alive on this capture: the worker function's timeout is
	// 900 s, so a row untouched for longer than that has no live worker, and a
	// retry may start one without starting a second delivery
	// (service.CaptureStuckAfter). Empty on rows written before 2026-09-05,
	// which read as CreatedAt. In the record blob only; nothing lists on it.
	LastProgressAt string `json:"last_progress_at,omitempty"`

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
	// Language is the language the transcript at RawKey was asked for:
	// LanguageAuto or the ISO-639-1 code sent to the provider, written in
	// the same persist as RawKey. It is what tells a retry that the
	// transcript already is in the destination note's language, so the
	// post-routing re-transcription (pipeline.run) happens once and not on
	// every delivery. Empty on captures transcribed before 2026-09-21, which
	// therefore get one re-transcription when their note names a language.
	Language string `json:"language,omitempty"`
	// RequestedLanguage is the language a person asked this recording to be
	// transcribed in (POST /v1/captures/{id}/retranscribe): LanguageAuto or a
	// code, or "" when nobody did. It outranks the note's language and the
	// default, and it stays on the row so the post-routing re-transcription
	// never undoes an explicit choice.
	RequestedLanguage string `json:"requested_language,omitempty"`
	// LanguageDetected is the language the provider reported the speech to
	// be, spelled as the provider spells it (Groq: `English`, `Malayalam`),
	// alongside the transcript it produced.
	// Kept for the cleanup prompt and for the operator; segments.json carries
	// the same value for the client.
	LanguageDetected string `json:"language_detected,omitempty"`

	// Source says what made the recording: "" for the app itself, or
	// DeviceSource(id) for a request a device key made to the inbox
	// (docs/design/inbox.md), so the recordings row can say which device it
	// came from. In the record blob only; the wire maps "" to "app".
	Source string `json:"source,omitempty"`

	// AudioBytes is the uploaded object's size as S3 reported it in the
	// notification that started the pipeline — the only measurement of the
	// recording this system ever gets (the request-time size_bytes is the
	// client's claim). Zero on captures processed before 2026-09 and on ones
	// the worker has not yet seen; GET /v1/usage sums it as storage.
	AudioBytes int64 `json:"audio_bytes,omitempty"`

	// RecordedAt is the sender's own claim of when the recording was made
	// (RFC 3339 UTC), from X-Chintan-Recorded-At or the ring form's
	// recordedAt part; empty when nothing was sent or the value was not
	// usable. StageAt is when each status was first written, status → RFC
	// 3339, stamped by every persist. Both are the timing record: they live
	// in the record blob only, never on the wire (handler.captureOf leaves
	// them out) and never in the app; the worker's CaptureQueueDelay and
	// CaptureEndToEnd metrics and `chintanctl latency` are what read them.
	RecordedAt string            `json:"recorded_at,omitempty"`
	StageAt    map[string]string `json:"stage_at,omitempty"`
}

// StageEntered records at as the moment status was first written, and
// leaves an earlier entry alone, so a retry that walks a stage again does
// not move its time. The map is copied rather than written in place: the
// memory store hands back the very struct it holds, and a write through a
// shared map would change a stored row before its conditional write.
func (c *CaptureIndex) StageEntered(status CaptureStatus, at string) {
	if _, ok := c.StageAt[string(status)]; ok {
		return
	}
	stages := make(map[string]string, len(c.StageAt)+1)
	for k, v := range c.StageAt {
		stages[k] = v
	}
	stages[string(status)] = at
	c.StageAt = stages
}

// ---------------------------------------------------------------------------
// Devices (the inbox for external recorders; docs/design/inbox.md)
// ---------------------------------------------------------------------------

// Device is one key a person issued to a recorder, watch or app so it can
// drop audio or text into their notes over the inbox routes. The key itself
// is shown once at creation and never stored: KeyHash is its SHA-256, and the
// inbox compares hashes in constant time. It is stored under the tenant's
// partition (sk DEVICE#<id>) with sparse GSI1 keys on the key id, so the
// inbox can find the tenant from the key alone.
type Device struct {
	ID       string `json:"id"`
	TenantID string `json:"tenant_id"`
	// Name is what the person called it (≤ MaxDeviceNameRunes runes); it is
	// what a recording's "From ⟨device⟩" says.
	Name    string `json:"name"`
	KeyHash string `json:"key_hash"`
	// CreatedAt and LastUsedAt are for the device list. LastUsedAt is
	// written with the request counter and is empty until the first use.
	CreatedAt  string `json:"created_at"`
	LastUsedAt string `json:"last_used_at,omitempty"`
	// RequestsDay counts the key's inbox requests on RequestsDayDate (a UTC
	// calendar day); the day rolling over resets it. DeviceDailyRequestLimit
	// bounds it.
	RequestsDay     int64  `json:"requests_day,omitempty"`
	RequestsDayDate string `json:"requests_day_date,omitempty"`
	// RequestsMonth and BytesMonth count the key's accepted inbox requests
	// and their body bytes in Month (UTC yyyy-mm); a new month starts them
	// over. They ride the same write as the day counter, so they cost no
	// extra request, and they are what the Devices card's "sent this month"
	// reads. Requests, not recordings: a two-step capture is two of them.
	RequestsMonth int64  `json:"requests_month,omitempty"`
	BytesMonth    int64  `json:"bytes_month,omitempty"`
	Month         string `json:"month,omitempty"`
	// RevokedAt is set by DELETE /v1/devices/{id}. A revoked row keeps its
	// hash but loses its GSI1 keys, so the key is unknown to the inbox from
	// then on; the row itself expires RevokedDeviceRetention later.
	RevokedAt string `json:"revoked_at,omitempty"`
	// Version is the optimistic-concurrency counter, as on a note. The
	// inbox's counter write is conditional on it, so a revoke that lands
	// between the inbox's read and its write is not overwritten by the write.
	Version int64 `json:"version"`
}

// Revoked reports whether the key has been revoked.
func (d Device) Revoked() bool { return d.RevokedAt != "" }

// DeviceSource is CaptureIndex.Source for a capture a device made.
func DeviceSource(deviceID string) string { return "device:" + deviceID }

// Device bounds. Ten devices is a household of gadgets, not a fleet; two
// hundred requests a day is one every seven minutes around the clock, which
// bounds what a leaked key can cost before its owner notices.
const (
	MaxDevicesPerTenant     = 10
	MaxDeviceNameRunes      = 60
	DeviceDailyRequestLimit = 200
	// RevokedDeviceRetention is how long a revoked row stays for the record
	// before DynamoDB TTL drops it.
	RevokedDeviceRetention = 30 * 24 * time.Hour
)

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
