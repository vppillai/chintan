# Note archive (soft delete + 30-day retention) — Design

**Date:** 2026-08-07  
**Status:** Approved  
**Context:** `DELETE /v1/notes/{id}` hard-deletes today; frontend has `api.deleteNote` but no UI. Captures are orphaned on hard delete. No soft-delete or archive.

## Goal

Let users remove notes from active use without immediate permanent loss:

1. **Delete** moves a note to **Archive** (soft delete).
2. Archive is a tab on the notes list; notes stay for **30 days**.
3. From Archive: **Restore** (back to active) or **Delete forever** (hard delete).
4. After 30 days, archived notes are purged (lazy hard delete).
5. Captures stay with the note through archive/restore; permanent delete cascades to captures.
6. Archived notes are **hidden** from voice routing and note match.

## Non-goals

- Configurable archive retention via Settings (fixed 30 days).
- Scheduled purge Lambda / EventBridge job (lazy purge only for v1).
- “Empty archive” bulk action.
- Restoring into match/routing from archived titles.
- Changing the existing Settings `RetentionDays` field (audio/transcript retention; leave as-is).

## Decision

**Soft-delete on the same Dynamo `NOTE#` row.**

Add `deleted_at` and `purge_after` on `NoteIndex`. Active lists filter them out; Archive lists them. Restore clears the fields. Permanent delete and expiry hard-delete note + captures + S3.

Rejected alternatives: moving rows to `ARCHIVE#` keys (more rewrite risk); scheduled purge worker (extra infra for a personal app).

## Data model

```go
type NoteIndex struct {
    // ... existing fields ...
    DeletedAt  string `json:"deleted_at,omitempty"`  // RFC3339; empty = active
    PurgeAfter string `json:"purge_after,omitempty"` // DeletedAt + 30 days
}
```

- Archive retention constant: `archiveRetention = 30 * 24 * time.Hour` in the notes service.
- Dynamo key unchanged: `pk=USER#<userID>`, `sk=NOTE#<noteID>`.
- S3 note objects stay in place until permanent delete / purge.

## API

| Method | Route | Behavior |
|--------|-------|----------|
| `GET` | `/v1/notes` | Active notes only (`deleted_at` empty). Lazy-purge expired archives for this user (best-effort). |
| `GET` | `/v1/notes?status=archived` | Archived notes with `purge_after` still in the future. Lazy-purge expired first. |
| `DELETE` | `/v1/notes/{id}` | **Archive**: set `deleted_at=now`, `purge_after=now+30d`. Idempotent if already archived. 404 if missing. |
| `POST` | `/v1/notes/{id}/restore` | Clear `deleted_at` / `purge_after`. 404 if missing or already purged. No-op success if already active. |
| `DELETE` | `/v1/notes/{id}?permanent=true` | **Hard delete** note + all captures. Only if archived; `400` if still active. |

`GET /v1/notes/{id}` for an archived note returns the note (so Archive detail can load). `PATCH` on an archived note returns `409` (restore first). Match (`POST /v1/notes/match`) and capture routing use active notes only.

### Store additions

- `Store.DeleteCapture(ctx, userID, captureID)` — remove Dynamo `CAPTURE#` row (needed for cascade; missing today).
- Permanent delete uses existing `Objects.Delete` for each S3 key.

### Permanent delete cascade

1. `ListCapturesByNote` (note may be archived; still listed)
2. For each capture: delete S3 audio/raw/routed/clean (ignore individual S3 errors); `DeleteCapture`
3. Delete S3 `note.md` + `meta.json`; delete Dynamo `NOTE#` row

### Lazy purge

When listing active or archived notes, restore, or permanent-delete: hard-delete any of the user’s notes where `purge_after <= now`. Failures for individual expired notes are logged and skipped so the list still returns.

## Capture / routing interactions

| Action | Archived note |
|--------|----------------|
| List captures / download | Allowed |
| Create capture targeting note | `409` — note is archived |
| Complete / retry capture already bound to note | `409` — note is archived |
| MatchNotes / LLM route candidates | Active notes only |
| PATCH note | `409` — restore first |

## UI

### Note detail (active)

- **Delete** button (danger/ghost) near Save.
- Confirm: “Move to Archive? You can restore for 30 days.”
- On success: navigate to notes list (Notes tab); toast “Moved to Archive”.

### Notes list

- Tabs: **Notes** | **Archive**.
- Notes: current active list behavior.
- Archive: title, deleted date, “Deletes in N days”; tap opens archive detail.

### Archive detail

- Read-only title/body (or editable fields disabled).
- **Restore** → Notes tab, editable.
- **Delete forever** → confirm → permanent delete → Archive list.
- Captures list + downloads still available.

### Home

- Recent notes: active only.

## Error handling

| Case | Response |
|------|----------|
| Archive missing note | 404 |
| Archive already archived | 204 / success (idempotent) |
| Restore missing / purged | 404 |
| Restore already active | 200 / success (no-op) |
| Permanent while active | 400 |
| Permanent missing | 404 |
| Capture against archived note | 409 |
| PATCH archived note | 409 |

## Testing

- Service: archive → list active excludes / list archived includes; restore; permanent cascade deletes captures; expired note purged on list; match excludes archived; create/complete capture rejected for archived.
- Handler: query `status=archived`, `permanent=true`, restore route.
- Frontend: delete confirm → archive tab; restore; delete forever.

## Out of scope (reiterated)

Settings-driven retention, scheduled purge Lambda, bulk empty archive.
