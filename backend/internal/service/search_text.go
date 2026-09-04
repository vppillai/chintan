package service

import (
	"strings"
	"unicode/utf8"

	"github.com/vppillai/chintan/backend/internal/model"
)

// SearchText derives the value NoteIndex.SearchText holds from a note body:
// append markers removed, lowercased, whitespace collapsed, and cut to
// model.MaxSearchTextBytes on a rune boundary.
//
// It exists because GET /v1/search matched titles, aliases, tags and the
// 500-rune snippet, and nothing else — the owner searched for a word several
// transcripts contained and got nothing. Fetching bodies from S3 per query was
// rejected for the reason search.go gives (one GET per note per keystroke), so
// the searchable text lives on the index row instead, and every writer of a
// body writes it: UpdateNote, the worker's index refresh after an append, and
// the chintanctl backfill for notes that predate the field.
//
// Lowercasing happens here rather than at query time so a search compares
// bytes it already has in the right case, and so the stored size is the size
// that is checked. strings.ToLower can grow a string (a two-byte rune can fold
// to a three-byte one), which is why the cap is applied after the fold.
func SearchText(body string) string {
	text := strings.ToLower(StripCaptureMarkers(body))
	text = strings.Join(strings.Fields(text), " ")
	if len(text) <= model.MaxSearchTextBytes {
		return text
	}
	cut := model.MaxSearchTextBytes
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}
