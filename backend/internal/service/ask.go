package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/vppillai/chintan/backend/internal/ask"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// Ask (backlog D5): a question over the tenant's own notes.
//
// The request half is this file and it does no retrieval and calls no model.
// POST /v1/ask validates, writes a pending row, hands the id to the worker and
// answers 202; GET /v1/ask/{id} reads the row back. The worker's ask task
// (internal/pipeline ask.go) is where the notes are ranked, packed and put in
// front of the model — the same shape as clean-note, and for the same reason:
// the API's integration is bounded by the gateway's 30-second ceiling and a
// retrieval pass plus an LLM call is not.

// ErrAskUnavailable means no worker invoker is wired, so a question has
// nowhere to go. Distinct from ErrCaptureWorkerUnavailable only in what the
// 503 says.
var ErrAskUnavailable = errors.New("ask is not configured")

// Validation errors for the question and its history. They are typed so the
// handler maps them to 400 without reading their text.
var (
	ErrAskQuestionRequired = errors.New("question is required")
	ErrAskQuestionTooLong  = fmt.Errorf("question is longer than %d characters", ask.MaxQuestionRunes)
	ErrAskHistoryTooLong   = fmt.Errorf("history may carry at most %d earlier turns", ask.MaxHistoryTurns)
	ErrAskHistoryTurnBad   = fmt.Errorf("each history turn needs a question of at most %d characters and an answer of at most %d", ask.MaxQuestionRunes, ask.MaxHistoryAnswerRunes)
)

// AskService owns the fast half of a question's lifecycle.
type AskService struct {
	store  repository.Store
	worker Invoker
	now    func() time.Time
}

// NewAskService builds the service. A nil worker makes every Begin answer
// ErrAskUnavailable rather than run anything inline.
func NewAskService(store repository.Store, worker Invoker) *AskService {
	return &AskService{store: store, worker: worker, now: time.Now}
}

// ValidateAsk checks a question and its history against the bounds the API
// declares. It returns the trimmed question. History turns are trimmed too;
// an empty question in a turn is refused because a turn is a pair.
func ValidateAsk(question string, history []model.AskTurn) (string, []model.AskTurn, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		return "", nil, ErrAskQuestionRequired
	}
	if len([]rune(question)) > ask.MaxQuestionRunes {
		return "", nil, ErrAskQuestionTooLong
	}
	if len(history) > ask.MaxHistoryTurns {
		return "", nil, ErrAskHistoryTooLong
	}
	turns := make([]model.AskTurn, 0, len(history))
	for _, turn := range history {
		q, a := strings.TrimSpace(turn.Question), strings.TrimSpace(turn.Answer)
		if q == "" || len([]rune(q)) > ask.MaxQuestionRunes || len([]rune(a)) > ask.MaxHistoryAnswerRunes {
			return "", nil, ErrAskHistoryTurnBad
		}
		turns = append(turns, model.AskTurn{Question: q, Answer: a})
	}
	return question, turns, nil
}

// Begin writes the pending row and hands it to the worker. The row is written
// first: an invocation that lands before the row exists would find nothing to
// answer, and the worker treats a missing row as done rather than retryable.
func (s *AskService) Begin(ctx context.Context, userID, question string, history []model.AskTurn) (model.Ask, error) {
	question, history, err := ValidateAsk(question, history)
	if err != nil {
		return model.Ask{}, err
	}
	if s.worker == nil {
		return model.Ask{}, ErrAskUnavailable
	}

	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return model.Ask{}, fmt.Errorf("failed to generate ask id: %w", err)
	}
	now := s.now().UTC()
	// A fixed-width instant first, as capture ids do, so the rows sort by
	// time under the partition; the random half keeps the id unguessable.
	a := model.Ask{
		ID:        fmt.Sprintf("ask_%016x_%s", uint64(now.UnixNano()), hex.EncodeToString(suffix)),
		UserID:    userID,
		Status:    model.AskPending,
		Question:  question,
		History:   history,
		CreatedAt: model.FormatTime(now),
		ExpiresAt: now.Add(model.AskTTL).Unix(),
	}
	if err := s.store.PutAsk(ctx, userID, a); err != nil {
		return model.Ask{}, fmt.Errorf("failed to store ask: %w", err)
	}
	if err := s.worker.InvokeAsk(ctx, userID, a.ID); err != nil {
		// The pending row is left to its TTL. Recording a failure on it
		// would give a client that never received the 202 a row it cannot
		// have asked about.
		return model.Ask{}, fmt.Errorf("failed to hand the ask to the worker: %w", err)
	}
	// Shape only: the question is user content and stays out of the log.
	obs.Log(ctx).Info("ask handed to the worker",
		slog.String("ask_id", a.ID),
		slog.Int("history_turns", len(history)),
		slog.Any("question", obs.Redact(question)))
	obs.Count(ctx, "AskRequested", map[string]string{"Trigger": "user"})
	return a, nil
}

// Get reads the caller's own row. Another tenant's id is simply absent.
func (s *AskService) Get(ctx context.Context, userID, askID string) (model.Ask, error) {
	return s.store.GetAsk(ctx, userID, askID)
}
