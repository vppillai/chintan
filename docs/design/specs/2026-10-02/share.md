# Round 12 spec: the Share drawer (F3, with F5's copy format and F7's drag to close)

Slug `ux`. Code references are to `main` as it stood on 2 October 2026
before the `r12/ux` pull request; the "Shipped" section names what landed.

Evidence is in `shots-share/`: `before-{note,checklist}-{ink,nocturne}.png`
are the drawer as found at 360×780 (Pixel 7 descriptor, the e2e stub's
notes), `after-*.png` the same four screens after the change — Linux
Chromium has no share sheet, so Copy note leads there — and
`after-share-capable-*.png` the layout with `navigator.share` present
(stubbed for the shot), where **Share…** leads.

## Current behaviour

`NoteDrawer.tsx` renders Share as `.note-copy`, a wrapping row of equal
pills (`screen__action`): **Copy note**, **Download note**, and when a
cleaned view exists **Copy cleaned view**, **Download cleaned view**.
`CopyButton` writes `text/plain` only; there is no `navigator.share`
anywhere in the app. The text for a checklist is `draft.body` as stored —
`- [ ] Milk` lines.

## What a newcomer meets on a 360 px phone

1. **No primary action.** Two identical pills; nothing says which is the
   thing to tap. With a cleaned view there are four, and two of them are
   called Copy.
2. **Sharing to WhatsApp is four steps and a leave.** ⋮ → Share → Copy
   note → switch app → find the chat → paste. The phone has a share sheet
   for exactly this and the app never offers it.
3. **A checklist pastes as markup.** `- [ ] Milk` in a message, and
   `- [x] Eggs` for the one that is done. The owner's words were "rich text,
   WhatsApp and messaging app friendliness".
4. **Nothing says what is copied.** Title and body? Body only? The download
   is a `.md` file, which the label does not say.
5. What is fine: every control is 44 px, the head's × is 44 px and never
   scrolls away, the sheet is a dialog with Escape and a focus trap, and
   both themes read at AA (the a11y sweep has scanned this sheet since
   round 7).

## Options

| | A. Rank the existing controls | B. System share sheet first (recommended) | C. A share menu per target |
|---|---|---|---|
| Shape | Keep the pills; fill Copy note as the primary; group the cleaned pair under a caption | A full-width **Share…** through `navigator.share` where the browser has it, else Copy note filled as the primary; the quieter row beneath; the cleaned pair under a caption | A row of targets — WhatsApp, Mail, Messages — each a deep link |
| Taps to a chat | 5 (unchanged) | 3: ⋮ → Share → Share… then the sheet | 3, but only for the targets listed |
| Rich checklist | needs the clipboard change either way | the clipboard carries text and HTML; the sheet carries text | each target's URL scheme takes plain text only |
| Cost | CSS and one caption | one small component, a feature check, the clipboard change | a URL scheme per app, none of them documented, and a row that is wrong on a desktop |

B. C is not recommended: `whatsapp://` and friends are undocumented,
fail silently on a desktop, and would put brand names in a product that
otherwise has none.

## Recommendation: B

```
 SHARE                                        ×      ← the head is also the handle (F7)
 ┌──────────────────────────────────────────────┐
 │                   Share…                     │    ← only where navigator.share exists; ink fill
 └──────────────────────────────────────────────┘
 ( Copy note )  ( Download note )                     ← the quieter row; Copy note takes the primary's
                                                        place and the first line where there is no sheet
 The title, then the text. The download is Markdown.  ← the hint; a checklist's says ☐ / ☑ and rich text
 ─────────────────────────────────────────────────
 CLEANED VIEW                                         ← only when the worker has written one
 ( Copy cleaned view )  ( Download cleaned view )
```

Interaction details:

- **Share…** calls `navigator.share({ title, text })` with the note's title
  and the same text Copy note would write. A dismissed sheet rejects with
  `AbortError` and the button says nothing. Any other refusal falls back to
  the clipboard and says "The share sheet would not open, so the note is
  copied instead."; a failed copy says the existing fixed sentence.
- **The text** is the title, a blank line, then the body. A checklist is
  `☐ item` / `☑ item`, two spaces of indent per level, items in the list's
  own order (done ones where they stand, so a shared list reads as the
  list on screen). The symbols rather than `[ ]`/`[x]`: every target in
  reach — WhatsApp, Messages, Mail, Notes, a document — renders them, and
  the brackets are the markup the owner complained of. Beside the text an
  HTML twin — the title in `<strong>`, nested `<ul>`s, done items in `<s>`
  — goes on the clipboard through one `ClipboardItem` where the browser
  allows (Chromium, Safari), the text alone where it does not (Firefox
  without the flag), so a rich-text target pastes a list.
- **Copy note** stays, as the way out on a desktop and the fallback on a
  phone. **Download note** gives Markdown as stored, named after the title.
- **Cleaned view** keeps its pair under its own caption, below a hairline:
  one group per text, so nothing is called Copy twice.
- **The hint** under the row names what the text is, in one sentence, and
  for a checklist says how it pastes.
- **Drag to close (F7).** The head is the handle: a finger or pen dragging
  it down past 30 % of the sheet's height, or flicking it at 0.4 px/ms
  (`SWIPE_COMMIT_FRACTION`, `SWIPE_FLICK_PX_PER_MS`, the tab swipe's
  numbers, moved to `hooks/gesture.ts` so one file holds every gesture
  constant), slides the sheet off and closes it; short of that it settles
  back. The × and Escape stay; focus returns to the ⋮ by every route;
  reduced motion makes the settle one frame through the motion tokens.

Owner decision: none needed; every call above is the recommended default.

## Shipped

`frontend/src/components/ShareButton.tsx` (new), `CopyButton.tsx`
(`ClipboardPayload`, `copyText` writing both flavours),
`features/notes/checklistClipboard.ts` (the checklists stream's
serializer, through `CopyButton`'s `html` prop),
`features/notes/NoteDrawer.tsx` (`ShareBody`, the head's drag),
`hooks/gesture.ts` (the shared constants), `styles/shell.css`
(`.note-share*`), `styles/notes.css` (the head as a handle, the sheet's
settle). Tests: `ShareButton.test.tsx`, `CopyButton.test.tsx` (the rich
write), `checklistClipboard.test.ts`, `NoteDetailScreen.test.tsx` (the
drag), `e2e/drawer.spec.ts` (a real touch through CDP), `e2e/a11y.spec.ts`
(the Share sheet of a checklist and of a cleaned note, both themes).
Docs: `docs/design/note-screen.md`, "The drawer".
