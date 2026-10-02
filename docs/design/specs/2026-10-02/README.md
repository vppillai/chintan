# Design specs, 2 October 2026

One spec from the owner's round of feedback: the Details/Share drawer as a
newcomer meets it on a 360 px phone. It gives the current behaviour with
screenshots, what is wrong, the options and the recommendation, and the
files and tests that built it.

| Spec | Feedback | What it decided | Shipped |
|---|---|---|---|
| `share.md` | F3 the share option and menu, F5 checklist copying, F7 drag to close | One primary action (the system share sheet where the browser has one, Copy note where it has not), a labelled Cleaned view group, a hint naming what is copied, a checklist as ☐ / ☑ lines with an HTML list beside them, and the head of the drawer as a handle that closes it | the `r12/ux` pull request (PR12-11, PR12-12, PR12-13) |

Mockups: `mock-share.html` with `mock-share-tokens.css` (the tokens of the
day). Screenshots: `shots-share/before-*.png` (the drawer as found, both
themes, a prose note and a checklist), `shots-share/after-*.png` (the same
four after the change) and `after-share-capable-*.png` (the sheet where the
browser has `navigator.share`, stubbed for the shot), all at 360×780 from
the e2e stub.
