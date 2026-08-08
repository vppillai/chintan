package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/provider"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// emfRecord is as much of an Embedded Metric Format line as these tests read.
// Dimension VALUES are plain top-level keys on the same object, so the decoded
// map is kept alongside.
type emfRecord struct {
	Values map[string]any
	Names  []string
	// DimensionSets is the identities this record declares. A metric an alarm
	// can read without naming dimensions has an empty set among them.
	DimensionSets [][]string
}

func decodeMetrics(t *testing.T, raw []byte) []emfRecord {
	t.Helper()
	var out []emfRecord
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var values map[string]any
		if err := json.Unmarshal([]byte(line), &values); err != nil {
			t.Fatalf("metric line is not JSON: %v (%s)", err, line)
		}
		var envelope struct {
			AWS struct {
				CloudWatchMetrics []struct {
					Dimensions [][]string `json:"Dimensions"`
					Metrics    []struct {
						Name string `json:"Name"`
					} `json:"Metrics"`
				} `json:"CloudWatchMetrics"`
			} `json:"_aws"`
		}
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			t.Fatalf("metric line has no EMF envelope: %v (%s)", err, line)
		}
		for _, directive := range envelope.AWS.CloudWatchMetrics {
			rec := emfRecord{Values: values, DimensionSets: directive.Dimensions}
			for _, m := range directive.Metrics {
				rec.Names = append(rec.Names, m.Name)
			}
			out = append(out, rec)
		}
	}
	return out
}

// findMetric returns the single record carrying name, failing if there is not
// exactly one. "Exactly one" matters: a dead key must produce one datapoint per
// failed capture, not several, or the alarm threshold means something different
// from what the template says.
func findMetric(t *testing.T, records []emfRecord, name string) emfRecord {
	t.Helper()
	var found []emfRecord
	for _, rec := range records {
		for _, n := range rec.Names {
			if n == name {
				found = append(found, rec)
			}
		}
	}
	if len(found) != 1 {
		t.Fatalf("found %d records carrying %s, want exactly 1 (saw %v)",
			len(found), name, metricNames(records))
	}
	return found[0]
}

func hasMetric(records []emfRecord, name string) bool {
	for _, rec := range records {
		for _, n := range rec.Names {
			if n == name {
				return true
			}
		}
	}
	return false
}

func metricNames(records []emfRecord) []string {
	var out []string
	for _, rec := range records {
		out = append(out, rec.Names...)
	}
	return out
}

// runFailingTranscribe drives one capture whose transcription is refused with
// err, and returns the stored capture and every metric the run emitted.
func runFailingTranscribe(t *testing.T, err error) (model.CaptureIndex, []emfRecord) {
	t.Helper()
	h := newHarness(t, harnessOpts{})
	h.stt.Err = err

	ctx := context.Background()
	capture := model.CaptureIndex{
		ID: "cap_fail", UserID: "user1", Status: model.StatusUploaded,
		CreatedAt: model.Now(), AudioKey: "tenants/user1/captures/cap_fail/audio.webm",
	}
	if _, err := h.store.PutCapture(ctx, capture); err != nil {
		t.Fatalf("PutCapture: %v", err)
	}

	var metrics bytes.Buffer
	restore := obs.SetMetricOutput(&metrics)
	final, runErr := h.pipeline.Run(ctx, "user1", capture.ID)
	restore()

	if runErr != nil {
		t.Fatalf("Run returned %v; a provider verdict must not ask SQS to redeliver", runErr)
	}
	return final, decodeMetrics(t, metrics.Bytes())
}

// TestARevokedKeyIsRecordedAsARevokedKey is the terminal half.
//
// A 401 or 403 means the credential is gone and every capture after this one
// fails identically. Recording it as an ordinary failure tells the user to try
// again, which cannot work, and gives an operator nothing to alarm on that is
// not also every other provider fault.
func TestARevokedKeyIsRecordedAsARevokedKey(t *testing.T) {
	for _, status := range []int{401, 403} {
		t.Run(http(status), func(t *testing.T) {
			final, records := runFailingTranscribe(t,
				&provider.StatusError{Op: "groq transcription failed", StatusCode: status})

			if final.Status != model.StatusFailed {
				t.Fatalf("status = %s, want %s", final.Status, model.StatusFailed)
			}
			if final.Error != ErrProviderKeyRejected.Error() {
				t.Errorf("error = %q, want %q — a generic failure leaves the UI unable to say what happened",
					final.Error, ErrProviderKeyRejected.Error())
			}

			rec := findMetric(t, records, "ProviderKeyRejected")
			if got := rec.Values["Provider"]; got != "groq" {
				t.Errorf("Provider dimension = %v, want groq", got)
			}
			if hasMetric(records, "ProviderRateLimited") {
				t.Error("a revoked key was also counted as rate limiting; the two alarms would fire together")
			}
		})
	}
}

// TestARateLimitIsNotARevokedKey is the transient half, and the direction that
// produces false alarms if it regresses. A 429 is ordinary and self-resolving;
// counting it as a key rejection would email the owner on the first busy minute
// of the provider's day.
func TestARateLimitIsNotARevokedKey(t *testing.T) {
	final, records := runFailingTranscribe(t,
		&provider.StatusError{Op: "groq transcription failed", StatusCode: 429})

	if final.Status != model.StatusFailed {
		t.Fatalf("status = %s, want %s", final.Status, model.StatusFailed)
	}
	if final.Error == ErrProviderKeyRejected.Error() {
		t.Error("a throttle was recorded as a rejected key")
	}
	if hasMetric(records, "ProviderKeyRejected") {
		t.Fatal("a 429 raised the key-rejected counter; that alarm emails on the first occurrence")
	}
	rec := findMetric(t, records, "ProviderRateLimited")
	if got := rec.Values["Provider"]; got != "groq" {
		t.Errorf("Provider dimension = %v, want groq", got)
	}
}

// TestAnOrdinaryProviderFaultRaisesNeitherCounter keeps the two new alarms from
// firing on a 500, a timeout or a decode failure. Those are already covered by
// CaptureStageFailures and by the worker's own error alarm.
func TestAnOrdinaryProviderFaultRaisesNeitherCounter(t *testing.T) {
	for name, err := range map[string]error{
		"a server error":     &provider.StatusError{Op: "groq transcription failed", StatusCode: 500},
		"a transport fault":  errors.New("provider: groq request: connection reset"),
		"a decode failure":   errors.New("provider: decode groq response: unexpected EOF"),
		"a repository fault": repository.ErrNotFound,
	} {
		t.Run(name, func(t *testing.T) {
			_, records := runFailingTranscribe(t, err)
			if hasMetric(records, "ProviderKeyRejected") {
				t.Error("raised ProviderKeyRejected")
			}
			if hasMetric(records, "ProviderRateLimited") {
				t.Error("raised ProviderRateLimited")
			}
			if !hasMetric(records, "CaptureStageFailures") {
				t.Error("did not raise CaptureStageFailures")
			}
		})
	}
}

// TestBothAlarmMetricsCarryADimensionlessRollup is what makes the alarms in
// infrastructure/template.yaml plain metric alarms rather than Metrics Insights
// queries.
//
// obs.Emit declares one dimension set, so a metric dimensioned by Provider has
// no identity an alarm can name without naming every provider. The rollup is
// that identity. Without it the alarms silently never receive a datapoint and,
// under TreatMissingData: notBreaching, sit green through a dead key —
// docs/cost-analysis.md §5.3 records exactly that failure for
// SpendCapRejections.
func TestBothAlarmMetricsCarryADimensionlessRollup(t *testing.T) {
	cases := map[string]struct {
		err  error
		name string
	}{
		"key rejected": {&provider.StatusError{Op: "groq transcription failed", StatusCode: 401}, "ProviderKeyRejected"},
		"rate limited": {&provider.StatusError{Op: "groq transcription failed", StatusCode: 429}, "ProviderRateLimited"},
	}

	for label, tc := range cases {
		t.Run(label, func(t *testing.T) {
			_, records := runFailingTranscribe(t, tc.err)
			rec := findMetric(t, records, tc.name)

			var dimensioned, dimensionless bool
			for _, set := range rec.DimensionSets {
				switch len(set) {
				case 0:
					dimensionless = true
				case 1:
					dimensioned = dimensioned || set[0] == "Provider"
				}
			}
			if !dimensioned {
				t.Errorf("%s declares no Provider dimension set: %v", tc.name, rec.DimensionSets)
			}
			if !dimensionless {
				t.Errorf("%s declares no dimensionless rollup: %v — the alarm would never get a datapoint",
					tc.name, rec.DimensionSets)
			}
		})
	}
}

// TestTheCleanupProviderIsNamedOnItsOwnFailures proves the Provider dimension
// follows the stage rather than being hardcoded to the speech provider. A
// rejected LLM key attributed to groq sends an operator to replace the wrong
// credential.
func TestTheCleanupProviderIsNamedOnItsOwnFailures(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.llm.Err = &provider.StatusError{Op: "llm request failed", StatusCode: 403}

	ctx := context.Background()
	note, err := h.creator.CreateNote(ctx, "user1", "Roof repair", nil)
	if err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	capture := model.CaptureIndex{
		ID: "cap_llm", UserID: "user1", NoteID: note.ID, Status: model.StatusUploaded,
		CreatedAt: model.Now(), AudioKey: "tenants/user1/captures/cap_llm/audio.webm",
	}
	if _, err := h.store.PutCapture(ctx, capture); err != nil {
		t.Fatalf("PutCapture: %v", err)
	}

	var metrics bytes.Buffer
	restore := obs.SetMetricOutput(&metrics)
	final, runErr := h.pipeline.Run(ctx, "user1", capture.ID)
	restore()
	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}
	if final.Status != model.StatusFailed {
		t.Fatalf("status = %s, want %s", final.Status, model.StatusFailed)
	}

	rec := findMetric(t, decodeMetrics(t, metrics.Bytes()), "ProviderKeyRejected")
	if got := rec.Values["Provider"]; got != "openai" {
		t.Errorf("Provider dimension = %v, want openai — the LLM key is the one that was refused", got)
	}
}

// http renders a status for a subtest name without pulling in net/http for one
// integer.
func http(status int) string {
	switch status {
	case 401:
		return "401 unauthorized"
	case 403:
		return "403 forbidden"
	default:
		return "other"
	}
}
