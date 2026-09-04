package handler_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/handler"
	"github.com/vppillai/chintan/backend/internal/model"
)

func TestHealthIsLiveness(t *testing.T) {
	h := newHarness(t)

	w := h.do(t, http.MethodGet, "/v1/health", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body map[string]string
	decodeInto(t, w, &body)
	if body["status"] != "ok" {
		t.Errorf("status = %q, want ok", body["status"])
	}
}

// Readiness is a real probe, not the static answer v1 served for both questions.
func TestReadinessProbesDependencies(t *testing.T) {
	t.Run("reachable", func(t *testing.T) {
		h := newHarness(t)
		w := h.do(t, http.MethodGet, "/v1/health/ready", "", nil)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
		}
		var out struct {
			Status string `json:"status"`
			Checks map[string]struct {
				OK        bool  `json:"ok"`
				LatencyMS int64 `json:"latency_ms"`
			} `json:"checks"`
		}
		decodeInto(t, w, &out)
		if out.Status != "ok" {
			t.Errorf("status = %q", out.Status)
		}
		for _, dep := range []string{"dynamodb", "s3"} {
			if _, ok := out.Checks[dep]; !ok {
				t.Errorf("no probe reported for %s", dep)
			}
		}
	})

	t.Run("a broken dependency is reported, not hidden", func(t *testing.T) {
		h := newHarness(t, withBrokenStore())
		w := h.do(t, http.MethodGet, "/v1/health/ready", "", nil)
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", w.Code)
		}
		body := w.Body.String()
		// The dependency's own error text names the table. It is logged, and it
		// must not be serialised.
		if strings.Contains(body, "connection refused") {
			t.Fatalf("response leaked the dependency error: %s", body)
		}
	})
}

// ------------------------------------------------------------------ settings

func TestSettingsValidatesStoresAndReturnsWhatWasStored(t *testing.T) {
	h := newHarness(t)

	t.Run("defaults are complete", func(t *testing.T) {
		w := h.do(t, http.MethodGet, "/v1/settings", "user1", nil)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d", w.Code)
		}
		var s handler.Settings
		decodeInto(t, w, &s)
		if s.CleanupMode != string(model.CleanupFaithful) {
			t.Errorf("cleanup_mode = %q", s.CleanupMode)
		}
		if s.Theme != string(model.ThemeInk) {
			t.Errorf("theme = %q, want the default rather than an empty string", s.Theme)
		}
	})

	t.Run("stores and reads back", func(t *testing.T) {
		w := h.do(t, http.MethodPut, "/v1/settings", "user1", map[string]any{
			"cleanup_mode":   "polished",
			"retention_days": 30,
			"theme":          "nocturne",
		})
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
		}
		var put handler.Settings
		decodeInto(t, w, &put)
		if put.RetentionDays != 30 || put.Theme != "nocturne" {
			t.Fatalf("stored settings = %+v", put)
		}

		w = h.do(t, http.MethodGet, "/v1/settings", "user1", nil)
		var got handler.Settings
		decodeInto(t, w, &got)
		if got != put {
			t.Fatalf("GET returned %+v, PUT returned %+v", got, put)
		}
	})

	// v1 stored whatever it was sent and echoed the request back, so a value the
	// server did not understand looked accepted.
	t.Run("an unknown value is refused, not coerced silently", func(t *testing.T) {
		for _, body := range []map[string]any{
			{"theme": "purple"},
			{"cleanup_mode": "creative"},
			{"retention_days": -1},
			{"retention_days": 100000},
		} {
			w := h.do(t, http.MethodPut, "/v1/settings", "user1", body)
			if w.Code != http.StatusBadRequest {
				t.Errorf("PUT %v: status = %d, want 400", body, w.Code)
			}
			problemOf(t, w)
		}
	})

	t.Run("requires auth", func(t *testing.T) {
		w := h.do(t, http.MethodGet, "/v1/settings", "", nil)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", w.Code)
		}
		problemOf(t, w)
	})
}

// --------------------------------------------------------------------- notes

func TestNotesLifecycle(t *testing.T) {
	h := newHarness(t)

	note := h.createNote(t, "user1", "My Test Note", map[string]any{
		"aliases": []string{"test", "note"},
		"tags":    []string{"Work", "work ", "roof"},
	})
	if note.ID == "" {
		t.Fatal("created note has no id")
	}
	if len(note.Aliases) != 2 {
		t.Errorf("aliases = %v", note.Aliases)
	}
	// Tags are folded to a canonical form, so "Work" and "work " are one tag.
	if len(note.Tags) != 2 {
		t.Errorf("tags = %v, want the duplicates folded", note.Tags)
	}
	if note.Archived {
		t.Error("a new note is not archived")
	}

	t.Run("detail carries the body and the captures", func(t *testing.T) {
		version := note.Version
		w := h.do(t, http.MethodPatch, "/v1/notes/"+note.ID, "user1", map[string]any{
			"version": version,
			"body":    "the body",
		})
		if w.Code != http.StatusOK {
			t.Fatalf("patch: status = %d body = %s", w.Code, w.Body.String())
		}

		w = h.do(t, http.MethodGet, "/v1/notes/"+note.ID, "user1", nil)
		if w.Code != http.StatusOK {
			t.Fatalf("get: status = %d", w.Code)
		}
		var detail handler.NoteDetail
		decodeInto(t, w, &detail)
		if detail.Body != "the body" {
			t.Errorf("body = %q", detail.Body)
		}
		if detail.Captures == nil {
			t.Error("captures is null; it must be an empty array")
		}
	})

	t.Run("a note never carries its storage keys", func(t *testing.T) {
		w := h.do(t, http.MethodGet, "/v1/notes/"+note.ID, "user1", nil)
		for _, leak := range []string{"s3_markdown_key", "s3_meta_key", "tenants/"} {
			if strings.Contains(w.Body.String(), leak) {
				t.Fatalf("response leaked %q: %s", leak, w.Body.String())
			}
		}
	})

	t.Run("archive, list, restore, purge", func(t *testing.T) {
		target := h.createNote(t, "user1", "Archive Me", nil)

		if w := h.do(t, http.MethodDelete, "/v1/notes/"+target.ID, "user1", nil); w.Code != http.StatusNoContent {
			t.Fatalf("archive: status = %d", w.Code)
		}

		active := listNotes(t, h, "user1", "/v1/notes")
		for _, n := range active {
			if n.ID == target.ID {
				t.Fatal("an archived note appeared in the active list")
			}
		}

		archived := listNotes(t, h, "user1", "/v1/notes?state=archived")
		found := false
		for _, n := range archived {
			if n.ID == target.ID {
				found = true
				if !n.Archived {
					t.Error("an archived note is not marked archived")
				}
				if n.PurgeAfter == nil || *n.PurgeAfter == "" {
					t.Error("an archived note has no purge_after")
				}
			}
		}
		if !found {
			t.Fatal("the archived note is missing from the archived list")
		}

		if w := h.do(t, http.MethodPost, "/v1/notes/"+target.ID+"/restore", "user1", nil); w.Code != http.StatusOK {
			t.Fatalf("restore: status = %d", w.Code)
		}

		// Purging an active note is refused: archive is the safety catch.
		if w := h.do(t, http.MethodDelete, "/v1/notes/"+target.ID+"/permanent", "user1", nil); w.Code != http.StatusBadRequest {
			t.Fatalf("purge while active: status = %d, want 400", w.Code)
		}

		h.do(t, http.MethodDelete, "/v1/notes/"+target.ID, "user1", nil)
		if w := h.do(t, http.MethodDelete, "/v1/notes/"+target.ID+"/permanent", "user1", nil); w.Code != http.StatusNoContent {
			t.Fatalf("purge: status = %d", w.Code)
		}
	})

	t.Run("editing an archived note is a conflict", func(t *testing.T) {
		locked := h.createNote(t, "user1", "Lock Me", nil)
		h.do(t, http.MethodDelete, "/v1/notes/"+locked.ID, "user1", nil)

		w := h.do(t, http.MethodPatch, "/v1/notes/"+locked.ID, "user1", map[string]any{
			"version": locked.Version,
			"title":   "Nope",
		})
		if w.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409 (body=%s)", w.Code, w.Body.String())
		}
		problemOf(t, w)
	})

	t.Run("a missing note is 404, not 500", func(t *testing.T) {
		for _, tc := range []struct{ method, path string }{
			{http.MethodGet, "/v1/notes/does-not-exist"},
			{http.MethodDelete, "/v1/notes/does-not-exist"},
			{http.MethodPost, "/v1/notes/does-not-exist/restore"},
		} {
			w := h.do(t, tc.method, tc.path, "user1", nil)
			if w.Code != http.StatusNotFound {
				t.Errorf("%s %s: status = %d, want 404", tc.method, tc.path, w.Code)
			}
		}
		w := h.do(t, http.MethodPatch, "/v1/notes/does-not-exist", "user1", map[string]any{"version": 1})
		if w.Code != http.StatusNotFound {
			t.Errorf("PATCH missing note: status = %d, want 404", w.Code)
		}
	})
}

// Optimistic concurrency is the point of `version`: a voice append landing while
// the editor is open must surface as a conflict, not discard one of the writes.
func TestUpdateRequiresVersionAndReportsTheCurrentOne(t *testing.T) {
	h := newHarness(t)
	note := h.createNote(t, "user1", "Concurrent", nil)

	t.Run("version is required", func(t *testing.T) {
		w := h.do(t, http.MethodPatch, "/v1/notes/"+note.ID, "user1", map[string]any{"title": "No version"})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", w.Code)
		}
	})

	first := h.do(t, http.MethodPatch, "/v1/notes/"+note.ID, "user1", map[string]any{
		"version": note.Version,
		"title":   "First writer",
	})
	if first.Code != http.StatusOK {
		t.Fatalf("first write: status = %d body = %s", first.Code, first.Body.String())
	}
	var afterFirst handler.Note
	decodeInto(t, first, &afterFirst)

	second := h.do(t, http.MethodPatch, "/v1/notes/"+note.ID, "user1", map[string]any{
		"version": note.Version, // stale
		"title":   "Second writer",
	})
	if second.Code != http.StatusConflict {
		t.Fatalf("stale write: status = %d, want 409", second.Code)
	}
	p := problemOf(t, second)
	current, ok := p["current_version"].(float64)
	if !ok {
		t.Fatalf("409 has no current_version: %v", p)
	}
	if int64(current) != afterFirst.Version {
		t.Fatalf("current_version = %v, want %d", current, afterFirst.Version)
	}

	// The losing write left nothing behind.
	w := h.do(t, http.MethodGet, "/v1/notes/"+note.ID, "user1", nil)
	var detail handler.NoteDetail
	decodeInto(t, w, &detail)
	if detail.Title != "First writer" {
		t.Fatalf("title = %q; the losing write was applied", detail.Title)
	}
}

// -------------------------------------------------------------- pagination

func TestListsReturnTheEnvelopeAndRoundTripTheirCursor(t *testing.T) {
	h := newHarness(t)
	const total = 7
	for i := 0; i < total; i++ {
		h.createNote(t, "user1", fmt.Sprintf("Note %d", i), nil)
	}

	seen := map[string]bool{}
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 20 {
			t.Fatal("pagination did not terminate")
		}
		path := "/v1/notes?limit=2"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		w := h.do(t, http.MethodGet, path, "user1", nil)
		if w.Code != http.StatusOK {
			t.Fatalf("list: status = %d body = %s", w.Code, w.Body.String())
		}

		var got handler.Page[handler.Note]
		decodeInto(t, w, &got)
		if got.Items == nil {
			t.Fatal("items is null; the envelope always carries an array")
		}
		if len(got.Items) > 2 {
			t.Fatalf("page has %d notes, want at most the requested 2", len(got.Items))
		}
		for _, n := range got.Items {
			if seen[n.ID] {
				t.Fatalf("note %s appeared on two pages", n.ID)
			}
			seen[n.ID] = true
		}
		cursor = got.Cursor
		if cursor == "" {
			break
		}
	}
	if len(seen) != total {
		t.Fatalf("paged through %d notes, want %d", len(seen), total)
	}
}

func TestPaginationParametersAreBounded(t *testing.T) {
	h := newHarness(t)
	for _, path := range []string{
		"/v1/notes?limit=0",
		"/v1/notes?limit=201",
		"/v1/notes?limit=abc",
		"/v1/notes?cursor=" + strings.Repeat("A", 4096),
		"/v1/notes?cursor=not-base64!!",
	} {
		w := h.do(t, http.MethodGet, path, "user1", nil)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", path, w.Code)
		}
	}
}

// ----------------------------------------------------------------- limits

func TestOversizeBodiesAreRefused(t *testing.T) {
	h := newHarness(t)
	note := h.createNote(t, "user1", "Big", nil)

	huge := strings.Repeat("x", handler.MaxNoteBodyBytes+1)
	w := h.do(t, http.MethodPatch, "/v1/notes/"+note.ID, "user1", map[string]any{
		"version": note.Version,
		"body":    huge,
	})
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize body: status = %d, want 413", w.Code)
	}
	problemOf(t, w)

	// Past the transport cap the reader stops before any JSON is parsed, which
	// is the point: the bytes never reach the decoder.
	overCap := make([]byte, handler.MaxNoteRequestBytes+1024)
	for i := range overCap {
		overCap[i] = 'x'
	}
	w = h.do(t, http.MethodPost, "/v1/notes", "user1", overCap)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over the transport cap: status = %d, want 413", w.Code)
	}
}

func TestFieldCapsAreEnforced(t *testing.T) {
	h := newHarness(t)

	tooManyAliases := make([]string, handler.MaxAliases+1)
	for i := range tooManyAliases {
		tooManyAliases[i] = "a"
	}
	tooManyTags := make([]string, handler.MaxTags+1)
	for i := range tooManyTags {
		tooManyTags[i] = "t"
	}

	for name, payload := range map[string]map[string]any{
		"title too long":   {"title": strings.Repeat("t", handler.MaxTitleRunes+1)},
		"alias too long":   {"title": "ok", "aliases": []string{strings.Repeat("a", handler.MaxAliasRunes+1)}},
		"too many aliases": {"title": "ok", "aliases": tooManyAliases},
		"tag too long":     {"title": "ok", "tags": []string{strings.Repeat("t", handler.MaxTagRunes+1)}},
		"too many tags":    {"title": "ok", "tags": tooManyTags},
	} {
		w := h.do(t, http.MethodPost, "/v1/notes", "user1", payload)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (body=%s)", name, w.Code, w.Body.String())
		}
	}
}

// ----------------------------------------------------------------- routing

func TestMethodNotAllowedSetsAllow(t *testing.T) {
	h := newHarness(t)
	for _, tc := range []struct {
		method, path string
		want         []string
	}{
		{http.MethodPut, "/v1/notes", []string{"GET", "POST"}},
		{http.MethodPost, "/v1/health", []string{"GET"}},
		{http.MethodPut, "/v1/notes/n1", []string{"GET", "PATCH", "DELETE"}},
	} {
		w := h.do(t, tc.method, tc.path, "user1", nil)
		if w.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s %s: status = %d, want 405", tc.method, tc.path, w.Code)
		}
		allow := w.Header().Get("Allow")
		if allow == "" {
			t.Fatalf("%s %s: no Allow header; RFC 9110 requires one", tc.method, tc.path)
		}
		for _, m := range tc.want {
			if !strings.Contains(allow, m) {
				t.Errorf("%s %s: Allow = %q, missing %s", tc.method, tc.path, allow, m)
			}
		}
		problemOf(t, w)
	}
}

// Every error on this API is problem+json. v1 had four shapes, two of which were
// not JSON at all.
func TestUnknownRoutesUseTheOneErrorEnvelope(t *testing.T) {
	h := newHarness(t)
	w := h.do(t, http.MethodGet, "/v1/nope", "user1", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	p := problemOf(t, w)
	if p["instance"] != "/v1/nope" {
		t.Errorf("instance = %v, want the request path", p["instance"])
	}
	if strings.Contains(w.Body.String(), "404 page not found") {
		t.Error("the net/http default body is still being served")
	}
}

// The 404 fallback rewrites net/http's plain-text body. It must not stomp a
// 404 one of our own handlers wrote, which says something more specific.
func TestAHandlerNotFoundKeepsItsOwnDetail(t *testing.T) {
	h := newHarness(t)

	routed := problemOf(t, h.do(t, http.MethodGet, "/v1/notes/missing", "user1", nil))
	unrouted := problemOf(t, h.do(t, http.MethodGet, "/v1/no-such-collection", "user1", nil))

	if routed["detail"] == unrouted["detail"] {
		t.Fatalf("a missing note and a missing route read the same: %v", routed["detail"])
	}
	if routed["detail"] != "no such resource" {
		t.Errorf("detail = %v", routed["detail"])
	}
}

func TestEveryProblemCarriesTheCorrelationID(t *testing.T) {
	h := newHarness(t)
	w := h.do(t, http.MethodGet, "/v1/notes/missing", "user1", nil)
	p := problemOf(t, w)
	header := w.Header().Get("X-Correlation-Id")
	if header == "" {
		t.Fatal("no X-Correlation-Id response header")
	}
	if p["correlation_id"] != header {
		t.Fatalf("correlation_id = %v, want the header value %q", p["correlation_id"], header)
	}
}

// --------------------------------------------------------------- CORS

func TestCORS(t *testing.T) {
	h := newHarness(t)

	t.Run("preflight", func(t *testing.T) {
		w := h.do(t, http.MethodOptions, "/v1/notes", "", nil,
			[2]string{"Origin", "http://localhost:3000"},
			[2]string{"Access-Control-Request-Method", "POST"})
		if w.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204", w.Code)
		}
		if got := w.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:3000" {
			t.Errorf("allow-origin = %q", got)
		}
	})

	t.Run("a foreign origin is not reflected", func(t *testing.T) {
		w := h.do(t, http.MethodGet, "/v1/health", "", nil, [2]string{"Origin", "https://evil.example"})
		if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Fatalf("allow-origin = %q, want empty for a foreign origin", got)
		}
	})
}

func listNotes(t *testing.T, h *harness, userID, path string) []handler.Note {
	t.Helper()
	w := h.do(t, http.MethodGet, path, userID, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("%s: status = %d body = %s", path, w.Code, w.Body.String())
	}
	var got handler.Page[handler.Note]
	decodeInto(t, w, &got)
	return got.Items
}

// search_text is up to 32 KB per note, so it rides on the notes list only when
// asked for, and on nothing else.
func TestNotesListCarriesSearchTextOnlyWhenIncluded(t *testing.T) {
	h := newHarness(t)
	note := h.createNote(t, "user1", "Recorder", map[string]any{
		"body": "There is a BUG in the recorder.",
	})

	w := h.do(t, http.MethodGet, "/v1/notes", "user1", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list: status = %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "search_text") {
		t.Errorf("a plain list carries search_text: %s", w.Body.String())
	}

	w = h.do(t, http.MethodGet, "/v1/notes?include=search_text", "user1", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list with include: status = %d body = %s", w.Code, w.Body.String())
	}
	var page handler.Page[handler.Note]
	decodeInto(t, w, &page)
	if len(page.Items) != 1 || page.Items[0].SearchText != "there is a bug in the recorder." {
		t.Errorf("items = %+v, want the lowercased body as search_text", page.Items)
	}

	// Detail, create and update responses never carry it.
	w = h.do(t, http.MethodGet, "/v1/notes/"+note.ID, "user1", nil)
	if strings.Contains(w.Body.String(), "search_text") {
		t.Errorf("note detail carries search_text: %s", w.Body.String())
	}

	w = h.do(t, http.MethodGet, "/v1/notes?include=everything", "user1", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("include=everything: status = %d, want 400", w.Code)
	}
}
