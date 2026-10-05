# Routing

Which note a dictated recording belongs to, what kind of note a new one is,
and which of its words were spoken to the app rather than into the note. The
model proposes; a fixed set of rules in the code decides. Code: the stage
(`backend/internal/pipeline/route.go`), the prompt and the span arithmetic
(`backend/internal/routing/prompt.go`, `spans.go`), the adapter that parses
the reply and derives the content (`backend/internal/provider/openai_router.go`),
and every number a rule reads (`backend/internal/routing/bounds.go`). The
prompt's text and intent are `prompts.md` §Routing; this page is what happens
around the call.

## When it runs

`Pipeline.route` runs for a capture with no destination — recorded on Home,
or posted to the inbox without `X-Chintan-Note-Id` — once it is transcribed.
A capture recorded into a note skips routing, except that
`Pipeline.stripInstructions` makes the same call with the target note as the
only candidate when the transcript contains an instruction cue
(`routing.MentionsInstruction` over `routing.instructionCues`, rule 11): the
destination it answers is ignored and only the spans are used, so "add this to
my roof note, the gutter leaks" appends without the first six words. No cue, no
call. Both are conveniences: a routing failure keeps the dictation as spoken (a
new note, titled from its first words) and only a store fault fails the
invocation, so a recording is never lost to the router.

## The candidates

The router sees the `routing.MaxCandidates` (200) most recently touched active
notes — the store lists notes in that order over the whole partition
(`repository.MaxNotesDrained`), so the first page is the window
(`decideTarget`) — cut again where their rendered lines pass
`maxRouteCandidateTokens` (2,000, `pipeline.go`; `withinRouteBudget`), so a
tenant of long titles and many tags cannot grow the prompt past what the
spend estimate priced. Each candidate is one numbered line,
`3 | Roof repair | also: gutters, roof, house`: the title, then the aliases
and the tags after `also:` with no word for which is which, because either
spoken is a request for that note; every field is one line of at most 120
runes with `|` removed (`sanitizeField`, `maxFieldLen`) so a title cannot
forge a second candidate; no id anywhere. Beyond a few hundred notes the
right tool is a lexical prefilter like Ask's ranker, not a wider window.

## The call

`routeWithRetries` asks at most `routeAttempts` (2) times, each attempt its
own breaker reservation with its own deadline (`RouteAttemptTimeout`,
default `defaultRouteAttemptTimeout` 15 s: a routing answer not back in
fifteen seconds is far likelier queued at the provider than about to
arrive). Only a timeout or a 5xx is retried (`routeRetryReason`;
`RouterRetried{Reason=timeout|provider_5xx}`, `RouterTimedOut{Attempt}`): a
4xx will fail identically, a spend cap is not retried around, and an
unparseable answer or an unlisted note is the model's verdict, met by the
fallback. The completion is capped at `routeMaxTokens` (200): a real reply
is under fifty tokens, so the cap only cuts a runaway one.

**The reply** is one JSON object: `action` `append` with `note` (the 1-based
line number) or `new` with `title` and `kind`; `confidence` 0–1;
`instruction_spans` as `{start_word, end_word}` pairs over the numbered
transcript. `parseRouteDecision` reads it out of a fence or surrounding
prose (`llm.ExtractJSONObject`), maps the number back to the id
(`listedNoteID`; a `note_id` string is accepted when it is a listed id), reads
`kind` strictly (`checklist` in any case makes a checklist, anything else a
plain note, and it is ignored on an append — the note has a kind), clamps
`confidence` to [0, 1] and bounds the title to one line of
`routing.MaxTitleRunes` (`SanitizeTitle`, the one bound every title shares
with the API and the store). A number off the list, a fraction, or an id that
was not offered is "unknown note id" and refused.

## Deriving the content

The model never returns note text. The content is the transcript with the
spans deleted (`routing.RemoveSpans`), so by construction it is words the
speaker said and nothing else. `routedContent` applies the spans in this
order, and every failure keeps the whole transcript, because a stray
instruction word in a note is trivial to fix and dictation removed from it is
lost:

1. No `instruction_spans` field at all is a router that ignored the format
   (`RouterSpansDiscarded{Reason=missing_field}`).
2. Each span is grown over the spoken title it stopped short of or inside,
   and over a trailing "note" (`routing.ExtendSpans`, rule 5): a title the
   span stops just before, or `k` words into, is the instruction's last
   words. On an append the title grown over is the destination's.
3. A span that does not fit the transcript (`malformed`), or a set removing
   more than `routing.MaxInstructionWords` (24) words in all (`too_long`,
   rule 6), discards every span.
4. When the growth left nothing and the model's title is longer than a name
   (`routing.MaxNameWords`, 5; rule 7), the growth is undone and the model's
   own spans decide: a title that long is the dictation mistaken for a name,
   and growing over it would empty the note. A naming-only recording whose
   title is a real name ("title this staging smoke") is emptied on purpose.
5. Spans that leave no content are believed only for a transcript of at
   most `routing.MaxInstructionOnlyWords` (20) words with a title of at most
   `routing.MaxSpokenTitleWords` (8) words (rule 8; `empty_content`
   otherwise): a longer one is dictation swallowed into the title. An
   append's title is the destination's, so only the transcript bound applies.
6. The result is re-checked as a sub-sequence of the transcript
   (`llm.VerifySubsequence`, rule 9; `not_derived`).

## The decision

`decide(reply, transcript, candidates)` is the pure half: it reads nothing and
logs nothing, so the worker and the replayed eval run the same rules on the
same reply. `existingNoteNamed` may turn the reply into an append to a note
the recording names (`filedInto`: confidence 1, no title, no kind), then
`outcomeOf` says what to do: an append at `routing.AppendConfidence` (0.75)
or above is filed (`append`), one below it asks (`needs_target`, the model's
note offered as the suggestion), anything else starts a note (`new`). The
rescues apply to a `new` decision and to an append under the bar; an append
the model was sure of stands. In order:

- **Rule 1, exact name.** A `new` title that is a candidate's title, alias
  or tag in `routing.NormalizeSpeech` form (lowercased, punctuation dropped,
  so "Roof repair." names "Roof repair") files into it (`titleNames`;
  `matched_by` title|alias|tag) — when every word of the name was spoken
  (`wordsSpoken`: whole words of the transcript, in any order). The title is
  the model's, and a candidate named as an instruction can make the model
  answer "new" with that name for a recording that never said it
  (`prompt-safety.md`); a title borrowed from the list for a topic mention
  ("Roof repair" for "more about the roof") starts a note, which is the
  prompt's own Destination rule.
- **Rule 2, a name opening the recording.** The model's title or the
  transcript itself opens with a listed name as whole words
  (`prefix_title`|`prefix_transcript`), the longest name winning; the name
  must be `routing.MinNameWords` (2) words or `routing.MinNameRunes` (8)
  letters (`prefixRuleName`), because "roof", "list" and "test" open too
  many sentences that are not about them, and `prefix_title` holds the name
  to the spoken words as rule 1 does. The derived content is kept as the
  model left it: a name left in the body is one word to delete, dictation
  stripped by a guess is gone.
- **Rule 3, the model's own unsure suggestion spoken as a name**
  (`spokenAsName`; `spoken_name`). Only the note the model suggested is
  looked at, never a re-pick: one of its names, whole words and within the
  same two-word or eight-letter bound, followed by "note" or "list" or spoken
  as the object of an instruction cue with at most "my", "the" or "our"
  between (`routing.NamedAfterCue`). "I was thinking about the roof today"
  names nothing this way, nor does a cue naming a different note than the
  model picked.
- **Rule 4, append or ask** (`outcomeOf`, the 0.75 bar).

Then `route` finishes the branch against the store. An append or ask whose
note is archived or gone when read becomes a new note (`new_after_missing`);
a store fault on that read fails the invocation rather than falling through,
because a DynamoDB throttle must not start a second note on a subject the
person has dictated into all week. On the path about to create a note the
active list is read once more and `existingNoteNamed` run again (rule 10,
the same `routing.MaxCandidates` window): a ring posts several recordings in
one second, so two captures naming a list nobody has yet make one list, not
two (`deduped`). A note the model could not title gets the first
`routing.FallbackTitleWords` (6) words or `routing.FallbackTitleRunes` (40)
characters of the dictation, whichever comes first (`fallbackNoteTitle`,
rule 12); only an empty transcript falls back to "Voice note <date>". The
note is created once, with `Kind` and the capture's language in the same
write (`CreateNoteOnce`, the id derived from the capture's), so a retry finds
its own note and a `checklist` decision takes the item-extraction branch in
the same run (`checklists.md`, "A new list"); `RouterNewNoteKind{Kind}`
counts the kind. A store fault listing the candidates (`errRouteCandidates`)
is the one routing error that fails the invocation instead of making a note,
for the same duplicate-note reason.

## The bounds

Every number a rule reads is one constant in `routing/bounds.go`, with the
reason it is that number; `TestRoutingBoundsAreRegistered`
(`routing/bounds_test.go`) holds the file to this table both ways, and
`TestRuleFilesHoldNoUnregisteredLiterals` fails on a numeric constant or a
comparison against a number in `spans.go`, `pipeline/route.go` or
`provider/openai_router.go` that is not registered here. A new rescue rule
or bound ships only with a line in `bounds.go`, a fixture case in
`provider/testdata/eval/fixtures.json` that exercises it and that case's
recording (§Replay).

| Rule | Bound | Constant | What it protects |
|---|---|---|---|
| 1 exact title, alias or tag (`pipeline.titleNames`, `wordsSpoken`) | every word of the name spoken | — | a spoken name that is a listed note's is that note, not a duplicate; a name the model borrowed files nothing |
| 2 `prefix_title` / `prefix_transcript` (`pipeline.existingNoteNamed`) | 2 words, or 8 letters; `prefix_title` also every word spoken | `MinNameWords`, `MinNameRunes` | short words that open unrelated sentences never file anything |
| 3 `spoken_name` on the model's unsure append (`pipeline.spokenAsName`, `NamedAfterCue`) | 2 words, or 8 letters | `MinNameWords`, `MinNameRunes` | a topic mention is not a filing request |
| 4 append or ask (`pipeline.outcomeOf`) | 0.75 | `AppendConfidence` | an unsure append asks instead of writing into the wrong note |
| 5 span growth over the title or a trailing "note" (`ExtendSpans`) | k < the title's word count | — | the instruction's last words do not open the body |
| 6 spans bound (`RemoveSpans`) | 24 words | `MaxInstructionWords` | dictation mistaken for instruction is kept |
| 7 un-grown fallback when the growth would empty the body (`provider.routedContent`) | 5 words | `MaxNameWords` | a sentence taken as a title does not empty the note |
| 8 empty-content guard (`provider.routedContent`) | 20 words; 8 words | `MaxInstructionOnlyWords`, `MaxSpokenTitleWords` | a swallowed dictation is kept whole |
| 9 content is a sub-sequence of the transcript (`llm.VerifySubsequence`) | — | — | nothing reaches the note that was not said |
| 10 same-second re-check before a create (`pipeline.route`) | 200 notes | `MaxCandidates` | sibling captures make one note |
| 11 targeted-capture instruction gate (`MentionsInstruction`) | the `instructionCues` list | — | a capture into a note pays for a call only when it may hold an instruction |
| 12 fallback title (`pipeline.fallbackNoteTitle`) | 6 words, 40 characters | `FallbackTitleWords`, `FallbackTitleRunes` | a row that reads as the thought it holds and fits a list line |
| 13 nothing heard (`pipeline.transcribe`, `provider.Transcription.NoSpeech`) | 0.04 peak; −1.0 | `QuietPeakRMS`, `LogprobThreshold` | silence and room noise are not filed |
| 14 hint echo and the short-dictation tidy (`pipeline.spellingHints`, `transcriptOutcome`, `isShortDictation`) | 1,500 ms; 50 notes; 12 words | `MinHintAudioMS`, `MaxHintNotes`, `ShortDictationWords` | note titles do not become dictation; a short dictation does not wait on a model |
| 15 the cleaned words share (`cleanup.WordShare`; `pipeline.clean`, `pipeline.CleanNote`) | 0.5 of the reply's words | `MinCleanedWordShare` | a translated, answered or replaced cleanup never becomes the paragraph or the view (`prompts.md` §Cleanup) |

`MaxTitleRunes` (200) is the one bound that is not a rule's: every title,
dictated, typed or stored, is cut at it (`SanitizeTitle`,
`handler.MaxTitleRunes`).

## Before routing: the transcription gates

Rules 13 and 14 run in the transcribe stage (`pipeline/transcribe.go`,
`transcriptOutcome`), and a capture they end never reaches the router. A
capture one of them ends is `no_content` with `gate` on its row and on the
wire (`quiet`, `no_speech` or `hint_echo`; null for the instruction-only
`no_content` the clean stage files), which is what lets the Filing tray and a
note's Recordings tab say "Nothing heard" rather than "Nothing to save"
(capture-ux.md). The transcript, when there is one, stays beside the audio,
and Transcribe again on the Recordings tab lifts the gate: the row's
`skip_gates` makes the next run send the audio whatever its peak and file the
words whatever their scores, refusing only the prompt read back and a
transcript with no letter or digit in it (`service.RetranscribeCapture`).

**Quiet.** The app measures the recording's loudest moment while recording
(`peaks.ts` `PeakCollector.max`, the RMS of one analyser frame in 0..1) and
sends it as `peak` on `POST /v1/captures`. A capture whose peak is known and
under `routing.QuietPeakRMS` (0.04) is filed as `no_content` with gate
`quiet` before any provider call: the floor is the recorder's own
`PEAK_FLOOR`, under which the live canvas never rose, so the person watched
the bars stay flat while recording. An idle or muted microphone sits around
0.01; someone speaking softly passes 0.05. A peak of zero — a device's inbox
request, an app without the field — means nothing was measured and the gate
does not apply. Measured as a file (ffmpeg's `astats` RMS peak is the same
quantity), the probe's text-to-speech voice is 0.25 at full level, 0.02 at
−20 dB and 0.007 at −30 dB; pink noise is 0.05, a fan 0.08, room tone 0.007.
So the floor, applied to a file, lets pink noise and the fan through and
refuses a soft voice Whisper transcribes word for word at −0.09. A file's
gain is not a phone microphone's automatic gain, which lifts a soft voice,
so the floor is judged from `peak` on the `transcribed capture` lines, not
from files.

**Spelling hints.** Whisper takes a prompt that biases spelling
(`prompts.md`, "Whisper's spelling prompt"). `spellingHints` supplies the
destination note's title and aliases for a capture recorded into a note, else
the titles and aliases of the `routing.MaxHintNotes` (50) most recently
touched notes, likeliest first. A recording of `routing.MinHintAudioMS`
(1,500 ms) or less, or of unknown length, gets none: Whisper can answer
near-silence by reading its prompt back, and a clip that short is mostly the
ring's start and stop. A store fault reading the notes is logged and the
recording is transcribed without hints.

**Hint echo.** A transcript whose every comma- or stop-separated piece is a
whole hint name, or which with two pieces or more is a run of the prompt's
words, is the prompt read back, not speech (`echoesHints`, in
`NormalizeSpeech` form). The capture ends `no_content` with the transcript
kept and nothing else of it — no segments, no excerpt. One piece that is not
a whole name is speech, so "Roof" said alone beside a note "Roof repair" is
kept.

**No speech.** `provider.Transcription.NoSpeech` ends a capture as
`no_content`, transcript and segments kept, when the transcript has no letter
or digit; when every sentence of it is a stock silence phrase — "Thank you.",
"Thanks for watching!", "you", "bye", a subtitle credit
(`silenceHallucinations`, compared by words) — whatever the scores, because
Whisper answers digital silence with "Thank you." at `no_speech_prob` 0; or
when the model was unsure of every word of every segment, `avg_logprob` at
or under `routing.LogprobThreshold` (−1.0), Whisper's own bound for a
doubtful segment. Only that one of Whisper's two silence scores is read:
Groq's `whisper-large-v3-turbo` reports `no_speech_prob` as 0 on every
segment, so a gate that needed it could never fire. The numbers behind the
bound, from the production worker's `transcribed capture` lines, which have
carried scores since the line gained them: 45 of 45 scored captures carried
`no_speech_prob_min` 0. Of the 45, thirty were the QA battery's identical
English clips, eleven were real English dictations with `avg_logprob_max`
between −0.08 and −0.604, one was a stock silence phrase at −0.88 that the
phrase list caught, and three were recordings of room noise that became
notes, at −1.35 (thirteen words in 1.6 s), −2.08 (one word, "Portuguese")
and −2.56 (two words in 5.4 s). The bound is measured on English production captures only: no Malayalam,
Tamil or Hindi capture has been scored, and that is why Transcribe again
lifts the gate rather than argues with it. "Thank you, Anu.", "Thank you. Buy
milk." and a quiet but confidently heard "Buy milk" are speech.
`TestLiveNoiseProbe` (`provider/live_stt_probe_test.go`) is how the gate is
measured against the real provider: it transcribes every clip in
`STT_PROBE_DIR` and prints each segment's three scores and text. The clip set
is generated — pink noise, room tone and a fan; a text-to-speech English
sentence at full level, −20 dB and −30 dB; three espeak-ng Malayalam
sentences, one of them also at −30 dB; two romanised-Malayalam sentences
read by the English voice — and `docs/history/prompt-evals.md` holds the
table. What it measures: generated noise never reaches the bound — pink
noise and room tone come back as " ." at −0.90 and −0.51 and the fan as
"Thank you." at −0.68, so the no-letter rule and the phrase list end them,
not the score; the English sentence sits at −0.08 to −0.09 at every level,
0.9 above the bound; Whisper never hears the synthetic Malayalam or the
romanised sentences as Malayalam (it labels them English or Italian and
answers English words or letter salad), and they sit at −0.34 to −0.81 at
full level, while one romanised clip (−1.62) and the −30 dB Malayalam clip
(−1.83) fall under the bound and are refused. `compression_ratio` separates
nothing: 0.9 to 1.05 on speech and on noise heard as words alike, under 0.6
only where the other two rules already fire. A synthetic voice Whisper
cannot place is not a Malayalam speaker, so the bound stays where the
production numbers put it until real Malayalam recordings on the test tenant
are scored; `avg_logprob_max` and `peak` on their `transcribed capture`
lines are that measurement.

The `transcribed capture` log line carries `gate` — `quiet`, `hint_echo`,
`no_speech` or empty when the transcript goes on — `peak` when the app
measured one, and, when there are segments, the least silent segment's
`no_speech_prob_min` and `avg_logprob_max` and the most repetitive one's
`compression_ratio_max`, so a silent capture that was filed can be judged
from the log and the next tuning has its numbers.

**The short-dictation tidy** (`pipeline/clean.go`). After routing, a
dictation of fewer than `routing.ShortDictationWords` (12) words makes no
cleanup call: `tidyDictation` collapses the whitespace, capitalises the first
word when it is an ordinary lowercase word (never "3 eggs", "iPhone" or a
URL) and adds a full stop when no sentence mark ends it, and that is stored
as the clean text. Most dictations are a sentence; the model call they would
make returns a capital letter and a full stop after a wait of seconds. The
cost is accepted: a misheard word in a dictation that short stays as Whisper
heard it. A script written without spaces (Han, kana, Thai, Lao, Khmer,
Myanmar) is never counted short (`isShortDictation`). The tidy's one trace is
the INFO line `short dictation tidied without a cleanup call` with the word
count. A verbatim note bypasses cleanup altogether
(`CaptureCleanupBypassed`), and a checklist never reaches it: its items come
from the extraction (`checklists.md`).

## The decision line

`route` logs one INFO line `routing decided` per routed capture
(`logRoutingDecision`), written once the branch is final so a deduped capture
reads as deduped, counts and enumerations only, so a week of routes can be
judged from the log alone: `capture_id` (an opaque id, as on every other line
of `route`), `action` (append|new), `confidence`, `matched_by`
(model|title|alias|tag|prefix_title|prefix_transcript|spoken_name|none —
`model` when the model's own append stood, `none` for a new note nothing
matched), `outcome` (append|needs_target|new|deduped|new_after_missing),
`candidates`, `transcript_words`, `title_words` (0 for an append), `spans` as
the reply carried them, `removed_words`, `checklist` (the reply's kind) and
`source` (app|device, never the device id). No title and no transcript word
is on the line, so the README's "nothing derived from speech reaches a log"
holds; the correlation id rides the context. A route the router did not
answer has no decision line: the `routing failed` warning is its line.

**Metrics.** `RouterSpansDiscarded{Reason=missing_field|malformed|too_long|empty_content|not_derived}`;
`RouterTitleMatchedExistingNote{MatchedBy}`, counted once for any rescue in
`preferExistingTitle`, with a rollup: `MatchedBy` is the decision line's
`matched_by`, so `title`, `alias` and `tag` are the prompt's own misses (the
model said "new" for a listed name) and `prefix_title`, `prefix_transcript`
and `spoken_name` the rules the code adds on top; the dimensionless total is
the rollup, and the pre-create dedupe is not counted; `RouterNewNoteKind{Kind=note|checklist}`;
`RouterRetried{Reason}`; `RouterTimedOut{Attempt}`. The gates have no
counters: the `gate` attribute and the tidy's INFO line are their trace.

## Replay: testing the rules without a key

The live eval (`prompts.md`, "Changing a prompt") tests the model's reply;
the worker's outcome is that reply through `provider.Route` and `decide`. The
two are tested together without a key by recording and replaying
completions, in test binaries only:

- `LLM_RECORD=<dir>` makes `OpenAICleanup.complete` also write every reply as
  `<dir>/<RecordingKey>.json` — the raw reply and its token usage, keyed by
  the SHA-256 of the model, the system prompt and the user prompt
  (`provider.RecordingKey`). Any change to a prompt's wording, the model, a
  fixture's transcript or the candidate list is a new key, so a stale
  recording is a miss, never a silent pass.
- `LLM_REPLAY=<dir>` serves completions from those files and calls nothing;
  a miss fails with `errNoRecording`, whose text is the re-record command.
- Both variables are read only when `testing.Testing()` is true
  (`recordReplayAllowed`): a worker with either set still calls the model
  (`TestANonTestBuildIgnoresRecordAndReplay`).

`pipeline.TestRoutingEvalReplay` (`pipeline/route_eval_test.go`) replays
every route case of `provider/testdata/eval/fixtures.json` through `Route`
and `decide` and asserts the case's expectations on the outcome — `append`
means filed without asking, `title_names` means the named note is where the
recording went. `provider.TestEvalReplay` (`live_eval_test.go`) replays the
cleanup, items, tasks and ask cases with the live eval's own checks, the
tasks cases through `cleanup.SplitOutput` as the worker runs them. Both read
`provider/testdata/eval/recordings/`, one file per prompt, and skip while it
holds no file. It is filled on the VM by `scripts/dev/record-replay.sh`: the
instance's key from SSM into the environment of three `go test` runs, never
printed; per prompt the reply a majority of the runs gave is kept, so a
recording is the model's usual answer and not a one-off; the directory is
then copied back and committed, with the runs' logs beside it
(`testdata/eval/record-logs/`). Two fixture keys say what the model does
with a case rather than what the case wants: `flaky: true` on one the live
eval sees answered differently across runs (set from the script's split
report), and `known_failure: "<reason>"` on one it answers the same wrong
way every run (cleared only when a prompt change fixes it). The replay logs
such a case's outcome and skips its expectation; the live eval asserts both
as it does every other case.

Record with the default `LLM_MODEL`, which the replay also uses. The
hand-written recordings in the tree are not eval data — each is a second
copy of a recording's shape, kept beside its test:
`pipeline/testdata/replay/synthetic-route.json`, which
`TestRouteReplayRunsTheRecordedReplyThroughDecide` files under the key of
the current prompt to prove the path end to end (a "new Roof repair" reply
turned by `decide` into an append to the listed Roof repair);
`synthetic-tasks.json` for `restoreLevels`; and the `injection-*.json`
replies, in which the model did what an injected text asked, which
`TestInjectionReplayLeavesTheOutcomeUnchanged` runs through the same rules,
one of them documenting an outcome that stands rather than one wanted
(`prompt-safety.md`). Once a set is recorded, a change to a rule needs no
re-record — that is what the replay measures — and a change to a prompt's
text, the model or a fixture is a miss, so it is re-recorded in the same
change (`prompts.md`, "Changing a prompt"). A new rescue rule or bound ships
with a fixture case that exercises it and that case's recording
(`bounds.go`).

Tests: `routing/prompt_test.go` (the wording and the fence),
`routing/spans_test.go` (`RemoveSpans`, `ExtendSpans`, the cues),
`routing/bounds_test.go` (the table), `provider/openai_router_test.go` (the
parser and `routedContent`), `pipeline/routing_test.go` (the rescues and
`decide`), `route_create_once_test.go`, `route_kind_test.go`,
`route_retry_test.go`, `route_fault_test.go`, `targeted_instruction_test.go`,
`no_speech_test.go`, `spelling_hints_test.go`, `clean_short_test.go`,
`transcribe_outcome_test.go`.

History: the rules' origins, the prompt rewrites and the production
batteries are in `docs/backlog.md` (R6-RT-*, R7-9, R7-10*, PR9-7) and
`docs/history/prompt-evals.md`.
