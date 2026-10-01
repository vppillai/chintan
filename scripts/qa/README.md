# scripts/qa — the live QA harness

Plain Node plus Playwright against the deployed app, no test runner, so one
step script can be re-run on its own while the app is being poked. A pass is a
set of step scripts that `require('./qaw1-lib.js')`, each run as
`node <step>.js` with the environment below; their logs, screenshots and
request dumps land in `QA_HOME` and are not committed. The reports they
produce live under `docs/reviews/<date>/`.

| File | What it is |
|---|---|
| `qaw1-base.js` | `launch(profile, step)` — a headless Chromium context (Pixel 8 Pro or 1280×800 desktop) signed in with the review tokens, a logger, numbered screenshots, a CDP session, and CDP touch helpers (`touchSwipe`, `touchHold`). |
| `qaw1-lib.js` | On top of it: `launch(step, {profile, theme, mic})` with the fake microphone and a speech clip, PASS/FAIL/INFO `check`/`info` results written as JSON, every API response recorded (device keys redacted), a contrast audit installed as `window.__qa`, and the note-row menu helpers. |
| `refresh-tokens.sh` | Refreshes the review tokens through Cognito's refresh-token flow (no password), from the stack's client id. |
| `inbox-live.sh` | One live check of the device inbox: mint a key, POST a clip one-shot, poll the capture until it files, then revoke the key and purge what it created. |

## Environment

Everything that used to be a personal path is a variable:

| Variable | Meaning | Default |
|---|---|---|
| `APP` | the Pages URL of the instance under test, e.g. `https://<owner>.github.io/chintan/dev/` | required |
| `API` | the instance's API endpoint | required |
| `TOKENS` | the review tokens file (`idToken`, `accessToken`, `refreshToken`, `expiresAt`) | `~/review-tokens.json` |
| `QA_HOME` | where logs, screenshots and results are written | the current directory |
| `QA_PLAYWRIGHT` | a `@playwright/test` package to load | `frontend/node_modules/@playwright/test` in this checkout |
| `QA_SPEECH_WAV` | the clip the fake microphone plays | required when `mic: true` |
| `STACK`, `AWS_PROFILE` | for `refresh-tokens.sh` | `chintan-dev-prod`, `chintan` |
| `CLIP` | the webm `inbox-live.sh` posts | `~/e2e.webm` |

Rules: the test tenant only, never the owner's; notes titled with the pass's
tag and deleted afterwards; one flow per ssh call under four minutes; tokens
never printed. On the VM `node` is a shim that execs Bun,
so put `~/.bun/bin` first on `PATH` before `node <step>.js`.
