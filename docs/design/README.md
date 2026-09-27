# Design

How each part of the system is meant to work, one document per part, kept
current with the code: each names the files that implement it, and CI's
contract and conformance tests hold the wire they describe to the backend.
Read `README.md` at the repository root for the system as a whole,
`docs/api/openapi.yaml` for the wire, `docs/backlog.md` for what is planned
and `docs/reviews/` for the dated reports these answer.

| File | What it is |
|---|---|
| `append-vs-autosave.md` | A voice append and an editor save landing on the same note: the one compare-and-set loop (`service.RewriteNoteBody`) and the one re-indexer (`service.RefreshNoteIndex`, both in `service/capture_edit.go`) that the pipeline's `append.go` and the editor go through, the append stamp, and the `PutIfMatch`/412 invariant `repository/s3.go` rests on. |
| `ask.md` | Ask (D5): a question answered over your notes — the ranking and the prompt in `internal/ask`, the worker's `pipeline/ask.go`, the endpoints in `openapi.yaml`. |
| `async-updates.md` | How the app learns that a recording landed: the poll and its cadence ladder (`usePendingCaptures`, `capturePollInterval` in `api/queries/captures.ts`), the seven-day baseline measured on prod, what was costed, and how Web Push would be built (`pipeline/notify.go`, proposed; owner decisions R5-RC-D1/D2). |
| `capture-ux.md` | The recording screen and its shortcuts: `/capture` and `/talk` (PTT), the recorder's state machine (`features/capture/machine.ts`, `store.ts`), the filing row and its receipts on Home (`FilingRow.tsx` with its parts under `filing/`), and what `dismissed.ts` remembers. |
| `checklists.md` | Checklist notes: the item syntax (`model/types.go`), how a recording becomes items (`cleanup/items.go`, `cleanup/prompt.go`, `pipeline/clean.go` and `append.go`), the whole-note Split up (`service/note_clean.go`), and the editor's grip, Done disclosure and menus. |
| `home.md` | Home: the library screen (`screens/NotesScreen.tsx` with its parts under `screens/library/`), the brand row (`Wordmark`), the Pinned group and its reorder, the day groups, filters, selection and the drawn checkbox, and what works offline. |
| `inbox.md` | The inbox: device keys and external recorders — `POST /v1/inbox/{audio,text}`, the key scheme and its constant-time check (`service/devices.go`), the throttles and alarms in `infrastructure/template.yaml`, per-key usage, the capture's timing record and `chintanctl latency`. |
| `pins.md` | Pinned notes: `pinned_at` and `pin_rank` as promoted attributes (`model/types.go`, `repository/dynamo.go`), the fifty-note limit, the one-request drag (`service/notes.go`), and what a re-pin does. |
| `pipeline-deadlines.md` | Pipeline deadlines: how long each stage may wait on the speech and cleanup providers (`provider/groq_stt.go`, `openai_cleanup.go`), and the test that holds the numbers (`pipeline/stage_deadline_test.go`). |
| `prompts.md` | Prompts: every prompt the worker sends — routing (`routing/prompt.go`), cleanup (`cleanup/prompt.go`, `provider/openai_cleanup.go`), items, the whole-note views, Ask — the shared rules in `llm/rules.go`, what each costs, and the live evaluation that gates a change. |
| `usage-accounting.md` | Per-tenant usage accounting: what is metered where (`internal/awscost`, `internal/storagesnap`), how a month is priced, and what the You screen shows (`features/settings/usage.ts`). |
