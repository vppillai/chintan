package service

import (
	"regexp"
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
// text, then every marker the stored body held, one per line at the end.
//
// The edited text is stripped first so a marker the client echoes back, or
// one a user types by hand, cannot be counted twice or forged. Markers move
// from beside their paragraph to the end of the body, which is fine: the
// guard asks whether the marker is anywhere in the body, not where.
func CarryCaptureMarkers(stored, edited string) string {
	markers := captureMarkerFind.FindAllString(stored, -1)
	edited = StripCaptureMarkers(edited)
	if len(markers) == 0 {
		return edited
	}
	seen := make(map[string]bool, len(markers))
	var b strings.Builder
	b.WriteString(edited)
	for _, m := range markers {
		if seen[m] {
			continue
		}
		seen[m] = true
		b.WriteString("\n")
		b.WriteString(m)
	}
	return b.String()
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
// After a user edit the markers sit at the end of the body with no text of
// their own (CarryCaptureMarkers moved them there), so a paragraph "boundary"
// then encloses nothing: cutting removes the marker alone and the user's text
// is untouched. That is the honest outcome — once the words have been edited
// there is no longer a fact about which of them the recording contributed.

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
