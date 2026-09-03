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
