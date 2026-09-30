package pipeline

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// releaseFailsStore clears the note's stamp and refuses the capture write, so
// only the claim half of releaseAppendClaim fails.
type releaseFailsStore struct{ repository.Store }

func (releaseFailsStore) ClearNoteAppend(context.Context, string, string, string) error { return nil }

func (releaseFailsStore) PutCapture(context.Context, model.CaptureIndex) (model.CaptureIndex, error) {
	return model.CaptureIndex{}, errors.New("induced capture write failure")
}

// A release whose capture write fails used to be dropped without a word, so
// the log read the same as a release that worked and the next attempt's wait
// for the lease had no explanation (R7-13). It is a Warn now, as the stamp's
// half already was, and the in-memory capture keeps its claim.
func TestReleaseAppendClaimWarnsWhenTheCaptureWriteFails(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(prev)

	p := &Pipeline{cfg: Config{Store: releaseFailsStore{}}}
	capture := model.CaptureIndex{ID: "c_1", UserID: "u1", NoteID: "n1", AppendToken: "t", AppendClaimedAt: 42}
	p.releaseAppendClaim(context.Background(), &capture)

	if !strings.Contains(logs.String(), `"could not release the capture's append claim`) {
		t.Fatalf("no warning for the failed release; logs:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), `"level":"WARN"`) {
		t.Errorf("the failed release was not logged at WARN; logs:\n%s", logs.String())
	}
	if capture.AppendToken != "t" {
		t.Errorf("the capture lost its claim in memory though the write failed: %+v", capture)
	}
}
