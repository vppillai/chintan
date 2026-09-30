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

	candidates := []routing.Candidate{routeCandidate(note)}
	decision, err := p.routeWithRetries(ctx, tenantID, capture.ID, transcript, candidates, cleanupLanguage(*capture))
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

	r, err := p.decideTarget(ctx, tenantID, capture.ID, transcript, cleanupLanguage(*capture))
	decision, matchedBy := r.decision, r.matchedBy
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
	// One decision line per routed capture, written from the branch this
	// function finally takes. Until 2026-09-29 decideTarget logged it before
	// the same-second re-check below, so a deduped capture read as a new note
	// nothing matched, and an archived destination or a park was only
	// inferable from other lines (DB6-13). None when the router did not
	// answer: the Warn above is that capture's line.
	decided := err == nil
	outcome := r.outcome
	if !decided {
		outcome = outcomeNew
	}
	finish := func(outcome string) error {
		if decided {
			logRoutingDecision(ctx, decision, matchedBy, outcome, r.candidates, transcript, sourceDim(capture.Source))
		}
		return p.persist(ctx, capture)
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

	if outcome == outcomeAppend || outcome == outcomeNeedsTarget {
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
			if outcome == outcomeAppend {
				capture.NoteID = decision.NoteID
				capture.TargetSource = model.TargetSourceRouter
				capture.Status = model.StatusTranscribed
				return finish(outcomeAppend)
			}
			// Plausible but unsure: ask before writing into an existing note.
			capture.SuggestedNoteID = decision.NoteID
			capture.Status = model.StatusNeedsTarget
			return finish(outcomeNeedsTarget)
		case err == nil:
			obs.Log(ctx).Info("routed note is archived; keeping the dictation in a new note",
				slog.String("capture_id", capture.ID),
				slog.String("note_id", decision.NoteID))
			outcome = outcomeNewAfterMissing
		case errors.Is(err, repository.ErrNotFound):
			obs.Log(ctx).Info("routed note no longer exists; keeping the dictation in a new note",
				slog.String("capture_id", capture.ID),
				slog.String("note_id", decision.NoteID))
			outcome = outcomeNewAfterMissing
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
		return finish(outcomeNeedsTarget)
	}

	// The candidate list was read before the model call, and the ring posts
	// several recordings in the same second (owner tenant 2026-09-27 18:23:02,
	// 2026-09-29 01:33:28), so two captures naming a list nobody has yet can
	// each be told "new" and make it twice. One more projected query here,
	// only on the path that is about to create a note, catches the sibling's
	// note and appends to it instead. A store fault fails the invocation as
	// errRouteCandidates does: the retry is cheap, the duplicate note is not.
	// The rule is preferExistingTitle's pure half: the dedupe is counted as
	// itself, not as a title match too (DB6-13).
	fresh, _, err := p.cfg.Store.DrainNotes(ctx, tenantID, repository.DrainOptions{MaxItems: maxRouteCandidates})
	if err != nil {
		return fmt.Errorf("pipeline: re-check routing candidates: %w", err)
	}
	if noteID, by := existingNoteNamed(decision, transcript, fresh); noteID != "" {
		obs.Log(ctx).Info("a note this recording names appeared while it was being routed; appending to it instead of creating one",
			slog.String("capture_id", capture.ID),
			slog.String("note_id", noteID))
		obs.Count(ctx, "RouterCreateDeduped", map[string]string{})
		decision, matchedBy = filedInto(decision, noteID), by
		capture.NoteID = noteID
		capture.RouteConfidence = decision.Confidence
		capture.TargetSource = model.TargetSourceRouter
		capture.Status = model.StatusTranscribed
		return finish(outcomeDeduped)
	}

	spec := model.NoteIndex{ID: routedNoteID(capture.ID), Title: title}
	if decision.Checklist {
		// The router heard a list — "add milk to the shopping list" with no
		// such note — so the note is a checklist before the capture points
		// at it, and run() takes the extractItems branch for this same
		// recording instead of cleaning the sentence into a plain note,
		// which left the owner's first item reading "Add milk to the
		// shopping list." (owner feedback 2026-09-27).
		spec.Kind = model.NoteKindChecklist
	}
	if capture.Language != "" && capture.Language != model.LanguageAuto {
		// The note starts in the language its first recording was
		// transcribed in, so a later change of the tenant's default does not
		// silently change what "Record into this" sends for it. Auto is not
		// written: the note then follows the default, as a note a person
		// creates does.
		spec.Language = capture.Language
	}
	// Kind and language ride on the create, one row write, and the id is the
	// capture's, so the create is create-if-absent (R7-21). Until 2026-09-30
	// a crash between CreateNote and persist left an empty note the retry
	// did not know was its own, and a failed follow-up PutNote left a plain
	// note titled like a list that the retry then filed items into as prose.
	note, err := p.cfg.Notes.CreateNoteOnce(ctx, tenantID, spec)
	if err != nil {
		return fmt.Errorf("pipeline: create note for capture: %w", err)
	}
	if !service.NoteIsActive(note) {
		// The note is this capture's own, from an attempt that crashed, and
		// the owner archived it before the retry. Filing into it would fail
		// the capture; making another would undo the archive. The capture
		// asks instead, with the title it would have had, as run() does for
		// a destination purged mid-flight.
		obs.Log(ctx).Info("this capture's own note was archived before the retry; asking for a destination",
			slog.String("capture_id", capture.ID),
			slog.String("note_id", note.ID))
		capture.SuggestedTitle = title
		capture.Status = model.StatusNeedsTarget
		return finish(outcomeNeedsTarget)
	}
	// One count per new note says how often the model answers "checklist"
	// in production, which the eval battery only samples.
	kind := "note"
	if note.Kind == model.NoteKindChecklist {
		kind = model.NoteKindChecklist
	}
	obs.Count(ctx, "RouterNewNoteKind", map[string]string{"Kind": kind})
	capture.NoteID = note.ID
	capture.TargetSource = model.TargetSourceRouter
	capture.Status = model.StatusTranscribed
	return finish(outcome)
}

// routedNoteID is the id of the note routing creates for a capture: the
// capture's own id under the note prefix, so a retry of the same capture
// names the same note, and, like any note id, it sorts by when the capture
// was made.
func routedNoteID(captureID string) string {
	return "note_" + strings.TrimPrefix(captureID, "c_")
}

// errRouteCandidates marks a routing failure that happened before the router
// was asked: the store would not list the notes it chooses among. It is the
// one routing error route() does not turn into a new note.
var errRouteCandidates = errors.New("pipeline: list routing candidates")

// routed is decideTarget's answer: the decision after preferExistingTitle,
// how it matched ("" when the model's own answer stood), what decide() says
// to do with it, and how many notes the router saw, the last for the
// decision line route() logs once its branch is final.
type routed struct {
	decision   provider.RouteDecision
	matchedBy  string
	outcome    string
	candidates int
}

func (p *Pipeline) decideTarget(ctx context.Context, tenantID, captureID, transcript, language string) (routed, error) {
	if p.cfg.Router == nil {
		return routed{}, fmt.Errorf("pipeline: routing is not configured")
	}

	// The store orders the list most recently touched first over every note
	// the tenant has, so the leading maxRouteCandidates are the window the
	// router should see — the likeliest destinations. Until 2026-09 this
	// drained a 500-note pool and cut it here, which was only right while the
	// store's own order was by creation.
	active, _, err := p.cfg.Store.DrainNotes(ctx, tenantID, repository.DrainOptions{MaxItems: maxRouteCandidates})
	if err != nil {
		return routed{}, fmt.Errorf("%w: %w", errRouteCandidates, err)
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
	active = withinRouteBudget(active)

	candidates := make([]routing.Candidate, 0, len(active))
	for _, n := range active {
		candidates = append(candidates, routeCandidate(n))
	}

	decision, err := p.routeWithRetries(ctx, tenantID, captureID, transcript, candidates, language)
	if err != nil {
		return routed{}, err
	}
	decision, matchedBy := preferExistingTitle(ctx, decision, transcript, active)
	return routed{decision: decision, matchedBy: matchedBy, outcome: outcomeOf(decision), candidates: len(candidates)}, nil
}

// The outcomes route() logs. decide() yields the first three from the reply
// alone; the last two need the store (route()).
const (
	outcomeAppend          = "append"
	outcomeNeedsTarget     = "needs_target"
	outcomeNew             = "new"
	outcomeDeduped         = "deduped"
	outcomeNewAfterMissing = "new_after_missing"
)

// decide is the deterministic half of routing: the router's reply and the
// transcript, over the notes the router saw, to what route() does with them
// before the store is asked again. The spoken-name rules (existingNoteNamed)
// may turn the reply into an append; an append at routeConfidenceThreshold
// or above is filed (outcomeAppend), one below it asks (outcomeNeedsTarget),
// and anything else starts a note (outcomeNew). It reads nothing and logs
// nothing, so the worker and the replayed routing eval
// (TestRoutingEvalReplay) run the same rules on the same reply. route() can
// still turn an append into outcomeNewAfterMissing (the note is gone) and a
// new note into outcomeDeduped (a sibling capture made it meanwhile).
func decide(decision provider.RouteDecision, transcript string, active []model.NoteIndex) (provider.RouteDecision, string, string) {
	noteID, matchedBy := existingNoteNamed(decision, transcript, active)
	if noteID != "" {
		decision = filedInto(decision, noteID)
	}
	return decision, matchedBy, outcomeOf(decision)
}

// outcomeOf is decide()'s last step on a decision the rules have already seen.
func outcomeOf(decision provider.RouteDecision) string {
	switch {
	case decision.Action != provider.RouteAppend || decision.NoteID == "":
		return outcomeNew
	case decision.Confidence >= routeConfidenceThreshold:
		return outcomeAppend
	default:
		return outcomeNeedsTarget
	}
}

// logRoutingDecision is the one INFO line `routing decided` per routed
// capture, counts and enumerations only, so a week of routes can be judged
// from the log alone: until 2026-09-29 telling a new note from an append
// meant joining DynamoDB rows, S3 transcripts and log lines, and a route
// whose note was since purged could not be judged at all. outcome is what
// route() did with the decision: append, needs_target, new, deduped (the
// pre-create re-check found the note a sibling capture had just made) or
// new_after_missing (the model's note was archived or gone by the time it
// was read). No title, no transcript word, no device id (source is
// sourceDim's app or device); the correlation id rides the context.
func logRoutingDecision(ctx context.Context, decision provider.RouteDecision, matchedBy, outcome string, candidates int, transcript, source string) {
	switch {
	case matchedBy != "":
	case decision.Action == provider.RouteAppend:
		matchedBy = "model"
	default:
		matchedBy = "none"
	}
	dictated := len(routing.Words(transcript))
	kept := len(routing.Words(decision.Content))
	titleWords := 0
	if decision.Action == provider.RouteNew {
		titleWords = len(routing.Words(decision.Title))
	}
	obs.Log(ctx).Info("routing decided",
		slog.String("action", string(decision.Action)),
		slog.Float64("confidence", decision.Confidence),
		slog.String("matched_by", matchedBy),
		slog.String("outcome", outcome),
		slog.Int("candidates", candidates),
		slog.Int("transcript_words", dictated),
		slog.Int("title_words", titleWords),
		slog.Int("spans", decision.Spans),
		slog.Int("removed_words", dictated-kept),
		slog.Bool("checklist", decision.Checklist),
		slog.String("source", source))
}

// routeCandidate is the note as the router sees it: title, aliases and tags,
// each a name the speaker may file by, and whether it is a checklist.
func routeCandidate(n model.NoteIndex) routing.Candidate {
	return routing.Candidate{NoteID: n.ID, Title: n.Title, Aliases: n.Aliases, Tags: n.Tags,
		Checklist: n.Kind == model.NoteKindChecklist}
}

// withinRouteBudget cuts the ordered list where its rendered lines would pass
// maxRouteCandidateTokens, so a tenant of two hundred long-titled notes cannot
// grow the prompt past what the ceiling was priced for. The most recently
// touched notes lead the list and are kept; the estimate is the same one the
// breaker reserves against.
func withinRouteBudget(active []model.NoteIndex) []model.NoteIndex {
	total := 0.0
	for i, n := range active {
		total += candidateTokens(routeCandidate(n))
		if total > maxRouteCandidateTokens {
			return active[:i]
		}
	}
	return active
}

// preferExistingTitle applies the rule the prompt already states — a spoken
// name of an existing note is the destination — after the model has answered,
// and says which name matched ("" when none did). The model started a second
// "staging smoke" beside the one that existed (live QA 2026-09-05 §5b), and a
// rule this mechanical is the code's to enforce, not the model's to remember.
// The candidates are the notes the router saw, so the rule reaches exactly as
// far as routing does; the id, never the title, is what gets logged.
//
// Three matches, in order. A "new" decision whose title IS an active
// candidate's title, alias or tag, compared case- and whitespace-insensitively
// (matched_by title, alias or tag). Then the name-first shape the owner's ring
// speaks — "App feedback checklist move seems to be good", "Business ideas by
// Priyanka seated pool for dogs" — which the model titled as new notes twice
// on 2026-09-27 and the owner moved by hand: the model's title opens with a
// listed name as whole words (prefix_title), or the transcript itself does
// (prefix_transcript). The prefix rules take a name of at least two words or
// eight letters, so "list" or "test" never files anything, prefer the longest
// name, and apply to a "new" decision and to an append the model was unsure
// of (under routeConfidenceThreshold), since filing silently on a spoken name
// (R6-RT-OD1) should not depend on which unsure verdict the model gave. The
// derived content is kept as the model left it; a name that stays in the
// body is one word to delete, dictation stripped by a guess is gone.
func preferExistingTitle(ctx context.Context, decision provider.RouteDecision, transcript string, active []model.NoteIndex) (provider.RouteDecision, string) {
	decision, matchedBy, _ := decide(decision, transcript, active)
	if matchedBy == "" {
		return decision, ""
	}
	obs.Log(ctx).Info("the recording names an existing note; appending to it instead of the router's answer",
		slog.String("note_id", decision.NoteID),
		slog.String("matched_by", matchedBy))
	obs.Count(ctx, "RouterTitleMatchedExistingNote", map[string]string{})
	return decision, matchedBy
}

// filedInto is decision turned into an append to noteID by a rule of the
// code's: confidence 1, since the rule is mechanical, and no title and no
// kind, since the note has both — Checklist is always false for an append
// (provider.RouteDecision), and a rescued "new checklist" kept it, so the
// decision line counted appends into checklists (DB6-39).
func filedInto(decision provider.RouteDecision, noteID string) provider.RouteDecision {
	decision.Action = provider.RouteAppend
	decision.NoteID = noteID
	decision.Title = ""
	decision.Checklist = false
	decision.Confidence = 1
	return decision
}

// existingNoteNamed finds the active note the decision or the transcript
// names, and how: the exact title rule first, then the longest name either
// opens with, then, for an append the model was unsure of, the model's own
// suggestion spoken as a name (spokenAsName). It applies to a "new" decision
// and to an append under routeConfidenceThreshold; an append the model was
// sure of stands. It is the pure half of preferExistingTitle, run again by
// route()'s pre-create re-check without the title-match count.
func existingNoteNamed(decision provider.RouteDecision, transcript string, active []model.NoteIndex) (string, string) {
	if decision.Action == provider.RouteAppend && decision.Confidence >= routeConfidenceThreshold {
		return "", ""
	}
	if decision.Action != provider.RouteNew && decision.Action != provider.RouteAppend {
		return "", ""
	}
	title := routing.NormalizeSpeech(decision.Title)
	if decision.Action == provider.RouteNew && title != "" {
		for _, n := range active {
			if kind := titleNames(n, title); kind != "" {
				return n.ID, kind
			}
		}
	}
	speech := routing.NormalizeSpeech(transcript)
	bestID, bestBy, bestLen := "", "", 0
	for _, n := range active {
		for _, name := range noteNames(n) {
			name = routing.NormalizeSpeech(name)
			if len(name) <= bestLen || !prefixRuleName(name) {
				continue
			}
			switch {
			case strings.HasPrefix(title, name+" "):
				bestID, bestBy, bestLen = n.ID, "prefix_title", len(name)
			case strings.HasPrefix(speech, name+" "):
				bestID, bestBy, bestLen = n.ID, "prefix_transcript", len(name)
			}
		}
	}
	if bestID == "" && decision.Action == provider.RouteAppend {
		// Only the note the model itself suggested is looked at; the rule
		// confirms a suggestion and never re-picks among the candidates.
		for _, n := range active {
			if n.ID == decision.NoteID && spokenAsName(n, transcript) {
				return n.ID, "spoken_name"
			}
		}
	}
	return bestID, bestBy
}

// spokenAsName reports whether one of n's names is spoken in transcript as a
// name rather than a topic: as whole words, two words or eight letters long
// (prefixRuleName, for the same reason it exists), and either followed by
// "note" or "list" ("okay so this goes in the roof repair note …", battery
// 2026-09-29 row 14, parked twice with that very note suggested) or as the
// object of an instruction cue ("Create a new note and add it to Pebble
// Ring Test", row 9, routing.NamedAfterCue). It is the rule for the model's
// own unsure append (matched_by spoken_name, R6-RT-7): the model picked the
// note and asked, and the name spoken as a name is what the person would
// answer with, so the append is taken — the silent-file-over-ask trade of
// decision R6-RT-OD1 applied to the model's own suggestion. "I was thinking
// about the roof today" names no note this way, whatever the model
// suggested; nor does a name mentioned elsewhere in a recording whose cue
// names something else ("put this in my journal I was thinking about the
// roof repair today"), nor the cue naming a different note than the model
// picked ("add this to my roof repair note …" with Portugal trip suggested)
// — the first PR 170 draft asked routing.MentionsInstruction, a
// whole-transcript predicate, and filed both. A one-word name of five to
// seven letters ("dentist", "house") is left to the owner's decision (triage
// 2026-09-29, owner decision 1): on a tenant with two Dentist notes it turns
// an ask into a silent file on a name the model was only half sure of.
func spokenAsName(n model.NoteIndex, transcript string) bool {
	speech := " " + routing.NormalizeSpeech(transcript) + " "
	for _, name := range noteNames(n) {
		name = routing.NormalizeSpeech(name)
		if !prefixRuleName(name) {
			continue
		}
		if routing.NamedAfterCue(transcript, name) || strings.Contains(speech, " "+name+" note ") || strings.Contains(speech, " "+name+" list ") {
			return true
		}
	}
	return false
}

// noteNames is the note's title, aliases and tags: every name the router was
// shown for it, and so every name a speaker may file by.
func noteNames(n model.NoteIndex) []string {
	return append([]string{n.Title}, append(append([]string(nil), n.Aliases...), n.Tags...)...)
}

// prefixRuleName is the guard on what may file a recording by opening it: two
// words, or one of at least eight letters. "Roof", "list" and "test" open too
// many sentences that are not about them.
func prefixRuleName(name string) bool {
	return strings.Contains(name, " ") || utf8.RuneCountInString(name) >= 8
}

// titleNames reports which of n's names want, already in NormalizeSpeech
// form, is — its title, one of its aliases or one of its tags, the names the
// router was shown for it — or "" when none. The comparison ignores
// punctuation as the prefix rules do: a model that answers new "Roof
// repair." for the note "Roof repair" named it (DB6-40; until 2026-09-29
// the exact rule kept the full stop and the prefix rule needed a following
// word, so a duplicate note was created).
func titleNames(n model.NoteIndex, want string) string {
	if routing.NormalizeSpeech(n.Title) == want {
		return "title"
	}
	for _, a := range n.Aliases {
		if routing.NormalizeSpeech(a) == want {
			return "alias"
		}
	}
	for _, t := range n.Tags {
		if routing.NormalizeSpeech(t) == want {
			return "tag"
		}
	}
	return ""
}

// routeWithRetries asks the router, with one retry on a stall or a 5xx. It
// is the model half of decideTarget, shared with stripInstructions, which
// wants the instruction spans and not the destination.
func (p *Pipeline) routeWithRetries(ctx context.Context, tenantID, captureID, transcript string, candidates []routing.Candidate, language string) (provider.RouteDecision, error) {
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
		decision, err := p.routeOnce(ctx, tenantID, transcript, candidates, language)
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
func (p *Pipeline) routeOnce(ctx context.Context, tenantID, transcript string, candidates []routing.Candidate, language string) (provider.RouteDecision, error) {
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
		out, err := p.cfg.Router.Route(attemptCtx, transcript, candidates, language)
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

// estimateCandidateTokens is the pre-call guess at the candidate block, the
// sum of its lines.
func estimateCandidateTokens(candidates []routing.Candidate) float64 {
	total := 1.0
	for _, c := range candidates {
		total += candidateTokens(c)
	}
	return total
}

// candidateTokens is one candidate's line: the usual four characters a token
// over every name, plus three tokens for the ordinal and the separators
// (measured 2026-09-26: "12 | Roof repair" is 7 tokens).
func candidateTokens(c routing.Candidate) float64 {
	chars := len(c.Title)
	for _, a := range c.Aliases {
		chars += len(a)
	}
	for _, t := range c.Tags {
		chars += len(t)
	}
	if c.Checklist {
		chars += len(" [checklist]")
	}
	return float64(chars)/4 + 3
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
