// Package config loads and validates the per-instance configuration file (§7).
//
// Three rules shape everything here, and each one is a spec requirement rather
// than a style preference:
//
//  1. "No model string appears anywhere else in the codebase" (I5). Every
//     provider, model name, endpoint, and API version arrives through this
//     package. Nothing downstream may carry a default for one.
//
//  2. "Every key in §7.4 is required unless explicitly optional — a missing
//     threshold must fail the deploy, never fall back to a hardcoded default"
//     (§Phase 0). This is why required scalars are pointers: a Go zero value
//     cannot distinguish `max_change_ratio: 0` from an absent key, and a
//     silently-defaulted threshold is exactly the failure the rule forbids. The
//     pointers are unwrapped into plain values on the returned struct, so
//     callers never see them — the awkwardness is confined to parsing.
//
//  3. Validation runs at cold start *and* in CI, from this same code
//     (`chintanctl config validate`). A separate CI validator could disagree
//     with the runtime one, and the disagreement would surface as a cold-start
//     failure on a config that passed its gate.
//
// Secrets are referenced by SSM Parameter Store path and never inlined (§7.4).
// This package validates the shape of those paths; it never resolves them.
// Resolution is the Lambda execution role's job, because the build environment
// must not be able to read a provider key (§9.4).
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// SchemaVersion is the config format version this build understands. A config
// declaring anything else is rejected rather than partially interpreted.
const SchemaVersion = 1

// Config is the validated, typed configuration. Every field is populated —
// there are no optional scalars with implied defaults, by rule 2 above.
type Config struct {
	Version       int    `yaml:"version"`
	Instance      string `yaml:"instance"`
	Region        string `yaml:"region"`
	AllowedOrigin string `yaml:"allowed_origin"`

	Branding   Branding   `yaml:"branding"`
	Providers  Providers  `yaml:"providers"`
	Capture    Capture    `yaml:"capture"`
	Ingest     Ingest     `yaml:"ingest"`
	Cleanup    Cleanup    `yaml:"cleanup"`
	Extraction Extraction `yaml:"extraction"`
	Routing    Routing    `yaml:"routing"`
	Rules      Rules      `yaml:"rules"`
	Search     Search     `yaml:"search"`
	Limits     Limits     `yaml:"limits"`
	Retention  Retention  `yaml:"retention"`
	Schedules  Schedules  `yaml:"schedules"`
	Triggers   Triggers   `yaml:"triggers"`

	// sourcePath records where this config was read from, for error messages
	// and for the config version reported by GET /v1/health (§0.6).
	sourcePath string
}

// Branding is everything a user sees (§7.3). Configurable, unlike the frozen
// system identifier.
//
// SystemID deliberately does not appear here: `voicenotes` is frozen, is not
// per-instance, and lives in the systemid package. Putting it in a config file
// would invite editing it, and §7.3 is explicit that changing it means
// recreating and migrating every resource.
type Branding struct {
	Name            string  `yaml:"name"`
	ShortName       string  `yaml:"short_name"`
	Description     string  `yaml:"description"`
	Tagline         *string `yaml:"tagline"` // explicitly nullable
	ThemeColor      string  `yaml:"theme_color"`
	BackgroundColor string  `yaml:"background_color"`
	IconSource      string  `yaml:"icon_source"`
}

// Providers holds the STT, LLM, and embedding catalogs (§7.1).
type Providers struct {
	STT        STTProviders        `yaml:"stt"`
	LLM        LLMProviders        `yaml:"llm"`
	Embeddings EmbeddingsProviders `yaml:"embeddings"`
}

// STTProviders selects an active provider and optionally a shadow one (§7.2).
type STTProviders struct {
	Active string `yaml:"active"`
	// Shadow doubles STT spend while set, so it is explicitly nullable and
	// expected to be null outside a bounded evaluation window (§7.2).
	Shadow  *string               `yaml:"shadow"`
	Catalog map[string]STTCatalog `yaml:"catalog"`
}

// STTCatalog is one STT provider entry. Capabilities are declared rather than
// assumed: swapping providers is not purely a config change, and a pipeline
// that assumes Whisper-shaped behaviour breaks silently against one that lacks
// prompt biasing (§7.1, G-042).
type STTCatalog struct {
	Adapter        string          `yaml:"adapter"`
	BaseURL        string          `yaml:"base_url"`
	Model          string          `yaml:"model"`
	SecretRef      string          `yaml:"secret_ref"`
	MaxFileMB      *int            `yaml:"max_file_mb"`
	MinBilledSecs  *int            `yaml:"min_billed_seconds"`
	CostPerHourUSD *float64        `yaml:"cost_per_hour_usd"`
	Capabilities   STTCapabilities `yaml:"capabilities"`
	Params         map[string]any  `yaml:"params"`
}

// STTCapabilities is what the pipeline branches on (§7.1). Every field is a
// pointer because "absent" must not silently read as false: a provider whose
// prompt_biasing was simply not filled in would be treated as incapable, the
// Phase 4 correction architecture would quietly route everything to the LLM
// layer, and the failure would be invisible (G-042).
type STTCapabilities struct {
	PromptBiasing     *bool    `yaml:"prompt_biasing"`
	PromptTokenBudget *int     `yaml:"prompt_token_budget"`
	Timestamps        []string `yaml:"timestamps"`
	// MaxSyncSeconds is nullable in the spec's own example (null = no limit),
	// so a null here is meaningful rather than missing.
	MaxSyncSeconds *int  `yaml:"max_sync_seconds"`
	AsyncBatchAPI  *bool `yaml:"async_batch_api"`
	CodeMixing     *bool `yaml:"code_mixing"`
}

// LLMProviders holds the chat catalog and the per-task routing table (§7.4).
type LLMProviders struct {
	Catalog map[string]LLMCatalog `yaml:"catalog"`
	// Tasks maps each pipeline task to a catalog key. §10.5.4 requires cheap
	// tasks on cheap models; the keys exist from Phase 0 so that repointing
	// them is a config edit once a second model is catalogued.
	Tasks LLMTasks `yaml:"tasks"`
}

// LLMCatalog is one chat model entry.
type LLMCatalog struct {
	Adapter    string `yaml:"adapter"`
	BaseURL    string `yaml:"base_url"`
	Model      string `yaml:"model"`
	SecretRef  string `yaml:"secret_ref"`
	MaxContext *int   `yaml:"max_context"`
}

// LLMTasks is the per-task model routing (§7.4).
type LLMTasks struct {
	Cleanup string `yaml:"cleanup"`
	Routing string `yaml:"routing"`
	Summary string `yaml:"summary"`
	Search  string `yaml:"search"`
}

// EmbeddingsProviders holds the embedding catalog.
type EmbeddingsProviders struct {
	Active  string                       `yaml:"active"`
	Catalog map[string]EmbeddingsCatalog `yaml:"catalog"`
}

// EmbeddingsCatalog is one embedding model entry. Dimensions is load-bearing
// for the Phase 5 memory floor: rows × dimensions × 4 bytes must fit the
// function's allocation, and halving dimensions halves that floor (G-061).
type EmbeddingsCatalog struct {
	Adapter    string `yaml:"adapter"`
	BaseURL    string `yaml:"base_url"`
	Model      string `yaml:"model"`
	Dimensions *int   `yaml:"dimensions"`
	SecretRef  string `yaml:"secret_ref"`
}

// Capture holds VAD and audio settings (§7.4).
type Capture struct {
	VAD   VAD   `yaml:"vad"`
	Audio Audio `yaml:"audio"`
}

// VAD is the segmentation policy — defined once here and implemented twice, in
// the browser and in the Go worker (§Phase 2). Both read these same values;
// that is what makes cross-implementation parity testable.
type VAD struct {
	Enabled      *bool  `yaml:"enabled"`
	Model        string `yaml:"model"`
	FrameSamples *int   `yaml:"frame_samples"`
	// SampleRate applies ONLY to the raw PCM path feeding Silero, which
	// requires 16kHz. It is not an encoder setting: MediaRecorder follows the
	// track's native rate and exposes no rate option at all (G-059).
	SampleRate      *int     `yaml:"sample_rate"`
	OnsetThreshold  *float64 `yaml:"onset_threshold"`
	OffsetThreshold *float64 `yaml:"offset_threshold"`
	// PrerollMS is not optional in practice: clipped word onsets degrade WER
	// worse than no VAD at all (G-014, §Phase 2).
	PrerollMS  *int `yaml:"preroll_ms"`
	HangoverMS *int `yaml:"hangover_ms"`
	// TargetSegmentMS is ~28s because Whisper is trained on 30s windows and
	// short isolated clips lose the decoder context that disambiguates them
	// (G-012). It also keeps requests above the 10-second minimum billing floor
	// (G-013). Do not tune this down for latency without re-running A6.
	TargetSegmentMS *int `yaml:"target_segment_ms"`
	MaxSegmentMS    *int `yaml:"max_segment_ms"`
}

// Audio is the encoder configuration.
//
// There is deliberately no sample_rate key here. The browser does not honour a
// sample-rate constraint on MediaRecorder, and it does not matter — Whisper
// downsamples anyway, so transcoding costs compute and gains nothing (G-059,
// §Phase 1). A key that cannot be honoured is worse than no key, because
// someone will later "fix" the mismatch by adding a pointless transcode.
type Audio struct {
	Codec    string `yaml:"codec"`
	Bitrate  *int   `yaml:"bitrate"`
	Channels *int   `yaml:"channels"`
}

// Ingest holds the ingestion-path settings (§5A).
type Ingest struct {
	// SessionSplitSilenceMS: one imported file may hold many unrelated thought
	// streams, so one file does not imply one capture (§5A.3.3).
	SessionSplitSilenceMS *int `yaml:"session_split_silence_ms"`
	// TelegramMaxMB is the Bot API getFile ceiling, checked before starting a
	// download rather than after it fails (G-029).
	TelegramMaxMB *int     `yaml:"telegram_max_mb"`
	AcceptedMIME  []string `yaml:"accepted_mime"`
}

// Cleanup holds the I4 patch-validation gate thresholds (§Phase 3).
type Cleanup struct {
	MaxChangeRatio *float64 `yaml:"max_change_ratio"`
	// MaxPhoneticDistance rejects substitutions that do not plausibly sound
	// like the original — the check that separates an STT correction from an
	// authored content change (§Phase 4).
	MaxPhoneticDistance *float64 `yaml:"max_phonetic_distance"`
	RejectOnLengthDelta *float64 `yaml:"reject_on_length_delta"`
	// MinAvgLogprob is the need-gate: above this, cleanup is skipped entirely
	// (§10.5.2). Negative by nature — it is a log probability.
	MinAvgLogprob *float64 `yaml:"min_avg_logprob"`
	DeferBatch    *bool    `yaml:"defer_batch"`
}

// Extraction holds the confidence gates (§3A.5).
type Extraction struct {
	AutoFileConfidence *float64 `yaml:"auto_file_confidence"`
	// PromptKindConfidence holds `prompt` to a higher bar than any other kind:
	// a prompt misfiled as an idea gets summarised and the artifact is
	// destroyed (§3A.3, A4). This is the failure with the worst blast radius in
	// the product, which is why it has its own threshold.
	PromptKindConfidence *float64 `yaml:"prompt_kind_confidence"`
}

// Routing holds thread-selection settings (§Phase 3).
type Routing struct {
	CandidateK    *int     `yaml:"candidate_k"`
	MinSimilarity *float64 `yaml:"min_similarity"`
	// AlwaysShowDecision: silent misfiling is the most damaging failure mode in
	// this system, because the user does not discover it until they go looking
	// for a thought that is not where they expect (§Phase 3).
	AlwaysShowDecision *bool `yaml:"always_show_decision"`
}

// Rules holds the correction rule store's health thresholds (§Phase 4).
type Rules struct {
	// TopicSimilarityMin is what resolves rule collisions and suppresses stale
	// rules without deleting them: a phonetic match alone is insufficient,
	// because phonetic keys carry no semantics (G-039).
	TopicSimilarityMin *float64 `yaml:"topic_similarity_min"`
	// DemoteBelowPrecision / RetireBelowPrecision act on the re-edit rate, the
	// only ground truth available without labelling effort (§Phase 4).
	DemoteBelowPrecision *float64 `yaml:"demote_below_precision"`
	RetireBelowPrecision *float64 `yaml:"retire_below_precision"`
	// PromptAlwaysOnRatio splits the ~224-token Whisper prompt ceiling between
	// always-on high-frequency terms and topically retrieved ones (§Phase 4.1,
	// G-010). No retrieval scheme raises that ceiling.
	PromptAlwaysOnRatio *float64 `yaml:"prompt_always_on_ratio"`
}

// Search holds retrieval settings (§Phase 5).
type Search struct {
	TopK *int `yaml:"top_k"`
}

// Limits holds the cost and security caps.
type Limits struct {
	// DailySpendUSD is the per-tenant circuit breaker that converts an
	// unbounded worst case into a known number (§10.5.9). Fails closed.
	DailySpendUSD *float64 `yaml:"daily_spend_usd"`
	// PresignTTLSeconds is capped at 15 minutes by §9.
	PresignTTLSeconds *int `yaml:"presign_ttl_seconds"`
}

// Retention holds every retention window. LogGroupDays is mandatory and
// explicit because unset CloudWatch retention is infinite at $0.50/GB ingested
// (§10.1).
type Retention struct {
	AuditDays           *int `yaml:"audit_days"`
	UsageMonths         *int `yaml:"usage_months"`
	LogGroupDays        *int `yaml:"log_group_days"`
	ContinuousAudioDays *int `yaml:"continuous_audio_days"`
}

// Schedules holds the three EventBridge scheduled rules (§7.4). This is the
// only sanctioned use of EventBridge — S3 notifications stay direct (§10.2).
type Schedules struct {
	Metrics         string `yaml:"metrics"`
	Verify          string `yaml:"verify"`
	DeferredCleanup string `yaml:"deferred_cleanup"`
}

// Triggers holds the enabled capture triggers (§5).
type Triggers struct {
	Enabled []string `yaml:"enabled"`
	// DebounceMS guards against double-fire; NFC tags and BLE buttons both
	// bounce (§5.2 rule 4).
	DebounceMS *int `yaml:"debounce_ms"`
}

// SourcePath reports where this config was loaded from.
func (c *Config) SourcePath() string { return c.sourcePath }

// ---------------------------------------------------------------------------
// Loading
// ---------------------------------------------------------------------------

// Load reads and validates a config file, returning a typed Config or an error
// naming every problem found.
//
// Fails loudly and completely: all validation errors are reported together, not
// one per run. An operator fixing a config at 2am should see the whole list
// rather than discovering the next problem after each redeploy.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: reading %s: %w", path, err)
	}
	return Parse(raw, path)
}

// Parse validates config bytes. Separated from Load so tests need no filesystem.
func Parse(raw []byte, sourcePath string) (*Config, error) {
	var cfg Config

	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	// KnownFields rejects keys the struct does not declare. A typo'd key would
	// otherwise be silently ignored and the real key would read as absent —
	// which the required-key rule turns into an error anyway, but with a
	// confusing message. Rejecting the typo directly is the actionable failure.
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("config: parsing %s: %w", sourcePath, err)
	}

	cfg.sourcePath = sourcePath

	if errs := cfg.validate(); len(errs) > 0 {
		return nil, &ValidationError{Path: sourcePath, Problems: errs}
	}
	return &cfg, nil
}

// LoadDir validates every *.yaml in a directory, which is what CI runs against
// config/instances so an invalid config is caught before it reaches a cold
// start (§0.5A, §7.4).
func LoadDir(dir string) (map[string]*Config, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("config: no *.yaml files in %s; the CI matrix discovers instances from this directory, so an empty one would deploy nothing while reporting success", dir)
	}
	sort.Strings(matches)

	out := make(map[string]*Config, len(matches))
	var problems []error
	for _, m := range matches {
		cfg, err := Load(m)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		// The filename is what the CI matrix keys on and what the stack is
		// named after, so a file whose `instance` disagrees with its name would
		// deploy one instance's config under another's stack name.
		base := strings.TrimSuffix(filepath.Base(m), ".yaml")
		if cfg.Instance != base {
			problems = append(problems, fmt.Errorf(
				"config: %s declares instance %q but the filename says %q; the CI matrix and the stack name both derive from the filename",
				m, cfg.Instance, base))
			continue
		}
		out[base] = cfg
	}
	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}
	return out, nil
}

// ValidationError reports every problem in one config, together.
type ValidationError struct {
	Path     string
	Problems []string
}

func (e *ValidationError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "config: %s is invalid (%d problem(s)):", e.Path, len(e.Problems))
	for _, p := range e.Problems {
		fmt.Fprintf(&b, "\n  - %s", p)
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

var (
	instanceRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	regionRe   = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-\d$`)
	hexColorRe = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
	// Secret paths are SSM Parameter Store paths. {env} is substituted at load
	// time by the caller; the shape is validated here.
	secretRefRe = regexp.MustCompile(`^/[A-Za-z0-9._{}/-]+$`)
	cronRe      = regexp.MustCompile(`^cron\(.+\)$`)
	httpsRe     = regexp.MustCompile(`^https://`)
)

// validate returns every problem found. It never mutates cfg and never supplies
// a default — supplying one is precisely the behaviour §Phase 0 forbids.
func (c *Config) validate() []string {
	var p []string
	req := func(cond bool, msg string, args ...any) {
		if !cond {
			p = append(p, fmt.Sprintf(msg, args...))
		}
	}

	// --- envelope ---
	req(c.Version == SchemaVersion, "version must be %d, got %d", SchemaVersion, c.Version)
	req(instanceRe.MatchString(c.Instance),
		"instance %q must be a short lowercase identifier; it becomes part of every resource name (§6.3)", c.Instance)
	req(regionRe.MatchString(c.Region),
		"region %q is not an AWS region id", c.Region)
	req(httpsRe.MatchString(c.AllowedOrigin),
		"allowed_origin %q must be an https origin", c.AllowedOrigin)
	// A wildcard origin on an API serving someone's private thinking is not a
	// convenience, it is a data leak (§10.6).
	req(!strings.Contains(c.AllowedOrigin, "*"),
		"allowed_origin must never be a wildcard (§10.6)")
	req(!strings.HasSuffix(c.AllowedOrigin, "/"),
		"allowed_origin %q must not have a trailing slash; browsers send Origin without one and the comparison is exact", c.AllowedOrigin)

	// --- branding (§7.3) ---
	req(c.Branding.Name != "", "branding.name is required")
	req(c.Branding.ShortName != "", "branding.short_name is required")
	req(c.Branding.Description != "", "branding.description is required")
	req(hexColorRe.MatchString(c.Branding.ThemeColor),
		"branding.theme_color %q must be a #RRGGBB hex colour", c.Branding.ThemeColor)
	req(hexColorRe.MatchString(c.Branding.BackgroundColor),
		"branding.background_color %q must be a #RRGGBB hex colour", c.Branding.BackgroundColor)
	req(c.Branding.IconSource != "", "branding.icon_source is required")
	// The launcher truncates a long short_name, and voice launch matches on the
	// name, so an over-long one is a functional problem rather than a cosmetic
	// one (G-005).
	req(len(c.Branding.ShortName) <= 12,
		"branding.short_name %q is longer than 12 characters; Android's launcher truncates it (G-005)", c.Branding.ShortName)

	// --- providers.stt (§7.1) ---
	req(len(c.Providers.STT.Catalog) > 0, "providers.stt.catalog must have at least one entry")
	req(c.Providers.STT.Active != "", "providers.stt.active is required")
	if c.Providers.STT.Active != "" {
		if _, ok := c.Providers.STT.Catalog[c.Providers.STT.Active]; !ok {
			p = append(p, fmt.Sprintf("providers.stt.active %q is not in providers.stt.catalog", c.Providers.STT.Active))
		}
	}
	if s := c.Providers.STT.Shadow; s != nil {
		if *s == "" {
			p = append(p, "providers.stt.shadow is an empty string; use null to disable shadow mode (§7.2)")
		} else if _, ok := c.Providers.STT.Catalog[*s]; !ok {
			p = append(p, fmt.Sprintf("providers.stt.shadow %q is not in providers.stt.catalog", *s))
		} else if *s == c.Providers.STT.Active {
			p = append(p, "providers.stt.shadow equals providers.stt.active; shadow mode would double spend to compare a provider with itself (§7.2)")
		}
	}
	for name, e := range c.Providers.STT.Catalog {
		where := "providers.stt.catalog." + name
		req(e.Adapter != "", "%s.adapter is required", where)
		req(httpsRe.MatchString(e.BaseURL), "%s.base_url must be https, got %q", where, e.BaseURL)
		req(e.Model != "", "%s.model is required (I5 — no model string outside config)", where)
		req(secretRefRe.MatchString(e.SecretRef), "%s.secret_ref %q must be an SSM parameter path", where, e.SecretRef)
		// A key that looks like a literal secret rather than a path is the one
		// mistake here that must never reach a commit (§9, G-019).
		req(!looksLikeInlineSecret(e.SecretRef), "%s.secret_ref looks like an inline secret; secrets are referenced by SSM path, never by value (§7.4, §9.4)", where)
		req(e.CostPerHourUSD != nil, "%s.cost_per_hour_usd is required; per-provider cost must be metered, and providers differ by an order of magnitude (G-044)", where)
		req(e.MinBilledSecs != nil, "%s.min_billed_seconds is required; a per-request billing floor makes short clips cost the same as long ones (G-013)", where)
		req(e.MaxFileMB != nil, "%s.max_file_mb is required and must match the account tier actually in use (§7.1)", where)

		cap := e.Capabilities
		req(cap.PromptBiasing != nil, "%s.capabilities.prompt_biasing is required; an absent capability must not read as false, or corrections stop silently (G-042)", where)
		req(cap.PromptTokenBudget != nil, "%s.capabilities.prompt_token_budget is required", where)
		req(cap.AsyncBatchAPI != nil, "%s.capabilities.async_batch_api is required", where)
		req(cap.CodeMixing != nil, "%s.capabilities.code_mixing is required; the primary speaker code-switches (§1.2)", where)
		req(len(cap.Timestamps) > 0, "%s.capabilities.timestamps must list at least one granularity", where)
		for _, g := range cap.Timestamps {
			req(g == "segment" || g == "word", "%s.capabilities.timestamps contains unknown granularity %q", where, g)
		}
		// A provider claiming prompt biasing with a zero budget cannot bias
		// anything; the combination would pass a naive check and then quietly
		// inject nothing (§Phase 4.1).
		if cap.PromptBiasing != nil && *cap.PromptBiasing && cap.PromptTokenBudget != nil && *cap.PromptTokenBudget <= 0 {
			p = append(p, fmt.Sprintf("%s declares prompt_biasing: true with prompt_token_budget %d; it cannot bias with no budget (§Phase 4.1)", where, *cap.PromptTokenBudget))
		}
	}
	// The active provider having no biasing surface is legal but consequential:
	// Phase 4's corrections must route to the LLM cleanup layer instead, and
	// that is a code path with a test, not a config note (§7.1).
	if act, ok := c.Providers.STT.Catalog[c.Providers.STT.Active]; ok {
		if act.Capabilities.PromptBiasing != nil && !*act.Capabilities.PromptBiasing && act.Capabilities.PromptTokenBudget != nil && *act.Capabilities.PromptTokenBudget != 0 {
			p = append(p, fmt.Sprintf("providers.stt.catalog.%s declares prompt_biasing: false but a non-zero prompt_token_budget; one of the two is wrong", c.Providers.STT.Active))
		}
	}

	// --- providers.llm (§7.4, §10.5.4) ---
	req(len(c.Providers.LLM.Catalog) > 0, "providers.llm.catalog must have at least one entry")
	for name, e := range c.Providers.LLM.Catalog {
		where := "providers.llm.catalog." + name
		req(e.Adapter != "", "%s.adapter is required", where)
		req(httpsRe.MatchString(e.BaseURL), "%s.base_url must be https, got %q", where, e.BaseURL)
		req(e.Model != "", "%s.model is required (I5)", where)
		req(secretRefRe.MatchString(e.SecretRef), "%s.secret_ref %q must be an SSM parameter path", where, e.SecretRef)
		req(!looksLikeInlineSecret(e.SecretRef), "%s.secret_ref looks like an inline secret (§9.4)", where)
		req(e.MaxContext != nil, "%s.max_context is required; per-session LLM batching depends on knowing it (§10.5.1)", where)
	}
	// §7.4: "the schema validator rejects a tasks value naming a model absent
	// from the catalog." A task pointing at nothing would fail at the first
	// pipeline run instead of at deploy.
	for task, model := range map[string]string{
		"cleanup": c.Providers.LLM.Tasks.Cleanup,
		"routing": c.Providers.LLM.Tasks.Routing,
		"summary": c.Providers.LLM.Tasks.Summary,
		"search":  c.Providers.LLM.Tasks.Search,
	} {
		if model == "" {
			p = append(p, fmt.Sprintf("providers.llm.tasks.%s is required", task))
			continue
		}
		if _, ok := c.Providers.LLM.Catalog[model]; !ok {
			p = append(p, fmt.Sprintf("providers.llm.tasks.%s names %q, which is not in providers.llm.catalog (§7.4)", task, model))
		}
	}

	// --- providers.embeddings ---
	req(len(c.Providers.Embeddings.Catalog) > 0, "providers.embeddings.catalog must have at least one entry")
	req(c.Providers.Embeddings.Active != "", "providers.embeddings.active is required")
	if c.Providers.Embeddings.Active != "" {
		if _, ok := c.Providers.Embeddings.Catalog[c.Providers.Embeddings.Active]; !ok {
			p = append(p, fmt.Sprintf("providers.embeddings.active %q is not in providers.embeddings.catalog", c.Providers.Embeddings.Active))
		}
	}
	for name, e := range c.Providers.Embeddings.Catalog {
		where := "providers.embeddings.catalog." + name
		req(e.Adapter != "", "%s.adapter is required", where)
		req(httpsRe.MatchString(e.BaseURL), "%s.base_url must be https, got %q", where, e.BaseURL)
		req(e.Model != "", "%s.model is required (I5)", where)
		req(secretRefRe.MatchString(e.SecretRef), "%s.secret_ref %q must be an SSM parameter path", where, e.SecretRef)
		req(e.Dimensions != nil && *e.Dimensions > 0,
			"%s.dimensions is required; it sets the Phase 5 memory floor at rows × dimensions × 4 bytes (G-061)", where)
	}

	// --- capture (§7.4) ---
	v := c.Capture.VAD
	req(v.Enabled != nil, "capture.vad.enabled is required")
	req(v.Model != "", "capture.vad.model is required")
	req(v.FrameSamples != nil && *v.FrameSamples > 0, "capture.vad.frame_samples is required")
	req(v.SampleRate != nil, "capture.vad.sample_rate is required (VAD path only — the encoder has no rate setting, G-059)")
	if v.SampleRate != nil {
		// Silero requires 16kHz. Feeding it another rate produces silently
		// wrong boundaries rather than an error (G-059).
		req(*v.SampleRate == 16000, "capture.vad.sample_rate must be 16000; Silero requires it and another rate yields silently wrong boundaries (G-059)")
	}
	req(v.OnsetThreshold != nil, "capture.vad.onset_threshold is required")
	req(v.OffsetThreshold != nil, "capture.vad.offset_threshold is required")
	if v.OnsetThreshold != nil && v.OffsetThreshold != nil {
		req(inUnit(*v.OnsetThreshold), "capture.vad.onset_threshold must be in [0,1]")
		req(inUnit(*v.OffsetThreshold), "capture.vad.offset_threshold must be in [0,1]")
		// Hysteresis requires offset below onset. Equal or inverted thresholds
		// turn the detector into a chattering switch at the boundary, which
		// fragments segments instead of holding them.
		req(*v.OffsetThreshold < *v.OnsetThreshold,
			"capture.vad.offset_threshold (%v) must be below onset_threshold (%v); hysteresis is what stops the detector chattering at the boundary",
			*v.OffsetThreshold, *v.OnsetThreshold)
	}
	req(v.PrerollMS != nil && *v.PrerollMS > 0,
		"capture.vad.preroll_ms is required and must be positive; clipped word onsets degrade WER worse than no VAD at all (G-014)")
	req(v.HangoverMS != nil && *v.HangoverMS > 0, "capture.vad.hangover_ms is required and must be positive")
	req(v.TargetSegmentMS != nil && *v.TargetSegmentMS > 0, "capture.vad.target_segment_ms is required")
	req(v.MaxSegmentMS != nil && *v.MaxSegmentMS > 0, "capture.vad.max_segment_ms is required")
	if v.TargetSegmentMS != nil && v.MaxSegmentMS != nil {
		req(*v.MaxSegmentMS >= *v.TargetSegmentMS,
			"capture.vad.max_segment_ms (%d) is below target_segment_ms (%d)", *v.MaxSegmentMS, *v.TargetSegmentMS)
	}

	req(c.Capture.Audio.Codec != "", "capture.audio.codec is required")
	req(c.Capture.Audio.Bitrate != nil && *c.Capture.Audio.Bitrate > 0, "capture.audio.bitrate is required")
	req(c.Capture.Audio.Channels != nil && *c.Capture.Audio.Channels > 0, "capture.audio.channels is required")

	// --- ingest (§5A) ---
	req(c.Ingest.SessionSplitSilenceMS != nil && *c.Ingest.SessionSplitSilenceMS > 0,
		"ingest.session_split_silence_ms is required; one imported file may hold many sessions (§5A.3.3)")
	req(c.Ingest.TelegramMaxMB != nil && *c.Ingest.TelegramMaxMB > 0, "ingest.telegram_max_mb is required")
	if c.Ingest.TelegramMaxMB != nil && *c.Ingest.TelegramMaxMB > 20 {
		p = append(p, fmt.Sprintf("ingest.telegram_max_mb is %d; the Bot API getFile ceiling is 20MB and a larger value would let a download start that cannot finish (G-029)", *c.Ingest.TelegramMaxMB))
	}
	req(len(c.Ingest.AcceptedMIME) > 0, "ingest.accepted_mime must list at least one type")

	// --- cleanup (I4 gate) ---
	req(c.Cleanup.MaxChangeRatio != nil, "cleanup.max_change_ratio is required")
	req(c.Cleanup.MaxPhoneticDistance != nil, "cleanup.max_phonetic_distance is required")
	req(c.Cleanup.RejectOnLengthDelta != nil, "cleanup.reject_on_length_delta is required")
	req(c.Cleanup.MinAvgLogprob != nil, "cleanup.min_avg_logprob is required")
	req(c.Cleanup.DeferBatch != nil, "cleanup.defer_batch is required")
	if c.Cleanup.MaxChangeRatio != nil {
		req(inUnit(*c.Cleanup.MaxChangeRatio), "cleanup.max_change_ratio must be in [0,1]")
	}
	if c.Cleanup.MaxPhoneticDistance != nil {
		req(inUnit(*c.Cleanup.MaxPhoneticDistance), "cleanup.max_phonetic_distance must be in [0,1]")
	}
	if c.Cleanup.RejectOnLengthDelta != nil {
		req(inUnit(*c.Cleanup.RejectOnLengthDelta), "cleanup.reject_on_length_delta must be in [0,1]")
	}
	if c.Cleanup.MinAvgLogprob != nil {
		// A log probability is never positive; a positive value here would make
		// the need-gate skip every cleanup call and look like a cost win.
		req(*c.Cleanup.MinAvgLogprob <= 0,
			"cleanup.min_avg_logprob must be <= 0; it is a log probability, and a positive value would skip every cleanup call while looking like a saving (§10.5.2)")
	}

	// --- extraction (§3A.5) ---
	req(c.Extraction.AutoFileConfidence != nil, "extraction.auto_file_confidence is required")
	req(c.Extraction.PromptKindConfidence != nil, "extraction.prompt_kind_confidence is required")
	if c.Extraction.AutoFileConfidence != nil {
		req(inUnit(*c.Extraction.AutoFileConfidence), "extraction.auto_file_confidence must be in [0,1]")
	}
	if c.Extraction.PromptKindConfidence != nil {
		req(inUnit(*c.Extraction.PromptKindConfidence), "extraction.prompt_kind_confidence must be in [0,1]")
	}
	if c.Extraction.AutoFileConfidence != nil && c.Extraction.PromptKindConfidence != nil {
		// §11A.4: prompt precision is tracked separately and weighted highest,
		// because a prompt misfiled as an idea gets summarised and the artifact
		// is destroyed. A prompt bar at or below the general bar silently gives
		// up that protection.
		req(*c.Extraction.PromptKindConfidence > *c.Extraction.AutoFileConfidence,
			"extraction.prompt_kind_confidence (%v) must be strictly above auto_file_confidence (%v); `prompt` is held to a higher bar because misclassifying it destroys the artifact (§3A.3, A4)",
			*c.Extraction.PromptKindConfidence, *c.Extraction.AutoFileConfidence)
	}

	// --- routing ---
	req(c.Routing.CandidateK != nil && *c.Routing.CandidateK > 0, "routing.candidate_k is required")
	req(c.Routing.MinSimilarity != nil, "routing.min_similarity is required")
	if c.Routing.MinSimilarity != nil {
		req(inUnit(*c.Routing.MinSimilarity), "routing.min_similarity must be in [0,1]")
	}
	req(c.Routing.AlwaysShowDecision != nil, "routing.always_show_decision is required")
	if c.Routing.AlwaysShowDecision != nil && !*c.Routing.AlwaysShowDecision {
		// §Phase 3 and §4A.5 both require every automatic filing decision to be
		// visible and reversible. Turning that off is not a tuning knob.
		p = append(p, "routing.always_show_decision must be true; silent misfiling is the most damaging failure mode in this system (§Phase 3, §4A.5)")
	}

	// --- rules (§Phase 4) ---
	req(c.Rules.TopicSimilarityMin != nil, "rules.topic_similarity_min is required")
	req(c.Rules.DemoteBelowPrecision != nil, "rules.demote_below_precision is required")
	req(c.Rules.RetireBelowPrecision != nil, "rules.retire_below_precision is required")
	req(c.Rules.PromptAlwaysOnRatio != nil, "rules.prompt_always_on_ratio is required")
	for name, val := range map[string]*float64{
		"rules.topic_similarity_min":   c.Rules.TopicSimilarityMin,
		"rules.demote_below_precision": c.Rules.DemoteBelowPrecision,
		"rules.retire_below_precision": c.Rules.RetireBelowPrecision,
		"rules.prompt_always_on_ratio": c.Rules.PromptAlwaysOnRatio,
	} {
		if val != nil {
			req(inUnit(*val), "%s must be in [0,1]", name)
		}
	}
	if c.Rules.DemoteBelowPrecision != nil && c.Rules.RetireBelowPrecision != nil {
		// Retire must be the lower bar: a rule is demoted before it is retired.
		// Inverted, a rule would be retired before it was ever demoted and the
		// LLM-candidate tier would never be used.
		req(*c.Rules.RetireBelowPrecision < *c.Rules.DemoteBelowPrecision,
			"rules.retire_below_precision (%v) must be below demote_below_precision (%v); a rule is demoted before it is retired (§Phase 4)",
			*c.Rules.RetireBelowPrecision, *c.Rules.DemoteBelowPrecision)
	}

	// --- search ---
	req(c.Search.TopK != nil && *c.Search.TopK > 0, "search.top_k is required")

	// --- limits ---
	req(c.Limits.DailySpendUSD != nil && *c.Limits.DailySpendUSD > 0,
		"limits.daily_spend_usd is required and must be positive; it is the breaker that converts an unbounded worst case into a known number (§10.5.9)")
	req(c.Limits.PresignTTLSeconds != nil && *c.Limits.PresignTTLSeconds > 0, "limits.presign_ttl_seconds is required")
	if c.Limits.PresignTTLSeconds != nil && *c.Limits.PresignTTLSeconds > 900 {
		p = append(p, fmt.Sprintf("limits.presign_ttl_seconds is %d; §9 caps presigned URL lifetime at 900 (15 minutes)", *c.Limits.PresignTTLSeconds))
	}

	// --- retention ---
	req(c.Retention.AuditDays != nil && *c.Retention.AuditDays > 0, "retention.audit_days is required")
	req(c.Retention.UsageMonths != nil && *c.Retention.UsageMonths > 0, "retention.usage_months is required")
	req(c.Retention.LogGroupDays != nil && *c.Retention.LogGroupDays > 0,
		"retention.log_group_days is required; unset CloudWatch retention is infinite at $0.50/GB ingested (§10.1)")
	req(c.Retention.ContinuousAudioDays != nil && *c.Retention.ContinuousAudioDays > 0,
		"retention.continuous_audio_days is required")

	// --- schedules (§7.4) ---
	req(cronRe.MatchString(c.Schedules.Metrics), "schedules.metrics must be a cron(...) expression, got %q", c.Schedules.Metrics)
	req(cronRe.MatchString(c.Schedules.Verify), "schedules.verify must be a cron(...) expression, got %q", c.Schedules.Verify)
	req(cronRe.MatchString(c.Schedules.DeferredCleanup), "schedules.deferred_cleanup must be a cron(...) expression, got %q", c.Schedules.DeferredCleanup)

	// --- triggers (§5) ---
	req(len(c.Triggers.Enabled) > 0, "triggers.enabled must list at least one trigger")
	req(c.Triggers.DebounceMS != nil && *c.Triggers.DebounceMS > 0,
		"triggers.debounce_ms is required; NFC tags and BLE buttons both bounce (§5.2 rule 4)")
	for _, tr := range c.Triggers.Enabled {
		switch tr {
		case "ui", "voice_launch", "nfc", "ble_hid", "ble_gatt":
			// Adapter-backed sources (§5.1).
		case "auto", "telegram":
			// §5.2 rule 6: these are provenance-only. `auto` is emitted by the
			// controller when resuming an interrupted session; `telegram`
			// originates server-side. Neither has an adapter, so enabling one
			// as a trigger would mean writing an adapter that cannot exist —
			// "a registry containing one is a defect".
			p = append(p, fmt.Sprintf("triggers.enabled contains %q, which is a provenance-only source with no adapter (§5.2 rule 6)", tr))
		default:
			p = append(p, fmt.Sprintf("triggers.enabled contains unknown trigger %q", tr))
		}
	}

	return p
}

func inUnit(f float64) bool { return f >= 0 && f <= 1 }

// looksLikeInlineSecret catches the mistake that must never reach a commit: a
// literal API key pasted where a parameter path belongs. Not a security control
// — secret scanning and push protection are (§9.6) — but a fast, local failure
// beats discovering it in a scanning alert.
func looksLikeInlineSecret(ref string) bool {
	lower := strings.ToLower(ref)
	for _, marker := range []string{"sk-", "gsk_", "api_key=", "bearer ", "eyj"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Resolved accessors
// ---------------------------------------------------------------------------
//
// These exist so that callers never dereference a pointer or re-check a
// constraint validate() already proved. A caller that reads
// cfg.ActiveSTT().Capabilities.PromptBiasing gets a bool, not a *bool, because
// by the time a Config exists the value is known to be present.

// ActiveSTT returns the active STT provider entry. Safe after validation.
func (c *Config) ActiveSTT() STTCatalog { return c.Providers.STT.Catalog[c.Providers.STT.Active] }

// ShadowSTT returns the shadow provider entry and whether shadow mode is on.
func (c *Config) ShadowSTT() (STTCatalog, bool) {
	if c.Providers.STT.Shadow == nil {
		return STTCatalog{}, false
	}
	return c.Providers.STT.Catalog[*c.Providers.STT.Shadow], true
}

// PromptBiasingAvailable reports whether the active STT provider can bias its
// decode with supplied vocabulary.
//
// When this is false, Phase 4 must route all corrections to the LLM cleanup
// layer rather than silently producing none (§7.1). That is a code path with a
// test, which is why this is a method rather than an inline field read.
func (c *Config) PromptBiasingAvailable() bool {
	caps := c.ActiveSTT().Capabilities
	return caps.PromptBiasing != nil && *caps.PromptBiasing
}

// PromptTokenBudget returns the active provider's biasing budget in tokens.
func (c *Config) PromptTokenBudget() int {
	caps := c.ActiveSTT().Capabilities
	if caps.PromptTokenBudget == nil {
		return 0
	}
	return *caps.PromptTokenBudget
}

// LLMForTask resolves the catalog entry for one pipeline task (§10.5.4).
func (c *Config) LLMForTask(task string) (LLMCatalog, error) {
	var key string
	switch task {
	case "cleanup":
		key = c.Providers.LLM.Tasks.Cleanup
	case "routing":
		key = c.Providers.LLM.Tasks.Routing
	case "summary":
		key = c.Providers.LLM.Tasks.Summary
	case "search":
		key = c.Providers.LLM.Tasks.Search
	default:
		return LLMCatalog{}, fmt.Errorf("config: unknown llm task %q", task)
	}
	e, ok := c.Providers.LLM.Catalog[key]
	if !ok {
		// Unreachable after validation; returned rather than panicked so a
		// future task added without a validation entry fails loudly.
		return LLMCatalog{}, fmt.Errorf("config: llm.tasks.%s names %q, absent from the catalog", task, key)
	}
	return e, nil
}
