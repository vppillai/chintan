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

func (p *Pipeline) append(ctx context.Context, tenantID string, capture *model.CaptureIndex, note model.NoteIndex) (model.CaptureIndex, error) {
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
		if written, err := p.paragraphInNote(ctx, note.S3MarkdownKey, capture.ID, cleanedText); err != nil {
			return current, fmt.Errorf("pipeline: check note for interrupted append: %w", err)
		} else if written {
			obs.Log(ctx).Info("append claim is held but the capture's paragraph is already in the note; finishing the interrupted attempt",
				slog.String("note_id", note.ID))
			obs.Count(ctx, "AppendResumedWithoutRewriting", map[string]string{"Stage": string(service.StatusAppending)})
			return p.finishAppend(ctx, tenantID, capture, note, cleanedText, token)
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

	if err := p.appendToNote(ctx, note.S3MarkdownKey, capture.ID, cleanedText); err != nil {
		// Hand the claim back so a transient object-store failure does not park
		// the capture until the claim lease expires.
		p.releaseAppendClaim(ctx, capture)
		return *capture, fmt.Errorf("pipeline: append to note: %w", err)
	}

	return p.finishAppend(ctx, tenantID, capture, note, cleanedText, token)
}

// appendStampWait bounds how long one append waits for another capture's
// stamp on the same note to clear before stamping over it. The row holds one
// stamp, so two appends to one note in flight together would leave the first
// unprotected once the second's refresh cleared it; waiting for the first to
// finish keeps one append in flight per note. Ten seconds is two orders of
// magnitude above the stamp-to-refresh span (one S3 GET and PUT, one GetItem,
// one S3 GET, one PutItem), so only a holder that died mid-append is ever
// stamped over, and it is not waited on for its whole twenty-minute lease.
const appendStampWait = 10 * time.Second

// stampNoteAppend writes the append stamp under the row's current version,
// re-reading on a lost race the way every other writer of this row does, and
// waiting first for another capture's fresh stamp on the same note to clear.
func (p *Pipeline) stampNoteAppend(ctx context.Context, tenantID, noteID, captureID string) error {
	giveUpWaiting := time.Now().Add(appendStampWait)
	conflicts := 0
	for {
		note, err := p.cfg.Store.GetNote(ctx, tenantID, noteID)
		if err != nil {
			return err
		}
		if p.anotherAppendInFlight(note, captureID) && time.Now().Before(giveUpWaiting) {
			// Another capture's paragraph is going into this body right now.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(appendStampPoll):
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
	return p.now().Sub(at) < appendStampWait
}

// appendStampPoll is how often a waiting append re-reads the row.
const appendStampPoll = 200 * time.Millisecond

// finishAppend is the bookkeeping after the text is durably in the note body:
// the index refresh and the completion of the claim. Both are safe to repeat,
// which is what lets a retry that finds the marker already written finish an
// attempt that died here.
func (p *Pipeline) finishAppend(ctx context.Context, tenantID string, capture *model.CaptureIndex, note model.NoteIndex, cleanedText, token string) (model.CaptureIndex, error) {
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
	p.autoCleanAfterAppend(ctx, tenantID, refreshed)
	return appended, nil
}

// paragraphInNote reports whether the note body carries text as the paragraph
// under captureID's marker — the exact statement that this attempt's words
// have been written. A checklist item the person ticked meanwhile still
// counts: the words are there, the tick is theirs.
func (p *Pipeline) paragraphInNote(ctx context.Context, noteKey, captureID, text string) (bool, error) {
	existing, err := p.cfg.Objects.Get(ctx, noteKey)
	if errors.Is(err, repository.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, old, found := service.CutCaptureParagraph(string(existing), captureID)
	return found && old == keepTick(old, text), nil
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

// keepTick returns text carrying old's ticks, line for line: a checklist
// item the person ticked stays ticked when its words are written again. A
// recording that now yields a different number of items carries nothing —
// there is no saying which new line was which old one, and an open item the
// person can tick again is better than a tick on the wrong item.
//
// ponytail: the carry is positional. A retranscription that keeps the count
// but reorders or reshapes the items puts the tick on the line that holds
// the old one's place, not its words; matching lines by their words would
// need a similarity rule and this is a retranscription of a ticked list, so
// the count check is the ceiling for now.
func keepTick(old, text string) string {
	oldLines, lines := strings.Split(old, "\n"), strings.Split(text, "\n")
	if len(oldLines) != len(lines) {
		return text
	}
	for i, line := range lines {
		if strings.HasPrefix(oldLines[i], "- [x] ") && strings.HasPrefix(line, "- [ ] ") {
			lines[i] = "- [x] " + strings.TrimPrefix(line, "- [ ] ")
		}
	}
	return strings.Join(lines, "\n")
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
func (p *Pipeline) appendToNote(ctx context.Context, noteKey, captureID, text string) error {
	_, err := service.RewriteNoteBody(ctx, p.cfg.Objects, noteKey, func(existing string) (string, bool) {
		if service.HasCaptureMarker(existing, captureID) {
			// A recording whose text is already in the note: transcribed
			// again (service.RetranscribeCapture, or run's re-transcription
			// after a retry from before the language was recorded), or this
			// attempt's own earlier try that died after writing. The
			// paragraph is replaced where it stands; appending would leave
			// the wrong-script text beside the right one.
			obs.Count(ctx, "AppendReplacedParagraph", map[string]string{"Stage": string(service.StatusAppending)})
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
