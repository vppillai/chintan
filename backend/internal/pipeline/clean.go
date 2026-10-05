package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"unicode"

	"github.com/vppillai/chintan/backend/internal/breaker"
	"github.com/vppillai/chintan/backend/internal/cleanup"
	"github.com/vppillai/chintan/backend/internal/keys"
	"github.com/vppillai/chintan/backend/internal/llm"
	"github.com/vppillai/chintan/backend/internal/meter"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/provider"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/routing"
	"github.com/vppillai/chintan/backend/internal/service"
)

// ---------------------------------------------------------------------------
// Stage 3 — clean
// ---------------------------------------------------------------------------

// clean is the third stage: the cleanup for a plain note, the item
// extraction for a checklist, or nothing when CleanKey says an earlier
// attempt did it (cleanForNote, which keeps the rule because
// regenerateCapture shares it). What it hands the append is the items a
// checklist recording left the last time it was appended.
func (r *captureRun) clean(ctx context.Context) error {
	previous, err := r.p.cleanForNote(ctx, r.tenantID, r.capture, r.note)
	r.previousItems = previous
	return err
}

// clean rewrites the transcript faithfully, or — for a verbatim note —
// records the transcript itself as the cleaned text. README, the
// OpenAPI document and the About screen promised that a verbatim note
// bypasses cleanup, and until 2026-09-21 nothing in the pipeline read the
// switch: the verifier seeded a verbatim note and counted one paid cleanup
// call (review T11). Pointing CleanKey at the source key, rather than copying
// the text, keeps every reader of CleanKey working and costs no write.
//
// A dictation under routing.ShortDictationWords words is tidied instead of sent to
// the model (tidyDictation).
//
// Cleaned is not written on its own: both callers of cleanForNote go straight
// to the append, whose setStatus writes CleanKey with it (deferPersist).
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
	// The routed text, with any spoken "add this to my roof note" taken out,
	// is what every path below appends when it does not clean: the verbatim
	// bypass here, and any later one. The model's answer overwrites it.
	// Transcribe's excerpt was cut from the raw transcript, command and all.
	capture.Excerpt = model.CaptureExcerpt(source)

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
		return p.deferPersist(capture)
	}
	if isShortDictation(source) {
		// No model call: the dictation is too short for the rewrite to be
		// worth its wait (routing.ShortDictationWords). The tidy is stored as the
		// clean text, so the append and every reader of CleanKey see it as
		// they see the model's; the usage meter simply has no cleanup call
		// to record. The line below is the tidy's only trace since its
		// counter went (D6, 2026-10-01); it carries a word count, never the words.
		obs.Log(ctx).Info("short dictation tidied without a cleanup call",
			slog.String("capture_id", capture.ID),
			slog.Int("words", len(llm.Words(source))))
		cleanKey, err := keys.CaptureClean(tenantID, capture.ID)
		if err != nil {
			return fmt.Errorf("pipeline: clean key: %w", err)
		}
		tidied := tidyDictation(source)
		if err := p.cfg.Objects.Put(ctx, cleanKey, []byte(tidied), "text/plain"); err != nil {
			return fmt.Errorf("pipeline: store clean text: %w", err)
		}
		capture.CleanKey = cleanKey
		// The tidy is this recording's clean text, so the excerpt is cut from
		// it as it is from the model's: a receipt reads "The bins go out on
		// Wednesday night.", the line that was filed, not the transcript.
		capture.Excerpt = model.CaptureExcerpt(tidied)
		capture.Status = model.StatusCleaned
		capture.Error = ""
		return p.deferPersist(capture)
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
	// The words bound (routing.MinCleanedWordShare): a reply that shares
	// under half its words with the transcript is a translation, an answer
	// or a replacement, not a cleanup — the cleanup prompt is one of the
	// places a dictated "translate this" reaches a model — and the transcript
	// stands as the cleaned paragraph. The share only, never the words.
	if share := cleanup.WordShare(cleaned.Text, source); share < routing.MinCleanedWordShare {
		obs.Log(ctx).Warn("cleanup reply shares too few words with the transcript; keeping the transcript",
			slog.String("capture_id", capture.ID),
			slog.Float64("share", share))
		obs.Count(ctx, "CleanupRefused", map[string]string{"Reason": "words"})
		cleaned.Text = source
	}

	cleanKey, err := keys.CaptureClean(tenantID, capture.ID)
	if err != nil {
		return fmt.Errorf("pipeline: clean key: %w", err)
	}
	if err := p.cfg.Objects.Put(ctx, cleanKey, []byte(cleaned.Text), "text/plain"); err != nil {
		return fmt.Errorf("pipeline: store clean text: %w", err)
	}

	capture.CleanKey = cleanKey
	capture.Excerpt = model.CaptureExcerpt(cleaned.Text)
	capture.Status = model.StatusCleaned
	capture.Error = ""
	return p.deferPersist(capture)
}

// isShortDictation reports whether text is under routing.ShortDictationWords words.
// A script written without spaces between words (Han, kana, Thai, Lao,
// Khmer, Myanmar) is never short by this count, since a whole sentence of it
// is one field; it goes to the model as before.
func isShortDictation(text string) bool {
	for _, r := range text {
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Thai, unicode.Lao, unicode.Khmer, unicode.Myanmar) {
			return false
		}
	}
	return len(strings.Fields(text)) < routing.ShortDictationWords
}

// tidyDictation is the deterministic cleanup of a short dictation, and
// nothing more: whitespace collapsed to single spaces, the first word
// capitalised when it is an ordinary lowercase word, and a full stop added
// when the text does not already end a sentence.
//
// Capitalising is deliberately narrow, because a wrong capital is a
// corruption the model would never have made: "3 eggs", "7pm", a URL,
// "iPhone" and "eBay" are left as they are (capitalFirstWord). A closing
// quote or bracket after the sentence's own mark counts as ending it, and a
// trailing URL gets no full stop, which would read as part of the address.
func tidyDictation(text string) string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return ""
	}
	words[0] = capitalFirstWord(words[0])
	text = strings.Join(words, " ")
	if lastWord := words[len(words)-1]; strings.Contains(lastWord, "://") || strings.HasPrefix(strings.ToLower(lastWord), "www.") {
		return text
	}
	runes := []rune(text)
	last := len(runes) - 1
	for last >= 0 && (unicode.In(runes[last], unicode.Pe, unicode.Pf) || runes[last] == '"' || runes[last] == '\'') {
		last--
	}
	if last >= 0 && (unicode.Is(unicode.Sentence_Terminal, runes[last]) || runes[last] == '…') {
		return text
	}
	return text + "."
}

// capitalFirstWord capitalises word when, after any opening quote or
// bracket, it is lowercase letters only — an apostrophe or hyphen inside,
// and trailing , ; ! ? aside. A word with a digit, an uppercase letter, or
// one of / : @ . is a number, a name, an address or an abbreviation, and is
// returned as it is. Georgian is treated as a script without case: its
// letters are lowercase to Unicode, but a capitalised Georgian word is not
// how Georgian is written.
func capitalFirstWord(word string) string {
	runes := []rune(word)
	start := 0
	for start < len(runes) && (unicode.In(runes[start], unicode.Ps, unicode.Pi) || runes[start] == '"' || runes[start] == '\'') {
		start++
	}
	body := strings.TrimRight(string(runes[start:]), ",;!?")
	if body == "" {
		return word
	}
	for i, r := range []rune(body) {
		switch {
		case unicode.IsLower(r) && !unicode.Is(unicode.Georgian, r):
		case i > 0 && (r == '\'' || r == '’' || r == '-'):
		default:
			return word
		}
	}
	runes[start] = unicode.ToUpper(runes[start])
	return string(runes)
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
	previous, err = p.keepPreviousItems(ctx, tenantID, capture.ID, cleanKey)
	if err != nil {
		return nil, err
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
	// An item with no spoken word is the model's, not the person's — leaked
	// prompt text, an answer to a dictated instruction — and is dropped
	// (cleanup.DropUnspoken); a reply that was nothing but such items is
	// settled as an unusable one, the recording as one item, so the
	// dictation is kept whatever the model wrote.
	items, invented := cleanup.DropUnspoken(result.Items, transcript)
	if invented > 0 {
		obs.Log(ctx).Warn("checklist item extraction returned items with no spoken word; dropped",
			slog.String("capture_id", capture.ID),
			slog.Int("dropped", invented))
		for range invented {
			obs.CountWithRollup(ctx, "ChecklistItemsDiscarded", map[string]string{"Reason": "invented"})
		}
		if len(items) == 0 && unusable == nil {
			unusable = errItemsAllInvented
		}
	}
	switch {
	case unusable != nil:
		obs.Log(ctx).Warn("checklist item extraction returned no list; appending the recording as one item",
			slog.String("capture_id", capture.ID),
			slog.String("error", unusable.Error()))
		obs.CountWithRollup(ctx, "ChecklistItemsDiscarded", map[string]string{"Reason": "unusable"})
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
	capture.Excerpt = model.CaptureExcerpt(itemsExcerpt(items))
	capture.Status = model.StatusCleaned
	capture.Error = ""
	return previous, p.persist(ctx, capture)
}

// keepPreviousItems reads the items at cleanKey from the recording's last
// append, nil when there are none, and keeps a copy beside the artefact
// this call is about to overwrite, because the append that replaces these
// items can fail after it — an object store fault, a stamp wait that ran
// out — and the attempt that resumes at the append (run, regenerateCapture)
// has no other record of which lines are the recording's; without it the
// new items went in beside the old ones (review of #138).
func (p *Pipeline) keepPreviousItems(ctx context.Context, tenantID, captureID, cleanKey string) ([]string, error) {
	before, err := p.cfg.Objects.Get(ctx, cleanKey)
	if errors.Is(err, repository.ErrNotFound) || (err == nil && len(before) == 0) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("pipeline: get previous items: %w", err)
	}
	prevKey, err := keys.CaptureCleanPrevious(tenantID, captureID)
	if err != nil {
		return nil, fmt.Errorf("pipeline: previous items key: %w", err)
	}
	if err := p.cfg.Objects.Put(ctx, prevKey, before, "text/plain"); err != nil {
		return nil, fmt.Errorf("pipeline: keep previous items: %w", err)
	}
	return strings.Split(string(before), "\n"), nil
}

// errItemsAllInvented is the verdict on an items reply whose every item was
// dropped for having no spoken word: usable as a list, useless as this
// recording's, and settled like a reply that was no list at all.
var errItemsAllInvented = errors.New("pipeline: every item the model returned had no spoken word")

// previousItems reads the copy extractItems kept of a checklist recording's
// earlier items, for an append resumed after the extraction has already
// overwritten the artefact at CleanKey. Nil when there is none: the
// recording's first append. The lines keep their indent; every reader folds
// it away with the whitespace and the punctuation (llm.FoldWords).
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

// itemsExcerpt is a checklist capture's items as one line, top-level items
// only, for the filing row's excerpt: "Milk · Eggs · Bread" says what was
// added where the rendered "- [ ] Milk" list would spend the row on markup.
func itemsExcerpt(items []cleanup.Item) string {
	texts := make([]string, 0, len(items))
	for _, it := range items {
		texts = append(texts, it.Text)
	}
	return strings.Join(texts, " · ")
}
