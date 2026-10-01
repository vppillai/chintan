# Design specs, 30 September 2026

Three specs written against `main` @ `3ac3b2c` from the owner's nine points
of feedback (`owner-feedback.md`, verbatim). Each spec gives the current
behaviour with code references, two or three options, one recommendation with
exact interaction details, the files to change and the tests to add; the
"owner decision" marks are taste calls with their recommended default. The
last column names the pull requests that built each one; the backlog rows of
the same ids hold what changed.

| Spec | Feedback | What it decided | Shipped |
|---|---|---|---|
| `checklists.md` | F1 deeper sub-items, F8 the Split up tab | Three checklist levels (`MAX_DEPTH` = 2) in the parsers, the merge and the editor — Tab, grip arrows and a sideways drag with an absolute preview; Split up becomes **Tidy up list**, a menu action with Undo, and checklists stop auto-cleaning | #191 (R8-F1a), #194 (R8-F1b), #195 (R8-F8); round 10 took the depth to four (PR10-11, PR10-12) |
| `gestures.md` | F5 + F6 + F7 push-to-talk, F2 back navigation, F3 recording while typing | The record disc holds to talk on every screen, slide left to cancel and up to lock, thresholds measured from the press point; `/talk` and the You row go; Home is always history entry 0 (option B); a small banner mic while a text field has focus (option C). Reverses the 27 September F4 decision (D0) | #193 (R8-F5/F6/F7), #197 (R8-F2), #198 (R8-F3); #206 later sent a locked take to the recording screen (R8-F5b, reverses this spec's D1) |
| `visual.md` | F9 filing notices, F4 the tab-swipe motion | Option A: one inset "Filing" tray with a glyph per kind, hairlines between rows and a fold for receipts; the swipe follows the finger to the end with a directional enter and a sliding pill | #190 and #192 (R8-F9, R8-F9a), #196 (R8-F4) |

Mockups: `mock-checklists-depth.svg`, `mock-checklists-tidy.svg`,
`mock-gestures-{holding,locked,typing}.svg`; `mock-visual-f9.html` with
`mock-visual-tokens.css` (the real tokens of the day) rendered to
`shots-visual/f9-*.png`, beside `shots-visual/home-*.png` (the live app on the
test tenant, both themes) and `shots-visual/swipe-*.png` (frames of a real
swipe). Every image is under 300 kB.
