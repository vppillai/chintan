# Round 8 spec: visual (F9 notification treatment, F4 tab-swipe animation)

Slug `visual`. This is a design spec only; nothing is implemented. All code references are to `main` as it was on 30 September 2026.

Evidence is in `shots-visual/` beside this file:
- `home-*.png`: the live app at 390×844, both themes, with `GET /v1/captures` stubbed (read-only, nothing written to the tenant).
- `f9-*.png`: the mockups, rendered on orb from `mock-visual-f9.html` with the real `tokens.css` (copied to `mock-visual-tokens.css`).
- `swipe-*.png`: frames from a real swipe.

---

## F9: filing notices look like note rows

### Current behaviour

`FilingRow` (`features/capture/FilingRow.tsx`) is rendered by `screens/NotesScreen.tsx:307` between the field and the first day group. It draws four tiers:
1. the local upload;
2. moving rows;
3. rows that need you (needs_target, failed, capped, stuck);
4. receipts, which fold when there are two or more.

Each row is a `FilingItem` (`filing/FilingItem.tsx`) inside a `SwipeRow`.

**Styling (`styles/capture.css:17-31`).** `.filing-row` uses `--color-raised`, a `1px --color-line` border, `--radius-lg` and `--shadow-xs`. The rows sit in a `--space-2` grid gap, with `--space-4` below the section. `.note-row` (`styles/home.css:83-100`) has the same surface, border, radius, shadow and gap. The only difference is that failed rows get a `line-strong` border. The screenshot `home-all-ink.png` shows four cards, then TODAY, then more cards of the same kind, and nothing marks the top ones as notices.

**Dismiss.** The receipt's × is hidden under a finger (`capture.css:631-643`, R7-7a). The receipt is put away by the swipe tray or by Clear all. Failed and stuck rows use a text "Dismiss" button.

**Bug found while looking: the receipt layout is broken on main and in prod** (`home-single-ink.png`, `home-all-open-nocturne.png`):
- The chevron sits alone on the first line and the title drops to a second line.
- Cause: `.filing-row--receipt > .filing-row__receipt, .filing-row__dismiss { grid-row: 1 / span 2 }` (`capture.css:573-576`) sets a row but no column. The grid places row-locked items first, so the chevron takes column 1 at rows 1–2, and the head (column 1, auto row) is pushed to row 3.
- The recommendation below replaces this grid. Whatever ships, the e2e check in "Tests" must guard it.

### The problem

A notification needs four things a note card does not have:
- its own surface;
- a status cue before the text;
- a dismiss control you can see;
- a boundary that separates it from the note list.

Today it has none of them.

### Options (mockups: `f9-A-*.png`, `f9-B-*.png`, `f9-C-*.png`, recommended `f9-R-*.png`)

| | A. Inset tray (recommended) | B. Notice cards with a status rail | C. Ledger lines |
|---|---|---|---|
| Shape | One recessed tray holding every filing row, divided by hairlines, under an h2 "Filing" | Separate flat cards on the notice surface, each with a 3 px rail at the start edge, coloured by kind | No surface; rows on the page between top and bottom hairlines, under the label |
| Reads as | Something pressed into the page, the opposite of the raised notes | Alerts | Page chrome or a list header |
| Pros | One object, so it is clearly a different thing from the list. Rows lose their 8 px gaps (shorter). Works unchanged as the note screen's `FilingBanner`. | Strongest signal per row | Lightest; about 16 px shorter than A |
| Cons | In Ink & Paper the tray's tone is subtle (surface on ground-deep), so the inset shadow, the glyphs and the hairlines do most of the work | Four rails of three colours is the noisiest option. In Ink the failed rail is burnt orange, the accent hue reserved for record. Least minimal. | On the note screen the banner would merge with the meta line. In Ink it is the weakest separation from the cards. |

**Owner decision: A or C. The recommended default is A.** B is not recommended.

### Recommendation: A, the inset tray

```
 FILING                                   ← h2.note-group__label (same style as "TODAY")
╭─────────────────────────────────────────╮  --color-notice, --shadow-notice, --radius-lg
│ ◔  Filing…  0:21                        │  moving: Bindu glyph, no ×
│    ▬▬ ▬▬ ▬▬ ──   Filing                 │
├─────────────────────────────────────────┤  1px --color-notice-line
│ ↳  Which note should this go in? 0:14 ✕ │  needs you: route glyph (ink)
│    Call the plumber about the water…    │
│    ( Start "Water heater" ) ( Choose… ) │
├─────────────────────────────────────────┤
│ ⓘ  Couldn't transcribe this  0:08    ✕ │  failed/capped/stuck: alert glyph (--color-notice-alert)
│    Remember to book the dentist         │
│    ( Retry )                            │
├─────────────────────────────────────────┤
│ ✓  3 filed into 2 notes ›   ( Clear all )│  fold: check glyph
╰─────────────────────────────────────────╯
                                             --space-6 (24px)
 TODAY
╭─────────────────────────────────────────╮  note cards unchanged
```

A single receipt row is `✓  Filed into "Grocery list" · 2 min  ✕`, with the excerpt as a second muted line. A started receipt is `+  Started "Roof repair" · 30 min  ✕`.

#### Tokens

Add these to `tokens.css` in all three theme blocks (Ink, Nocturne, and the `system` dark block), next to `--color-destructive`. They are semantic aliases, so no new literals are needed:

```css
--color-notice: var(--color-surface);
--color-notice-line: var(--color-line);
--color-notice-glyph: var(--color-muted);
--color-notice-alert: var(--color-destructive);
--shadow-notice: var(--shadow-inset);
```

Add this primitive to `:root` in the Layout section:

```css
--layout-notice-glyph: 1.75rem; /* 28px glyph column */
```

**Contrast on `--color-notice`** (computed; the figures match the existing table in `tokens.css`):

| Token | Ink (`#f2eee4`) | Nocturne (`#141816`) |
|---|---|---|
| ink | 15.16 | 16.32 |
| muted (glyph, ×) | 7.03 | 7.72 |
| faint ("· 2 min") | 4.63 | 5.06 |
| accent-strong (failed glyph) | 5.60 | — (Nocturne's alert glyph is ink) |
| line-strong (action borders) | 3.21 | 3.33 |

All pass AA / 1.4.11.

A darker Ink tray was tried and rejected: `#efeadf` puts faint at 4.47, which fails. That is why the tray reuses `surface`.

#### Layout rules (`styles/capture.css`, replacing `.filing` and `.filing-row` and the receipt block at lines 17-31 and 540-710)

**The section**
- `.filing`: `display: grid`, `gap: 0`, `margin-block-end: var(--space-6)`.
- `.filing__label` is the `h2.note-group__label` (reuse the class, add `margin-block-end: var(--space-2)`).
- `.filing__tray`: `background: var(--color-notice)`, `box-shadow: var(--shadow-notice)`, `border-radius: var(--radius-lg)`, no border, no `overflow` clip (it would clip focus rings).

**Dividers**
- `.filing__tray > * + *` and `.filing-fold__rows > *`: `border-block-start: 1px solid var(--color-notice-line)`.

**Rows**
- `.filing-row`:
  - `grid-template-columns: var(--layout-notice-glyph) minmax(0, 1fr) var(--layout-touch-target-min)`
  - `column-gap: var(--space-2)`, `row-gap: 0`, `align-items: start`
  - `padding: var(--space-1) var(--space-1) var(--space-1) var(--space-3)`
  - `background: var(--color-notice)`, which stays opaque so the swipe tray stays hidden behind it
  - no border, no shadow
- Corners: the first and last children of the tray (and their `.filing-swipe`) take `--radius-lg` on their outer corners. `.filing-swipe` itself becomes radius 0.
- Explicit placement, so the R7 bug cannot recur:
  - glyph: `grid-column: 1`, `grid-row: 1 / -1`
  - head, excerpt, stages, actions, error and TargetPrompt: `grid-column: 2`
  - ×: `grid-column: 3`, `grid-row: 1`
- The text column takes `padding-block: var(--space-3)` on its first and last children, so the 44 px × aligns with the first line.

**Glyph**
- `.filing-row__glyph`: `grid-column: 1`, `display: grid`, `place-items: center`, `block-size: var(--layout-notice-glyph)`, `margin-block-start: var(--space-2)`, `color: var(--color-notice-glyph)`.
- Icon size 18.
- `[data-kind='needs'] .filing-row__glyph { color: var(--color-ink) }`
- `[data-kind='failed'] .filing-row__glyph { color: var(--color-notice-alert) }`

**× (`.filing-row__dismiss`)**
- 44×44, `color: var(--color-muted)`, `grid-column: 3`.
- **Always visible on every pointer type.** Delete both media blocks at `capture.css:624-644`.
- Hover (fine pointer only): `background: var(--color-line)`, `color: var(--color-ink)`, with `--motion-duration-fast` transitions.

**Receipt**
- The chevron is dropped from view and the whole row opens the note.
- `.filing-row__receipt` keeps its accessible name ("⟨title⟩ Open the note"). It becomes a zero-size static button whose `::after` stretches over the row (`inset: 0`) under the ×, which keeps `z-index: 1`.
- Its focus ring is drawn on the row: `.filing-row--receipt:has(.filing-row__receipt:focus-visible) { outline: var(--layout-focus-ring-width) solid …; outline-offset: calc(-1 * var(--layout-focus-ring-width)) }`. Use the same ring colour token as the global focus rule.
- **Owner decision: keep a visible chevron as well. The default is no**, because three trailing controls was what squeezed the title in R7.

**Fold row**
- Columns: glyph, then toggle (`1fr`), then Clear all (`auto`).
- The toggle keeps its chevron, because it expands rather than navigates.
- Clear all is unchanged.

**FilingBanner (note screen)**
- It gets the same tray (`.filing--banner` wraps its one row in `.filing__tray`). There is no label and `margin-block: var(--space-2) var(--space-3)`.
- The landed "Show" line (`.filing-landed`) also sits in the tray, with the check glyph.

#### Glyph per kind

Add a pure function `noticeKind()` in `filing/model.ts`, returning `'moving' | 'needs' | 'failed' | 'filed' | 'started'`. The row carries `data-kind`.

| Kind | When | Icon (`components/Icon.tsx`) |
|---|---|---|
| moving | local upload in progress, or any non-terminal capture that is not stuck | new `bindu`: `'M18.4 17.3A8.25 8.25 0 1 1 18.4 6.7M13.6 11a1 1 0 1 0 0 2 1 1 0 1 0 0-2'` (the brand's open C and dot, from `docs/design/branding/bindu-mark.svg`; the stroked r=1 ring at 1.75 width reads as the brand's 1.85 dot). Static; the stage strip already pulses. |
| needs | `needs_target` | `route` |
| failed | `failed`, `spend_capped`, `upload-failed`, stuck, `no_content` (in muted, not alert) | `alert` |
| filed | an appended receipt, and the fold | `check` |
| started | a receipt with `createdNote` | `plus` |

For the started kind, remove the inline `.filing-row__started-icon` (`FilingItem.tsx:146`, `capture.css:579-585`); the glyph column replaces it.

#### Controls per row

| Row | Controls |
|---|---|
| moving | none (no ×, as today) |
| needs | TargetPrompt plus × |
| failed or stuck | Retry plus ×. The text "Dismiss" button is removed, and the × calls the same `onDismiss`. |
| no_content | × only |
| receipt | row tap opens, × dismisses; the swipe tray stays as a second way |
| local upload failed | Retry plus **Discard stays a labelled text button, never an ×**. Discard deletes the audio, and an × would read as hide. Never lose notes. |

#### Accessibility (keep round 7 intact)

**Live region.** There is still exactly one always-mounted polite region for the fold, plus each unfolded row's own `role=status` title, as today. The glyphs are `aria-hidden` (Icon already is), and the title text carries the meaning ("Couldn't…", "Which note…", "Filed into…"), so colour and shape are never the only cue.

**Section name.** The section gets `aria-labelledby` pointing to the h2 "Filing" (it was `aria-label="Recordings being filed"`). The h2 lands in heading navigation beside "Pinned" and "Today". The banner keeps `aria-label="Filing a recording"`.

**Focus hand-off.**
- `focusPastDismissed` still queries `.filing-row__dismiss, .filing-row__action, .filing-fold__toggle`. A failed row's × is `.filing-row__dismiss`, so the neighbour lookup keeps working.
- The neighbour is still the `.filing-swipe` or `.filing-row` sibling. Siblings are now inside `.filing__tray`; the function walks siblings, so it still works if the tray is the parent.
- When the tray empties to the last row, the fallback is still `.library-heading`.
- `refocusSurvivor` is unchanged.

**Targets.** The ×, Retry, Clear all and the fold toggle are all at least 44 px. The 28 px glyph is not a target.

**Screen reader.** A screen reader hears "Filing, heading level 2", then each row as today. The × is named "Dismiss" everywhere.

**Reduced motion.** Nothing new animates. The hover transitions use tokens.

#### Copy

- **Owner decision: the label text. The default is "Filing"**, which is accurate for moving and failed rows. The alternative is "Recent filings", which reads wrong over a failure.

### Overriding a past decision

R7-7a (owner, 2026-09-30) hid the receipt × under a finger to save width. F9 asks for the notices to be "clearly identifiable as dismissible", and that overrides it. The width is paid back:
- the chevron column goes;
- the 8 px gaps between rows go.

The title still truncates on one line. The fold and the tier order (R7-7a) are kept.

### Files (frontend)

| File | Change |
|---|---|
| `styles/tokens.css` | the 5 colour/shadow tokens in 3 blocks, plus the 1 layout token |
| `styles/capture.css` | the filing rules rewritten as above; delete the touch-hiding blocks and the started icon |
| `features/capture/FilingRow.tsx` | h2 plus `aria-labelledby`; `.filing__tray` wrapper; fold row glyph |
| `features/capture/filing/FilingItem.tsx` | glyph cell, `data-kind`, × on capture rows replacing the Dismiss text button, chevron hidden, started icon moved |
| `features/capture/filing/model.ts` | `noticeKind` |
| `features/capture/FilingBanner.tsx` | tray wrapper |
| `components/Icon.tsx` | `bindu` |
| `docs/design/capture-ux.md` ("Receipts on Home") and `home.md` ("Filing, above the list") | update; note that R7-7a's × rule is superseded by F9 |

### Tests

**Unit**
- `model.test.ts`: `noticeKind` for each status, stuck, and createdNote.
- `FilingItem.test.tsx`:
  - each kind renders its glyph and `data-kind`;
  - a failed row has an × named Dismiss and no text "Dismiss";
  - the × is rendered without focus;
  - a local failed upload still has a "Discard" text button.
- `FilingRow.test.tsx`:
  - `getByRole('region', { name: 'Filing' })` and `getByRole('heading', { name: 'Filing', level: 2 })`;
  - still exactly one `[aria-live]` outside the rows;
  - × on a failed row from the keyboard moves focus to the next row's control, and on the last row to `.library-heading`;
  - the existing survivor-focus test still passes.

**e2e**
- `layout.spec.ts` / `screenshots.spec.ts`, Pixel 7 and desktop, both themes:
  - for a receipt and a failed row, the × box is visible at 44×44;
  - the title's and the ×'s vertical centres are within 4 px (this guards the grid bug);
  - the tray's computed background equals `--color-surface` and differs from `.note-row`'s.
- `a11y.spec.ts`: axe on Home with all tiers stubbed, both themes.

**Lint**
- `bun run lint` (check-tokens) passes, because there are no literals outside `tokens.css`.

### Risks

- **Ink & Paper subtlety.** surface vs ground-deep is 1.08:1. The tray reads through the inset shadow, the glyphs and the hairlines, not the fill. If the owner finds it too quiet, the next step is a 1px `--color-line` border on the tray. Do not darken the fill, which would cost faint's AA.
- **`:has()` focus ring.** This needs Safari 15.4+ and Chrome 105+, which is fine for the PWA targets. Without it the fallback is the button's own (zero-size) ring, so keep `outline` on the row as the primary approach.
- **Tests that query the old names.** Any test that queries "Recordings being filed" or the text "Dismiss" on capture rows needs updating.

**Effort: M (frontend only).**

---

## F4: the animation of the swipe between note tabs

### Current behaviour (#156, R6-NAV-1)

The behaviour was measured on prod at 390×844 with a CDP touch drag of −168 px, a 150 ms hold, then release. The trace was taken with `getComputedStyle` every frame (`r8d-swipe.js`).

**Follow**
- `useHorizontalSwipe` (`hooks/useHorizontalSwipe.ts:197-201`) writes `--tab-swipe-x` on `.note-views`.
- `.note-views[data-swiping] .note-tabpanel { transform: translateX(var(--tab-swipe-x)) }` (`styles/notes.css:408-411`).
- Only the panel moves, 1:1 with the finger, **capped at 40 % of the width** (`FOLLOW_MAX_FRACTION`). The trace shows −12, −24 … −156 px and then a flat line at −156 while the finger reached −168.
- Where there is no neighbour the follow is ×0.25, linear, and also capped.

**During the drag**
- The vacated side shows bare ground (`swipe-mid.png`), with no hint of the neighbour.
- The tab strip does not move.

**Release**
- `data-swiping` is removed and the `.note-tabpanel` transition applies: `transform var(--motion-duration-base) var(--motion-ease-decelerate)`, which is 220 ms `cubic-bezier(0,0,0,1)` (`notes.css:375`).

**Step: the defect**
- The tab changes in the same commit, and `NotePanel` is not keyed, so the **new** panel is drawn at the old offset and slides back to 0.
- Trace: `964 ms −156 Cleaned → 981 −95.7 → 1016 −52.7 → 1066 −21 → 1199 none`.
- A left swipe to Cleaned brings Cleaned in **from the left, moving right**, against the finger. It reads as a snap back with the content changed (`swipe-after40.png`).

**Indicator.** The selected pill does not slide. Each tab's `background-color` fades over `--motion-duration-fast` (140 ms), so the highlight jumps at release.

**Reduced motion.** The tokens become 1 ms, so the release is instant. The follow is still 1:1.

**Cost per move.** One custom property is set on `.note-views`, an inherited property on the container of the whole panel, so every move restyles the subtree: a 40-row checklist and the textarea. `widthOf()` reads `clientWidth` on every move.

### Options

| | A. Directional settle | B. True carousel with neighbour peek | C. A plus a sliding indicator (recommended) |
|---|---|---|---|
| What | Follow 1:1 to the full width. On a step the new panel enters from the side the finger pulled away from, starting where a neighbour would have been, and decelerates to 0. A proper rubber band at the edges. | Render the neighbouring panel beside the current one during the drag | A, plus one indicator pill in the strip that tracks the drag and slides at commit |
| Cost | S | L: mounts a TextPanel, checklist or Recordings (players, peaks) at every drag start; duplicate tabpanel ids, a second textarea and autogrow measurement; the neighbour sits at the current scroll offset, so you would see its middle, not its top | S+ |
| Feel | Correct direction; the empty side during the drag is softened by a fade | The most native | Correct direction, and the strip moves with the hand: most of B's feel at A's cost |

B is rejected. Its scroll mismatch alone makes the peek show the wrong part of the panel.

### Recommendation: C

**Constants.** `W` is the region's `clientWidth`, read once when the axis is decided (390 at 390×844) and cached on the gesture. The following stay as they are:
- axis slop 12 px;
- edge exclusion 24 px;
- commit at 30 % (117 px) or a fresh flick of 0.4 px/ms.

**Follow (finger down)**
- Panel: `translate: dx` with **no cap** (clamped to ±W), 1:1.
- Fade: `opacity: 1 − 0.4·|dx|/W`, so the vacated ground reads as "something is coming" rather than broken.
- Rubber band where there is no neighbour: `x = sign(dx)·R·(1 − 1/(1 + |dx|/R))`, with `R = 0.15·W` (58 px at 390). The asymptote is R. At dx=50 → 27 px; at 150 → 42 px; it never reaches 58.
- Indicator: `progress = clamp(−dx/W, −1, 1)` toward an existing neighbour, else `−x/W` (the rubbered x, so the pill nudges at most about 15 %). The pill's translate is `(index + progress) × (its width + --space-1)`.

**Release, no step (snap back)**
- Panel: to 0, opacity to 1, over `--motion-duration-base` (220 ms) with `--motion-ease-decelerate`.
- Indicator: back to `index`, with the same duration and easing.
- **Owner decision: snap back with `--motion-ease-spring` (a small overshoot). The default is no**, keep decelerate.

**Release, step**
1. In the handler, before `onSwipe`:
   - compute `enter = dx < 0 ? W + dx : dx − W` (for example, dx −156 → +234: the new panel starts to the right);
   - set the panel's `translate` to `enter` and its opacity to `1 − 0.4·|enter|/W`;
   - set `data-tab-enter` on the region (with `transition: none`);
   - then call `onSwipe`.

   pointerup is a discrete event, so React commits the new panel synchronously before paint. No frame shows the old content at the new offset.
2. After two `requestAnimationFrame`s:
   - remove `data-tab-enter`;
   - clear the inline `translate` and `opacity`.

   The panel transitions to rest over **`--motion-duration-base` (220 ms), `--motion-ease-decelerate`**, or over **`--motion-duration-fast` (140 ms)** when the step was a flick. A flick sets `data-tab-enter="fast"`, and CSS picks the token from that.
3. The indicator's `index` changes in the same commit and `progress` is cleared, so it continues from `index + progress` to `index ± 1` over the same duration and easing as the panel. The text colour of the selected tab changes at commit, over `--motion-duration-fast`.

**A new pointerdown during the settle.** Cut the settle (remove the attribute and the inline styles, so the panel lands at rest), then begin the new gesture normally. The 220 ms is too short for a continuation to matter.

**Taps and keys.**
- The indicator slides (base, decelerate).
- The panel swaps instantly, as today. The enter animation is for the finger only.
- Arrow keys, Home and End are unchanged.

**Reduced motion**
- The follow, fade and rubber band stay, because they are direct manipulation and move only with the finger.
- All settles and the indicator slide become instant through the tokens (1 ms) and the global `*` rule. The new panel appears at rest on release.
- Use no JS timers for durations. The double rAF clears the attribute either way.

**Screen readers.** Nothing changes: no announcement, `aria-selected` as before, and the indicator `aria-hidden`.

**Mouse.** Unchanged: no swipe, and the indicator slides on click.

#### Vertical scroll lock

Unchanged. The axis is decided at 12 px, and `|dy| ≥ |dx|` is a scroll. Once the axis is x, the region takes pointer capture and the non-passive `touchmove` `preventDefault` holds the page still, so vertical wander does not scroll. Pull-to-refresh stands down through `defaultPrevented`.

**New:** the vertical component is ignored for the follow (x only), as it is today. Say so in the hook's doc comment.

#### Where the new panel starts vertically

- Today the scroll offset is kept across a tab change. Swiping from deep in Text lands mid-Cleaned or clamped at its foot, and with an enter animation that looks like arriving in the middle of a page.
- **Recommendation:** on every tab change (swipe, tap or key), in a `useLayoutEffect` keyed on `tab` in `NoteViews` (skipping the first render):
  - if `.note-views`' top is above `.app__main`'s top, so the strip is stuck, set `main.scrollTop += views.top − main.top` (instant), so the new panel starts directly under the strip;
  - if the head is still on screen, do nothing.
- **Owner decision. The default is yes.** The alternative is a per-tab scroll memory, which is YAGNI for alpha.

#### R7-6a scroll restore

- A tab change is `setParams(..., { replace: true })`, which gives a new `location.key` with navigation type REPLACE. `useScrollRestore` then:
  - writes the old key with the last scroll-event offset;
  - reads target 0 for the new key, because the type is not POP and there is no `restoreScroll`;
  - starts no restore loop.

  So it never fights the enter animation or the clamp.
- Effect order: `NoteViews`' layout effect (the child) runs before `AppShell`'s (the parent), so `position.current = main.scrollTop` reads the clamped value. Back to Home restores Home as before. Back/Forward to the note restores the last replaced entry's offset.
- **Test this explicitly** (see below), because the order matters.

#### Keyboard inset

- A swipe that starts on the focused textarea unmounts it, and the keyboard closes. `useKeyboardInset` then drops `--keyboard-inset` on its next visualViewport resize, which shrinks `.app__main`'s `padding-block-end` while the 220 ms settle runs.
- Because the settle is transform-only, the padding reflow does not interrupt it. The browser may clamp `scrollTop` for a short panel, and that is expected.
- No special handling. The device check is that the enter does not stutter on iPhone and Pixel with the keyboard up.
- (Blocking the swipe while editing was considered. It would change behaviour the owner called "as expected", so it is not recommended.)

#### Performance budget

- **Compositor-only properties.** Every frame of the gesture and the settle changes only `translate` and `opacity` on `.note-tabpanel`, and `translate` on `.note-tabs__indicator`. There is no layout, and no paint beyond the composite.
- **Write to the leaves, not the container.** The hook sets `panel.style.translate`, `panel.style.opacity` and `indicator.style.setProperty('--tab-progress', p)` on those two elements. It finds them once at axis commit via `[data-swipe-panel]` and `[data-swipe-indicator]` inside the region. `translate` is not inherited, so a move restyles one element and not the checklist under it. Remove `--tab-swipe-x` from `.note-views`.
- **No reads in pointermove.** Read `clientWidth` once at axis commit. No `getBoundingClientRect` in the move path.
- **Layer promotion only while moving.** `will-change: translate, opacity` only under `[data-swiping]` and `[data-tab-enter]`. At rest there is no transform or translate, as today, so the row menus' stacking and the layout sweep are unaffected.
- **No React work per move.** React commits only on axis commit (`dragging`), on release, and on the tab change.
- **Targets:**
  - the pointermove handler takes ≤ 0.3 ms;
  - style recalc per gesture frame is ≤ 0.5 ms on a 40-item checklist (DevTools trace on orb, Pixel 7 profile, 4× CPU throttle: ≤ 2 ms);
  - no long task over 50 ms from pointerup to rest;
  - no dropped frames in the 220 ms settle on a Pixel 7.

#### CSS (`styles/notes.css`)

```css
.note-tabpanel { transition: translate var(--motion-duration-base) var(--motion-ease-decelerate),
                             opacity var(--motion-duration-base) var(--motion-ease-decelerate); }
.note-views[data-tab-enter='fast'] .note-tabpanel { transition-duration: var(--motion-duration-fast); }
.note-views:is([data-swiping], [data-tab-enter]) .note-tabpanel { will-change: translate, opacity; }
.note-views[data-swiping] .note-tabpanel,
.note-views[data-tab-enter] .note-tabpanel { transition: none; }   /* release removes these → settle */

.note-tabs__list { position: relative; }
.note-tabs__indicator {
  position: absolute; inset-block: var(--space-1); inset-inline-start: var(--space-1);
  inline-size: calc((100% - (var(--tab-count) + 1) * var(--space-1)) / var(--tab-count));
  border-radius: var(--radius-full); background-color: var(--color-raised); box-shadow: var(--shadow-sm);
  translate: calc((var(--tab-index) + var(--tab-progress, 0)) * (100% + var(--space-1))) 0;
  transition: translate var(--motion-duration-base) var(--motion-ease-decelerate);
}
.note-views[data-swiping] .note-tabs__indicator { transition: none; will-change: translate; }
.note-tabs__tab { position: relative; z-index: 1; }   /* above the pill */
/* .note-tabs__tab[aria-selected='true'] loses its background-color and box-shadow (the pill paints them) */
```

The `data-tab-enter` rule sets the panel's start pose with no transition. Removing it, with the inline styles cleared, is what animates. In RTL the strip is LTR-only today; `translate` sign handling is not needed now.

#### Files (frontend)

| File | Change |
|---|---|
| `hooks/useHorizontalSwipe.ts` | full-width follow; rubber curve; fade; leaf writes; width cached; enter pose plus double rAF; `data-tab-enter`; progress |
| `features/notes/NoteTabs.tsx` | `<span className="note-tabs__indicator" data-swipe-indicator aria-hidden="true">` as the list's first child; `style={{'--tab-index': index, '--tab-count': tabs.length}}` on the list |
| `features/notes/NoteDetailScreen.tsx` | `data-swipe-panel` on `NotePanel`'s div; scroll clamp `useLayoutEffect` in `NoteViews` |
| `styles/notes.css` | as above |
| `docs/design/note-screen.md` and the `useHorizontalSwipe` doc comment | update; the backlog row R6-NAV-1 gains an F4 follow-up |

#### Tests

**Unit, `useHorizontalSwipe.test.tsx`** (rAF faked):
- follows 1:1 past 40 % (dx −250 → panel `translate` "−250px");
- rubber band at the first tab: dx +100, W 390 → about 36.7 px, and never ≥ 58.5;
- a left commit:
  - sets `data-tab-enter` and `translate` W+dx before `onSwipe` is called;
  - after two frames, removes both;
  - after a flick, `data-tab-enter="fast"`;
- a cancel or a snap back sets no `data-tab-enter`;
- `--tab-progress` on the indicator tracks −dx/W and is cleared at end;
- `clientWidth` is read once per gesture (getter spy);
- a pointerdown during the enter clears it.

**Unit, `NoteTabs` test**
- The indicator exists, is `aria-hidden`, and its `--tab-index` follows `value`.

**Unit, `NoteDetailScreen` test**
- Scroll clamp with the strip stuck (mock rects) sets `scrollTop` to the strip line.
- With the strip not stuck it leaves `scrollTop` alone.

**e2e, `note-tabs.spec.ts`** (Pixel 7):
- After a left swipe past 30 %, the first frame after release has panel x > 0 (it enters from the right), it reaches rest within 400 ms, and the tab is Cleaned.
- A right swipe on Text rubber-bands (max |x| < 0.15·W) and snaps back.
- With `reducedMotion: 'reduce'`, translate is none within 50 ms of release.
- The indicator's box ends centred on the selected tab.

**e2e, `scroll-restore.spec.ts`**
- Home scrolled → note → swipe tab → Back: Home's offset is restored.
- A note scrolled deep → swipe: `scrollTop` equals the strip line, and Back then Forward restores it.

### Risks

- **The double-rAF start pose** relies on React committing synchronously in pointerup. This holds for a discrete event in React 18/19. If it ever batches past a paint, one frame shows the old panel at `enter`. The e2e first-frame assertion catches that.
- **Indicator geometry.** `--tab-count` and the gap must match `.note-tabs__list`'s gap (`--space-1`). The narrow-phone rule (hide the count under 22.5em) does not change the segment widths, because the segments are equal flex.
- **Pen users.** The "pen is a finger" trade is unchanged.

**Effort: S–M (frontend only).**

---

## Owner decisions (with defaults)

1. F9: option A (inset tray) or C (ledger lines). **Default A.**
2. F9: the section label. **Default "Filing"** (the alternative is "Recent filings").
3. F9: keep a visible chevron on receipts. **Default no** (the row opens).
4. F9: always-visible × supersedes R7-7a's hidden-× rule. **Recommended yes, per F9.**
5. F4: spring overshoot on snap back. **Default no** (decelerate).
6. F4: a new panel starts at its top under the stuck strip. **Default yes.**

## Implementation streams

These two streams touch no common files and can run in parallel.

| Stream | Kind | Files | Effort |
|---|---|---|---|
| **visual-F9** | fe | `tokens.css`, `capture.css`, `FilingRow.tsx`, `filing/FilingItem.tsx`, `filing/model.ts`, `FilingBanner.tsx`, `Icon.tsx`, their tests, `capture-ux.md`, `home.md` | M |
| **visual-F4** | fe | `useHorizontalSwipe.ts`, `NoteTabs.tsx`, `NoteDetailScreen.tsx`, `notes.css`, their tests, `note-tabs.spec.ts`, `scroll-restore.spec.ts`, `note-screen.md` | S–M |
