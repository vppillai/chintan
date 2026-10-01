package routing

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/vppillai/chintan/backend/internal/llm"
)

// Span is a run of words in the whitespace-tokenised transcript that the
// router says is an app instruction. StartWord is the index of the first word
// and EndWord the index one past the last, both counted from zero — the same
// numbers the prompt prints in front of each word, so the model reads
// positions instead of counting them.
type Span struct {
	StartWord int `json:"start_word"`
	EndWord   int `json:"end_word"`
}

var (
	// ErrSpanMalformed means a span does not describe this transcript: a
	// negative or out-of-range index, or an end that is not after its start.
	ErrSpanMalformed = errors.New("routing: instruction span does not fit the transcript")
	// ErrSpansTooLong means the spans together remove more words than an app
	// instruction can plausibly hold. See MaxInstructionWords.
	ErrSpansTooLong = errors.New("routing: instruction spans remove too many words")
)

// Words tokenises a transcript exactly as the prompt numbers it and as
// RemoveSpans indexes it.
func Words(transcript string) []string {
	return strings.Fields(transcript)
}

// NumberWords renders words as "0:first 1:second …", the form the router sees
// inside the fence. The numbers cost roughly two tokens a word on the input
// side and buy exact positions on the output side, which is the cheaper side
// by a factor of four.
func NumberWords(words []string) string {
	var b strings.Builder
	for i, w := range words {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(strconv.Itoa(i))
		b.WriteByte(':')
		b.WriteString(w)
	}
	return b.String()
}

// RemoveSpans returns transcript with the words in spans deleted.
//
// The result is by construction the transcript with words removed and nothing
// else, which is the guarantee the router's output verifier used to have to
// check after the fact: the model never returns note content, so it cannot
// return content it invented. With no spans the transcript is returned
// byte-for-byte, whitespace included; otherwise the kept words are joined with
// single spaces. Spans may overlap. A span that does not fit the transcript
// fails with ErrSpanMalformed and a set that removes more than
// MaxInstructionWords fails with ErrSpansTooLong; the caller decides what to
// keep in either case, and the right answer is every word.
func RemoveSpans(transcript string, spans []Span) (string, error) {
	if len(spans) == 0 {
		return transcript, nil
	}
	words := Words(transcript)
	remove := make([]bool, len(words))
	for _, s := range spans {
		if s.StartWord < 0 || s.EndWord > len(words) || s.StartWord >= s.EndWord {
			return "", fmt.Errorf("%w: [%d,%d) of %d words", ErrSpanMalformed, s.StartWord, s.EndWord, len(words))
		}
		for i := s.StartWord; i < s.EndWord; i++ {
			remove[i] = true
		}
	}
	removed := 0
	kept := make([]string, 0, len(words))
	for i, w := range words {
		if remove[i] {
			removed++
			continue
		}
		kept = append(kept, w)
	}
	if removed > MaxInstructionWords {
		return "", fmt.Errorf("%w: %d words, limit %d", ErrSpansTooLong, removed, MaxInstructionWords)
	}
	return strings.Join(kept, " "), nil
}

// ExtendSpans closes the gaps the router leaves most often at the end of an
// instruction span, each deterministic once the span and the title are known
// (production battery 2026-09-29, rows 2 and 13, three of three each): a span
// that stops just before the spoken title ("0:title 1:this" with the title
// "staging smoke" following) or inside it ("0:title 1:this 2:staging" with
// the same title, which left "smoke" opening row 13's body in every run)
// grows to the title's end, and a span followed by the word "note" ("put that
// in my roof" + "note") grows over it, since "note" after a filing phrase is
// the noun of the phrase and not dictation. The title is the new note's, or
// on an append the destination's, since a span may stop inside either
// ("Create a new note and add it to" + "Pebble Ring Test", row 9). Spans that
// do not fit the transcript are returned as they are, so RemoveSpans still
// refuses them. The result is still only words deleted from the transcript;
// nothing here can add a word.
//
// ponytail: "note" is the one trailing noun the battery showed; "list" is the
// same shape and joins it the day a battery row shows it.
func ExtendSpans(words []string, spans []Span, title string) []Span {
	title = NormalizeSpeech(title)
	n := len(strings.Fields(title))
	out := make([]Span, len(spans))
	for i, s := range spans {
		out[i] = s
		if s.StartWord < 0 || s.EndWord > len(words) || s.StartWord >= s.EndWord {
			continue
		}
		end := s.EndWord
		// The title's words start k words back into the span: k = 0 is the
		// title right after it, k = 1..n-1 a span that stopped inside it. The
		// smallest k wins, so a title repeated in the transcript costs the
		// fewest words.
		for k := 0; k < n; k++ {
			start := end - k
			if start < s.StartWord || start+n > len(words) {
				continue
			}
			if NormalizeSpeech(strings.Join(words[start:start+n], " ")) == title {
				end = start + n
				break
			}
		}
		if end < len(words) && NormalizeSpeech(words[end]) == "note" {
			end++
		}
		out[i].EndWord = end
	}
	return out
}

// NormalizeSpeech is the comparison form of spoken words: lowercased, with
// everything but word runes (llm.IsWordRune: letters, digits and combining
// marks, so Indic vowel signs stay in their words) and apostrophes dropped,
// one space between words. Speech-to-text punctuates and capitalises as it
// likes, so a name heard in a transcript is compared to a note's name in
// this form.
func NormalizeSpeech(s string) string {
	words := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !llm.IsWordRune(r) && r != '\''
	})
	return strings.Join(words, " ")
}

// instructionCues are the openings of the two app instructions the router
// honours (systemPrompt), as people say them. A transcript containing none of
// them has no span to remove, so MentionsInstruction lets the pipeline skip the
// router call for a capture that was recorded straight into a note; one
// containing any may have. Recall over precision: a cue that fires on ordinary
// dictation ("call it a day") costs one small routing call, while an
// instruction no cue catches stays in the note.
var instructionCues = []string{
	"add this to", "add that to", "add it to", "append this to", "append that to", "append it to",
	"put this in", "put that in", "put it in", "put this under", "put that under",
	"file this", "file that", "file it", "save this to", "save this in", "save this under",
	"create a note", "create a new note", "make a note", "make a new note", "start a note", "start a new note", "new note",
	"title this", "title it", "titled", "with the title", "under the title",
	"call this note", "call this", "call it", "name this note", "name this", "name it",
}

// MentionsInstruction reports whether transcript contains any instruction cue
// as whole words, case-insensitively and ignoring punctuation, so "Create a
// note with the title Staging Smoke." is caught and "the gutter leaks" is not.
func MentionsInstruction(transcript string) bool {
	padded := " " + NormalizeSpeech(transcript) + " "
	for _, cue := range instructionCues {
		if strings.Contains(padded, " "+cue+" ") {
			return true
		}
	}
	return false
}

// NamedAfterCue reports whether name is spoken as the object of an
// instruction cue in transcript — "add it to Pebble Ring Test", "put this in
// my roof repair note" — with at most one of "my", "the" or "our" between
// the cue and the name, both compared in NormalizeSpeech form. It is the
// precise sibling of MentionsInstruction, which only says a recording
// carries a cue somewhere: a rule that files silently on a spoken name
// (spoken_name, R6-RT-7) needs the name to be what the cue names, or "put
// this in my journal I was thinking about the roof repair today" would file
// into Roof repair.
func NamedAfterCue(transcript, name string) bool {
	name = NormalizeSpeech(name)
	if name == "" {
		return false
	}
	padded := " " + NormalizeSpeech(transcript) + " "
	for _, cue := range instructionCues {
		for _, between := range []string{" ", " my ", " the ", " our "} {
			if strings.Contains(padded, " "+cue+between+name+" ") {
				return true
			}
		}
	}
	return false
}
