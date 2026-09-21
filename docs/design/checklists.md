# Checklist notes

A note can be a checklist: recordings filed into it become items, items are
ticked, crossed items sink, and all checklists are one filter away. This note
is the backend half — what is stored, what the worker writes, how the cleaned
view differs — and why the body stays the single source of truth. Code:
`model.NoteIndex.Kind` (`backend/internal/model/types.go`), `checklistItem`
in `Pipeline.append` (`backend/internal/pipeline/pipeline.go`),
`service.CheckCleanMode` / `EffectiveCleanMode`
(`backend/internal/service/note_clean.go`), the `tasks` prompt and
`NoteOutput` (`backend/internal/cleanup/prompt.go`).

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

When the destination note is a checklist, the worker appends the cleaned
transcript as one open item: `- [ ] ` and the text with line breaks and
whitespace runs collapsed to single spaces, trimmed, cut at 2,000 runes. One
recording is one item however long it ran; splitting it into several tasks is
a judgement and belongs to the cleaned view, because the append must stay a
plain record of what was said.

The capture marker keeps its place on the line before the item
(`<marker>\n- [ ] text`, after `\n\n` when the body has content), so
`CutCaptureParagraph` still finds exactly this recording's line. Deleting or
moving the recording removes its item and nothing else — a typed item with no
marker and an item the person has since ticked are untouched — and a moved
item lands in the target in chronological position, still one line, still
ticked if it was. Snippet and search text see the raw lines.

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
item per action, the person's words, done items verbatim and in place, order
otherwise kept, nothing invented or merged — and for task-list lines as the
whole answer. The answer is held to that: every non-blank line must match
`^- \[( |x)\] \S`, blank lines are dropped, at most 500 items. Anything else
is the fixed verdict `the cleanup model returned nothing usable` and the
previous view is kept, because a checklist view in prose is no view. Stored in
`cleaned_body` as today; `stale` and `auto_clean` are unchanged.

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
