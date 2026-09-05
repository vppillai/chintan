package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/vppillai/chintan/backend/internal/ask"
	"github.com/vppillai/chintan/backend/internal/breaker"
	"github.com/vppillai/chintan/backend/internal/meter"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/provider"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/service"
)

// TaskAsk is the worker task that answers one question over the tenant's
// notes (service/ask.go, backlog D5). The API sends it for POST /v1/ask.
//
// The payload is Invocation with Task set: {"task":"ask","tenant_id",
// "ask_id","correlation_id"}.
const TaskAsk = "ask"

// The verdicts an ask can leave on the row. Fixed sentences, for the same
// reason as the clean-note ones: they reach the user and must never carry a
// provider's words or anything from a note.
const (
	askProviderFail = "the answer could not be produced; try again"
	askSpendCapped  = "daily provider spend cap reached"
	askTooLong      = "the answer was too long"
)

const (
	// defaultAskAttemptTimeout bounds one call to the model. Twenty-five
	// seconds is long enough for a full 40,000-rune prompt to be answered
	// and short enough that a request queued at the provider is retried
	// rather than waited on; the person is watching a spinner.
	defaultAskAttemptTimeout = 25 * time.Second
	// askAttempts is how many times the model is asked before the row
	// records a failure. Two, for the reason routing gives: a stall or a
	// 5xx clears more often than not on the second try.
	askAttempts = 2
	// askOutputTokensEstimate is what an answer is reserved against before
	// the model reports; a few paragraphs, reconciled to what was used.
	askOutputTokensEstimate = 600
)

// Ask answers the question stored under askID from the tenant's notes and
// writes the answer — or a fixed reason it could not be produced — onto the
// row. The return value is the worker protocol: nil means the row reached a
// verdict and must not be retried; an error means an infrastructure fault
// interrupted it and Lambda should try again.
//
// One LLM call, through the breaker, on meter.OpAsk. Retrieval is lexical
// over the index rows (internal/ask), and the bodies are read only for the
// notes that ranked, best first, until the prompt budget is spent.
func (p *Pipeline) Ask(ctx context.Context, tenantID, askID string) error {
	ctx = obs.WithTenant(ctx, tenantID)
	log := obs.Log(ctx).With(slog.String("ask_id", askID))
	started := p.now()

	row, err := p.cfg.Store.GetAsk(ctx, tenantID, askID)
	if errors.Is(err, repository.ErrNotFound) {
		// Expired or never written. Nothing to answer, nothing to record it
		// on, and retrying cannot bring it back.
		log.Info("ask: the question no longer exists; nothing to do")
		return nil
	}
	if err != nil {
		return fmt.Errorf("pipeline: ask: get ask: %w", err)
	}
	if row.Status != model.AskPending {
		// A second delivery of a task the first one finished.
		log.Info("ask: already answered; nothing to do", slog.String("status", string(row.Status)))
		return nil
	}

	notes, err := repository.DrainPages(ctx, ask.MaxNotesConsidered, func(ctx context.Context, opts repository.ListOptions) (repository.Page[model.NoteIndex], error) {
		// The search text is the body the ranker reads; without it only
		// titles and names would score.
		opts.IncludeSearchText = true
		return p.cfg.Store.ListNotes(ctx, tenantID, opts)
	})
	if err != nil {
		return fmt.Errorf("pipeline: ask: list notes: %w", err)
	}
	row.NotesConsidered = len(notes)
	if len(notes) == 0 {
		// An answer, not a failure: the honest reply to a question over an
		// empty corpus is that there is nothing to search yet.
		return p.recordAskAnswer(ctx, &row, ask.NoNotesAnswer, false, nil, "no_notes")
	}

	packer := ask.NewPacker(row.Question)
	for _, r := range ask.Choose(ask.Rank(row.Question, notes)) {
		if packer.Full() {
			break
		}
		raw, err := p.cfg.Objects.Get(ctx, r.Note.S3MarkdownKey)
		if errors.Is(err, repository.ErrNotFound) {
			// Purged between the list and this read, or a note that never
			// had a body. Not a fault: the ranking simply had one candidate
			// fewer than it thought.
			continue
		}
		if err != nil {
			return fmt.Errorf("pipeline: ask: get note body: %w", err)
		}
		packer.Add(r.Note, service.StripCaptureMarkers(string(raw)))
	}
	packed := packer.Notes()
	prompt := ask.Prompt{
		Today:    p.now().UTC().Format("2006-01-02"),
		Notes:    packed,
		History:  row.History,
		Question: row.Question,
	}

	answer, err := p.askModel(ctx, tenantID, prompt)
	if err != nil {
		return p.recordAskFailure(ctx, &row, askProviderVerdict(ctx, log, err), "provider")
	}
	if strings.TrimSpace(answer.Text) == "" {
		// The adapter refuses an empty answer already; this is the belt for
		// an adapter that does not.
		return p.recordAskFailure(ctx, &row, askProviderVerdict(ctx, log, ask.ErrNoAnswer), "provider")
	}
	if n := len([]rune(answer.Text)); n > ask.MaxAnswerRunes {
		// Refused whole rather than cut: an answer with its ending missing
		// presented as the answer is worse than an honest "too long".
		log.Warn("ask: the answer is over the length cap",
			slog.Int("answer_runes", n),
			slog.Int("limit_runes", ask.MaxAnswerRunes))
		return p.recordAskFailure(ctx, &row, askTooLong, "too_long")
	}

	// A source is a note that was packed AND cited; an answer that cites no
	// such note has nothing the person can open, so it is not grounded
	// whatever the model claimed.
	sources := ask.Sources(answer.Sources, packed)
	grounded := answer.Grounded && len(sources) > 0

	packedBytes := 0
	for _, n := range packed {
		packedBytes += len(n.Text)
	}
	// Shape only, never the question or the answer.
	log.Info("ask: answered",
		slog.Int("notes_considered", len(notes)),
		slog.Int("packed", len(packed)),
		slog.Int("packed_bytes", packedBytes),
		slog.Int("input_tokens", answer.Usage.InputTokens),
		slog.Int("output_tokens", answer.Usage.OutputTokens),
		slog.Int64("latency_ms", p.now().Sub(started).Milliseconds()),
		slog.Bool("grounded", grounded),
		slog.Int("sources", len(sources)))
	return p.recordAskAnswer(ctx, &row, answer.Text, grounded, sources, "ok")
}

// askModel asks the model, with one retry on a stall or a 5xx.
//
// Each attempt is its own breaker.Do, for the reason decideTarget gives: an
// attempt that fails reports no usage and releases what it reserved, so a
// retry that succeeds is charged once, for what the provider said it used.
func (p *Pipeline) askModel(ctx context.Context, tenantID string, prompt ask.Prompt) (provider.Answer, error) {
	var lastErr error
	for attempt := 1; attempt <= askAttempts; attempt++ {
		answer, err := p.askOnce(ctx, tenantID, prompt)
		if err == nil {
			return answer, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			// The invocation itself is ending; a fresh context would only
			// borrow time the worker no longer has.
			return provider.Answer{}, err
		}
		reason, retryable := routeRetryReason(err)
		if !retryable || attempt == askAttempts {
			break
		}
		if reason == routeRetryTimeout {
			obs.Count(ctx, "AskTimedOut", map[string]string{"Attempt": strconv.Itoa(attempt)})
		}
		obs.Log(ctx).Warn("ask: attempt failed; retrying once with a fresh context",
			slog.Int("attempt", attempt),
			slog.String("reason", reason),
			slog.Int64("attempt_timeout_ms", p.cfg.AskAttemptTimeout.Milliseconds()),
			slog.String("error", err.Error()))
		obs.Count(ctx, "AskRetried", map[string]string{"Reason": reason})
	}
	return provider.Answer{}, lastErr
}

// askOnce is one reserved, bounded call. The attempt's deadline applies to the
// provider call only, so the breaker's release still has a live context when
// the attempt times out (see routeOnce).
func (p *Pipeline) askOnce(ctx context.Context, tenantID string, prompt ask.Prompt) (provider.Answer, error) {
	system, user, err := prompt.Render()
	if err != nil {
		return provider.Answer{}, err
	}
	var answer provider.Answer
	_, err = p.cfg.Breaker.Do(ctx, breaker.Estimate{
		Provider: p.cfg.LLMProvider,
		Model:    p.cfg.LLMModel,
		Op:       meter.OpAsk,
		Usage: meter.Quantities{
			meter.UnitInputTokens:  estimateTokens(system) + estimateTokens(user),
			meter.UnitOutputTokens: askOutputTokensEstimate,
		},
		TenantID: tenantID,
	}, func(ctx context.Context) (breaker.Result, error) {
		attemptCtx, cancel := context.WithTimeout(ctx, p.cfg.AskAttemptTimeout)
		defer cancel()
		out, err := p.cfg.LLM.Ask(attemptCtx, prompt)
		if err != nil {
			return breaker.Result{}, err
		}
		answer = out
		return breaker.Result{Usage: tokenUsage(out.Usage)}, nil
	})
	if err != nil {
		return provider.Answer{}, err
	}
	return answer, nil
}

// askProviderVerdict turns a failed model call into the sentence the row
// records, and emits the same signals the other stages do for the same
// failures, so a revoked key seen here raises the same alarm.
func askProviderVerdict(ctx context.Context, log *slog.Logger, cause error) string {
	switch {
	case errors.Is(cause, breaker.ErrSpendCapExceeded):
		log.Warn("ask: stopped by the daily spend cap")
		return askSpendCapped
	case errors.Is(cause, ask.ErrNoAnswer):
		log.Warn("ask: the model returned nothing usable")
		return askProviderFail
	case provider.IsAuthRejection(cause):
		log.Error("ask: provider rejected this instance's API key", slog.String("error", cause.Error()))
		obs.CountWithRollup(ctx, "ProviderKeyRejected", map[string]string{"Provider": "openai"})
		return ErrProviderKeyRejected.Error()
	case provider.IsRateLimited(cause):
		log.Warn("ask: provider rate-limited the call", slog.String("error", cause.Error()))
		obs.CountWithRollup(ctx, "ProviderRateLimited", map[string]string{"Provider": "openai"})
		return askProviderFail
	default:
		log.Error("ask: provider call failed", slog.String("error", cause.Error()))
		return askProviderFail
	}
}

// recordAskAnswer stores an answer. Sources is never left nil on the row so
// the wire's [] does not depend on the handler remembering to substitute it.
func (p *Pipeline) recordAskAnswer(ctx context.Context, row *model.Ask, text string, grounded bool, sources []model.AskSource, outcome string) error {
	if sources == nil {
		sources = []model.AskSource{}
	}
	row.Status = model.AskAnswered
	row.Answer = text
	row.Grounded = grounded
	row.Sources = sources
	row.Error = ""
	row.AnsweredAt = model.FormatTime(p.now())
	if err := p.cfg.Store.PutAsk(ctx, row.UserID, *row); err != nil {
		return fmt.Errorf("pipeline: ask: store the answer: %w", err)
	}
	obs.Count(ctx, "AskOutcome", map[string]string{"Outcome": outcome})
	return nil
}

// recordAskFailure stores the reason no answer was produced.
func (p *Pipeline) recordAskFailure(ctx context.Context, row *model.Ask, reason, outcome string) error {
	row.Status = model.AskFailed
	row.Answer = ""
	row.Grounded = false
	row.Sources = []model.AskSource{}
	row.Error = reason
	row.AnsweredAt = model.FormatTime(p.now())
	if err := p.cfg.Store.PutAsk(ctx, row.UserID, *row); err != nil {
		return fmt.Errorf("pipeline: ask: record the verdict: %w", err)
	}
	obs.Count(ctx, "AskOutcome", map[string]string{"Outcome": outcome})
	return nil
}
