package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The real configs are the fixtures. Loading them here means a change to
// config/instances that breaks the schema fails the unit tests as well as the
// dedicated CI check — the shipped config and the validator cannot drift apart
// without something going red.
const instancesDir = "../../../config/instances"

func TestShippedInstanceConfigsAreValid(t *testing.T) {
	cfgs, err := LoadDir(instancesDir)
	if err != nil {
		t.Fatalf("shipped configs do not validate: %v", err)
	}
	for _, want := range []string{"dev", "prod"} {
		if _, ok := cfgs[want]; !ok {
			t.Errorf("no %q instance found in %s", want, instancesDir)
		}
	}
}

// loadDev returns the dev config's raw bytes, the basis for every mutation test
// below. Mutating a real config rather than a minimal fixture means these tests
// exercise the file that actually deploys.
func loadDev(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(instancesDir, "dev.yaml"))
	if err != nil {
		t.Fatalf("reading dev.yaml: %v", err)
	}
	return raw
}

// mutate applies a literal string replacement to the config source, failing if
// the target is absent — so a test cannot silently pass because the line it
// meant to break was renamed.
func mutate(t *testing.T, raw []byte, old, new string) []byte {
	t.Helper()
	s := string(raw)
	if !strings.Contains(s, old) {
		t.Fatalf("mutation target %q not present in dev.yaml; the test is stale", old)
	}
	return []byte(strings.Replace(s, old, new, 1))
}

// expectInvalid asserts the config is rejected and that the reason mentions the
// expected substring. Checking the message matters: a config rejected for the
// wrong reason would pass a bare "did it error" assertion while leaving the real
// constraint unverified.
func expectInvalid(t *testing.T, raw []byte, wantSubstring string) {
	t.Helper()
	_, err := Parse(raw, "test.yaml")
	if err == nil {
		t.Fatalf("config was accepted; expected rejection mentioning %q", wantSubstring)
	}
	if !strings.Contains(err.Error(), wantSubstring) {
		t.Fatalf("rejection did not mention %q:\n%v", wantSubstring, err)
	}
}

// This is the §Phase 0 requirement that a missing threshold fails the deploy
// rather than falling back to a hardcoded default. Removing a required key must
// be an error, not a silent zero.
func TestMissingRequiredThresholdIsRejected(t *testing.T) {
	raw := loadDev(t)
	cases := map[string]struct{ line, want string }{
		"max_change_ratio":     {"  max_change_ratio: 0.25", "cleanup.max_change_ratio is required"},
		"auto_file_confidence": {"  auto_file_confidence: 0.80", "extraction.auto_file_confidence is required"},
		"daily_spend_usd":      {"  daily_spend_usd: 0.50", "limits.daily_spend_usd is required"},
		"log_group_days":       {"  log_group_days: 14", "retention.log_group_days is required"},
		"preroll_ms":           {"    preroll_ms: 400", "capture.vad.preroll_ms is required"},
		"debounce_ms":          {"  debounce_ms: 750", "triggers.debounce_ms is required"},
		"topic_similarity_min": {"  topic_similarity_min: 0.65", "rules.topic_similarity_min is required"},
		"min_avg_logprob":      {"  min_avg_logprob: -0.55", "cleanup.min_avg_logprob is required"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			// Deleting the line entirely, which is how a key actually goes
			// missing — a hand edit, or a merge dropping a hunk.
			without := strings.Replace(string(raw), c.line+"\n", "", 1)
			if without == string(raw) {
				t.Fatalf("line %q not found; the test is stale", c.line)
			}
			expectInvalid(t, []byte(without), c.want)
		})
	}
}

// A zero value must be distinguishable from an absent key. This is the whole
// reason the parse structs use pointers, so it is asserted directly.
func TestExplicitZeroIsAcceptedWhereMeaningful(t *testing.T) {
	raw := mutate(t, loadDev(t), "  max_change_ratio: 0.25", "  max_change_ratio: 0.0")
	if _, err := Parse(raw, "test.yaml"); err != nil {
		t.Fatalf("an explicit 0.0 was rejected, so absent and zero are being conflated: %v", err)
	}
}

// §7.4: "the schema validator rejects a tasks value naming a model absent from
// the catalog." Otherwise the failure surfaces at the first pipeline run.
func TestLLMTaskNamingAbsentModelIsRejected(t *testing.T) {
	raw := mutate(t, loadDev(t), "      cleanup: minimax_m3", "      cleanup: some_model_that_does_not_exist")
	expectInvalid(t, raw, "not in providers.llm.catalog")
}

func TestUnknownKeyIsRejected(t *testing.T) {
	// A typo'd key would otherwise be ignored while the real key read as absent,
	// producing a confusing error about a value the operator can see in the file.
	raw := mutate(t, loadDev(t), "  candidate_k: 8", "  candidate_kk: 8")
	expectInvalid(t, raw, "candidate_kk")
}

// §3A.3 / A4: `prompt` must be held to a strictly higher bar, because a prompt
// misfiled as an idea gets summarised and the artifact is destroyed.
func TestPromptConfidenceMustExceedGeneralConfidence(t *testing.T) {
	raw := mutate(t, loadDev(t), "  prompt_kind_confidence: 0.90", "  prompt_kind_confidence: 0.70")
	expectInvalid(t, raw, "held to a higher bar")

	// Equal is also wrong: it gives up the protection while looking deliberate.
	raw = mutate(t, loadDev(t), "  prompt_kind_confidence: 0.90", "  prompt_kind_confidence: 0.80")
	expectInvalid(t, raw, "strictly above")
}

// Hysteresis requires offset below onset; equal thresholds chatter at the
// boundary and fragment segments instead of holding them.
func TestVADThresholdsMustProvideHysteresis(t *testing.T) {
	raw := mutate(t, loadDev(t), "    offset_threshold: 0.35", "    offset_threshold: 0.50")
	expectInvalid(t, raw, "hysteresis")
}

// G-059: Silero requires 16kHz and produces silently wrong boundaries otherwise.
// "Silently" is why this is a config-time rejection rather than a runtime check.
func TestVADSampleRateMustBe16k(t *testing.T) {
	raw := mutate(t, loadDev(t), "    sample_rate: 16000", "    sample_rate: 48000")
	expectInvalid(t, raw, "must be 16000")
}

// G-029: the Bot API getFile ceiling is 20MB. A larger value lets a download
// start that cannot finish.
func TestTelegramLimitCannotExceedBotAPICeiling(t *testing.T) {
	raw := mutate(t, loadDev(t), "  telegram_max_mb: 20", "  telegram_max_mb: 50")
	expectInvalid(t, raw, "20MB")
}

// §9 caps presigned URL lifetime at 15 minutes.
func TestPresignTTLIsCapped(t *testing.T) {
	raw := mutate(t, loadDev(t), "  presign_ttl_seconds: 900", "  presign_ttl_seconds: 3600")
	expectInvalid(t, raw, "900")
}

// §10.6: never a wildcard. This is an API serving someone's private thinking.
func TestWildcardOriginIsRejected(t *testing.T) {
	raw := mutate(t, loadDev(t), "allowed_origin: https://vppillai.github.io", "allowed_origin: https://*.github.io")
	expectInvalid(t, raw, "wildcard")
}

func TestNonHTTPSOriginIsRejected(t *testing.T) {
	raw := mutate(t, loadDev(t), "allowed_origin: https://vppillai.github.io", "allowed_origin: http://localhost:8080")
	expectInvalid(t, raw, "must be an https origin")
}

// §Phase 3 and §4A.5: every automatic filing decision is visible and reversible.
// Silent misfiling is the most damaging failure in this system, so this is not a
// tuning knob.
func TestAlwaysShowDecisionCannotBeDisabled(t *testing.T) {
	raw := mutate(t, loadDev(t), "  always_show_decision: true", "  always_show_decision: false")
	expectInvalid(t, raw, "must be true")
}

// §5.2 rule 6: `auto` and `telegram` are provenance-only and have no adapter.
// "A registry containing one is a defect."
func TestProvenanceOnlyTriggersCannotBeEnabled(t *testing.T) {
	for _, bad := range []string{"telegram", "auto"} {
		raw := mutate(t, loadDev(t), "  enabled: [ui, voice_launch]", "  enabled: [ui, voice_launch, "+bad+"]")
		expectInvalid(t, raw, "provenance-only")
	}
}

// G-042: an absent capability must not read as false, or learned corrections
// silently stop being applied with nothing to complain about.
func TestAbsentSTTCapabilityIsRejectedRatherThanDefaultingToFalse(t *testing.T) {
	raw := loadDev(t)
	without := strings.Replace(string(raw), "          prompt_biasing: true\n", "", 1)
	if without == string(raw) {
		t.Fatal("prompt_biasing line not found; the test is stale")
	}
	expectInvalid(t, []byte(without), "must not read as false")
}

// §7.2: shadow mode doubles STT spend, so shadowing the active provider would
// pay twice to compare a provider with itself.
func TestShadowCannotEqualActive(t *testing.T) {
	raw := mutate(t, loadDev(t), "    shadow: null", "    shadow: groq_whisper_turbo")
	expectInvalid(t, raw, "equals providers.stt.active")
}

func TestShadowMustBeInCatalog(t *testing.T) {
	raw := mutate(t, loadDev(t), "    shadow: null", "    shadow: not_a_provider")
	expectInvalid(t, raw, "not in providers.stt.catalog")
}

// §9.4: secrets are referenced by path, never by value. A local failure beats a
// secret-scanning alert after the push.
func TestInlineSecretIsRejected(t *testing.T) {
	raw := mutate(t, loadDev(t),
		"        secret_ref: /voicenotes/{env}/groq_api_key",
		"        secret_ref: /gsk_livekeymaterialpastedhere")
	expectInvalid(t, raw, "inline secret")
}

// §Phase 4: a rule is demoted before it is retired. Inverted, the LLM-candidate
// tier would never be used.
func TestRetireThresholdMustBeBelowDemote(t *testing.T) {
	raw := mutate(t, loadDev(t), "  retire_below_precision: 0.60", "  retire_below_precision: 0.95")
	expectInvalid(t, raw, "demoted before it is retired")
}

// §10.5.2: min_avg_logprob is a log probability. A positive value would skip
// every cleanup call while looking like a cost saving.
func TestPositiveLogprobIsRejected(t *testing.T) {
	raw := mutate(t, loadDev(t), "  min_avg_logprob: -0.55", "  min_avg_logprob: 0.55")
	expectInvalid(t, raw, "log probability")
}

// The filename is what the CI matrix and the stack name derive from, while the
// code reads the key — so a disagreement deploys one instance's config under
// another's stack name.
func TestInstanceKeyMustMatchFilename(t *testing.T) {
	dir := t.TempDir()
	raw := mutate(t, loadDev(t), "instance: dev", "instance: staging")
	if err := os.WriteFile(filepath.Join(dir, "dev.yaml"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadDir(dir)
	if err == nil {
		t.Fatal("a config whose instance key disagrees with its filename was accepted")
	}
	if !strings.Contains(err.Error(), "filename") {
		t.Fatalf("rejection did not explain the filename mismatch: %v", err)
	}
}

func TestEmptyInstanceDirectoryIsRejected(t *testing.T) {
	// An empty directory would make the CI matrix deploy nothing while
	// reporting success — a green pipeline that shipped no code.
	_, err := LoadDir(t.TempDir())
	if err == nil {
		t.Fatal("an empty instances directory was accepted")
	}
}

// The accessors exist so callers never re-check what validation already proved.
func TestResolvedAccessors(t *testing.T) {
	cfg, err := Load(filepath.Join(instancesDir, "dev.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.ActiveSTT().Model; got == "" {
		t.Error("ActiveSTT returned an entry with no model")
	}
	if !cfg.PromptBiasingAvailable() {
		t.Error("dev config's active provider should report prompt biasing available")
	}
	if got := cfg.PromptTokenBudget(); got != 224 {
		t.Errorf("PromptTokenBudget = %d, want 224 (G-010)", got)
	}
	if _, ok := cfg.ShadowSTT(); ok {
		t.Error("shadow mode should be off by default; it doubles STT spend (§7.2)")
	}
	for _, task := range []string{"cleanup", "routing", "summary", "search"} {
		if _, err := cfg.LLMForTask(task); err != nil {
			t.Errorf("LLMForTask(%q): %v", task, err)
		}
	}
	if _, err := cfg.LLMForTask("nonexistent"); err == nil {
		t.Error("LLMForTask accepted an unknown task")
	}
}
