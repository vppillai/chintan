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
	"github.com/vppillai/chintan/backend/internal/service"
)

// ---------------------------------------------------------------------------
// Stage 3 — clean
// ---------------------------------------------------------------------------

// clean rewrites the transcript in the capture's mode, or — for a verbatim
// note — records the transcript itself as the cleaned text. README, the
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
		out, err := p.cfg.LLM.Cleanup(stageCtx, capture.Mode, source, cleanupLanguage(*capture))
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
// one per line at CleanKey for the append to render. It runs in the cleaning
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
func (p *Pipeline) extractItems(ctx context.Context, tenantID string, capture *model.CaptureIndex, note model.NoteIndex) error {
	if err := p.setStatus(ctx, capture, service.StatusCleaning); err != nil {
		return err
	}
	rawBytes, err := p.cfg.Objects.Get(ctx, capture.RawKey)
	if err != nil {
		return fmt.Errorf("pipeline: get raw text: %w", err)
	}
	transcript := string(rawBytes)
	if strings.TrimSpace(transcript) == "" {
		capture.Status = model.StatusNoContent
		capture.Error = ""
		return p.persist(ctx, capture)
	}
	if note.Verbatim {
		obs.Count(ctx, "CaptureCleanupBypassed", map[string]string{"Stage": string(service.StatusCleaning)})
		capture.CleanKey = capture.RawKey
		capture.Status = model.StatusCleaned
		capture.Error = ""
		return p.persist(ctx, capture)
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
		return p.handleProviderError(ctx, capture, "cleanup", err)
	}
	items := result.Items
	switch {
	case unusable != nil:
		obs.Log(ctx).Warn("checklist item extraction returned no list; appending the recording as one item",
			slog.String("capture_id", capture.ID),
			slog.String("error", unusable.Error()))
		obs.Count(ctx, "ChecklistItemsDiscarded", map[string]string{"Reason": "unusable"})
		items = []string{strings.Join(strings.Fields(transcript), " ")}
	case len(items) == 0:
		// The speaker only told the app what to do.
		obs.Count(ctx, "ChecklistItemsExtracted", map[string]string{"Outcome": "none"})
		capture.Status = model.StatusNoContent
		capture.Error = ""
		return p.persist(ctx, capture)
	default:
		obs.Count(ctx, "ChecklistItemsExtracted", map[string]string{"Outcome": "items"})
	}

	cleanKey, err := keys.CaptureClean(tenantID, capture.ID)
	if err != nil {
		return fmt.Errorf("pipeline: clean key: %w", err)
	}
	if err := p.cfg.Objects.Put(ctx, cleanKey, []byte(strings.Join(items, "\n")), "text/plain"); err != nil {
		return fmt.Errorf("pipeline: store clean text: %w", err)
	}
	capture.CleanKey = cleanKey
	capture.Status = model.StatusCleaned
	capture.Error = ""
	return p.persist(ctx, capture)
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
