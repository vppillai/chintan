# Note Archive Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Soft-delete notes into a 30-day Archive with restore and permanent delete (cascade to captures), plus Archive UI tabs.

**Architecture:** Soft-delete via `deleted_at` / `purge_after` on the same Dynamo `NOTE#` row. `DELETE /v1/notes/{id}` archives; `POST .../restore` restores; `DELETE ...?permanent=true` hard-deletes note + captures. Lazy purge of expired archives on list/restore/permanent. Frontend adds Delete on detail and Notes | Archive tabs.

**Tech Stack:** Go Lambda, DynamoDB single-table, S3 objects, vanilla JS PWA.

**Spec:** `docs/superpowers/specs/2026-08-07-note-archive-design.md`

## Global Constraints

- Archive retention is fixed **30 days** (do not wire Settings `RetentionDays`)
- Active lists, match, and voice routing never see archived notes
- Captures stay with the note through archive/restore; permanent delete cascades
- Permanent delete only allowed when the note is already archived (`400` if active)
- Mutations (`PATCH`, create/complete capture) against archived notes return **409**
- Lazy purge only — no scheduled Lambda
- Keep passbook-* AWS resources untouched; deploy via existing GitHub Actions on `main`

## File map

| File | Responsibility |
|------|----------------|
| `backend/internal/model/types.go` | `DeletedAt`, `PurgeAfter` on `NoteIndex` |
| `backend/internal/repository/store.go` | `DeleteCapture` on `Store` |
| `backend/internal/repository/memory.go` | `DeleteCapture` |
| `backend/internal/repository/dynamo.go` | `DeleteCapture` |
| `backend/internal/repository/memory_test.go` | `DeleteCapture` test |
| `backend/internal/service/notes.go` | Archive / restore / permanent / list filters / lazy purge / match |
| `backend/internal/service/notes_archive_test.go` | Service archive tests |
| `backend/internal/service/capture.go` | Reject ops on archived notes; filter route candidates |
| `backend/internal/service/capture_test.go` | Mock `DeleteCapture`; archived-note tests |
| `backend/internal/handler/notes.go` | `?status=archived`, restore route, `?permanent=true`, 409 mapping |
| `backend/internal/handler/handler_test.go` | HTTP archive tests |
| `frontend/js/api.js` | `listNotes({archived})`, `restoreNote`, `deleteNotePermanent` |
| `frontend/index.html` | Tabs, delete / restore / delete-forever buttons |
| `frontend/js/notes.js` | Archive UX |
| `frontend/css/styles.css` | Tab + archive meta styles |
| `frontend/sw.js` | Bump cache version |

---

### Task 1: Model fields + `DeleteCapture`

**Files:**
- Modify: `backend/internal/model/types.go`
- Modify: `backend/internal/repository/store.go`
- Modify: `backend/internal/repository/memory.go`
- Modify: `backend/internal/repository/dynamo.go`
- Modify: `backend/internal/repository/memory_test.go`
- Modify: `backend/internal/service/capture_test.go` (mockStore must implement `DeleteCapture`)

**Interfaces:**
- Produces: `NoteIndex.DeletedAt`, `NoteIndex.PurgeAfter`; `Store.DeleteCapture(ctx, userID, captureID) error`

- [ ] **Step 1: Write the failing test**

Add to `memory_test.go`:

```go
func TestDeleteCapture(t *testing.T) {
	store := repository.NewMemoryStore()
	ctx := context.Background()
	c := model.CaptureIndex{ID: "c1", UserID: "user1", NoteID: "n1", Status: model.StatusUploaded}
	if err := store.PutCapture(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteCapture(ctx, "user1", "c1"); err != nil {
		t.Fatalf("DeleteCapture: %v", err)
	}
	if _, err := store.GetCapture(ctx, "user1", "c1"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if err := store.DeleteCapture(ctx, "user1", "missing"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("missing err = %v, want ErrNotFound", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./internal/repository/ -run TestDeleteCapture -count=1`
Expected: FAIL — `DeleteCapture` undefined

- [ ] **Step 3: Implement model + DeleteCapture**

On `NoteIndex` in `types.go`:

```go
DeletedAt  string `json:"deleted_at,omitempty"`
PurgeAfter string `json:"purge_after,omitempty"`
```

On `Store`:

```go
DeleteCapture(ctx context.Context, userID, captureID string) error
```

`MemoryStore.DeleteCapture`: mirror `DeleteNote` but for `s.captures[userID][captureID]`.

`DynamoStore.DeleteCapture`: same pattern as `DeleteNote` with `captureSK(captureID)`.

Add empty stub on `mockStore` in `capture_test.go`:

```go
func (m *mockStore) DeleteCapture(ctx context.Context, userID, captureID string) error {
	key := userID + "/" + captureID
	if _, ok := m.captures[key]; !ok {
		return repository.ErrNotFound
	}
	delete(m.captures, key)
	return nil
}
```

- [ ] **Step 4: Run tests**

Run: `cd backend && go test ./internal/repository/ ./internal/service/ ./internal/handler/ -count=1`
Expected: PASS (all packages compile with new interface method)

- [ ] **Step 5: Commit**

```bash
git add backend/internal/model/types.go backend/internal/repository/ backend/internal/service/capture_test.go
git commit -m "Add note archive fields and DeleteCapture for cascade deletes."
```

---

### Task 2: NotesService archive lifecycle

**Files:**
- Modify: `backend/internal/service/notes.go`
- Create: `backend/internal/service/notes_archive_test.go`
- Modify: existing `DeleteNote` tests in `notes_test.go` / `handler_test.go` that expect hard delete

**Interfaces:**
- Consumes: `Store.PutNote`, `Store.DeleteNote`, `Store.DeleteCapture`, `Store.ListCapturesByNote`, `Objects.Delete`
- Produces:
  - `const ArchiveRetention = 30 * 24 * time.Hour`
  - `var ErrNoteArchived = errors.New("note is archived")`
  - `var ErrNoteNotArchived = errors.New("note is not archived")`
  - `ArchiveNote(ctx, userID, noteID) (model.NoteIndex, error)`
  - `RestoreNote(ctx, userID, noteID) (model.NoteIndex, error)`
  - `PermanentlyDeleteNote(ctx, userID, noteID) error`
  - `ListNotes` → active only (+ lazy purge)
  - `ListArchivedNotes(ctx, userID) ([]model.NoteIndex, error)`
  - `UpdateNote` returns `ErrNoteArchived` when `DeletedAt != ""`
  - `MatchNotes` uses active `ListNotes` (no change beyond ListNotes filtering)
  - Helper: `func NoteIsActive(n model.NoteIndex) bool`

- [ ] **Step 1: Write failing service tests**

Create `notes_archive_test.go` covering:

```go
func TestArchiveNoteHidesFromActiveList(t *testing.T) { /* create → ArchiveNote → ListNotes empty of it → ListArchivedNotes contains it with DeletedAt/PurgeAfter */ }
func TestRestoreNoteReturnsToActive(t *testing.T) { /* archive → restore → active list has it; archived empty */ }
func TestPermanentlyDeleteRequiresArchive(t *testing.T) { /* permanent while active → ErrNoteNotArchived */ }
func TestPermanentlyDeleteCascadesCaptures(t *testing.T) {
	/* archive note; put capture + S3 keys; PermanentlyDeleteNote;
	   GetNote ErrNotFound; GetCapture ErrNotFound; objects gone */
}
func TestLazyPurgeExpiredOnList(t *testing.T) {
	/* put note with DeletedAt/PurgeAfter in the past; ListNotes or ListArchivedNotes;
	   note gone from store */
}
func TestUpdateArchivedNoteRejected(t *testing.T) { /* ArchiveNote then UpdateNote → ErrNoteArchived */ }
func TestMatchNotesSkipsArchived(t *testing.T) { /* active + archived with same-ish titles; MatchNotes only ranks active */ }
```

Use `repository.NewMemoryStore()` + `NewMemoryObjects()` like existing `notes_test.go`.

For cascade test, put capture via store and fake S3 keys on the capture matching `keys.CaptureAudio` etc., or simply set `AudioKey`/`RawKey`/`CleanKey`/`RoutedKey` to known object keys you `Put` first.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd backend && go test ./internal/service/ -run 'Archive|Restore|Permanently|LazyPurge|UpdateArchived|MatchNotesSkips' -count=1`
Expected: FAIL — methods missing / DeleteNote still hard-deletes

- [ ] **Step 3: Implement**

In `notes.go`:

```go
const ArchiveRetention = 30 * 24 * time.Hour

var (
	ErrNoteArchived    = errors.New("note is archived")
	ErrNoteNotArchived = errors.New("note is not archived")
)

func NoteIsActive(n model.NoteIndex) bool {
	return strings.TrimSpace(n.DeletedAt) == ""
}

func (s *NotesService) ListNotes(ctx context.Context, userID string) ([]model.NoteIndex, error) {
	_ = s.purgeExpired(ctx, userID)
	all, err := s.store.ListNotes(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]model.NoteIndex, 0, len(all))
	for _, n := range all {
		if NoteIsActive(n) {
			out = append(out, n)
		}
	}
	return out, nil
}

func (s *NotesService) ListArchivedNotes(ctx context.Context, userID string) ([]model.NoteIndex, error) {
	_ = s.purgeExpired(ctx, userID)
	all, err := s.store.ListNotes(ctx, userID)
	// filter !NoteIsActive && purge_after > now
}

func (s *NotesService) ArchiveNote(...) (model.NoteIndex, error) {
	// GetNote; if already archived return as-is (idempotent);
	// set DeletedAt=now RFC3339Nano, PurgeAfter=now+ArchiveRetention; PutNote
}

func (s *NotesService) RestoreNote(...) (model.NoteIndex, error) {
	_ = s.purgeExpired(ctx, userID)
	// GetNote; if active return as-is; clear DeletedAt/PurgeAfter; PutNote
}

func (s *NotesService) PermanentlyDeleteNote(...) error {
	_ = s.purgeExpired(ctx, userID)
	note, err := s.store.GetNote(...)
	if NoteIsActive(note) { return ErrNoteNotArchived }
	captures, _ := s.store.ListCapturesByNote(...)
	for _, c := range captures {
		for _, key := range []string{c.AudioKey, c.RawKey, c.RoutedKey, c.CleanKey} {
			if key != "" { _ = s.objects.Delete(ctx, key) }
		}
		_ = s.store.DeleteCapture(ctx, userID, c.ID)
	}
	_ = s.objects.Delete(ctx, note.S3MarkdownKey)
	_ = s.objects.Delete(ctx, note.S3MetaKey)
	return s.store.DeleteNote(ctx, userID, noteID)
}

// Change DeleteNote to call ArchiveNote and ignore returned note:
func (s *NotesService) DeleteNote(ctx context.Context, userID, noteID string) error {
	_, err := s.ArchiveNote(ctx, userID, noteID)
	return err
}

func (s *NotesService) UpdateNote(...) {
	// after GetNote: if !NoteIsActive(note) { return ErrNoteArchived }
}

func (s *NotesService) purgeExpired(ctx context.Context, userID string) error {
	// list all; for each with PurgeAfter <= now, call PermanentlyDeleteNote
	// (PermanentlyDeleteNote must not recurse purge, or purgeExpired skips calling itself —
	// implement hardDeleteNote unexported that PermanentlyDeleteNote and purgeExpired share)
}
```

**Important:** extract unexported `hardDeleteNote(ctx, userID, note)` used by both `PermanentlyDeleteNote` (after archive check) and `purgeExpired` (no archive check — already expired). Avoid recursive purge.

Update any test that expected `DeleteNote` to remove the Dynamo row immediately — those should call `PermanentlyDeleteNote` after archive, or assert soft-delete fields instead.

- [ ] **Step 4: Run tests**

Run: `cd backend && go test ./internal/service/ -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add backend/internal/service/
git commit -m "Archive notes for 30 days with restore and permanent cascade delete."
```

---

### Task 3: Capture service rejects archived notes

**Files:**
- Modify: `backend/internal/service/capture.go`
- Modify: `backend/internal/service/capture_test.go` / create `capture_archive_test.go`

**Interfaces:**
- Consumes: `NoteIsActive`, `ErrNoteArchived`
- Produces: `CreateCapture` / `CompleteCapture` / `SetCaptureTarget` return `ErrNoteArchived` when target note is archived; `decideTarget` skips archived candidates

- [ ] **Step 1: Write failing tests**

```go
func TestCreateCaptureRejectsArchivedNote(t *testing.T) {
	// put note with DeletedAt set; CreateCapture → errors.Is(err, ErrNoteArchived)
}
func TestCompleteCaptureRejectsArchivedNote(t *testing.T) {
	// capture bound to archived note with audio uploaded; CompleteCapture → ErrNoteArchived
}
func TestDecideTargetSkipsArchivedNotes(t *testing.T) {
	// FakeRouter would receive only active candidates — assert router.Calls / candidate list
	// by using a FakeRouter that records candidates if you extend it, or assert
	// CompleteCapture with spoken append does not land on archived note id
}
```

Simplest route-candidate check: extend `FakeRouter` in `provider/fake.go` to record `LastCandidates []routing.Candidate`, then assert archived id absent.

- [ ] **Step 2: Run tests — expect FAIL**

- [ ] **Step 3: Implement**

In `CreateCapture`, after `GetNote`:

```go
if !NoteIsActive(note) {
	return nil, "", ErrNoteArchived
}
```

In `CompleteCapture`, when loading the target note (after routing has set NoteID):

```go
if !NoteIsActive(note) {
	return nil, ErrNoteArchived
}
```

Also reject early if capture already has NoteID pointing at archived note before append.

In `SetCaptureTarget` when `noteID != ""`, after GetNote check `NoteIsActive`.

In `decideTarget`, when building candidates:

```go
if !NoteIsActive(n) {
	continue
}
```

- [ ] **Step 4: Run** `go test ./internal/service/ -count=1` — PASS

- [ ] **Step 5: Commit**

```bash
git add backend/internal/service/ backend/internal/provider/fake.go
git commit -m "Reject captures and routing against archived notes."
```

---

### Task 4: HTTP handlers

**Files:**
- Modify: `backend/internal/handler/notes.go`
- Modify: `backend/internal/handler/handler_test.go`
- Modify: `backend/internal/handler/captures.go` (map `ErrNoteArchived` → 409)

**Interfaces:**
- Produces:
  - `GET /v1/notes?status=archived` → `ListArchivedNotes`
  - `POST /v1/notes/{id}/restore` → `RestoreNote`
  - `DELETE /v1/notes/{id}` → `ArchiveNote` / `DeleteNote` → 204
  - `DELETE /v1/notes/{id}?permanent=true` → `PermanentlyDeleteNote` → 204
  - Map `ErrNoteArchived` → 409, `ErrNoteNotArchived` → 400, `ErrNotFound` → 404

- [ ] **Step 1: Write failing handler tests**

```go
// POST create note, DELETE → 204, GET /v1/notes → empty, GET /v1/notes?status=archived → 1
// POST /v1/notes/{id}/restore → 200, GET /v1/notes → 1
// DELETE without archive then ?permanent=true → 400
// Archive then permanent → 204, both lists empty
// PATCH archived → 409
```

Routing for restore: today `handleNoteDetail` treats path after `/v1/notes/` as noteID. Change to split:

```go
// path like "/{id}" or "/{id}/restore"
parts := strings.Split(strings.Trim(path, "/"), "/")
if len(parts) == 2 && parts[1] == "restore" && r.Method == http.MethodPost {
	h.restoreNote(w, r, userID, parts[0])
	return
}
if len(parts) == 1 {
	h.handleNoteDetail(w, r, userID, parts[0])
	return
}
```

- [ ] **Step 2: Run handler tests — FAIL**

- [ ] **Step 3: Implement handler changes**

`listNotes`:

```go
if r.URL.Query().Get("status") == "archived" {
	notes, err = h.notesService.ListArchivedNotes(...)
} else {
	notes, err = h.notesService.ListNotes(...)
}
```

`deleteNote`:

```go
if r.URL.Query().Get("permanent") == "true" {
	err = h.notesService.PermanentlyDeleteNote(...)
	// map ErrNoteNotArchived → 400
} else {
	err = h.notesService.DeleteNote(...) // archive
}
```

`updateNote` / captures create: if `errors.Is(err, service.ErrNoteArchived)` → 409.

- [ ] **Step 4: Run** `go test ./internal/handler/ ./internal/service/ -count=1` — PASS

- [ ] **Step 5: Commit**

```bash
git add backend/internal/handler/
git commit -m "Expose note archive, restore, and permanent delete over HTTP."
```

---

### Task 5: Frontend Archive UI

**Files:**
- Modify: `frontend/js/api.js`
- Modify: `frontend/index.html`
- Modify: `frontend/js/notes.js`
- Modify: `frontend/css/styles.css`
- Modify: `frontend/sw.js` (bump `v8` → `v9`)

**Interfaces:**
- Produces: `api.getNotes({ archived: true })`, `api.restoreNote(id)`, `api.deleteNote(id)` (archive), `api.permanentlyDeleteNote(id)`

- [ ] **Step 1: API client**

```js
async getNotes(opts = {}) {
	const q = opts.archived ? '?status=archived' : '';
	return this.request('/v1/notes' + q);
}
async restoreNote(noteId) {
	return this.request(`/v1/notes/${noteId}/restore`, { method: 'POST' });
}
async permanentlyDeleteNote(noteId) {
	return this.request(`/v1/notes/${noteId}?permanent=true`, { method: 'DELETE' });
}
// existing deleteNote already DELETEs without query → archives
```

- [ ] **Step 2: HTML**

Notes screen — add tabs under header:

```html
<div class="notes-tabs">
  <button id="notes-tab-active" class="notes-tab active" type="button">Notes</button>
  <button id="notes-tab-archive" class="notes-tab" type="button">Archive</button>
</div>
```

Note detail header — add:

```html
<button id="delete-note-btn" class="btn btn-danger btn-ghost hidden" type="button">Delete</button>
<button id="restore-note-btn" class="btn btn-secondary hidden" type="button">Restore</button>
<button id="purge-note-btn" class="btn btn-danger hidden" type="button">Delete forever</button>
```

- [ ] **Step 3: notes.js behavior**

- Track `this.viewingArchive = false` and `this.currentNoteArchived = false`
- Tab clicks → `showNotesScreen()` vs `showArchiveScreen()`
- `showArchiveScreen`: `api.getNotes({archived:true})`, render with subtitle `Deletes in N days` using `purge_after`
- `displayNoteDetail`: if `note.deleted_at`, set readOnly on inputs, hide Save, show Restore + Delete forever; else show Delete
- Delete click → `ui.confirm('Move to Archive? You can restore for 30 days.')` → `api.deleteNote` → toast → `showNotesScreen`
- Restore → `api.restoreNote` → toast → `showNoteDetail` (active)
- Delete forever → confirm → `api.permanentlyDeleteNote` → `showArchiveScreen`
- `handleBackToNotes`: return to the tab that was active (`viewingArchive`)

Days remaining helper:

```js
daysUntilPurge(purgeAfter) {
	const ms = new Date(purgeAfter) - Date.now();
	return Math.max(0, Math.ceil(ms / 86400000));
}
```

- [ ] **Step 4: CSS**

Minimal tab styles (underline active tab; archive meta muted). No new card chrome beyond existing list patterns.

- [ ] **Step 5: Bump SW cache to v9**

- [ ] **Step 6: Manual sanity** — `node`/bun parse-check edited JS files

- [ ] **Step 7: Commit**

```bash
git add frontend/
git commit -m "Add Archive tab with restore and delete-forever for notes."
```

---

### Task 6: Deploy and smoke

**Files:** none (push `main`)

- [ ] **Step 1: Push and watch workflows**

```bash
git push
gh run watch $(gh run list --workflow=deploy-backend.yaml --limit 1 --json databaseId -q '.[0].databaseId') --exit-status
gh run watch $(gh run list --workflow=deploy-frontend.yaml --limit 1 --json databaseId -q '.[0].databaseId') --exit-status
```

- [ ] **Step 2: API smoke** (create throwaway Cognito user or use existing SRP helper)

1. Create note → DELETE → `GET /v1/notes` empty of it → `GET /v1/notes?status=archived` contains it  
2. POST restore → active again  
3. Archive → DELETE `?permanent=true` → gone from both  
4. Confirm live frontend shows tabs and Delete button after hard refresh  

- [ ] **Step 3: Commit nothing further unless smoke finds a bug**

---

## Spec coverage checklist

| Spec requirement | Task |
|------------------|------|
| `deleted_at` / `purge_after` | 1, 2 |
| Active vs archived list | 2, 4, 5 |
| DELETE archives | 2, 4, 5 |
| Restore | 2, 4, 5 |
| Permanent + capture cascade | 1, 2, 4, 5 |
| 30-day lazy purge | 2 |
| Hidden from match/routing | 2, 3 |
| 409 on PATCH/capture archived | 2, 3, 4 |
| Archive UI tabs + buttons | 5 |
| No Settings retention / no purge Lambda | Global constraints |

## Placeholder / consistency self-review

- Method names locked: `ArchiveNote`, `RestoreNote`, `PermanentlyDeleteNote`, `ListArchivedNotes`, `NoteIsActive`, `ErrNoteArchived`, `ErrNoteNotArchived`
- `DeleteNote` remains as archive wrapper for existing `api.deleteNote` / handler DELETE
- `DeleteCapture` added before cascade tests in Task 2
- No scheduled worker, no Settings wiring
