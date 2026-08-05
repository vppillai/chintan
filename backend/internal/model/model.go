// Package model holds the persisted entity types.
//
// The vocabulary here is §1.3's object model, used with exactly those meanings and
// no others:
//
//	Session ──produces──▶ Capture ──extracted into──▶ Item ──filed into──▶ Thread
//	(one recording)        (one transcript)            (one discrete thing)  (ongoing topic)
//
// Two consequences a reader should not have to infer, both from §1.3:
//
//   - A session and its capture are one-to-one. They are separate entities because a
//     session records *how the audio arrived* — trigger, device, clocks — and a
//     capture records *what was said*. Neither ever fans out to several of the other.
//   - Items are views over captures, never replacements. Deleting or dismissing an
//     item leaves the capture untouched (§3A.1).
//
// **The word "note" does not appear in this system** — not in code, UI copy, schemas,
// or documentation (§1.3). The only surviving occurrence is inside the frozen system
// identifier `voicenotes`, which users never see.
package model

import "github.com/vppillai/chintan/backend/internal/keys"

// ---------------------------------------------------------------------------
// Tenant
// ---------------------------------------------------------------------------

// Tenant is the top-level isolation unit (I11).
//
// During the personal phase tenant_id == user_id, but nothing in the code assumes
// it. Building tenancy now costs nothing; retrofitting it into a populated
// single-table design is a full data migration under load (§2A.1).
type Tenant struct {
	TenantID keys.TenantID `json:"tenant_id" dynamodbav:"-"`
	Plan     string        `json:"plan"`

	// Region is the data-residency field. If the product ever sells into the EU, a
	// design assuming one global region is a rebuild; an attribute plus
	// region-scoped resource naming costs nothing today (§2A.1).
	Region string `json:"region"`

	// KMSKeyID is the per-tenant key reference (§2A.1, I8).
	//
	// In the personal phase this records the AWS-managed key in use
	// (alias/aws/s3, alias/aws/dynamodb) — there is no CMK yet. **It is never null
	// and never absent**, because a resolver with nothing to resolve is how the
	// indirection quietly stops being exercised (§6.3). Pointing a tenant at a
	// customer-managed key later then becomes a provisioning change rather than a
	// re-encryption of the entire corpus.
	KMSKeyID string `json:"kms_key_id"`

	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`

	// Consent is keyed by purpose (I14). Absence of a purpose is refusal — see
	// the consent package, which fails closed rather than defaulting to granted.
	Consent map[string]ConsentGrant `json:"consent"`
}

// ConsentGrant records one purpose's consent state, with the timestamp and version
// it was granted under.
//
// The version matters for withdrawal: §Phase 4 requires that "a later consent
// withdrawal must be able to identify and purge exactly the affected records", which
// is only possible if each stored record carries the version it was collected under.
type ConsentGrant struct {
	Granted bool   `json:"granted"`
	TS      string `json:"ts"`
	Version string `json:"version"`
}

// Consent purposes (I14). Recognised values are enumerated rather than free-form so
// that a typo becomes an unrecognised purpose — which fails closed — instead of a
// silently-granted one.
const (
	// PurposeCorpusRetention covers storing the (audio, L0, L2) triples that
	// accumulate a personal gold-transcript corpus (§Phase 4).
	//
	// The distinction §Phase 4 draws is important and easy to lose: deriving a
	// correction rule in flight is operating the service the user asked for, and
	// needs no separate consent. *Retaining the source pair* is a different purpose
	// and needs its own. Without consent, corrections still work; only the corpus
	// is not kept.
	PurposeCorpusRetention = "corpus_retention"

	// PurposeModelImprovement covers any future training use.
	PurposeModelImprovement = "model_improvement"
)

// ---------------------------------------------------------------------------
// Session and Capture
// ---------------------------------------------------------------------------

// TriggerSource records how recording was started (§5.1). Stamped onto the session
// from Phase 1 so no schema migration is needed when Phase 8 adds hardware triggers.
type TriggerSource string

const (
	TriggerUI          TriggerSource = "ui"           // on-screen control (Phase 1)
	TriggerVoiceLaunch TriggerSource = "voice_launch" // Assistant deep link (Phase 1)
	TriggerNFC         TriggerSource = "nfc"          // NDEF tag (Phase 8)
	TriggerBLEHID      TriggerSource = "ble_hid"      // media-key remote (Phase 8)
	TriggerBLEGATT     TriggerSource = "ble_gatt"     // custom GATT (Phase 8)

	// Provenance-only sources (§5.2 rule 6). Never implemented as an adapter,
	// because no browser trigger is involved: `auto` is emitted by the controller
	// itself when resuming a session interrupted by a crash or reload (I2), and
	// `telegram` originates server-side. They exist so session provenance is
	// uniform across origins.
	TriggerAuto     TriggerSource = "auto"
	TriggerTelegram TriggerSource = "telegram"
)

// IngestSource records how the audio arrived (§5A.2).
//
// Only `app` has client-side VAD, trustworthy timestamps, or pre-segmented audio.
// Everything downstream of ingestion must be identical regardless of origin, so the
// pipeline must never assume audio originated in the browser (§5A.1).
type IngestSource string

const (
	IngestApp          IngestSource = "app"
	IngestTelegram     IngestSource = "telegram"
	IngestDeviceImport IngestSource = "device_import"

	// IngestAPI is reserved for a future authenticated ingestion endpoint. No
	// phase in the spec implements it; reject it at the adapter boundary until one
	// does (§5A.2).
	IngestAPI IngestSource = "api"
)

// Session is one recording event — a button press to a stop, or one continuous
// stretch of an imported file after session splitting (§5A.3.3). Immutable once
// closed.
type Session struct {
	SessionID     string        `json:"session_id"`
	CaptureID     string        `json:"capture_id"`
	TriggerSource TriggerSource `json:"trigger_source"`
	IngestSource  IngestSource  `json:"ingest_source"`

	// ContentHash is the SHA-256 of the raw bytes as delivered. Required, and
	// checked before processing: re-importing the same file — which will happen,
	// because users plug the device in twice — must produce no duplicate captures
	// and no duplicate provider spend (§5A.3.4).
	ContentHash string `json:"content_hash"`

	// DeclaredTS is device-reported time, as delivered. **Explicitly untrusted**
	// and never overwritten (§5A.4). Cheap recorders have no backup cell for the
	// RTC, so it resets to epoch or a fixed date whenever the battery fully drains
	// (G-015).
	DeclaredTS string `json:"declared_ts,omitempty"`

	// ResolvedTS is what the system decided the time actually was.
	ResolvedTS string `json:"resolved_ts"`

	// TSDerived marks ResolvedTS as computed from an anchor plus file ordering
	// rather than declared, so a later correction can re-derive the whole batch
	// (§5A.4). File *ordering* is trustworthy even when absolute time is not.
	TSDerived bool `json:"ts_derived"`

	StartedAt string `json:"started_at"`
	Device    string `json:"device"`

	// MicLabel records which input was used. Worth persisting because Bluetooth
	// pairing can silently route capture through the hands-free profile at 8kHz
	// narrowband, which collapses transcription quality specifically in the car and
	// is easy to misattribute to road noise (G-004).
	MicLabel string `json:"mic_label"`
}

// Capture is the transcript of one session, in three layers (§6.1). Retained
// permanently; L0 is never modified (I1).
type Capture struct {
	CaptureID   string `json:"capture_id"`
	OwnerUserID string `json:"owner_user_id"`
	SessionID   string `json:"session_id"`
	Label       string `json:"label"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	S3Prefix    string `json:"s3_prefix"`

	// ActiveL0Run names the run L1 and L2 derive from (§6.1).
	//
	// **Never inferred from sort order.** A capture accumulates further L0 runs over
	// its life — shadow mode writes a concurrent one, retranscribe.sh writes another
	// — and a shadow or evaluation run must never become authoritative merely by
	// being newest.
	ActiveL0Run string `json:"active_l0_run"`

	// IngestSource is denormalised from the session for list filtering. The session
	// record is authoritative (§6.3).
	IngestSource IngestSource `json:"ingest_source"`

	// Deleted marks a soft delete. DELETE /v1/captures/{id} soft-deletes and
	// **never deletes L0** (I1); only tenant erasure does (§9.3).
	Deleted bool `json:"deleted,omitempty"`
}

// Segment is one audio segment of a capture, with its position on the wall clock.
type Segment struct {
	CaptureID string `json:"capture_id"`
	Seq       int    `json:"seq"`
	BlockID   string `json:"block_id"`
	AudioKey  string `json:"audio_key"`

	// WallStartMS is the segment's true timeline position. Whisper's within-segment
	// offsets are segment-relative, so this must be added back to recover absolute
	// position. Silence is elided from the audio but **not** from the wall clock
	// (§Phase 2) — which is also what makes the silence-scaled timeline possible
	// (§4A.4).
	WallStartMS int64 `json:"wall_start_ms"`
	DurMS       int64 `json:"dur_ms"`

	// GapBeforeMS is the silence preceding this segment. Persisted because pause
	// structure is real data: Phase 3 feeds it in as a topic-shift prior, and §4A.4
	// renders it (§Phase 2).
	GapBeforeMS int64 `json:"gap_before_ms"`

	// L0Keys maps run_id to the S3 key of that run's output (§6.1). A map rather
	// than a single key because more than one run exists per capture, and none may
	// overwrite an earlier one (I1).
	L0Keys map[string]string `json:"l0_keys"`
}

// ---------------------------------------------------------------------------
// Item and Thread
// ---------------------------------------------------------------------------

// ItemKind is what a span of transcript was classified as (§1.1).
type ItemKind string

const (
	KindAction    ItemKind = "action"
	KindIdea      ItemKind = "idea"
	KindPrompt    ItemKind = "prompt"
	KindReference ItemKind = "reference"
	KindQuestion  ItemKind = "question"

	// KindNoise is a classifier verdict, **never a persisted item** (§3A.4).
	//
	// It exists in the type because the classifier must be able to return it and
	// the extraction metrics count it (§11A.4). A span classified noise produces no
	// Item record at all; it stays in the transcript and is searchable from there.
	// Code that writes an Item with this kind is a defect, and verify.sh asserts
	// none exist (§11.6).
	KindNoise ItemKind = "noise"
)

// ItemStatus is where an item sits in triage (§3A.5).
type ItemStatus string

const (
	StatusInbox     ItemStatus = "inbox"
	StatusFiled     ItemStatus = "filed"
	StatusDone      ItemStatus = "done"
	StatusDismissed ItemStatus = "dismissed"
)

// Item is one discrete thing extracted from a capture (§3A.4).
type Item struct {
	ItemID    string   `json:"item_id"`
	CaptureID string   `json:"capture_id"`
	Kind      ItemKind `json:"kind"`

	// Text is compressed for action, **verbatim for prompt** (§3A.3).
	Text string `json:"text"`

	// TextKey points at S3 when the body would push the record past DynamoDB's
	// 400KB item ceiling; Text then holds a truncated preview (§3A.4). A long
	// verbatim prompt is the only realistic case — and it is exactly the content
	// type that must not be truncated, so this overflow path is required rather
	// than defensive.
	TextKey string `json:"text_key,omitempty"`

	// SourceBlocks is always populated. It preserves audio alignment back to the
	// source, so every action item still points at the audio it was spoken in
	// (§3A.1). Extraction annotates; it never deletes, rewrites, or replaces.
	SourceBlocks []string `json:"source_blocks"`

	// Confidence is compared against extraction.auto_file_confidence — or, for
	// prompt, against the strictly higher prompt_kind_confidence (§7.4).
	Confidence float64 `json:"confidence"`

	Status   ItemStatus `json:"status"`
	ThreadID string     `json:"thread_id,omitempty"`

	// CreatedAt is required: inbox age is measured from it, and inbox age is the
	// leading indicator of triage abandonment (§11A.7, §3A.4).
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`

	Action   *ActionFields   `json:"action,omitempty"`
	Idea     *IdeaFields     `json:"idea,omitempty"`
	Question *QuestionFields `json:"question,omitempty"`
}

// ActionFields carries the decomposition of an action item (§3A.4).
type ActionFields struct {
	Verb      string `json:"verb"`
	Object    string `json:"object"`
	DueHint   string `json:"due_hint,omitempty"`
	BlockedOn string `json:"blocked_on,omitempty"`
}

// IdeaFields carries the threading hint for an idea.
type IdeaFields struct {
	ThreadHint string `json:"thread_hint,omitempty"`
}

// QuestionFields carries open/resolved state for a question.
type QuestionFields struct {
	Resolved   bool   `json:"resolved"`
	ResolvedBy string `json:"resolved_by,omitempty"`
}

// Thread is a curated, accumulating collection of items on one topic. **The main
// working surface** (§1.3) — a flat list of captures is the wrong primary surface
// (§3A.6).
type Thread struct {
	ThreadID string `json:"thread_id"`
	Title    string `json:"title"`
	Summary  string `json:"summary"`

	// KindMix counts items by kind, for the surface badges in §3A.6.
	KindMix   map[ItemKind]int `json:"kind_mix"`
	ItemCount int              `json:"item_count"`
	CreatedAt string           `json:"created_at"`
	UpdatedAt string           `json:"updated_at"`
}

// ---------------------------------------------------------------------------
// Metering and audit
// ---------------------------------------------------------------------------

// MeterUnit is a billable unit (I12, §Phase 0).
//
// Every pricing model that could ever exist is built on these, and **you cannot
// retroactively measure the past** — which is the whole reason metering is in Phase 0
// rather than deferred with the rest of billing (§2A.1).
type MeterUnit string

const (
	UnitSTTSeconds      MeterUnit = "stt_seconds"
	UnitLLMInputTokens  MeterUnit = "llm_input_tokens"
	UnitLLMOutputTokens MeterUnit = "llm_output_tokens"
	UnitEmbeddingTokens MeterUnit = "embedding_tokens"
	UnitStorageBytes    MeterUnit = "storage_bytes"
	UnitRequests        MeterUnit = "requests"
)

// Usage is one metering record. **Write-once**: no update or delete path exists in
// application code (§6.3).
type Usage struct {
	ID       string    `json:"id"`
	Unit     MeterUnit `json:"unit"`
	Quantity float64   `json:"quantity"`

	// Provider records which third party processed this, so a future privacy policy
	// can be accurate and a provider change can be reasoned about (§9.2).
	Provider string `json:"provider"`

	// CostMicros is millionths of a USD. Integer, not float: money summed across
	// thousands of records must not accumulate binary rounding error, and §Phase 0
	// acceptance requires summed cost to match the provider's reported cost within
	// 5% — a tolerance that is meaningless if the arithmetic itself drifts.
	CostMicros int64 `json:"cost_micros"`

	// Op distinguishes operations sharing a unit. Shadow-mode transcription uses a
	// distinct op so its doubled spend is visible and can be switched off knowingly
	// (§7.2).
	Op string `json:"op"`

	TS string `json:"ts"`

	// TTL expires the record after retention.usage_months — 25 months, covering
	// annual reconciliation plus a year (§6.3).
	TTL int64 `json:"ttl"`
}

// Audit is one access record. **Append-only, never mutated** (I13).
//
// "You cannot reconstruct history you did not record. Any future SOC 2 or enterprise
// conversation begins with 'show me the access log,' and a gap in it is not
// repairable" (§2A.1).
type Audit struct {
	ID       string `json:"id"`
	Actor    string `json:"actor"`
	Action   string `json:"action"`
	Resource string `json:"resource"`
	IP       string `json:"ip"`
	UA       string `json:"ua"`
	Result   string `json:"result"`
	TS       string `json:"ts"`
	TTL      int64  `json:"ttl"`
}

// Audit results.
const (
	AuditAllowed = "allowed"
	AuditDenied  = "denied"
)
