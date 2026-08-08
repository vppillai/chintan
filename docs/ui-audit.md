# Chintan v2 frontend — layout and alignment audit

**Date** 2026-08-08 · **Commit audited** `8913ec9` · **Scope** `frontend/`

The owner reported visible defects in the running app — "bad UI like partially
overlapping". This is what was found, how it was found, what was changed, and
what was deliberately left alone.

Nothing here was reasoned out of the stylesheet. The app was built, served from
`vite preview`, driven with Playwright across 11 viewports × 2 themes × 13
states (286 renders), and both measured and looked at. The measurements are in
`frontend/e2e/layout.spec.ts`, which now runs on every `bun run e2e`; the
pictures cited below are in `frontend/e2e/__screenshots__/`.

## Coverage

| Axis | Values |
| --- | --- |
| Viewports | 320×568, 375×667, 390×844, 393×873, **844×390 (landscape phone)**, 768×1024, 1024×768, 1280×800, 1440×900, 1920×1080, 2560×1080 |
| Themes | Ink & Paper, Nocturne |
| States | home (collapsed), notes (expanded), note detail, search with results, search empty, settings, capture (live mic), progress card, resume prompt, confirm dialog, update prompt, and a long-content stress variant of the note screens (116-char title, 56-char untruncated tag, 20 tags, ~5 000-word body, 40-minute transcript) |
| Checks | document and element horizontal overflow, container sideways scroll, chrome-vs-content overlap, chrome inside the viewport, touch-target size, focus-ring clipping, safe-area handling |

## Summary

Four defects were real breakage — one of them is the overlap the owner saw.
Five more were alignment or reachability problems that would read as "sloppy"
rather than "broken". Two things are ugly and were left alone on purpose, and
are named at the end.

Every fix is in the design system — the shell grid, the tokens, one layout
primitive per defect. No screen was patched individually.

---

## D1 — The bottom bar and library strip were pushed off the bottom of the screen

**Severity** critical · **Both themes** · **80 + 10 measured occurrences**

**Where** every screen whose content exceeds the viewport. Settings on every
phone size; note detail; the notes list once a progress card appears at 320×568;
every long-content stress case; and the *home* screen at 844×390, where the
navigation strip was simply cut in half by the bottom edge.

**Screenshots**
`e2e/__screenshots__/before/settings__390x844__ink.png` — the settings list runs
off the bottom, no bar, no record button.
`e2e/__screenshots__/before/notes-progress__320x568__ink.png` — the failed
progress card is sliced through mid-sentence and the bar is 146px below the fold.
`e2e/__screenshots__/before/home__844x390__ink.png` — "Notes / Search / You"
clipped by the viewport edge.
The matching `after/` images show all three fixed.

**Offending code** `frontend/src/styles/shell.css:11` (original), `.app`:

```css
.app {
  display: grid;
  grid-template-rows: auto 1fr auto;
  min-height: 100dvh;   /* ← the whole defect */
}
```

**Cause.** `min-height` does not give a grid container a *definite* block size,
so the `1fr` track has no free space to be a fraction of and sizes itself to its
content instead. `.app__main`'s `overflow-y: auto` therefore never engaged: the
middle row grew, the document scrolled, and the bottom bar — which is a grid row
in normal flow by design, not a fixed element — was carried off the bottom with
nothing able to bring it back. Measured at 390×844 on `/settings`: `.app` was
1110.5px tall in an 844px viewport, and the bar's top edge sat at y=1046.

**Fix** `frontend/src/styles/shell.css:29`. `block-size: 100dvh` (with a `100vh`
fallback) makes the track definite, and `grid-template-rows: auto minmax(0, 1fr)`
lets the middle row shrink. `.app__main` is now the only scroller, as intended.
`dvh` rather than `vh` so a mobile URL bar or an on-screen keyboard resizes the
shell instead of stranding it.

**Consequence handled.** With the shell pinned to one viewport, an unbounded
stack of progress cards could squeeze `.app__main` to nothing. `.progress-stack`
(`shell.css:741`) is now capped at `38dvh` and scrolls.

---

## D2 — The live waveform drew straight through the capture controls

**Severity** critical · **Both themes** · **every viewport**

This is the literal "partially overlapping".

**Screenshot** `e2e/__screenshots__/before/capture__844x390__ink.png` — the
orange waveform bars hang below the beige panel and cross the Cancel / Pause /
Stop row. `after/` shows the bars contained and the panel full width.

**Offending code** `frontend/src/styles/shell.css:574` (original):

```css
.waveform { inline-size: 100%; block-size: 100%; }
```

inside `.capture__waveform { display: grid; place-items: center; block-size: 6rem; }`.

**Cause.** `block-size: 100%` on a replaced element (`<canvas>`) in a
`place-items: center` grid area does not resolve in Chrome, so the canvas fell
back to the intrinsic 300×150 of a bare `<canvas>`. Measured at 844×390: a 150px
canvas inside a 96px panel, centred, hanging 66px below it — and because
`Waveform.tsx` sizes its backing store from `clientHeight`, the bars were painted
to fill all 150px. Overlap with `.capture__controls`: **42px**.

**Fix** `frontend/src/styles/shell.css:608` and `:682`. The panel is now a single
explicit grid area (`grid-template: minmax(0, 1fr) / minmax(0, 1fr)`) with
`overflow: hidden` as a backstop, and the canvas is `place-self: stretch` with
`inline-size`/`block-size: auto` — stretch only applies to auto sizes, which is
why both had to be reset rather than tweaked. Canvas is now 72×(panel width),
exactly its area, at every viewport.

---

## D3 — The update prompt covered the record button

**Severity** critical · **Both themes**

**Offending code** `frontend/src/styles/shell.css:1237` (original):

```css
.update-prompt {
  position: fixed;
  inset-block-end: calc(var(--space-4) + env(safe-area-inset-bottom, 0px));
  z-index: var(--z-toast);   /* 50, above --z-bottom-bar: 30 */
}
```

**Cause.** The only floating element in an app whose stated design principle
(the comment above `.bottom-bar`) is "the bar is a grid row in normal flow, NOT
a floating action button… content above it can therefore never be overlaid".
Measured at 390×844: **68px** over the bottom bar and **55px** over the record
button, on every screen, for as long as a service-worker update was pending.

**Fix** `frontend/src/components/AppShell.tsx` — `<UpdatePrompt />` moved above
the strip/bar in the shell's child order — and `frontend/src/styles/shell.css:1384`,
which drops `position: fixed` entirely. It is now a shell row like the progress
card and cannot overlay anything.

---

## D4 — The confirm dialog was unreachable on a short viewport

**Severity** major · **Both themes** · **320×300 and below**

**Offending code** `frontend/src/styles/shell.css:1267` (original) —
`.dialog-layer` centred its child with no `overflow`.

**Cause.** A centred item taller than its container overflows in both
directions, and with no scrolling on the layer neither end is reachable.
Measured at 320×300 with a realistic body: the destructive **"Discard
recording"** button sat 56px below the fold with nothing to scroll. A landscape
phone with the keyboard open is roughly this size, and this is the app's only
destructive confirmation.

**Fix** `frontend/src/styles/shell.css:1427`: `place-items: safe center` (falls
back to `start` the moment the content stops fitting) plus `overflow-y: auto`.
`.dialog-scrim` changed from `absolute` to `fixed` (`:1444`) so it keeps covering
the page while the layer scrolls.

---

## D5 — Safe areas were ignored by everything `position: fixed`

**Severity** major (unverifiable in a desktop browser, correct by inspection)

`.app` consumed `env(safe-area-inset-*)`, but `.dialog-layer` is `position: fixed`
and escapes `.app`'s padding entirely — on a notched phone in landscape the
dialog title would sit under the notch. `.update-prompt` handled only the bottom
inset.

**Fix.** Four tokens in `frontend/src/styles/tokens.css:112` (`--safe-top`,
`--safe-right`, `--safe-bottom`, `--safe-left`) wrapping the `env()` calls, and
every consumer reads a token. `env()` now appears in exactly one file. Two things
follow: an overlay honours a notch by construction rather than by the author
remembering, and a notch becomes *simulable* — overriding four custom properties
is something a test can do, and `env()` is not.

---

## D6 — Shell cards did not share the screens' content column

**Severity** major (cosmetic, but badly so at width)

**Screenshot** `e2e/__screenshots__/before/notes-progress__2560x1080__ink.png` —
the note list is a 672px centred column while the progress cards span the full
2 530px, parking **Retry** roughly 1 400px from the sentence it belongs to. The
bottom bar's tabs were flung to the far corners for the same reason.

**Cause.** `.screen` was capped at `--layout-content-max` and centred;
`.progress-stack`, `.resume-prompt`, `.update-prompt`, `.bottom-bar` and
`.strip__list` were not. Gutters also disagreed: 20px on screens, 16px on the
progress stack and resume prompt.

**Fix.** `--layout-gutter` and `--layout-card-max` tokens
(`tokens.css:122`, `:125`); every screen and every shell-level card now reads
them (`shell.css:332`, `:741`, `:1319`, `:1384`). The bar and strip keep a
full-bleed background but place their controls in the same column via
`padding-inline: max(<gutter>, (100% - var(--layout-content-max)) / 2)`
(`shell.css:234`, `:273`).

---

## D7 — Touch targets below the product's own 44px floor

**Severity** major · **all viewports, worst at 320×568**

The app defines `--layout-touch-target-min: 2.75rem` (44px) and most controls
honour it. Four did not:

| Control | Measured | File |
| --- | --- | --- |
| `.transcript__line` — every seek target in a transcript | 36px tall | `shell.css:884` |
| `.note-title-input` | 34.8px tall | `shell.css:934` |
| `.tag-editor__input` | 38px tall | `shell.css:1063` |
| `.settings-input` | 42px tall | `shell.css:1161` |

All four now carry `min-block-size: var(--layout-touch-target-min)`
(`shell.css:1008`, `:1060`, `:1194`, `:1293`). The transcript line also gained
`align-items: center` so the extra height does not top-align the text against
the timestamp. A regression test asserts the floor across every screen at 320×568.

---

## D8 — The capture screen was 0.53px too wide at 320, and one text step from breaking

**Severity** minor as measured, major as a latent bug

At 320×568 `.capture` measured 320.53px in a 320px viewport. The cause is not
rounding: `.capture__controls` was a non-wrapping flex row, so its min-content
width (87.1 + 81.2 + 80.3 + two 16px gaps = 280.5px) plus 40px of padding became
the grid item's automatic minimum size. At a larger browser text setting the
same rule pushes **Stop** off the screen — which is the version of this bug that
loses a recording.

**Fix** `shell.css:641`: `flex-wrap: wrap`, `justify-content: center`,
`min-inline-size: 0`, and `min-inline-size: 0` on `.capture` itself (`:579`).

---

## D9 — A note's date broke across two lines

**Severity** minor · visible whenever a note has more than a couple of tags

`.note-row__meta` was a non-wrapping flex row, so with 20 tags the `<time>`
shrank and rendered as "Aug" above "6" (see
`e2e/__screenshots__/before/note-detail-stress__768x1024__nocturne.png`'s
sibling list view). The tag list itself then consumed four lines of the row.

**Fix** `shell.css:435`: the row wraps, `.note-row__date` is `flex: none` with
`white-space: nowrap`, and `.note-row__tags` is clamped to one line.
`NoteRow.tsx` gained the two class names.

---

## D10 — Focus rings shaved at the edge of every scroll container

**Severity** minor · keyboard users only

`:focus-visible` draws a 3px ring at a 2px offset. `scrollIntoView` — what the
browser does when Tab lands below the fold — aligns the *border box*, so the
5px of ring outside it is clipped by `.app__main`, `.transcript__list`,
`.progress-stack` and `.dialog-layer`.

**Fix.** `scroll-padding: calc(var(--layout-focus-ring-width) + var(--layout-focus-ring-offset))`
on all four containers (`shell.css:78`, `:741`, `:1427`, and the transcript list).

---

## Verified and *not* broken

Worth recording, because they were the obvious suspects:

- **No horizontal document overflow anywhere.** `documentElement.scrollWidth <=
  clientWidth` held at all 11 viewports in both themes, before and after, on
  every state including the stress cases. The "looks broken on mobile" classic
  is not what was wrong here.
- **The record button is optically centred** in the bottom bar at every width
  (measured centre = viewport centre to the pixel at 390, 844 and 2560) and in
  the home surface. The `1fr auto 1fr` grid does its job.
- **The progress card never overlapped the bottom bar.** Both are grid rows;
  the defect was D1 pushing the bar away, not the card covering it.
- **No element bled past the viewport's inline edges** other than D8's half pixel.
- **The dialog scrim covers the full viewport** in both themes.

## Deliberately not changed

- **A long note title is still cut off.** `.note-title-input` is a single-line
  `<input>`; a 116-character dictated title shows about 18 of them at 390px. This
  is **ugly, not broken** — the field scrolls its full value once focused, and it
  is the right element for the job (label association, `onBlur` autosave, and the
  editor tests all depend on it). Changing it to an auto-growing `<textarea>` is
  a real redesign of the note header and out of scope for a layout pass. It now
  gets `text-overflow: ellipsis` (`shell.css:1060`) so an unfocused long title at
  least says there is more, and the regression spec excludes form controls from
  its sideways-scroll check with that reasoning written down.
- **Twenty tags is still a lot of grey text** on a note row, now clamped to one
  line. Truncating with a "+14 more" affordance would be better; it needs a
  product decision about what the row is for, not a CSS fix.
- **`.bottom-bar__group` uses `justify-content: space-evenly`**, so at 2560px the
  two left tabs sit a little loosely inside the content column. It is within the
  column and symmetric, so it is a taste call rather than a defect.

## Regression tests

`frontend/e2e/layout.spec.ts` — 72 tests, parameterised over the full viewport
list in both themes:

- every route measured for document overflow, element bleed, container sideways
  scroll, chrome-inside-viewport, and chrome-vs-content overlap, with the
  long-content stress fixture applied;
- the same with two pending captures on screen;
- the capture screen's waveform asserted inside its panel and clear of the
  controls, with a live microphone;
- the update prompt asserted clear of the bar and the record button;
- the confirm dialog asserted reachable at 320×390, 320×300 and 320×240;
- every control asserted at ≥44×44 on the narrowest phone.

The overlap probe intersects each element with its clipping ancestors before
comparing, so "overlap" means *covers* rather than *shares coordinates with* —
content scrolled under the bar inside `.app__main` is correctly not a finding.

**Against the audited commit: 54 failed, 18 passed. After the fixes: 72 passed**,
and the full suite is 100 passed (28 pre-existing specs unchanged).

Representative pre-fix failures:

```
Error: note detail @ 390x844 iphone-14/ink: the bottom bar / library strip is not fully on screen
  + Array [ Object { "bottom": 1706, "selector": ".bottom-bar", "top": 1622, "viewport": 844 } ]

Error: the waveform hangs below its panel
  Expected: <= 0
  Received:    66

Error: the update prompt covers the record button
  Expected: <= 0
  Received:    55

Error: the confirm button sits below the fold with no way to scroll to it
  Expected: <= 240
  Received:    356
```

## Screenshots

`frontend/e2e/__screenshots__/before/` and `.../after/` hold the six pairs cited
above (560KB total, committed — they are the evidence for this document).

The full 286-render sweep is opt-in and gitignored:

```
LAYOUT_SHOTS=1 bun run e2e layout
```

writes it to `frontend/e2e/__screenshots__/sweep/`.
