# Checklist notes

A note can be a checklist: recordings filed into it become items, items are
ticked, crossed items sink, and all checklists are one filter away. This note
is the backend half — what is stored, what the worker writes, how the cleaned
view differs — and why the body stays the single source of truth. Code:
`model.NoteIndex.Kind` (`backend/internal/model/types.go`),
`Pipeline.extractItems` (`backend/internal/pipeline/clean.go`), `checklistItems` in
`Pipeline.append` (`backend/internal/pipeline/append.go`), the items prompt and `ParseItems`
(`backend/internal/cleanup/items.go`), `service.CheckCleanMode` /
`EffectiveCleanMode` (`backend/internal/service/note_clean.go`), the `tasks`
prompt and `NoteOutput` (`backend/internal/cleanup/prompt.go`).

## Data model

`NoteIndex.Kind` is `""` for a plain note or `"checklist"`. It is a promoted
DynamoDB attribute like `language`, written only when set, so a row from
before 2026-09-21 carries no attribute and needs no backfill;
`NoteItemAttributes` includes it, so `chintanctl reconcile` re-promotes it
with the rest. The wire maps `""` to `"note"` and `kind` is required on every
`Note` and `NoteDetail`, the search-corpus rows included. `GET
/v1/notes?kind=checklist` filters before the page is cut, exactly as `?tag=`
does, so a page holds up to `limit` matches and the cursor is set only when
more matches exist.

A checklist body is GitHub task-list syntax, one item per line — `- [ ] text`
open, `- [x] text` done — and nothing else. Blank lines are ignored by every
reader. The server never converts a body: switching `kind` is a PATCH that
carries the converted body from the client, and a body the client did not
send is left as it is.

## The append rule

When the destination note is a checklist, the recording becomes the items it
named, one open line each — `- [ ] Chickpeas`, `- [ ] Green gram` — and
nothing else. The items come from one model call in place of the transcript
cleanup (`Pipeline.extractItems`, `cleanup.ItemsPrompt`): the prompt reads
the **raw** transcript with the list's title beside it and answers
`{"items":[…]}` — short noun phrases in the speaker's words, language and
script, quantities kept, the words addressed to the app left out ("add",
"into it", "to my list", the list's own name), "X and Y" split into two. It
runs in the `cleaning` status, under the cleanup op and deadline, and stores
the items one per line at `clean_key`, so a retry does not call again.

Until 2026-09-26 the item was the cleaned transcript on one line, and which
words those were depended on the router's span removal, which knows filing
and naming instructions and nothing about items: "Add umbrella to shopping
list" became the item `list` and "create a shopping list and add chickpeas
and green gram into it" became `Add chickpeas and green gram into it.`. The
router still decides the destination for a routed recording; its spans, and
the targeted path's instruction strip, are not applied to a checklist's
content, because the extraction handles those words itself and the item is
no longer at the mercy of where a span ended.

Three outcomes besides items:

- **No items** (`{"items":[]}`): the recording only told the app what to do
  — "create a shopping list". The capture is `no_content`, as an
  instruction-only recording is for a plain note; the note exists and gets
  no item.
- **Not a list** (no JSON object, no `items` array, an empty completion, more
  than 100 items): the recording is appended as one item with its line
  breaks collapsed, the pre-2026-09-26 behaviour, and
  `ChecklistItemsDiscarded{Reason=unusable}` counts it. Dictation is never
  lost to a bad reply.
- **Verbatim checklist**: no model call; the raw transcript — the recording
  as spoken, on the routed path as on the targeted one, never the router's
  span-cut text — is one item with its line breaks collapsed.

The prompt is the only guard on what an item is. `ParseItems` checks the
shape (a JSON object with an `items` array, at most 100, each at most 2,000
runes) and nothing about the words: a subsequence check against the
transcript would refuse the STT-garbling fix the prompt asks for, and a rule
dropping an item equal to the title would silently lose "add batteries" to a
list titled Batteries. An item the prompt should not have produced is visible
in the list and one tap from gone; a dropped one is lost. So an invented item
is caught only by the live evaluation (`provider.TestLiveChecklistItems`,
run against the real model before a prompt change ships) —
`ChecklistItemsExtracted{Outcome=items}` counts recordings, not whether their
items were spoken.

A request that is not an add — "remove milk from the list", "tick off the
eggs", "actually make that two umbrellas" — is returned by the prompt as one
open item exactly as spoken: honest and visible, never silently dropped and
never turned into an add of the thing named. Applying such a request to the
list is future work; its shape would be `{"add":[…],"remove":[…],"done":[…]}`
applied as a body edit under the recording's marker, and it is not built.

The capture marker keeps its place on the line before the first item
(`<marker>\n- [ ] A\n- [ ] B`, after `\n\n` when the body has content). A
paragraph runs to the next marker, so `CutCaptureParagraph` finds exactly
this recording's items: deleting or moving the recording removes them and
nothing else — a typed item with no marker and an item the person has since
ticked are untouched — and a moved recording lands in the target in
chronological position, its items still ticked if they were. That holds
until the first save from the Items tab: `serialiseChecklist` writes the
items with no blank line between recordings, so `CarryCaptureMarkers` finds
no paragraph boundary to keep a marker at and carries every marker to the
end, after which delete and move cut nothing (a tick is such a save). Not a
checklist regression — a plain note's marker moves the same way once its
paragraph is edited — but a checklist is edited far more often than it is
dictated into, so in practice the per-recording delete works for a list that
has only been spoken to. Transcribing a
recording again replaces its items where they stand and carries the ticks
line for line (`keepTick`); when the new transcription yields a different
number of items no tick is carried, because no line can be said to be the
one that was ticked. Each item is cut at 2,000 runes. Snippet and search text
see the raw lines.

## The `tasks` clean mode

A checklist cleans in `tasks` and in nothing else. `EffectiveCleanMode`
answers `tasks` for every checklist, whatever preference the row held before
it became one, so auto-clean and an unspecified `POST …/clean` run it without
being told. `CheckCleanMode` is the one rule for `PATCH cleaned_mode` and
`POST …/clean {mode}`: a checklist takes anything but `tasks` as 400
`cleaned_mode must be tasks for a checklist`; a plain note takes `tasks` as
400 `cleaned_mode must be polished or structured`. The check runs against the
kind as the PATCH leaves it, so `{kind: checklist, cleaned_mode: tasks}` is one
request. A `tasks` preference left on a note switched back to plain is ignored,
not run, and comes back into force if the note becomes a checklist again.

The prompt asks for the items rewritten as granular, actionable tasks — one
item per action, an item that is already one thing left exactly as written
with no verb added, the person's words, done items verbatim and in place,
order otherwise kept, nothing invented or merged — and for task-list lines as
the whole answer. The answer is held to that, and two of the promises are
checked against the body rather than trusted, because adopting the view (the
first tick in Split up, or Use this list) writes it over the body:

- every non-blank line must match `^- \[( |x)\] \S`, blank lines are
  dropped, at most 500 items — else the fixed verdict `the cleanup model
  returned nothing usable` and the previous view is kept;
- the `- [x]` lines must be the body's `- [x]` lines, verbatim (whitespace
  runs aside) and in order — else the same verdict, because a view that lost
  or invented a tick is worse than the view it would replace;
- an open item whose words are not the body's words, in order
  (`llm.VerifySubsequence`), is dropped and `TasksItemsDropped` counts it;
  the rest of the answer is stored. This is what catches `- [x] Make a list.`
  — the model inventing an antecedent for "it" — while keeping the two
  splits beside it. A reply with nothing left is `nothing usable`.

Stored in `cleaned_body` as today; `stale` and `auto_clean` are unchanged.
With items now extracted per recording, the split has less to do; whether
Split up still earns its place is an open question for the owner
(`docs/backlog.md` F2, C7).

## Why the body stays the single source of truth

Every reader — the row's "3 of 7 done", the Items tab, the offline corpus,
Ask, the export — derives from the body, and every writer writes the body:
the worker's append, the editor's save, delete and move, the client's
conversion between kinds. Ticking an item is a body edit that flips `[ ]` to
`[x]` in place, so the recording's marker stays attached to its item and the
delete/move rule keeps working after any number of ticks.

The alternative — items as rows, or a done-set beside the body — would put the
state of a task in two places and make every existing invariant conditional:
the exactly-once append guard reads the body; the autosave protocol
(`append-vs-autosave.md`) conditions on the body's ETag; the cleaned view is
regenerated from the body and is never written by the API. A checklist that is
a body with a line format inherits all of that for the cost of a parser on
each side, and `chintanctl export` needs no change because a task list is
Markdown.
