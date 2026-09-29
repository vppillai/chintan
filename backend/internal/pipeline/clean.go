package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/vppillai/chintan/backend/internal/breaker"
	"github.com/vppillai/chintan/backend/internal/cleanup"
	"github.com/vppillai/chintan/backend/internal/keys"
	"github.com/vppillai/chintan/backend/internal/meter"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/provider"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/service"
)

// ---------------------------------------------------------------------------
// Stage 3 — clean
// ---------------------------------------------------------------------------

// clean rewrites the transcript faithfully, or — for a verbatim note —
// records the transcript itself as the cleaned text. README, the
// OpenAPI document and the About screen promised that a verbatim note
// bypasses cleanup, and until 2026-09-21 nothing in the pipeline read the
// switch: the verifier seeded a verbatim note and counted one paid cleanup
// call (review T11). Pointing CleanKey at the source key, rather than copying
// the text, keeps every reader of CleanKey working and costs no write.
func (p *Pipeline) clean(ctx context.Context, tenantID string, capture *model.CaptureIndex, verbatim bool) error {
	if err := p.setStatus(ctx, capture, service.StatusCleaning); err != nil {
		return err
	}

	sourceKey := capture.RoutedKey
	if sourceKey == "" {
		sourceKey = capture.RawKey
	}
	sourceBytes, err := p.cfg.Objects.Get(ctx, sourceKey)
	if err != nil {
		return fmt.Errorf("pipeline: get source text: %w", err)
	}
	source := string(sourceBytes)

	if strings.TrimSpace(source) == "" {
		// The speaker only told the app what to do, so the note they asked for
		// exists and there is nothing to clean or append.
		capture.Status = model.StatusNoContent
		capture.Error = ""
		return p.persist(ctx, capture)
	}
	if verbatim {
		obs.Count(ctx, "CaptureCleanupBypassed", map[string]string{"Stage": string(service.StatusCleaning)})
		capture.CleanKey = sourceKey
		capture.Status = model.StatusCleaned
		capture.Error = ""
		return p.persist(ctx, capture)
	}

	var cleaned provider.Cleaned
	_, err = p.cfg.Breaker.Do(ctx, breaker.Estimate{
		Provider: p.cfg.LLMProvider,
		Model:    p.cfg.LLMModel,
		Op:       meter.OpCleanup,
		Usage: meter.Quantities{
			meter.UnitInputTokens: estimateTokens(source),
			// Cleanup rewrites the transcript, so it writes about as much as
			// it reads. Reserving for the output too is what keeps the
			// reservation near the bill: output tokens cost four times input.
			meter.UnitOutputTokens: estimateTokens(source),
		},
		TenantID: tenantID,
	}, func(ctx context.Context) (breaker.Result, error) {
		stageCtx, cancel := context.WithTimeout(ctx, p.cfg.CleanupTimeout)
		defer cancel()
		out, err := p.cfg.LLM.Cleanup(stageCtx, source, cleanupLanguage(*capture))
		if err != nil {
			return breaker.Result{}, err
		}
		cleaned = out
		return breaker.Result{Usage: tokenUsage(out.Usage)}, nil
	})
	if err != nil {
		return p.handleProviderError(ctx, capture, "cleanup", err)
	}

	cleanKey, err := keys.CaptureClean(tenantID, capture.ID)
	if err != nil {
		return fmt.Errorf("pipeline: clean key: %w", err)
	}
	if err := p.cfg.Objects.Put(ctx, cleanKey, []byte(cleaned.Text), "text/plain"); err != nil {
		return fmt.Errorf("pipeline: store clean text: %w", err)
	}

	capture.CleanKey = cleanKey
	capture.Status = model.StatusCleaned
	capture.Error = ""
	return p.persist(ctx, capture)
}

// extractItems is clean for a checklist: one model call over the RAW
// transcript that answers with the items to add (cleanup.ItemsPrompt), stored
// one per line at CleanKey — a sub-item's line indented two spaces
// (cleanup.RenderItems) — for the append to render. It runs in the cleaning
// status and under the cleanup op and deadline, because it is the cleanup
// call for this kind of note — one call replaces one call, and a retry that
// finds CleanKey set does not make it again.
//
// A recording that names nothing to add — "create a shopping list" — is
// StatusNoContent, exactly as an instruction-only recording is for a plain
// note; the note the speaker asked for exists and gets no item. A reply that
// is not a list of items falls back to the recording as one item, the
// pre-2026-09-26 behaviour: the dictation is never lost to a bad reply, and
// the fallback is counted so a prompt that has stopped working is visible.
// A provider failure is handled as cleanup's is.
//
// A verbatim checklist makes no call: CleanKey points at the raw transcript,
// as clean does for a verbatim note, and the append collapses it to one
// item. Raw rather than routed, because "as spoken" is what verbatim
// promises and the routed text is the transcript with the router's spans
// cut out of it.
//
// previous is what the recording's clean artefact held before this call
// overwrote it — the items it added the last time it was appended — or nil
// on a first run. It is read here because this is the last moment it exists
// at CleanKey: a recording transcribed or regenerated again is re-appended
// over its old items, and a list that has been ticked or reordered holds
// those items under nobody's marker, so the append finds them by their words
// (replaceChecklistItems). A copy is kept at keys.CaptureCleanPrevious for
// the attempt that resumes at the append after this one fails (previousItems).
func (p *Pipeline) extractItems(ctx context.Context, tenantID string, capture *model.CaptureIndex, note model.NoteIndex) (previous []string, err error) {
	if err := p.setStatus(ctx, capture, service.StatusCleaning); err != nil {
		return nil, err
	}
	rawBytes, err := p.cfg.Objects.Get(ctx, capture.RawKey)
	if err != nil {
		return nil, fmt.Errorf("pipeline: get raw text: %w", err)
	}
	transcript := string(rawBytes)
	cleanKey, err := keys.CaptureClean(tenantID, capture.ID)
	if err != nil {
		return nil, fmt.Errorf("pipeline: clean key: %w", err)
	}
	if before, err := p.cfg.Objects.Get(ctx, cleanKey); err == nil && len(before) > 0 {
		previous = strings.Split(string(before), "\n")
		// Kept beside the artefact this call is about to overwrite, because
		// the append that replaces these items can fail after it — an object
		// store fault, a stamp wait that ran out — and the attempt that
		// resumes at the append (run, regenerateCapture) has no other record
		// of which lines are the recording's; without it the new items went
		// in beside the old ones (review of #138).
		prevKey, err := keys.CaptureCleanPrevious(tenantID, capture.ID)
		if err != nil {
			return nil, fmt.Errorf("pipeline: previous items key: %w", err)
		}
		if err := p.cfg.Objects.Put(ctx, prevKey, before, "text/plain"); err != nil {
			return nil, fmt.Errorf("pipeline: keep previous items: %w", err)
		}
	} else if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return nil, fmt.Errorf("pipeline: get previous items: %w", err)
	}
	if strings.TrimSpace(transcript) == "" {
		capture.Status = model.StatusNoContent
		capture.Error = ""
		return previous, p.persist(ctx, capture)
	}
	if note.Verbatim {
		obs.Count(ctx, "CaptureCleanupBypassed", map[string]string{"Stage": string(service.StatusCleaning)})
		capture.CleanKey = capture.RawKey
		capture.Status = model.StatusCleaned
		capture.Error = ""
		return previous, p.persist(ctx, capture)
	}

	var result provider.ChecklistItems
	var unusable error
	_, err = p.cfg.Breaker.Do(ctx, breaker.Estimate{
		Provider: p.cfg.LLMProvider,
		Model:    p.cfg.LLMModel,
		Op:       meter.OpCleanup,
		Usage: meter.Quantities{
			meter.UnitInputTokens: estimateTokens(transcript),
			// The items are words of the transcript; the provider's count
			// reconciles what the JSON around them cost.
			meter.UnitOutputTokens: estimateTokens(transcript),
		},
		TenantID: tenantID,
	}, func(ctx context.Context) (breaker.Result, error) {
		stageCtx, cancel := context.WithTimeout(ctx, p.cfg.CleanupTimeout)
		defer cancel()
		out, err := p.cfg.LLM.Items(stageCtx, transcript, note.Title, cleanupLanguage(*capture))
		if errors.Is(err, cleanup.ErrNotAnItemList) {
			// The provider answered and billed for it; a reply that would
			// not parse is settled like any other and judged outside the
			// reservation, where an error would release it.
			result, unusable = out, err
			return breaker.Result{Usage: tokenUsage(out.Usage)}, nil
		}
		if err != nil {
			return breaker.Result{}, err
		}
		result = out
		return breaker.Result{Usage: tokenUsage(out.Usage)}, nil
	})
	if err != nil {
		return previous, p.handleProviderError(ctx, capture, "cleanup", err)
	}
	items := result.Items
	switch {
	case unusable != nil:
		obs.Log(ctx).Warn("checklist item extraction returned no list; appending the recording as one item",
			slog.String("capture_id", capture.ID),
			slog.String("error", unusable.Error()))
		obs.Count(ctx, "ChecklistItemsDiscarded", map[string]string{"Reason": "unusable"})
		items = []cleanup.Item{{Text: strings.Join(strings.Fields(transcript), " ")}}
	case len(items) == 0:
		// The speaker only told the app what to do.
		obs.Count(ctx, "ChecklistItemsExtracted", map[string]string{"Outcome": "none"})
		capture.Status = model.StatusNoContent
		capture.Error = ""
		return previous, p.persist(ctx, capture)
	default:
		obs.Count(ctx, "ChecklistItemsExtracted", map[string]string{"Outcome": "items"})
	}

	if err := p.cfg.Objects.Put(ctx, cleanKey, []byte(cleanup.RenderItems(items)), "text/plain"); err != nil {
		return previous, fmt.Errorf("pipeline: store clean text: %w", err)
	}
	capture.CleanKey = cleanKey
	capture.Status = model.StatusCleaned
	capture.Error = ""
	return previous, p.persist(ctx, capture)
}

// previousItems reads the copy extractItems kept of a checklist recording's
// earlier items, for an append resumed after the extraction has already
// overwritten the artefact at CleanKey. Nil when there is none: the
// recording's first append. The lines keep their indent; every reader folds
// it away with the whitespace (foldWords).
func (p *Pipeline) previousItems(ctx context.Context, tenantID, captureID string) ([]string, error) {
	key, err := keys.CaptureCleanPrevious(tenantID, captureID)
	if err != nil {
		return nil, fmt.Errorf("pipeline: previous items key: %w", err)
	}
	before, err := p.cfg.Objects.Get(ctx, key)
	if errors.Is(err, repository.ErrNotFound) || (err == nil && len(before) == 0) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("pipeline: get previous items: %w", err)
	}
	return strings.Split(string(before), "\n"), nil
}

// cleanupLanguage is the ISO-639-1 code the cleanup prompt names the
// transcript as being in: the code the transcription was asked for when it
// was one, else the code for the language Whisper detected under auto, else
// "" — nothing known, nothing claimed.
func cleanupLanguage(c model.CaptureIndex) string {
	if c.Language != "" && c.Language != model.LanguageAuto {
		return c.Language
	}
	return model.LanguageCode(c.LanguageDetected)
}

// tokenUsage converts a provider's token report into what the breaker prices.
// An empty report (a provider that returned no usage block) stays empty, so
// the breaker keeps the estimate rather than reconciling to zero.
func tokenUsage(u provider.TokenUsage) meter.Quantities {
	if u.InputTokens == 0 && u.OutputTokens == 0 {
		return nil
	}
	return meter.Quantities{
		meter.UnitInputTokens:  float64(u.InputTokens),
		meter.UnitOutputTokens: float64(u.OutputTokens),
	}
}

// estimateTokens is a pre-call guess at prompt size. Four characters per token
// is the usual English rule of thumb; the breaker reconciles against the
// provider's own count once the call returns, so the guess only has to be close
// enough to reserve against.
func estimateTokens(s string) float64 {
	if s == "" {
		return 0
	}
	return float64(len(s))/4 + 1
}
