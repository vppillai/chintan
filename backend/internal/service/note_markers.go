package service

import (
	"regexp"
	"sort"
	"strings"
)

// The worker's append is guarded by a marker written into the note body in the
// same conditional PUT as the dictated paragraph:
//
//	<!-- chintan:capture:<captureID> -->
//	The paragraph the user dictated.
//
// A retry that finds the marker knows its own paragraph landed and finishes the
// bookkeeping instead of writing the text again. This is exact where the check
// it replaced — "is the cleaned text a substring of the body?" — was not: a
// user editing the paragraph inside the retry window removed the evidence, and
// a second short capture saying the same sentence was mistaken for the first.
//
// The marker is an HTML comment because the body is markdown and every
// markdown renderer drops comments — but the app's own editor is a plain
// textarea and would show it verbatim. So the API strips markers on the way
// out (GetNoteDetail, the export) and carries them on the way back in
// (UpdateNote), and nothing the user sees or types contains one. Carrying them
// is what keeps the guard alive across a save: a capture that died between
// writing the body and marking itself appended is finished by a retry minutes
// later, or by the user pressing retry days later, and both must find the
// marker whatever editing happened in between.

// captureMarkerPrefix and captureMarkerSuffix bracket the capture id. The id
// charset is the one internal/keys enforces, so the closing " -->" is
// unambiguous and one id can never be a prefix match for another.
const (
	captureMarkerPrefix = "<!-- chintan:capture:"
	captureMarkerSuffix = " -->"
)

// captureMarkerPattern removes markers with the line break that placed them.
//
// Two placements exist, and the pattern restores each to exactly the text
// that was there without the marker. An append writes
// "\n\n<marker>\n<text>" after existing content, or "<marker>\n<text>" into
// an empty note; a carried marker is written as "\n<marker>" at the very end.
// Removing "\n<marker>" undoes both the append (leaving "\n\n<text>") and the
// carry, and removing "<marker>\n" at the start of the body undoes the
// empty-note append. Anchored to \A rather than (?m)^ so the second
// alternative cannot leave a stray blank line mid-body.
var captureMarkerPattern = regexp.MustCompile(
	`\A(?:<!-- chintan:capture:[A-Za-z0-9_-]+ -->\n?)+|\n<!-- chintan:capture:[A-Za-z0-9_-]+ -->`)

// captureMarkerFind lists the markers in a body, in order, for CarryCaptureMarkers.
var captureMarkerFind = regexp.MustCompile(`<!-- chintan:capture:[A-Za-z0-9_-]+ -->`)

// CaptureMarker is the marker the worker writes ahead of the paragraph it
// appends for captureID.
func CaptureMarker(captureID string) string {
	return captureMarkerPrefix + captureID + captureMarkerSuffix
}

// HasCaptureMarker reports whether the body already carries captureID's
// marker, which is the exact statement "this capture's paragraph is in this
// note".
func HasCaptureMarker(body, captureID string) bool {
	return strings.Contains(body, CaptureMarker(captureID))
}

// StripCaptureMarkers returns the body as the user should see it: every
// marker gone, and the text around each exactly as it would have been written
// without one.
func StripCaptureMarkers(body string) string {
	if !strings.Contains(body, captureMarkerPrefix) {
		return body
	}
	return captureMarkerPattern.ReplaceAllString(body, "")
}

// CarryCaptureMarkers returns the body to store for a user edit: the edited
// text with every marker the stored body held put back — beside its paragraph
// where that paragraph is still there word for word, and as a trailer line at
// the end where it is not.
//
// The edited text is stripped first so a marker the client echoes back, or
// one a user types by hand, cannot be counted twice or forged.
//
// An edited body that reads the same as the stored one is not an edit, and
// the stored body comes back byte for byte. The client sends its whole draft
// with any Details change (language, kind, word-for-word, auto-clean), and
// treating that as an edit moved every marker to the end: each recording was
// detached from its paragraph, so "transcribe again" appended a second copy
// and "delete recording" left the words behind (QA 2026-09-21, finding 1).
//
// Otherwise each marker follows its paragraph: the stored paragraph's text
// (the positional rule below) is looked for in the edited body as a paragraph
// of its own — opening the body or following a blank line, and running to the
// end of a line, verbatim — and the marker is written ahead of the first
// occurrence no other marker has claimed. A paragraph the user rewrote or
// deleted has nowhere to go, so its marker is carried at the end; the guard
// asks whether the marker is anywhere in the body, not where.
//
// A marker beside its paragraph claims everything up to the next marker, so
// it stays there only while nothing but line breaks separates the paragraph
// from the next kept marker or the end of the body. Text typed below the last
// recording, or between two, would otherwise join the paragraph — and be cut
// with it by "delete recording", carried by "move to" and replaced by
// "transcribe again" (review 2026-09-21 r4). Its marker is carried at the end
// instead, and so is every marker before it whose paragraph would now run on
// to that text: the rule can say where a paragraph starts, not where it ends.
func CarryCaptureMarkers(stored, edited string) string {
	markers := captureMarkerFind.FindAllString(stored, -1)
	edited = StripCaptureMarkers(edited)
	if len(markers) == 0 {
		return edited
	}
	if StripCaptureMarkers(stored) == edited {
		return stored
	}

	type placed struct {
		marker  string
		at, end int
	}
	var unique []string
	var inPlace []placed
	var claimed [][2]int
	seen := make(map[string]bool, len(markers))
	for _, m := range markers {
		if seen[m] {
			continue
		}
		seen[m] = true
		unique = append(unique, m)
		id := strings.TrimSuffix(strings.TrimPrefix(m, captureMarkerPrefix), captureMarkerSuffix)
		p, _ := findCaptureParagraph(stored, id)
		if at := paragraphIndex(edited, p.text, claimed); at >= 0 {
			inPlace = append(inPlace, placed{marker: m, at: at, end: at + len(p.text)})
			claimed = append(claimed, [2]int{at, at + len(p.text)})
		}
	}
	sort.Slice(inPlace, func(i, j int) bool { return inPlace[i].at < inPlace[j].at })

	// Walked from the end, because demoting a marker hands the text after it
	// to the marker before.
	kept := make(map[string]bool, len(inPlace))
	nextAt := len(edited)
	for i := len(inPlace) - 1; i >= 0; i-- {
		p := inPlace[i]
		if strings.Trim(edited[p.end:nextAt], "\r\n") != "" {
			continue
		}
		kept[p.marker] = true
		nextAt = p.at
	}

	var b strings.Builder
	prev := 0
	for _, p := range inPlace {
		if !kept[p.marker] {
			continue
		}
		b.WriteString(edited[prev:p.at])
		b.WriteString(p.marker)
		b.WriteString("\n")
		prev = p.at
	}
	b.WriteString(edited[prev:])
	for _, m := range unique {
		if !kept[m] {
			b.WriteString("\n")
			b.WriteString(m)
		}
	}
	return b.String()
}

// paragraphIndex is where text stands in body as a paragraph of its own —
// opening the body or following a blank line, as an append places it, and
// running to the end of a line — outside the claimed spans, or -1. The blank
// line keeps the same lines inside a block the user typed from being taken
// for the paragraph. Two recordings can say the same sentence, so each claims
// one occurrence, in body order.
func paragraphIndex(body, text string, claimed [][2]int) int {
	if text == "" {
		return -1
	}
	for from := 0; ; {
		i := strings.Index(body[from:], text)
		if i < 0 {
			return -1
		}
		at, end := from+i, from+i+len(text)
		from = at + 1
		if at > 0 && !strings.HasSuffix(body[:at], "\n\n") && !strings.HasSuffix(body[:at], "\n\r\n") {
			continue
		}
		if end < len(body) && body[end] != '\n' && body[end] != '\r' {
			continue
		}
		free := true
		for _, c := range claimed {
			if at < c[1] && end > c[0] {
				free = false
				break
			}
		}
		if free {
			return at
		}
	}
}

// ---------------------------------------------------------------------------
// Paragraph boundaries
// ---------------------------------------------------------------------------
//
// Deleting or moving one recording means taking its paragraph out of the body,
// and the marker is the only exact statement of where that paragraph is. The
// rule is positional, not textual: nothing here compares the body against the
// transcript, because the user may have rewritten every word of it.
//
// A capture's paragraph is its marker, the text after it up to the next marker
// or the end of the body, and the line break(s) that placed it. Precisely:
//
//   - The paragraph text runs from the character after the marker's own line
//     break to the start of the newline run that precedes the next marker (or
//     to the end of the body). The run before the next marker belongs to the
//     next paragraph — it is that paragraph's separator — so cutting one
//     paragraph leaves the next one placed exactly as it was.
//   - The span removed is the newline run immediately before the marker (the
//     "\n\n" an append wrote, or the "\n" a carry wrote), the marker, and the
//     text. When the marker is the first thing in the body there is no run
//     before it, so the run after the text is removed instead, which keeps the
//     next marker at the start of the body.
//
// After a user edit a paragraph left as it was keeps its marker beside it and
// is cut whole. A marker whose paragraph was rewritten or deleted sits at the
// end of the body with no text of its own (CarryCaptureMarkers carried it
// there), so its "boundary" encloses nothing: cutting removes the marker
// alone and the user's text is untouched. That is the honest outcome — once
// the words have been edited there is no longer a fact about which of them
// the recording contributed.

// captureParagraph is one capture's span in a body: the half-open range
// [start, end) that CutCaptureParagraph removes, and the paragraph text
// itself without its marker or separators.
type captureParagraph struct {
	start, end int
	text       string
}

// findCaptureParagraph locates captureID's paragraph in body under the rule
// above. ok is false when the body carries no marker for the capture.
func findCaptureParagraph(body, captureID string) (p captureParagraph, ok bool) {
	marker := CaptureMarker(captureID)
	markerAt := strings.Index(body, marker)
	if markerAt < 0 {
		return captureParagraph{}, false
	}

	// The text ends where the next marker's separator begins.
	textStart := markerAt + len(marker)
	if strings.HasPrefix(body[textStart:], "\n") {
		textStart++
	}
	rest := body[textStart:]
	textEnd := len(body)
	if next := captureMarkerFind.FindStringIndex(rest); next != nil {
		textEnd = textStart + next[0]
	}
	text := strings.TrimRight(body[textStart:textEnd], "\r\n")
	textEnd = textStart + len(text)
	if text == "" {
		// An empty paragraph — a carried marker. The break after it, if any,
		// is the next marker's separator and stays.
		textEnd = markerAt + len(marker)
	}

	start := markerAt
	for start > 0 && (body[start-1] == '\n' || body[start-1] == '\r') {
		start--
	}
	end := textEnd
	if start == 0 {
		// Nothing placed this paragraph, so the break that placed the next
		// one has to go with it or it would open the body.
		for end < len(body) && (body[end] == '\n' || body[end] == '\r') {
			end++
		}
	}
	return captureParagraph{start: start, end: end, text: text}, true
}

// CutCaptureParagraph removes captureID's paragraph from body and returns the
// body without it and the paragraph text on its own. found is false, and the
// body is returned unchanged, when the capture has no marker in it.
//
// The text comes back without its marker so a caller moving it can place it
// with InsertCaptureParagraph, which writes the marker again.
func CutCaptureParagraph(body, captureID string) (rest, text string, found bool) {
	p, ok := findCaptureParagraph(body, captureID)
	if !ok {
		return body, "", false
	}
	return body[:p.start] + body[p.end:], p.text, true
}

// CaptureMarkerIDs lists the capture ids whose markers appear in body, in body
// order. It is what a chronological insert compares against.
func CaptureMarkerIDs(body string) []string {
	matches := captureMarkerFind.FindAllString(body, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		id := strings.TrimSuffix(strings.TrimPrefix(m, captureMarkerPrefix), captureMarkerSuffix)
		out = append(out, id)
	}
	return out
}

// InsertCaptureParagraph writes captureID's marker and text into body ahead of
// the first marker for which before(id) is true, or at the end when there is
// none. before is how the caller says "this capture is older than that one";
// an id it does not recognise is never a reason to stop, so the paragraph
// lands after it.
//
// The body ends up exactly as if the worker had appended the paragraphs in
// that order: "\n\n" before a paragraph that follows content, none before one
// that opens the body, and the separator that already placed the following
// paragraph left attached to it.
//
// An empty text is a marker with no paragraph — a recording whose words were
// edited away. Its position is meaningless, so it is carried as a trailer the
// way CarryCaptureMarkers writes one.
func InsertCaptureParagraph(body, captureID, text string, before func(id string) bool) string {
	marker := CaptureMarker(captureID)
	if text == "" {
		return body + "\n" + marker
	}
	paragraph := marker + "\n" + text

	for _, loc := range captureMarkerFind.FindAllStringIndex(body, -1) {
		id := strings.TrimSuffix(strings.TrimPrefix(body[loc[0]:loc[1]], captureMarkerPrefix), captureMarkerSuffix)
		if !before(id) {
			continue
		}
		at := loc[0]
		sepStart := at
		for sepStart > 0 && (body[sepStart-1] == '\n' || body[sepStart-1] == '\r') {
			sepStart--
		}
		lead, trail := "", ""
		if sepStart > 0 {
			lead = "\n\n"
		}
		if sepStart == at {
			// No separator to reuse: the next marker opens the body.
			trail = "\n\n"
		}
		return body[:sepStart] + lead + paragraph + trail + body[sepStart:]
	}

	if body == "" {
		return paragraph
	}
	return body + "\n\n" + paragraph
}
