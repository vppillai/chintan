// Package llm holds the guards every prompt over user speech shares.
//
// A transcript is untrusted input: it can say "ignore your instructions" as
// easily as "the gutter is leaking". Three defences keep that from mattering,
// and they are here rather than in the routing or cleanup package so the next
// feature that puts a transcript in front of a model (D1 cleanup modes, D5
// Ask) reuses them instead of re-deriving them:
//
//   - Fence wraps the transcript in markers the prompt declares to be a data
//     boundary, and defangs any occurrence of the marker in the speech itself.
//   - VerifySubsequence checks that a model's output is the transcript with
//     words deleted and nothing else — no summary, translation, answer or
//     commentary can pass it.
//   - ExtractJSONObject pulls the one JSON object out of a reply that may be
//     wrapped in a markdown fence or prose, so a chatty model still parses.
package llm

import (
	"fmt"
	"strings"
	"unicode"
)

// FenceMarker delimits the untrusted transcript inside a user prompt. The
// prompts that use it tell the model that everything between two markers is
// data, not instructions.
const FenceMarker = "-----TRANSCRIPT-----"

// Fence renders text between two FenceMarker lines. Any marker spoken inside
// the text is defanged so the speech cannot close the block early and put its
// own words outside the boundary.
func Fence(text string) string {
	return FenceMarker + "\n" + strings.ReplaceAll(text, FenceMarker, "-----") + "\n" + FenceMarker
}

// VerifySubsequence reports whether every word of out appears, in order, in in
// — that is, whether out is in with words deleted. Casing and punctuation are
// ignored, so a model that capitalised a sentence or added a full stop has not
// rewritten it, but one that summarised, reordered, translated or appended has.
//
// An empty out is vacuously a sub-sequence and returns true; a caller that
// cannot accept "nothing" has to say so itself.
func VerifySubsequence(out, in string) bool {
	outWords := comparableWords(out)
	if len(outWords) == 0 {
		return true
	}

	i := 0
	for _, word := range comparableWords(in) {
		if word == outWords[i] {
			if i++; i == len(outWords) {
				return true
			}
		}
	}
	return false
}

// ExtractJSONObject returns the outermost {...} span of raw, ignoring markdown
// fences and surrounding prose.
func ExtractJSONObject(raw string) (string, error) {
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return "", fmt.Errorf("llm: response contained no JSON object")
	}
	return raw[start : end+1], nil
}

// comparableWords reduces text to lowercase alphanumeric words, so that
// punctuation and casing differences do not count as rewriting.
func comparableWords(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}
