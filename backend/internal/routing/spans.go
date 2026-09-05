package routing

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
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

// MaxInstructionWords bounds how many words the router may remove in total.
//
// A routing or naming instruction is a few words ("add this to my roof repair
// note", "create a note titled Portugal trip"); a span much longer than that is
// the router mistaking dictation for instruction, and dictation removed from
// the note is lost while a stray instruction word in it is trivial to fix.
const MaxInstructionWords = 24

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
	words := strings.FieldsFunc(strings.ToLower(transcript), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\''
	})
	padded := " " + strings.Join(words, " ") + " "
	for _, cue := range instructionCues {
		if strings.Contains(padded, " "+cue+" ") {
			return true
		}
	}
	return false
}
