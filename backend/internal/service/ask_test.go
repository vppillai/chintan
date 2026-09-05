package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
)

func TestValidateAskBoundsTheQuestionAndTheHistory(t *testing.T) {
	turn := func(q, a string) model.AskTurn { return model.AskTurn{Question: q, Answer: a} }
	six := make([]model.AskTurn, 6)
	for i := range six {
		six[i] = turn("q", "a")
	}
	for _, tc := range []struct {
		name     string
		question string
		history  []model.AskTurn
		wantErr  error
		wantQ    string
	}{
		{"trimmed question", "  roof?  ", nil, nil, "roof?"},
		{"six turns are fine", "roof?", six, nil, "roof?"},
		{"empty", "  ", nil, ErrAskQuestionRequired, ""},
		{"over 1000 runes", strings.Repeat("é", 1001), nil, ErrAskQuestionTooLong, ""},
		{"exactly 1000 runes", strings.Repeat("é", 1000), nil, nil, strings.Repeat("é", 1000)},
		{"seven turns", "roof?", append(six, turn("q", "a")), ErrAskHistoryTooLong, ""},
		{"turn without a question", "roof?", []model.AskTurn{turn(" ", "a")}, ErrAskHistoryTurnBad, ""},
		{"turn with an over-long answer", "roof?", []model.AskTurn{turn("q", strings.Repeat("a", 4001))}, ErrAskHistoryTurnBad, ""},
		{"turn with an empty answer is allowed", "roof?", []model.AskTurn{turn("q", "")}, nil, "roof?"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, _, err := ValidateAsk(tc.question, tc.history)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if q != tc.wantQ {
				t.Errorf("question = %q, want %q", q, tc.wantQ)
			}
		})
	}
}

// Begin writes the row before the hand-off, and without a worker writes
// nothing.
func TestAskBeginWritesThePendingRowThenHandsOff(t *testing.T) {
	store := memory.NewStore()
	worker := &stubInvoker{}
	svc := NewAskService(store, worker)
	a, err := svc.Begin(context.Background(), "user1", "roof?", nil)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if a.Status != model.AskPending || !strings.HasPrefix(a.ID, "ask_") || a.ExpiresAt == 0 || a.CreatedAt == "" {
		t.Errorf("ask = %+v", a)
	}
	if len(worker.calls) != 1 || worker.calls[0] != "ask/user1/"+a.ID {
		t.Errorf("hand-offs = %v", worker.calls)
	}
	if stored, err := store.GetAsk(context.Background(), "user1", a.ID); err != nil || stored.Question != "roof?" {
		t.Errorf("stored = %+v, %v", stored, err)
	}

	if _, err := NewAskService(store, nil).Begin(context.Background(), "user1", "roof?", nil); !errors.Is(err, ErrAskUnavailable) {
		t.Errorf("no worker: err = %v, want ErrAskUnavailable", err)
	}
	if _, err := svc.Begin(context.Background(), "user1", "  ", nil); !errors.Is(err, ErrAskQuestionRequired) {
		t.Errorf("empty question: err = %v", err)
	}
}

// The destination's provenance is recorded where it is decided.
func TestCaptureTargetSourceIsRecordedByWhoChose(t *testing.T) {
	store := memory.NewStore()
	objects := memory.NewObjects()
	worker := &stubInvoker{}
	svc := NewCaptureService(store, objects).WithInvoker(worker)
	ctx := context.Background()
	if _, err := store.PutNote(ctx, "user1", model.NoteIndex{ID: "n1", Title: "Roof"}); err != nil {
		t.Fatalf("PutNote: %v", err)
	}

	aimed, err := svc.BeginCapture(ctx, "user1", CaptureRequest{NoteID: "n1", ContentType: "audio/webm"})
	if err != nil {
		t.Fatalf("BeginCapture: %v", err)
	}
	if aimed.Capture.TargetSource != model.TargetSourceClient || !aimed.Capture.TargetSource.Targeted() {
		t.Errorf("client-named note: source = %q", aimed.Capture.TargetSource)
	}
	open, err := svc.BeginCapture(ctx, "user1", CaptureRequest{ContentType: "audio/webm"})
	if err != nil {
		t.Fatalf("BeginCapture: %v", err)
	}
	if open.Capture.TargetSource != "" {
		t.Errorf("no note yet: source = %q, want empty", open.Capture.TargetSource)
	}

	// A person picks the note for the open one.
	stored, err := store.GetCapture(ctx, "user1", open.Capture.ID)
	if err != nil {
		t.Fatalf("GetCapture: %v", err)
	}
	stored.Status = model.StatusNeedsTarget
	if _, err := store.PutCapture(ctx, stored); err != nil {
		t.Fatalf("PutCapture: %v", err)
	}
	picked, err := svc.SetCaptureTarget(ctx, "user1", open.Capture.ID, "n1", "")
	if err != nil {
		t.Fatalf("SetCaptureTarget: %v", err)
	}
	if picked.TargetSource != model.TargetSourceUser || !picked.TargetSource.Targeted() {
		t.Errorf("user-chosen note: source = %q", picked.TargetSource)
	}
	if model.TargetSourceRouter.Targeted() || model.TargetSource("").Targeted() {
		t.Error("the router's choice, or none, counts as targeted")
	}
}
