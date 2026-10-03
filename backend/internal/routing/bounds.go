package routing

import (
	"strings"
	"unicode"
)

// The bounds of the deterministic routing rules, in one place.
//
// Fourteen rules rescue or refuse what the router answers (routing.md
// §The bounds; the table is TestRoutingBoundsAreRegistered). Every number one
// of them reads lives here, with why it is that number and the rule that
// reads it — the two rules whose numbers belong to the transcription and the
// cleanup (13: the silence scores; 14: the hint and short-dictation bounds)
// included, read from here by provider and pipeline. Until 2026-10-01 these
// sat in three packages and disagreed with each other — a "name" was five
// words to one rule, eight to another, two words or eight letters to a third
// — and two prompt experiments left bounds behind whose comments cited
// reverted sentences (review 2026-10-01, BE-5, BE-12).
//
// Two tests hold the line. TestRoutingBoundsAreRegistered holds this file to
// the rule table; TestRuleFilesHoldNoUnregisteredLiterals reads the three
// files the rules live in (route.go, openai_router.go, spans.go) and fails
// on a numeric constant or a comparison against a number that is not here.
// The gate (decision D8, 2026-10-01): no new rescue rule or bound ships
// without a line in this file and a fixture case that exercises it in
// provider/testdata/eval/fixtures.json with its recording under
// provider/testdata/eval/recordings (scripts/dev/record-replay.sh), which
// the replay tests run in CI with no key; the revert rule (R7-10a) holds
// prompt text the same way.
const (
	// MinNameWords and MinNameRunes are what may file a recording by opening
	// it or by being spoken as a name (rules 2 and 3: prefix_title,
	// prefix_transcript, spoken_name; pipeline.prefixRuleName): two words, or
	// one of at least eight letters. "Roof", "list" and "test" open too many
	// sentences that are not about them; a one-word name of five to seven
	// letters ("dentist", "house") waits on the owner (triage 2026-09-29,
	// decision 1).
	MinNameWords = 2
	MinNameRunes = 8

	// AppendConfidence is how sure the router must be before appending to an
	// existing note without asking (rule 4; pipeline.outcomeOf). Below it the
	// person confirms first (needs_target).
	AppendConfidence = 0.75

	// MaxInstructionWords bounds how many words the router may remove in
	// total (rule 6; RemoveSpans). A routing or naming instruction is a few
	// words ("add this to my roof repair note", "create a note titled
	// Portugal trip"); a span much longer than that is the router mistaking
	// dictation for instruction, and dictation removed from the note is lost
	// while a stray instruction word in it is trivial to fix.
	MaxInstructionWords = 24

	// MaxNameWords is the prompt's own bound on a name: its Titles rule
	// invents "a short descriptive title (one to five words)" (systemPrompt),
	// and the fixture for a sentence taken as a title pins the shape (rule 7;
	// provider.routedContent, DB6-4). A title within it is a name the speaker
	// may have said in full, so a span grown over it removes instruction; a
	// title past it is the dictation the model mistook for a name, and
	// growing over it removes the note.
	MaxNameWords = 5

	// MaxInstructionOnlyWords is the longest transcript that is plausibly
	// nothing but a spoken app instruction, and MaxSpokenTitleWords the
	// longest title that still reads as a name rather than a sentence the
	// router mistook for one (rule 8; provider.routedContent). Past either,
	// spans that cover every word look like lost dictation and the whole
	// transcript is kept. Eight here and five above because they bound
	// different mistakes: a spoken title can run to eight words ("Things to
	// talk with Milos about the Portugal trip") without the dictation having
	// been swallowed into it, while a growth over the title is undone as soon
	// as the title stops reading as a name.
	MaxInstructionOnlyWords = 20
	MaxSpokenTitleWords     = 8

	// MaxCandidates bounds the note list handed to the router, and the list
	// the pre-create re-check reads again (rules 1 to 3 and 10;
	// pipeline.decideTarget, pipeline.route): the most recently touched two
	// hundred. The store lists notes in that order over the whole partition
	// (repository.MaxNotesDrained), so the first page of the list IS the
	// window; there is no separate pool to drain and cut. It was fifty until
	// 2026-09-27, which left the owner's seven least-touched notes
	// unreachable by voice; with candidates rendered as numbered lines rather
	// than ids, two hundred cost about what fifty did (round-5 prompts lens,
	// PR-D2). Beyond a few hundred notes the right tool is a lexical
	// prefilter like Ask's ranker, not a bigger window.
	MaxCandidates = 200

	// FallbackTitleWords and FallbackTitleRunes name a note the router could
	// not title (rule 12; pipeline.fallbackNoteTitle): the first words of what
	// was said, six or forty characters, whichever comes first, so the row
	// reads as the thought it holds and still fits a list line.
	FallbackTitleWords = 6
	FallbackTitleRunes = 40

	// MaxTitleRunes is the one bound on a note title, wherever it comes from:
	// the router's dictated title (SanitizeTitle, in provider.parseRouteDecision),
	// a title the API accepts (handler.MaxTitleRunes, the OpenAPI maxLength)
	// and the title the store writes (service). One number, so a title the
	// API accepts is stored whole and a dictated title is cut no shorter than
	// a typed one; until 2026-10-01 the router cut at 120 and the other two
	// at 200, and route() ran both sanitisers on one title. The routing
	// prompt does not depend on it: it bounds every rendered candidate field
	// itself (maxFieldLen).
	MaxTitleRunes = 200

	// LogprobThreshold is the one of Whisper's two silence scores that is live
	// on this provider (rule 13; provider.Transcription.NoSpeech): a segment
	// whose avg_logprob is at or under it is one the model was unsure of
	// every word of, and a recording made only of such segments is noise
	// heard as words. It is Whisper's own number, the bound its reference
	// transcribe() keeps a doubtful segment by. Whisper's other score,
	// no_speech_prob, is not read: Groq's whisper-large-v3-turbo reported it
	// as 0 on every one of the 45 production captures that carried scores
	// (scores are logged since 2026-09-30; the 192 earlier lines have none),
	// so a gate that needed it over 0.6 could never fire, and three noise
	// recordings (one, two and thirteen words at avg_logprob -2.08, -2.56
	// and -1.35) each became a note. Of the other 42: thirty were the QA
	// battery's identical English clips, eleven were real English dictations
	// whose least confident was -0.604, and one was a stock silence phrase at
	// -0.88 that the phrase list caught. The bound is measured for English
	// only — no Malayalam, Tamil or Hindi capture has been scored — which is
	// why a person's Transcribe again lifts it (CaptureIndex.SkipGates).
	LogprobThreshold = -1.0

	// QuietPeakRMS is the recorder's own line for "nothing heard" (rule 13;
	// pipeline.transcribe): the loudest analyser frame of the recording, as
	// RMS of the time-domain signal in 0..1, under which the app's live
	// canvas never rose above its floor (frontend peaks.ts PEAK_FLOOR, the
	// same number for the same reason). An idle or muted microphone sits
	// around 0.01; someone speaking softly passes 0.05. A capture whose
	// peak never crossed it is filed as no_content without a transcription
	// call, and the person saw the bars stay flat while recording. Zero
	// means the client measured nothing (a device, an older app) and the
	// gate does not apply.
	QuietPeakRMS = 0.04

	// MinHintAudioMS is the shortest recording that gets a spelling prompt
	// (rule 14; pipeline.spellingHints). Whisper can answer near-silence by
	// echoing its prompt back as the transcript, and a 1.5 s clip is mostly
	// the ring's start and stop; below it a list of the person's note titles
	// would become the "dictation".
	MinHintAudioMS = 1500

	// MaxHintNotes bounds the notes read for an untargeted capture's hints
	// (rule 14). The adapter keeps only what fits Whisper's 224-token prompt,
	// which is fewer names than this, so the extra rows only cost read bytes.
	MaxHintNotes = 50

	// ShortDictationWords is the length under which a dictation skips the
	// cleanup model and gets pipeline.tidyDictation instead (rule 14). In the
	// seven days to 2026-09-30, 59% of cleanup calls returned 15 output
	// tokens or fewer, the median transcript was 11 words, and the call took
	// 818 ms at p50 and 3 s at p95: most recordings waited up to three
	// seconds for a capital letter and a full stop. Twelve words keeps that
	// median case off the model. The owner accepted the cost: a misheard word
	// in a dictation this short is kept as Whisper heard it.
	ShortDictationWords = 12
)

// SanitizeTitle bounds a title to one line of at most MaxTitleRunes: control
// characters become spaces, whitespace runs collapse to one, and the rest is
// cut. One function for every place a title enters — dictation, the API, the
// store — since a title is later rendered back into prompts and list lines.
func SanitizeTitle(title string) string {
	title = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, title)
	title = strings.Join(strings.Fields(title), " ")
	if runes := []rune(title); len(runes) > MaxTitleRunes {
		title = strings.TrimSpace(string(runes[:MaxTitleRunes]))
	}
	return title
}
