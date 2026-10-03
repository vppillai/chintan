package service

import (
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// retry_after is CaptureStuck's rule, stated as an instant: the moment the
// rule first holds is the moment Retry is allowed, for every pending shape,
// and absent for a finished one.
func TestCaptureRetryAfterIsTheInstantCaptureStuckFirstHolds(t *testing.T) {
	last := time.Date(2026, 10, 3, 1, 41, 59, 0, time.UTC)
	claimed := last.Add(-2 * time.Minute)
	cases := []struct {
		name    string
		capture model.CaptureIndex
		want    time.Time
		wantOK  bool
	}{
		{"a stage write", model.CaptureIndex{Status: model.StatusTranscribing, LastProgressAt: model.FormatTime(last)}, last.Add(CaptureStuckAfter), true},
		{"no progress stamp falls back to creation", model.CaptureIndex{Status: model.StatusUploaded, CreatedAt: model.FormatTime(last)}, last.Add(CaptureStuckAfter), true},
		{"appending under a claim waits for the lease", model.CaptureIndex{Status: model.StatusAppending, LastProgressAt: model.FormatTime(last), AppendClaimedAt: claimed.Unix()}, claimed.Add(repository.AppendClaimLease), true},
		{"appending with no claim is the plain rule", model.CaptureIndex{Status: model.StatusAppending, LastProgressAt: model.FormatTime(last)}, last.Add(CaptureStuckAfter), true},
		{"a finished capture has none", model.CaptureIndex{Status: model.StatusAppended, LastProgressAt: model.FormatTime(last)}, time.Time{}, false},
		{"needs_target has none", model.CaptureIndex{Status: model.StatusNeedsTarget, LastProgressAt: model.FormatTime(last)}, time.Time{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := CaptureRetryAfter(tc.capture)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if got != model.FormatTime(tc.want) {
				t.Fatalf("retry_after = %s, want %s", got, model.FormatTime(tc.want))
			}
			// The rule and the instant agree: not stuck a second before, stuck at it.
			if CaptureStuck(tc.capture, tc.want.Add(-time.Second)) {
				t.Error("CaptureStuck held a second before retry_after")
			}
			if !CaptureStuck(tc.capture, tc.want) {
				t.Error("CaptureStuck did not hold at retry_after")
			}
		})
	}
}
