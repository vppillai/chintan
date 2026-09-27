package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/vppillai/chintan/backend/internal/cleanup"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/service"
)

// ---------------------------------------------------------------------------
// Stage 4 — append
// ---------------------------------------------------------------------------

// appendOptions is what differs between a capture's own run and a
// regeneration of its note (regenerate.go).
type appendOptions struct {
	// autoClean regenerates the note's cleaned view after this append when the
	// note asks for it. A capture's own run does; a regeneration passes
	// false and cleans once, after its last capture, rather than once per
	// capture with every run but the last superseded and billed.
	autoClean bool
	// previousItems are the checklist items this recording produced the last
	// time it was appended — the lines of its clean artefact before the
	// extraction overwrote it (extractItems) — so a re-append can find them
	// in a list that has since been ticked or reordered. Nil for a plain
	// note, a first append, or a retry that resumed past the extraction.
	previousItems []string
}

func (p *Pipeline) append(ctx context.Context, tenantID string, capture *model.CaptureIndex, note model.NoteIndex, opts appendOptions) (model.CaptureIndex, error) {
	if err := p.setStatus(ctx, capture, service.StatusAppending); err != nil {
		return *capture, err
	}

	cleanBytes, err := p.cfg.Objects.Get(ctx, capture.CleanKey)
	if err != nil {
		return *capture, fmt.Errorf("pipeline: get clean text: %w", err)
	}
	cleanedText := string(cleanBytes)
	if note.Kind == model.NoteKindChecklist {
		// One line per item the extraction returned, all under this one
		// marker, so deleting or moving the recording cuts exactly its items.
		// A verbatim checklist skipped the extraction and the cleaned text
		// is the recording as spoken: one item however it was line-broken.
		if note.Verbatim {
			cleanedText = strings.Join(strings.Fields(cleanedText), " ")
		}
		cleanedText = checklistItems(cleanedText)
	}

	// The append is the one step that must happen exactly once. Append, index
	// update and status flip are three writes with nothing tying them together:
	// a failure after the append leaves the capture in `cleaned`, and an
	// unguarded retry re-appends the same text.
	//
	// Two things guard it, and they do different jobs. The claim is a
	// mutex: one attempt at a time writes to the note, and a holder that dies
	// releases it when AppendClaimLease runs out. The paragraph under the
	// marker is the idempotency guard: appendToNote writes
	// "<!-- chintan:capture:<id> -->" into the body in the same conditional
	// PUT as the paragraph, so any later attempt can ask the body the exact
	// question "is this attempt's text under this capture's marker?" rather
	// than trusting the lease arithmetic. The marker alone is not that
	// answer: a recording transcribed again (service.RetranscribeCapture)
	// keeps its earlier paragraph, marker and all, until the new one replaces
	// it, and an attempt that took the marker as proof marked the capture
	// appended with the wrong-script paragraph untouched and no error anywhere
	// (review 2026-09-21, T7 follow-up). The token is derived from the capture
	// and its cleaned artefact, so every attempt at the same work computes the
	// same value and can recognise its own earlier claim; a takeover of that
	// claim once the lease has run out needs no resume path of its own,
	// because appendToNote replaces the paragraph where it stands and for the
	// same words that is the same body.
	token := appendToken(capture.ID, capture.CleanKey)

	claimed, current, err := p.cfg.Store.ClaimCaptureAppend(ctx, tenantID, capture.ID, token)
	if err != nil {
		return *capture, fmt.Errorf("pipeline: claim capture append: %w", err)
	}
	if !claimed {
		*capture = current
		if current.AppendToken != token || current.AppendedAt != 0 {
			// Somebody else owns this append, or an earlier attempt finished
			// it. Either way this attempt must not write the text a second time.
			return current, nil
		}

		// Our own token, unfinished, inside the lease. Either the earlier
		// attempt is still running, or it died after taking the claim; from
		// here the two look the same, and the paragraph under the marker is
		// what tells them apart.
		//
		// If this attempt's text is in the note body the dangerous part is
		// over, whoever did it: finishing the bookkeeping is idempotent (the
		// index refresh re-derives from the body, the completion is a
		// versioned write on the same token), so do it now rather than wait
		// for the lease. This is the case a Lambda retry a minute later
		// actually meets — the paragraph was written and the worker died
		// before marking the capture appended.
		//
		// Otherwise fail the invocation. Conceding here would leave the capture
		// in `appending` with nothing left to finish it; appending would race a
		// holder that may still be about to write. Lambda's automatic retries
		// at about one and two minutes both fall inside the twenty-minute
		// lease, so an attempt that died between claiming and writing — a
		// window of one object read and one write — dead-letters and raises
		// the alarm, and the user's retry after the lease takes the claim over
		// and does the append once.
		if written, err := p.paragraphInNote(ctx, note.S3MarkdownKey, capture.ID, cleanedText, opts.previousItems); err != nil {
			return current, fmt.Errorf("pipeline: check note for interrupted append: %w", err)
		} else if written {
			obs.Log(ctx).Info("append claim is held but the capture's paragraph is already in the note; finishing the interrupted attempt",
				slog.String("note_id", note.ID))
			obs.Count(ctx, "AppendResumedWithoutRewriting", map[string]string{"Stage": string(service.StatusAppending)})
			return p.finishAppend(ctx, tenantID, capture, note, token, opts.autoClean)
		}
		return current, fmt.Errorf("pipeline: append claim for this capture is still held by an "+
			"unfinished attempt (claimed %s ago, lease %s): %w",
			time.Since(time.Unix(current.AppendClaimedAt, 0)).Round(time.Second),
			repository.AppendClaimLease, errAppendClaimHeld)
	}
	*capture = current

	// Tell the note row before touching the body. The claim and the marker keep
	// the append exactly-once between workers; neither is visible to an editor
	// save, which checks the row's version and the body's ETag. The body write
	// below moves the ETag but not the version — the index refresh bumps that
	// afterwards — so a save that read the version before this point and the
	// ETag after the write passed both checks, rewrote the body from the text
	// it had, and the paragraph just dictated was gone with nothing left to
	// retry it (review 2026-09-05, S1). The stamp bumps the version now and
	// names the capture, so service.UpdateNote refuses a body write for as long
	// as the append can still be in flight, and the index refresh clears it.
	//
	// The row's version has usually moved since run() read it — the stages
	// before this one take seconds — so a lost race re-reads and stamps again.
	if err := p.stampNoteAppend(ctx, tenantID, note.ID, capture.ID); err != nil {
		p.releaseAppendClaim(ctx, capture)
		return *capture, fmt.Errorf("pipeline: stamp note for append: %w", err)
	}

	if err := p.appendToNote(ctx, note.S3MarkdownKey, capture.ID, cleanedText, opts.previousItems); err != nil {
		// Hand the claim back so a transient object-store failure does not park
		// the capture until the claim lease expires.
		p.releaseAppendClaim(ctx, capture)
		return *capture, fmt.Errorf("pipeline: append to note: %w", err)
	}

	return p.finishAppend(ctx, tenantID, capture, note, token, opts.autoClean)
}

// defaultAppendStampWait bounds how long one append waits for another
// capture's stamp on the same note to clear before stamping over it. The row holds one
// stamp, so two appends to one note in flight together would leave the first
// unprotected once the second's refresh cleared it; waiting for the first to
// finish keeps one append in flight per note. Ten seconds is two orders of
// magnitude above the stamp-to-refresh span (one S3 GET and PUT, one GetItem,
// one S3 GET, one PutItem), so only a holder that died mid-append is ever
// stamped over, and it is not waited on for its whole twenty-minute lease.
// Config.AppendStampWait overrides it.
const defaultAppendStampWait = 10 * time.Second

// stampNoteAppend writes the append stamp under the row's current version,
// re-reading on a lost race the way every other writer of this row does, and
// waiting first for another capture's fresh stamp on the same note to clear.
func (p *Pipeline) stampNoteAppend(ctx context.Context, tenantID, noteID, captureID string) error {
	// On the pipeline's clock, like the stamp it writes and the age check in
	// anotherAppendInFlight, so a test with a fixed clock sees one consistent
	// time and the wait ends when the other stamp clears, not when the wall
	// clock says so.
	giveUpWaiting := p.now().Add(p.cfg.AppendStampWait)
	conflicts := 0
	for {
		note, err := p.cfg.Store.GetNote(ctx, tenantID, noteID)
		if err != nil {
			return err
		}
		if p.anotherAppendInFlight(note, captureID) && p.now().Before(giveUpWaiting) {
			// Another capture's paragraph is going into this body right now.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(p.cfg.AppendStampPoll):
			}
			continue
		}
		_, err = p.cfg.Store.StampNoteAppend(ctx, tenantID, noteID, captureID, note.Version, p.now())
		if err == nil {
			return nil
		}
		if !errors.Is(err, repository.ErrVersionConflict) {
			return err
		}
		if conflicts++; conflicts >= maxIndexRefreshAttempts {
			return err
		}
	}
}

// anotherAppendInFlight reports whether the row carries a different capture's
// stamp young enough to be an append still writing. A stamp older than the
// wait bound was left by a holder that died; waiting on it would only delay
// this append by the whole bound for nothing.
func (p *Pipeline) anotherAppendInFlight(note model.NoteIndex, captureID string) bool {
	if note.AppendingCapture == "" || note.AppendingCapture == captureID {
		return false
	}
	at, err := model.ParseTime(note.AppendingAt)
	if err != nil {
		return false
	}
	return p.now().Sub(at) < p.cfg.AppendStampWait
}

// defaultAppendStampPoll is how often a waiting append re-reads the row.
// Config.AppendStampPoll overrides it.
const defaultAppendStampPoll = 200 * time.Millisecond

// finishAppend is the bookkeeping after the text is durably in the note body:
// the index refresh and the completion of the claim. Both are safe to repeat,
// which is what lets a retry that finds the marker already written finish an
// attempt that died here.
func (p *Pipeline) finishAppend(ctx context.Context, tenantID string, capture *model.CaptureIndex, note model.NoteIndex, token string, autoClean bool) (model.CaptureIndex, error) {
	// The worker's refresh is the editor's (service.RefreshNoteIndex) on the
	// worker's clock, clearing this capture's stamp, and refusing to index a
	// body it could not read — a paragraph was just written, so a failed read
	// is a fault to retry, not an empty note.
	refreshed, err := service.RefreshNoteIndex(ctx, p.cfg.Store, p.cfg.Objects, tenantID, note.ID, service.RefreshOptions{
		Now:                 p.now,
		ClearAppendStampFor: capture.ID,
		RequireBody:         true,
		Attempts:            maxIndexRefreshAttempts,
	})
	if err != nil {
		return *capture, fmt.Errorf("pipeline: refresh note index: %w", err)
	}

	appended, err := p.cfg.Store.CompleteCaptureAppend(ctx, tenantID, capture.ID, token)
	if err != nil {
		return *capture, fmt.Errorf("pipeline: complete capture append: %w", err)
	}
	*capture = appended

	// Only now, with the capture marked appended: the cleaned view follows the
	// body, and the body is settled.
	if autoClean {
		p.autoCleanAfterAppend(ctx, tenantID, refreshed)
	}
	return appended, nil
}

// paragraphInNote reports whether the note body already carries this
// attempt's words — the exact statement that the append has been written.
// For a paragraph under captureID's marker that is text standing there; a
// checklist item the person ticked meanwhile still counts, the words are
// there and the tick is theirs. For a recording whose items live under
// nobody's marker (previousItems set, see replaceChecklistItems) it is that
// replacing them would change nothing: the new lines are in, or the person
// took the old ones out.
func (p *Pipeline) paragraphInNote(ctx context.Context, noteKey, captureID, text string, previousItems []string) (bool, error) {
	existing, err := p.cfg.Objects.Get(ctx, noteKey)
	if errors.Is(err, repository.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	body := string(existing)
	_, old, found := service.CutCaptureParagraph(body, captureID)
	if !found {
		return false, nil
	}
	if old == keepTick(old, text) {
		return true, nil
	}
	return previousItems != nil && replaceChecklistItems(body, captureID, previousItems, text) == body, nil
}

// appendToken is deterministic so a retry of the same work recognises its own
// claim rather than treating it as somebody else's.
func appendToken(captureID, cleanKey string) string {
	sum := sha256.Sum256([]byte(captureID + "\x00" + cleanKey))
	return hex.EncodeToString(sum[:16])
}

// releaseAppendClaim hands an unwritten append back: the claim on the capture
// and the stamp on the note, so neither an editor save nor the next attempt
// waits out the lease for an append that is not coming. Both are best effort
// — the lease and the stamp's age are what bound a release that fails.
func (p *Pipeline) releaseAppendClaim(ctx context.Context, capture *model.CaptureIndex) {
	if err := p.cfg.Store.ClearNoteAppend(ctx, capture.UserID, capture.NoteID, capture.ID); err != nil {
		obs.Log(ctx).Warn("could not clear the note's append stamp after handing the claim back; editor saves wait for it to age out",
			slog.String("note_id", capture.NoteID),
			slog.String("error", err.Error()))
	}
	released := *capture
	released.AppendToken = ""
	released.AppendClaimedAt = 0
	if updated, err := p.cfg.Store.PutCapture(ctx, released); err == nil {
		*capture = updated
	}
}

// replaceCaptureParagraph puts text where captureID's paragraph stands in
// body: cut along the marker boundary the delete and move paths use, then
// inserted back in chronological position — the position it had, unless a
// user edit had carried its marker to the end, in which case the words it
// replaced were edited into the user's text and the new ones land as a new
// paragraph. A checklist item that was ticked stays ticked: the words were
// transcribed again, not the decision.
func replaceCaptureParagraph(body, captureID, text string) string {
	rest, old, _ := service.CutCaptureParagraph(body, captureID)
	text = keepTick(old, text)
	// ponytail: capture ids lead with their creation instant
	// (service.BeginCapture), so id order is chronological order and no
	// store read is needed; a note whose other recordings predate that id
	// format gets the paragraph at the end, which capture_move.go's
	// olderCapturesIn would place exactly.
	return service.InsertCaptureParagraph(rest, captureID, text, func(id string) bool { return id > captureID })
}

// keepTick returns text carrying old's ticks: a checklist item the person
// ticked stays ticked when its words are written again. A tick follows the
// item's words — an item that comes back in another place, as a re-extraction
// with the current prompt may order it, keeps it — and, when no words match
// at all and the two have the same number of lines, it follows the line, as
// it did before 2026-09-27, so a retranscription that reworded every item is
// carried the way it always was. A recording that now yields other words in
// another count carries nothing: there is no saying which new line was which
// old one, and an open item the person can tick again is better than a tick
// on the wrong item.
func keepTick(old, text string) string {
	oldLines, lines := strings.Split(old, "\n"), strings.Split(text, "\n")
	ticked := map[string]int{}
	for _, line := range oldLines {
		if t, ok := checklistLineText(line); ok && strings.HasPrefix(strings.TrimLeft(line, " \t"), "- [x] ") {
			ticked[t]++
		}
	}
	matched := false
	for i, line := range lines {
		t, ok := checklistLineText(line)
		if !ok || !strings.HasPrefix(line, "- [ ] ") || ticked[t] == 0 {
			continue
		}
		ticked[t]--
		lines[i] = "- [x] " + strings.TrimPrefix(line, "- [ ] ")
		matched = true
	}
	if !matched && len(oldLines) == len(lines) {
		for i, line := range lines {
			if strings.HasPrefix(oldLines[i], "- [x] ") && strings.HasPrefix(line, "- [ ] ") {
				lines[i] = "- [x] " + strings.TrimPrefix(line, "- [ ] ")
			}
		}
	}
	return strings.Join(lines, "\n")
}

// checklistLineText is the words of a task-list line, indent and box aside,
// whitespace runs collapsed the way checklistItems writes them and case
// folded, so a line the worker wrote, the same item after the editor's round
// trip, and the same item as a newer prompt capitalises it all compare
// equal. ok is false for a line that is not an item.
func checklistLineText(line string) (text string, ok bool) {
	trimmed := strings.TrimLeft(line, " \t")
	switch {
	case strings.HasPrefix(trimmed, "- [ ] "):
		trimmed = trimmed[len("- [ ] "):]
	case strings.HasPrefix(trimmed, "- [x] "):
		trimmed = trimmed[len("- [x] "):]
	default:
		return "", false
	}
	return foldWords(trimmed), true
}

// foldWords is a line's words as they are compared: whitespace runs collapsed
// the way checklistItems writes them, case folded.
func foldWords(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// replaceChecklistItems puts text — a recording's items, freshly extracted —
// where the recording's previous items stand in a checklist body, ticks
// carried by keepTick. Two shapes of body:
//
//   - The items are still under the recording's marker (a list only ever
//     spoken to): the ordinary replacement by the marker, as a plain note's
//     paragraph is replaced.
//   - The marker is a trailer with nothing under it (the Items tab carries
//     every marker to the end on each save, docs/design/checklists.md): the
//     items live in the list under nobody's marker. They are found by their
//     words, from previous — the lines of the recording's last clean
//     artefact — each once, and the new lines take the place of the first of
//     them; the rest of the list, typed items included, keeps its order. The
//     marker is left where it is, because a marker put back above the new
//     lines would claim every line down to the next marker, typed ones too,
//     for the next delete, move or regeneration. When none of the old words
//     are left the person deleted that recording's items, and putting them
//     back would overrule that: the body is returned as it was.
//
// Lines with the NEW items' words are taken up too, each once, and go back
// as part of the new block. That is what makes a second pass over a list the
// first pass already rewrote return it unchanged — the attempt that wrote the
// block and died before it could say so is finished, not repeated (append's
// claim-held branch, paragraphInNote) — and it means a typed line with the
// same words as a new item is folded into the block rather than kept as a
// duplicate. Indent is not read: a sub-item is matched by its words like any
// line, and the block is written at the top level (checklists.md,
// "indent-blind").
//
// An empty text removes the recording's items and writes nothing in their
// place: the recording, extracted again, named nothing to add.
func replaceChecklistItems(body, captureID string, previous []string, text string) string {
	_, old, found := service.CutCaptureParagraph(body, captureID)
	if !found || strings.TrimSpace(old) != "" || len(previous) == 0 {
		return replaceCaptureParagraph(body, captureID, text)
	}
	wanted := map[string]int{}
	for _, item := range previous {
		if t := foldWords(item); t != "" {
			wanted[t]++
		}
	}
	fresh := map[string]int{}
	for _, line := range strings.Split(text, "\n") {
		if t, ok := checklistLineText(line); ok {
			fresh[t]++
		}
	}
	lines := strings.Split(body, "\n")
	kept := make([]string, 0, len(lines))
	var removed []string
	at, oldSeen := -1, false
	for _, line := range lines {
		t, ok := checklistLineText(line)
		isOld, isNew := ok && wanted[t] > 0, ok && fresh[t] > 0
		if !isOld && !isNew {
			kept = append(kept, line)
			continue
		}
		if isOld {
			wanted[t]--
			oldSeen = true
		}
		if isNew {
			fresh[t]--
		}
		removed = append(removed, line)
		if at < 0 {
			at = len(kept)
		}
	}
	if !oldSeen {
		return body
	}
	out := make([]string, 0, len(kept)+len(lines))
	out = append(out, kept[:at]...)
	if text = keepTick(strings.Join(removed, "\n"), text); text != "" {
		out = append(out, strings.Split(text, "\n")...)
	}
	out = append(out, kept[at:]...)
	return strings.Join(out, "\n")
}

// checklistItems renders a recording's items — one per line of the cleaned
// text — as open task-list lines: each with its whitespace runs collapsed to
// single spaces, trimmed, cut to cleanup.MaxChecklistItemRunes; blank lines
// dropped. Text with no words stays empty — an empty paragraph, as a plain
// note would get — rather than becoming an item with nothing in it.
func checklistItems(text string) string {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		collapsed := strings.Join(strings.Fields(line), " ")
		if collapsed == "" {
			continue
		}
		if runes := []rune(collapsed); len(runes) > cleanup.MaxChecklistItemRunes {
			collapsed = strings.TrimSpace(string(runes[:cleanup.MaxChecklistItemRunes]))
		}
		lines = append(lines, "- [ ] "+collapsed)
	}
	return strings.Join(lines, "\n")
}

// appendToNote adds text to the end of a note body under a conditional write,
// preceded by the capture's marker.
//
// A bare read-concat-write with no concurrency control silently discards one of
// a voice append and an editor save that land together. The write carries the
// ETag that was read and a lost race re-reads and retries so both edits
// survive; that loop is service.RewriteNoteBody, the same one the delete and
// move paths use, and only the edit is the worker's.
//
// The marker and the paragraph go into the body in one PUT, so there is no
// state in which one is present without the other. The API keeps the marker
// out of everything the user sees (service.StripCaptureMarkers) and puts it
// back on every save (service.CarryCaptureMarkers), so it survives edits.
//
// A marker already in the body is never a reason to do nothing: the paragraph
// under it is replaced where it stands, which for the same words is the same
// body. That one rule covers a recording transcribed again and this capture's
// own attempt that wrote the body, died, and was taken over after the lease.
func (p *Pipeline) appendToNote(ctx context.Context, noteKey, captureID, text string, previousItems []string) error {
	_, err := service.RewriteNoteBody(ctx, p.cfg.Objects, noteKey, func(existing string) (string, bool) {
		if service.HasCaptureMarker(existing, captureID) {
			// A recording whose text is already in the note: transcribed
			// again (service.RetranscribeCapture, or run's re-transcription
			// after a retry from before the language was recorded),
			// regenerated with the current prompt (RegenerateNote), or this
			// attempt's own earlier try that died after writing. The
			// paragraph is replaced where it stands; appending would leave
			// the wrong-script text beside the right one. A checklist's
			// items are found by their words when the marker no longer
			// holds them.
			obs.Count(ctx, "AppendReplacedParagraph", map[string]string{"Stage": string(service.StatusAppending)})
			if previousItems != nil {
				next := replaceChecklistItems(existing, captureID, previousItems, text)
				return next, next != existing
			}
			return replaceCaptureParagraph(existing, captureID, text), true
		}
		paragraph := service.CaptureMarker(captureID) + "\n" + text
		if existing != "" {
			paragraph = existing + "\n\n" + paragraph
		}
		return paragraph, true
	})
	if err != nil {
		return fmt.Errorf("pipeline: update note: %w", err)
	}
	return nil
}
