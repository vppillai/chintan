package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vppillai/chintan/backend/internal/breaker"
	"github.com/vppillai/chintan/backend/internal/keys"
	"github.com/vppillai/chintan/backend/internal/meter"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/provider"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/routing"
	"github.com/vppillai/chintan/backend/internal/service"
)

// ---------------------------------------------------------------------------
// Stage 2 — route
// ---------------------------------------------------------------------------

// stripInstructions removes a spoken app instruction from a capture that was
// recorded into a note and so never reached routing.
//
// Routing deletes the words addressed to the app — "add this to my roof
// note", "create a note with the title …" — before the dictation is cleaned
// and appended, but a capture that already has its destination skips routing,
// and the same words were appended verbatim (QA 2026-09-05 §5a). This is the
// same span-extraction call, with the target note as the only candidate and
// the destination it answers ignored: one small routing-priced call for a
// targeted capture whose transcript contains an instruction cue, and no call
// for one that does not (routing.MentionsInstruction). The result is stored as
// the routed text, so a retry after a later fault finds it and does not call
// again.
//
// The call is a convenience, as routing is: its own failure keeps the words as
// spoken; a spend cap stops the capture as it would at the routing stage; and
// only a store or object-store fault fails the invocation.
func (p *Pipeline) stripInstructions(ctx context.Context, tenantID string, capture *model.CaptureIndex, note model.NoteIndex) error {
	rawBytes, err := p.cfg.Objects.Get(ctx, capture.RawKey)
	if err != nil {
		return fmt.Errorf("pipeline: get raw text: %w", err)
	}
	transcript := string(rawBytes)
	if p.cfg.Router == nil || !routing.MentionsInstruction(transcript) {
		obs.Count(ctx, "TargetedInstructionCheck", map[string]string{"Outcome": "no_cue"})
		return nil
	}

	candidates := []routing.Candidate{{NoteID: note.ID, Title: note.Title, Aliases: note.Aliases}}
	decision, err := p.routeWithRetries(ctx, tenantID, capture.ID, transcript, candidates)
	if err != nil {
		if errors.Is(err, breaker.ErrSpendCapExceeded) {
			return p.handleProviderError(ctx, capture, "route", err)
		}
		if ctx.Err() != nil {
			return err
		}
		obs.Log(ctx).Warn("instruction check failed; keeping the dictation as spoken",
			slog.String("capture_id", capture.ID),
			slog.String("error", err.Error()))
		obs.Count(ctx, "TargetedInstructionCheck", map[string]string{"Outcome": "failed"})
		return nil
	}

	routedKey, err := keys.CaptureRouted(tenantID, capture.ID)
	if err != nil {
		return fmt.Errorf("pipeline: routed key: %w", err)
	}
	if err := p.cfg.Objects.Put(ctx, routedKey, []byte(decision.Content), "text/plain"); err != nil {
		return fmt.Errorf("pipeline: store routed text: %w", err)
	}
	capture.RoutedKey = routedKey
	outcome := "nothing_removed"
	if decision.Content != transcript {
		outcome = "removed"
	}
	obs.Count(ctx, "TargetedInstructionCheck", map[string]string{"Outcome": outcome})
	return nil
}

func (p *Pipeline) route(ctx context.Context, tenantID string, capture *model.CaptureIndex) error {
	if err := p.setStatus(ctx, capture, service.StatusRouting); err != nil {
		return err
	}

	rawBytes, err := p.cfg.Objects.Get(ctx, capture.RawKey)
	if err != nil {
		return fmt.Errorf("pipeline: get raw text: %w", err)
	}
	transcript := string(rawBytes)

	decision, err := p.decideTarget(ctx, tenantID, capture.ID, transcript)
	if err != nil {
		if errors.Is(err, breaker.ErrSpendCapExceeded) {
			return p.handleProviderError(ctx, capture, "route", err)
		}
		if errors.Is(err, errRouteCandidates) {
			// The router was never asked. Filing the dictation into a new note
			// here would be the fault the GetNote branch below describes — a
			// DynamoDB throttle starting a second note on the subject the user
			// has been dictating into all week — one step earlier. The
			// invocation is worth retrying; a duplicate note is not.
			return err
		}
		// Routing is a convenience; a recording is never lost because of it.
		// From here the failure is the router's own — a stall past both
		// attempts, a 5xx, an answer that would not parse — and nothing the
		// store said is being second-guessed.
		obs.Log(ctx).Warn("routing failed; keeping the dictation in a new note",
			slog.String("capture_id", capture.ID),
			slog.String("error", err.Error()))
		decision = provider.RouteDecision{Action: provider.RouteNew, Content: transcript}
	}

	// Persist the transcript minus any spoken instruction, so cleanup and any
	// later retry work from the words the user meant to keep.
	routedKey, err := keys.CaptureRouted(tenantID, capture.ID)
	if err != nil {
		return fmt.Errorf("pipeline: routed key: %w", err)
	}
	if err := p.cfg.Objects.Put(ctx, routedKey, []byte(decision.Content), "text/plain"); err != nil {
		return fmt.Errorf("pipeline: store routed text: %w", err)
	}
	capture.RoutedKey = routedKey
	capture.RouteConfidence = decision.Confidence

	if decision.Action == provider.RouteAppend && decision.NoteID != "" {
		// Falling through to "make a new note" is only correct for an answer, not
		// for a failure to get one. A throttle or a 5xx on this read used to be
		// indistinguishable from ErrNotFound, so a transient DynamoDB fault
		// started a second note on the subject the user has been dictating into
		// all week — silently, unretried, and with the two halves of the thought
		// now in different notes. The invocation is worth retrying; a duplicate
		// note is not worth creating.
		note, err := p.cfg.Store.GetNote(ctx, tenantID, decision.NoteID)
		switch {
		case err == nil && service.NoteIsActive(note):
			if decision.Confidence >= routeConfidenceThreshold {
				capture.NoteID = decision.NoteID
				capture.TargetSource = model.TargetSourceRouter
				capture.Status = model.StatusTranscribed
			} else {
				// Plausible but unsure: ask before writing into an existing note.
				capture.SuggestedNoteID = decision.NoteID
				capture.Status = model.StatusNeedsTarget
			}
			return p.persist(ctx, capture)
		case err == nil:
			obs.Log(ctx).Info("routed note is archived; keeping the dictation in a new note",
				slog.String("capture_id", capture.ID),
				slog.String("note_id", decision.NoteID))
		case errors.Is(err, repository.ErrNotFound):
			obs.Log(ctx).Info("routed note no longer exists; keeping the dictation in a new note",
				slog.String("capture_id", capture.ID),
				slog.String("note_id", decision.NoteID))
		default:
			return fmt.Errorf("pipeline: get routed note %s: %w", decision.NoteID, err)
		}
	}

	title := service.SanitizeTitle(decision.Title)
	if title == "" {
		title = service.SanitizeTitle(fallbackNoteTitle(decision.Content, p.now()))
	}
	if p.cfg.Notes == nil {
		capture.SuggestedTitle = title
		capture.Status = model.StatusNeedsTarget
		return p.persist(ctx, capture)
	}

	note, err := p.cfg.Notes.CreateNote(ctx, tenantID, title, nil)
	if err != nil {
		return fmt.Errorf("pipeline: create note for capture: %w", err)
	}
	if capture.Language != "" && capture.Language != model.LanguageAuto {
		// The note starts in the language its first recording was
		// transcribed in, so a later change of the tenant's default does not
		// silently change what "Record into this" sends for it. Auto is not
		// written: the note then follows the default, as a note a person
		// creates does.
		note.Language = capture.Language
		if _, err := p.cfg.Store.PutNote(ctx, tenantID, note); err != nil {
			return fmt.Errorf("pipeline: set language on the new note: %w", err)
		}
	}
	capture.NoteID = note.ID
	capture.TargetSource = model.TargetSourceRouter
	capture.Status = model.StatusTranscribed
	return p.persist(ctx, capture)
}

// errRouteCandidates marks a routing failure that happened before the router
// was asked: the store would not list the notes it chooses among. It is the
// one routing error route() does not turn into a new note.
var errRouteCandidates = errors.New("pipeline: list routing candidates")

func (p *Pipeline) decideTarget(ctx context.Context, tenantID, captureID, transcript string) (provider.RouteDecision, error) {
	if p.cfg.Router == nil {
		return provider.RouteDecision{}, fmt.Errorf("pipeline: routing is not configured")
	}

	// The store orders the list most recently touched first over every note
	// the tenant has, so the leading maxRouteCandidates are the window the
	// router should see — the likeliest destinations. Until 2026-09 this
	// drained a 500-note pool and cut it here, which was only right while the
	// store's own order was by creation.
	active, _, err := p.cfg.Store.DrainNotes(ctx, tenantID, repository.DrainOptions{MaxItems: maxRouteCandidates})
	if err != nil {
		return provider.RouteDecision{}, fmt.Errorf("%w: %w", errRouteCandidates, err)
	}

	// Kept as a guard on the order the store promised, and because it is the
	// place this lesson lives: compare parsed instants, never RFC3339Nano
	// strings. Go trims trailing fractional zeros, so "…:00Z" sorts above
	// "…:00.1Z" because 'Z' > '.' and the router would be handed the wrong
	// fifty notes.
	sort.SliceStable(active, func(i, j int) bool {
		return noteTouchedAt(active[i]).After(noteTouchedAt(active[j]))
	})
	if len(active) > maxRouteCandidates {
		active = active[:maxRouteCandidates]
	}

	candidates := make([]routing.Candidate, 0, len(active))
	for _, n := range active {
		candidates = append(candidates, routing.Candidate{
			NoteID:  n.ID,
			Title:   n.Title,
			Aliases: n.Aliases,
		})
	}

	decision, err := p.routeWithRetries(ctx, tenantID, captureID, transcript, candidates)
	if err != nil {
		return decision, err
	}
	return preferExistingTitle(ctx, decision, active), nil
}

// preferExistingTitle applies the rule the prompt already states — an existing
// note with the same title is the destination — after the model has answered.
// A "new" decision whose title names an active candidate, by title or alias
// and compared case- and whitespace-insensitively, appends to that note. The
// model started a second "staging smoke" beside the one that existed (live QA
// 2026-09-05 §5b), and a rule this mechanical is the code's to enforce, not the
// model's to remember. The candidates are the notes the router saw, so the
// rule reaches exactly as far as routing does; the id, never the title, is
// what gets logged.
func preferExistingTitle(ctx context.Context, decision provider.RouteDecision, active []model.NoteIndex) provider.RouteDecision {
	if decision.Action != provider.RouteNew {
		return decision
	}
	want := normalizeTitle(decision.Title)
	if want == "" {
		return decision
	}
	for _, n := range active {
		if !titleNames(n, want) {
			continue
		}
		obs.Log(ctx).Info("router chose a new note whose title names an existing note; appending to it instead",
			slog.String("note_id", n.ID))
		obs.Count(ctx, "RouterTitleMatchedExistingNote", map[string]string{})
		decision.Action = provider.RouteAppend
		decision.NoteID = n.ID
		decision.Title = ""
		decision.Confidence = 1
		return decision
	}
	return decision
}

// titleNames reports whether want, already normalised, is n's title or one of
// its aliases.
func titleNames(n model.NoteIndex, want string) bool {
	if normalizeTitle(n.Title) == want {
		return true
	}
	for _, alias := range n.Aliases {
		if normalizeTitle(alias) == want {
			return true
		}
	}
	return false
}

// normalizeTitle is the comparison form of a title: lowercased, one space
// between words, none around them.
func normalizeTitle(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// routeWithRetries asks the router, with one retry on a stall or a 5xx. It
// is the model half of decideTarget, shared with stripInstructions, which
// wants the instruction spans and not the destination.
func (p *Pipeline) routeWithRetries(ctx context.Context, tenantID, captureID, transcript string, candidates []routing.Candidate) (provider.RouteDecision, error) {
	// Each attempt is its own breaker.Do, so each reserves before it calls and
	// settles for itself. An attempt that fails — a timeout included — reports
	// no usage, and the breaker releases exactly what that attempt reserved,
	// so two stalls cost the day's budget nothing and a retry that succeeds is
	// charged once, for what the provider said it consumed. Wrapping both
	// attempts in one reservation would have charged the estimate for a call
	// that never answered, or left the breaker's latency metric summing two
	// calls into one.
	var lastErr error
	for attempt := 1; attempt <= routeAttempts; attempt++ {
		decision, err := p.routeOnce(ctx, tenantID, transcript, candidates)
		if err == nil {
			return decision, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			// The invocation itself is ending; this is not a provider stall to
			// retry, and a fresh context would only borrow time the worker no
			// longer has.
			return provider.RouteDecision{}, err
		}
		reason, retryable := routeRetryReason(err)
		if !retryable {
			return provider.RouteDecision{}, err
		}
		if reason == routeRetryTimeout {
			obs.Count(ctx, "RouterTimedOut", map[string]string{"Attempt": strconv.Itoa(attempt)})
		}
		if attempt == routeAttempts {
			break
		}
		// The correlation id rides on the context (obs.Log), so this line joins
		// the API request that started the capture to the retry it caused.
		obs.Log(ctx).Warn("routing attempt failed; retrying once with a fresh context",
			slog.String("capture_id", captureID),
			slog.Int("attempt", attempt),
			slog.String("reason", reason),
			slog.Int64("attempt_timeout_ms", p.cfg.RouteAttemptTimeout.Milliseconds()),
			slog.String("error", err.Error()))
		obs.Count(ctx, "RouterRetried", map[string]string{"Reason": reason})
	}
	return provider.RouteDecision{}, lastErr
}

// routeOnce is one reserved, bounded routing call.
//
// The attempt's deadline applies to the provider call only. breaker.Do runs on
// the caller's context, so when the attempt times out the release of its
// reservation still has a live context to run on; a release attempted on the
// expired context would fail, and the estimate would stay in the day's total
// as spend that never happened.
func (p *Pipeline) routeOnce(ctx context.Context, tenantID, transcript string, candidates []routing.Candidate) (provider.RouteDecision, error) {
	var decision provider.RouteDecision
	_, err := p.cfg.Breaker.Do(ctx, breaker.Estimate{
		Provider: p.cfg.LLMProvider,
		Model:    p.cfg.LLMModel,
		Op:       meter.OpRoute,
		Usage: meter.Quantities{
			meter.UnitInputTokens:  estimateTokens(transcript) + estimateCandidateTokens(candidates),
			meter.UnitOutputTokens: routeOutputTokensEstimate,
		},
		TenantID: tenantID,
	}, func(ctx context.Context) (breaker.Result, error) {
		attemptCtx, cancel := context.WithTimeout(ctx, p.cfg.RouteAttemptTimeout)
		defer cancel()
		out, err := p.cfg.Router.Route(attemptCtx, transcript, candidates)
		if err != nil {
			return breaker.Result{}, err
		}
		decision = out
		return breaker.Result{Usage: tokenUsage(out.Usage)}, nil
	})
	if err != nil {
		return provider.RouteDecision{}, err
	}
	return decision, nil
}

// Values of the Reason dimension on RouterRetried. Two values, fixed: a
// dimension is a metric identity and is billed as one.
const (
	routeRetryTimeout     = "timeout"
	routeRetryServerError = "provider_5xx"
)

// routeRetryReason classifies a failed routing attempt as worth one more try.
//
// Only two things are: the attempt hitting its own deadline (the provider-side
// queueing the timeout exists for) and a 5xx or 529 from the provider (an
// overloaded moment). A 4xx is our request and will fail identically; a spend
// cap rejection must not be retried around; an unparseable answer or an
// unlisted note id is the model's verdict, and the fallback is the right
// answer to it. The caller has already ruled out its own context ending, so a
// deadline seen here is the attempt's.
func routeRetryReason(err error) (string, bool) {
	switch {
	case errors.Is(err, breaker.ErrSpendCapExceeded):
		return "", false
	case errors.Is(err, context.DeadlineExceeded):
		return routeRetryTimeout, true
	case provider.IsServerError(err):
		return routeRetryServerError, true
	default:
		return "", false
	}
}

// routeOutputTokensEstimate is what a routing decision is reserved against
// before the model answers. The answer is a short JSON object — an id, a
// confidence, a title — so the number is small and fixed; the reconcile step
// replaces it with what the provider reports.
const routeOutputTokensEstimate = 64

func estimateCandidateTokens(candidates []routing.Candidate) float64 {
	total := 0
	for _, c := range candidates {
		total += len(c.Title)
		for _, a := range c.Aliases {
			total += len(a)
		}
	}
	return float64(total)/4 + 1
}

// fallbackNoteTitle names a note the router could not title: the first words
// of what was said, so the row reads as the thought it holds. Until
// 2026-09-21 it was "Voice note <UTC date and time>", which sat beside the
// row's own local time and disagreed with it by the timezone offset (review
// T40). Six words or forty characters, whichever comes first, trailing
// punctuation dropped; only an empty transcript falls back to "Voice note
// <date>", with no clock, because the row's own time already carries one.
func fallbackNoteTitle(content string, now time.Time) string {
	const maxWords, maxRunes = 6, 40
	title := ""
	for i, word := range strings.Fields(content) {
		if i == maxWords {
			break
		}
		next := word
		if title != "" {
			next = title + " " + word
		}
		if i > 0 && utf8.RuneCountInString(next) > maxRunes {
			break
		}
		title = next
	}
	if runes := []rune(title); len(runes) > maxRunes {
		title = string(runes[:maxRunes])
	}
	if title = strings.TrimRight(strings.TrimSpace(title), ".,;:!?"); title == "" {
		return "Voice note " + now.UTC().Format("2006-01-02")
	}
	return title
}

// noteTouchedAt parses a note's update time, tolerating the RFC3339 and
// RFC3339Nano values written before the fixed-width layout existed.
func noteTouchedAt(n model.NoteIndex) time.Time {
	t, err := model.ParseTime(n.UpdatedAt)
	if err != nil {
		return time.Time{}
	}
	return t
}
