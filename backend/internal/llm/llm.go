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
//
// rules.go holds the three rule sentences every system prompt composes:
// DataRule, LanguageRule and NoInventionRule.
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
	outWords := Words(out)
	if len(outWords) == 0 {
		return true
	}

	i := 0
	for _, word := range Words(in) {
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

// chilluFold maps the five Malayalam chillu letters spelled the old way —
// consonant, virama (്), zero-width joiner — onto their atomic code points
// (Unicode 5.1, U+0D7A–U+0D7E), and drops any zero-width joiner or non-joiner
// left. Keyboards, fonts and Whisper differ on which spelling they produce,
// and byte comparison sees two different words where a reader sees one, so
// neither Ask, search nor find matched across the two (review 2026-09-21,
// T58). The sequences come before the bare joiners because the replacer
// takes the first pair that matches at a position.
var chilluFold = strings.NewReplacer(
	"ണ്\u200d", "ൺ", "ന്\u200d", "ൻ", "ര്\u200d", "ർ", "ല്\u200d", "ൽ", "ള്\u200d", "ൾ",
	"\u200c", "", "\u200d", "",
)

// FoldScript returns s with every chillu in its atomic spelling and no
// zero-width joiners. Words applies it, so every reader of comparable words
// — the checklist merge above all — takes a dictated and a typed Malayalam
// item for one item (decision D7, 2026-10-01); the stored search text, a
// search query and an Ask title call it directly because they match by
// substring, not by word. Text with no joiner is returned as it is.
func FoldScript(s string) string {
	if !strings.ContainsAny(s, "\u200c\u200d") {
		return s
	}
	return chilluFold.Replace(s)
}

// FoldWords is text as the checks compare it: its comparable words joined by
// single spaces, so "passport." and "Passport" are one item to every reader
// — the tick checks, the append's merge and the regeneration's search by
// words. Empty when the text has no letter, digit or mark.
func FoldWords(s string) string {
	return strings.Join(Words(s), " ")
}

// Words is the one word splitter: text with its chillu spellings folded
// (FoldScript), lowercased and cut into runs of IsWordRune, so punctuation,
// casing and joiner differences do not count as rewriting. Every reader that needs words — the checks here, ask.Tokenize,
// match's token overlap, the silence-hallucination test and the recording
// filename slug — calls this or IsWordRune, so an Indic or emoji fix lands
// everywhere at once (R7-2a was fixed in one place and missed in another).
func Words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(FoldScript(s)), func(r rune) bool { return !IsWordRune(r) })
}

// IsWordRune reports whether r belongs inside a word: a letter, a digit, or
// a nonspacing or spacing combining mark (Mn, Mc) of any script. The marks
// matter because an Indic vowel sign or virama is a mark, not a letter;
// treating it as a break folds പാൽ (milk) and പുൽ (grass) alike, or दाल and
// दिल, and every check built on the folded form then takes one word for the
// other. The variation selectors (U+FE0F after most emoji) are Mn too but
// belong to the emoji, not to a word, and the enclosing keycap U+20E3 is Me:
// counting either made "✈️ Travel" fold to a stray selector and "Milk❤️" stop
// matching "Milk", so both stay breaks.
func IsWordRune(r rune) bool {
	if unicode.IsLetter(r) || unicode.IsDigit(r) {
		return true
	}
	return (unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Mc, r)) && !unicode.Is(unicode.Variation_Selector, r)
}
