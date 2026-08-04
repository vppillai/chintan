package keys

import (
	"strings"
	"testing"
)

// The tests that matter most here are the negative ones. A key builder that
// produces the right string for good input but also produces something for
// empty input is how an unscoped query gets written (I11), so refusal is the
// behaviour under test.

func TestEveryConstructorRefusesAnEmptyTenant(t *testing.T) {
	// Table-driven over every exported constructor, so a new entity added
	// without a tenant guard fails here rather than shipping.
	cases := map[string]func(TenantID) error{
		"Tenant":      func(id TenantID) error { _, err := Tenant(id); return err },
		"User":        func(id TenantID) error { _, err := User(id, "u_1"); return err },
		"Capture":     func(id TenantID) error { _, err := Capture(id, "c_1"); return err },
		"Item":        func(id TenantID) error { _, err := Item(id, "i_1"); return err },
		"Thread":      func(id TenantID) error { _, err := Thread(id, "th_1"); return err },
		"Segment":     func(id TenantID) error { _, err := Segment(id, "c_1", 0); return err },
		"Session":     func(id TenantID) error { _, err := Session(id, "c_1", "s_1"); return err },
		"Ingest":      func(id TenantID) error { _, err := Ingest(id, "abc123"); return err },
		"Rule":        func(id TenantID) error { _, err := Rule(id, "TNSTRNT"); return err },
		"Usage":       func(id TenantID) error { _, err := Usage(id, "2026-08", "stt_seconds", "01H"); return err },
		"Audit":       func(id TenantID) error { _, err := Audit(id, "01H"); return err },
		"Metric":      func(id TenantID) error { _, err := Metric(id, "2026-08-04", "wer"); return err },
		"Idempotency": func(id TenantID) error { _, err := Idempotency(id, "k1"); return err },

		"S3TenantPrefix":      func(id TenantID) error { _, err := S3TenantPrefix(id); return err },
		"S3AudioSegment":      func(id TenantID) error { _, err := S3AudioSegment(id, "c_1", "seg_000"); return err },
		"S3AudioContinuous":   func(id TenantID) error { _, err := S3AudioContinuous(id, "c_1", "s_1"); return err },
		"S3CaptureContent":    func(id TenantID) error { _, err := S3CaptureContent(id, "c_1"); return err },
		"S3CaptureAlignment":  func(id TenantID) error { _, err := S3CaptureAlignment(id, "c_1"); return err },
		"S3TranscriptL0":      func(id TenantID) error { _, err := S3TranscriptL0(id, "c_1", "groq-01H", "seg_000"); return err },
		"S3TranscriptL1":      func(id TenantID) error { _, err := S3TranscriptL1(id, "c_1", "v1"); return err },
		"S3ItemText":          func(id TenantID) error { _, err := S3ItemText(id, "i_1"); return err },
		"S3EmbeddingsMatrix":  func(id TenantID) error { _, err := S3EmbeddingsMatrix(id); return err },
		"S3EmbeddingsMeta":    func(id TenantID) error { _, err := S3EmbeddingsMeta(id); return err },
		"GSI1":                func(id TenantID) error { _, _, err := GSI1(id, "2026-08-04T10:00:00Z"); return err },
		"UsageMonthPrefix":    func(id TenantID) error { _, _, err := UsageMonthPrefix(id, "2026-08"); return err },
		"S3TranscriptL0RunPr": func(id TenantID) error { _, err := S3TranscriptL0RunPrefix(id, "c_1", "groq-01H"); return err },
	}

	// Whitespace-only counts as empty: a tenant of " " would otherwise build a
	// syntactically valid key in a partition nothing else writes to, which is
	// harder to notice than an outright error.
	for _, empty := range []TenantID{"", " ", "\t", "\n"} {
		for name, fn := range cases {
			if err := fn(empty); err == nil {
				t.Errorf("%s(%q) returned no error; every key must be tenant-scoped (I11)", name, string(empty))
			}
		}
	}
}

func TestConstructorsRejectDelimiterInjection(t *testing.T) {
	// An ID containing the key delimiter could forge another entity's key. This
	// is why identRe excludes "#" rather than escaping it.
	forged := "c_1#SESSION#s_evil"
	if _, err := Capture("t1", forged); err == nil {
		t.Errorf("Capture accepted an ID containing the key delimiter: %q", forged)
	}
	if _, err := Item("t1", "i#1"); err == nil {
		t.Error("Item accepted an ID containing '#'")
	}
	if _, err := Tenant(TenantID("t#1")); err == nil {
		t.Error("Tenant accepted a tenant ID containing '#'")
	}
	// Path traversal in an S3 key would escape the tenant prefix that IAM
	// conditions are written against (§9.1), so "/" and ".." must be refused.
	if _, err := S3ItemText("t1", "../../other-tenant/secret"); err == nil {
		t.Error("S3ItemText accepted a traversal sequence; the tenant prefix is an IAM boundary")
	}
	if _, err := S3TenantPrefix(TenantID("../other")); err == nil {
		t.Error("S3TenantPrefix accepted a traversal sequence in the tenant ID")
	}
}

func TestEveryDynamoKeyCarriesTheTenantPrefix(t *testing.T) {
	const tenant TenantID = "t_01HXYZ"
	built := []DynamoKey{}
	for _, fn := range []func() (DynamoKey, error){
		func() (DynamoKey, error) { return Tenant(tenant) },
		func() (DynamoKey, error) { return User(tenant, "u_1") },
		func() (DynamoKey, error) { return Capture(tenant, "c_1") },
		func() (DynamoKey, error) { return Item(tenant, "i_1") },
		func() (DynamoKey, error) { return Thread(tenant, "th_1") },
		func() (DynamoKey, error) { return Segment(tenant, "c_1", 7) },
		func() (DynamoKey, error) { return Session(tenant, "c_1", "s_1") },
		func() (DynamoKey, error) { return Ingest(tenant, "deadbeef") },
		func() (DynamoKey, error) { return Rule(tenant, "TNSTRNT") },
		func() (DynamoKey, error) { return Usage(tenant, "2026-08", "stt_seconds", "01H") },
		func() (DynamoKey, error) { return Audit(tenant, "01H") },
		func() (DynamoKey, error) { return Metric(tenant, "2026-08-04", "corrected_wer") },
		func() (DynamoKey, error) { return Idempotency(tenant, "idem-1") },
	} {
		k, err := fn()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		built = append(built, k)
	}

	for _, k := range built {
		if !strings.HasPrefix(k.PK, "TENANT#") {
			t.Errorf("PK %q does not carry the tenant prefix (I11)", k.PK)
		}
		if !strings.Contains(k.PK, string(tenant)) {
			t.Errorf("PK %q does not contain the tenant id", k.PK)
		}
		if k.SK == "" {
			t.Errorf("PK %q produced an empty sort key", k.PK)
		}
	}
}

// The Telegram link record is the documented single exception to the
// tenant-prefix rule (§6.3), because it is the lookup that resolves a tenant.
// Asserted explicitly so that if someone later "fixes" it to be tenant-scoped —
// which would make it unusable for its only purpose — the intent is on record.
func TestTelegramLinkIsTheOnlyNonTenantScopedKey(t *testing.T) {
	k, err := TelegramLink("12345")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.HasPrefix(k.PK, "TENANT#") {
		t.Error("TelegramLink is tenant-scoped, but it exists to resolve the tenant it would need")
	}
	if k.PK != "TG#12345" || k.SK != "LINK" {
		t.Errorf("TelegramLink key = %+v, want PK=TG#12345 SK=LINK", k)
	}
	if _, err := TelegramLink(""); err == nil {
		t.Error("TelegramLink accepted an empty sender id")
	}
}

func TestSegmentSequenceSortsNumerically(t *testing.T) {
	// The failure this prevents: without zero-padding, segment 10 sorts before
	// segment 2 and the transcript reassembles out of order — which presents as
	// a transcription fault, not a key-format one.
	k2, err := Segment("t1", "c_1", 2)
	if err != nil {
		t.Fatal(err)
	}
	k10, err := Segment("t1", "c_1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if !(k2.SK < k10.SK) {
		t.Errorf("segment 2 (%q) does not sort before segment 10 (%q)", k2.SK, k10.SK)
	}
	if !strings.HasSuffix(k2.SK, "000002") {
		t.Errorf("segment SK %q is not zero-padded to six digits", k2.SK)
	}
	if _, err := Segment("t1", "c_1", -1); err == nil {
		t.Error("Segment accepted a negative sequence")
	}
	if _, err := Segment("t1", "c_1", 1000000); err == nil {
		t.Error("Segment accepted a sequence past the sortable range")
	}
}

func TestL0PathIncludesRunID(t *testing.T) {
	// I1: a second transcription of the same capture must not overwrite the
	// first. The run dimension in the path is what guarantees that, so assert
	// two runs of the same segment land in different objects.
	a, err := S3TranscriptL0("t1", "c_1", "groq_whisper_turbo-01HA", "seg_000")
	if err != nil {
		t.Fatal(err)
	}
	b, err := S3TranscriptL0("t1", "c_1", "sarvam_saaras_v3-01HB", "seg_000")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two L0 runs of one segment resolve to the same key; this is an I1 violation")
	}
	for _, k := range []string{a, b} {
		if !strings.Contains(k, "/transcripts/L0/") {
			t.Errorf("L0 key %q is not under the L0 prefix", k)
		}
	}
	if _, err := S3TranscriptL0("t1", "c_1", "", "seg_000"); err == nil {
		t.Error("S3TranscriptL0 accepted an empty run_id; the run dimension is mandatory (§6.1)")
	}
}

func TestEveryS3KeyStartsWithTheTenantPrefix(t *testing.T) {
	const tenant TenantID = "t_01HXYZ"
	want := "tenants/" + string(tenant) + "/"

	keys := []func() (string, error){
		func() (string, error) { return S3TenantPrefix(tenant) },
		func() (string, error) { return S3AudioSegment(tenant, "c_1", "seg_000") },
		func() (string, error) { return S3AudioContinuous(tenant, "c_1", "s_1") },
		func() (string, error) { return S3CaptureContent(tenant, "c_1") },
		func() (string, error) { return S3CaptureAlignment(tenant, "c_1") },
		func() (string, error) { return S3TranscriptL0(tenant, "c_1", "r_1", "seg_000") },
		func() (string, error) { return S3TranscriptL1(tenant, "c_1", "v1") },
		func() (string, error) { return S3ItemText(tenant, "i_1") },
		func() (string, error) { return S3EmbeddingsMatrix(tenant) },
		func() (string, error) { return S3EmbeddingsMeta(tenant) },
	}
	for _, fn := range keys {
		k, err := fn()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.HasPrefix(k, want) {
			t.Errorf("S3 key %q is not under the tenant prefix %q; IAM conditions rely on it (§9.1)", k, want)
		}
	}
}

func TestGSI1RequiresUTCTimestamp(t *testing.T) {
	if _, _, err := GSI1("t1", "2026-08-04T10:00:00Z"); err != nil {
		t.Fatalf("valid UTC timestamp rejected: %v", err)
	}
	// A local-offset timestamp sorts wrongly against a UTC one, which would
	// silently mis-order the capture list rather than failing visibly.
	for _, bad := range []string{
		"2026-08-04T10:00:00+05:30",
		"2026-08-04 10:00:00Z",
		"2026-08-04",
		"",
	} {
		if _, _, err := GSI1("t1", bad); err == nil {
			t.Errorf("GSI1 accepted a non-UTC-RFC3339 timestamp: %q", bad)
		}
	}
}

func TestUsageAndMetricRejectMalformedPeriods(t *testing.T) {
	if _, err := Usage("t1", "2026-8", "stt_seconds", "01H"); err == nil {
		t.Error("Usage accepted an unpadded month; the SK range read depends on a fixed width")
	}
	if _, err := Usage("t1", "2026-13", "stt_seconds", "01H"); err == nil {
		t.Error("Usage accepted month 13")
	}
	if _, err := Metric("t1", "2026-08-32", "wer"); err == nil {
		t.Error("Metric accepted day 32")
	}
}
