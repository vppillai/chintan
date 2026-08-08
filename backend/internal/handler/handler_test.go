package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/vppillai/chintan/backend/internal/handler"
	"github.com/vppillai/chintan/backend/internal/middleware"
	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/service"
)

func TestHealthHandler(t *testing.T) {
	store := repository.NewMemoryStore()
	objects := repository.NewMemoryObjects()
	notesService := service.NewNotesService(store, objects)
	settingsService := service.NewSettingsService(store)

	captureService := service.NewCaptureService(store, objects, nil, nil) // nil providers for handler tests
	router := handler.NewRouter(notesService, settingsService, captureService, nil, "http://localhost:3000")

	req := httptest.NewRequest("GET", "/v1/health", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var response map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if response["status"] != "ok" {
		t.Errorf("expected status=ok, got %s", response["status"])
	}
}

func TestSettingsHandler(t *testing.T) {
	store := repository.NewMemoryStore()
	objects := repository.NewMemoryObjects()
	notesService := service.NewNotesService(store, objects)
	settingsService := service.NewSettingsService(store)

	captureService := service.NewCaptureService(store, objects, nil, nil) // nil providers for handler tests
	router := handler.NewRouter(notesService, settingsService, captureService, nil, "http://localhost:3000")

	t.Run("GET settings returns defaults", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/v1/settings", nil)
		req = req.WithContext(middleware.WithUserID(req.Context(), "user1"))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != 200 {
			t.Errorf("expected 200, got %d", w.Code)
		}

		var settings model.Settings
		if err := json.Unmarshal(w.Body.Bytes(), &settings); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}

		if settings.CleanupMode != model.CleanupFaithful {
			t.Errorf("expected faithful cleanup mode, got %s", settings.CleanupMode)
		}
		if settings.RetentionDays != 0 {
			t.Errorf("expected 0 retention days, got %d", settings.RetentionDays)
		}
	})

	t.Run("PUT settings updates", func(t *testing.T) {
		newSettings := model.Settings{
			CleanupMode:   model.CleanupPolished,
			RetentionDays: 30,
		}
		body, _ := json.Marshal(newSettings)

		req := httptest.NewRequest("PUT", "/v1/settings", bytes.NewReader(body))
		req = req.WithContext(middleware.WithUserID(req.Context(), "user1"))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != 200 {
			t.Errorf("expected 200, got %d", w.Code)
		}

		// Verify it was saved
		req = httptest.NewRequest("GET", "/v1/settings", nil)
		req = req.WithContext(middleware.WithUserID(req.Context(), "user1"))
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)

		var settings model.Settings
		json.Unmarshal(w.Body.Bytes(), &settings)

		if settings.CleanupMode != model.CleanupPolished {
			t.Errorf("expected polished cleanup mode, got %s", settings.CleanupMode)
		}
		if settings.RetentionDays != 30 {
			t.Errorf("expected 30 retention days, got %d", settings.RetentionDays)
		}
	})

	t.Run("requires auth", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/v1/settings", nil)
		// No userID in context
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != 401 {
			t.Errorf("expected 401, got %d", w.Code)
		}
	})
}

func TestNotesHandler(t *testing.T) {
	store := repository.NewMemoryStore()
	objects := repository.NewMemoryObjects()
	notesService := service.NewNotesService(store, objects)
	settingsService := service.NewSettingsService(store)

	captureService := service.NewCaptureService(store, objects, nil, nil) // nil providers for handler tests
	router := handler.NewRouter(notesService, settingsService, captureService, nil, "http://localhost:3000")

	t.Run("POST creates note", func(t *testing.T) {
		createReq := map[string]interface{}{
			"title":   "My Test Note",
			"aliases": []string{"test", "note"},
		}
		body, _ := json.Marshal(createReq)

		req := httptest.NewRequest("POST", "/v1/notes", bytes.NewReader(body))
		req = req.WithContext(middleware.WithUserID(req.Context(), "user1"))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != 201 {
			t.Errorf("expected 201, got %d", w.Code)
		}

		var response model.NoteIndex
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}

		if response.Title != "My Test Note" {
			t.Errorf("expected title 'My Test Note', got %s", response.Title)
		}
		if len(response.Aliases) != 2 || response.Aliases[0] != "test" || response.Aliases[1] != "note" {
			t.Errorf("expected aliases [test, note], got %v", response.Aliases)
		}
		if response.ID == "" {
			t.Errorf("expected non-empty ID")
		}
	})

	t.Run("GET lists notes", func(t *testing.T) {
		// First create a note
		createReq := map[string]interface{}{
			"title": "Listed Note",
		}
		body, _ := json.Marshal(createReq)

		req := httptest.NewRequest("POST", "/v1/notes", bytes.NewReader(body))
		req = req.WithContext(middleware.WithUserID(req.Context(), "user1"))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		// Now list notes
		req = httptest.NewRequest("GET", "/v1/notes", nil)
		req = req.WithContext(middleware.WithUserID(req.Context(), "user1"))
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != 200 {
			t.Errorf("expected 200, got %d", w.Code)
		}

		var notes []model.NoteIndex
		if err := json.Unmarshal(w.Body.Bytes(), &notes); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}

		// Should have at least the notes we created (plus any from previous tests)
		found := false
		for _, note := range notes {
			if note.Title == "Listed Note" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected to find 'Listed Note' in list")
		}
	})

	t.Run("GET specific note", func(t *testing.T) {
		// First create a note
		createReq := map[string]interface{}{
			"title": "Specific Note",
		}
		body, _ := json.Marshal(createReq)

		req := httptest.NewRequest("POST", "/v1/notes", bytes.NewReader(body))
		req = req.WithContext(middleware.WithUserID(req.Context(), "user1"))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		var created model.NoteIndex
		json.Unmarshal(w.Body.Bytes(), &created)

		// Now get specific note
		req = httptest.NewRequest("GET", "/v1/notes/"+created.ID, nil)
		req = req.WithContext(middleware.WithUserID(req.Context(), "user1"))
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != 200 {
			t.Errorf("expected 200, got %d", w.Code)
		}

		var note model.NoteIndex
		if err := json.Unmarshal(w.Body.Bytes(), &note); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}

		if note.Title != "Specific Note" {
			t.Errorf("expected title 'Specific Note', got %s", note.Title)
		}
	})

	t.Run("PATCH updates note", func(t *testing.T) {
		// First create a note
		createReq := map[string]interface{}{
			"title": "Original Title",
		}
		body, _ := json.Marshal(createReq)

		req := httptest.NewRequest("POST", "/v1/notes", bytes.NewReader(body))
		req = req.WithContext(middleware.WithUserID(req.Context(), "user1"))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		var created model.NoteIndex
		json.Unmarshal(w.Body.Bytes(), &created)

		// Now patch the note
		patchReq := map[string]interface{}{
			"title": "Updated Title",
			"body":  "This is the body content",
		}
		body, _ = json.Marshal(patchReq)

		req = httptest.NewRequest("PATCH", "/v1/notes/"+created.ID, bytes.NewReader(body))
		req = req.WithContext(middleware.WithUserID(req.Context(), "user1"))
		req.Header.Set("Content-Type", "application/json")
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != 200 {
			t.Errorf("expected 200, got %d", w.Code)
		}

		var updated model.NoteIndex
		if err := json.Unmarshal(w.Body.Bytes(), &updated); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}

		if updated.Title != "Updated Title" {
			t.Errorf("expected title 'Updated Title', got %s", updated.Title)
		}
	})

	t.Run("GET missing note returns 404", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/v1/notes/does-not-exist", nil)
		req = req.WithContext(middleware.WithUserID(req.Context(), "user1"))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != 404 {
			t.Errorf("expected 404 for missing note, got %d", w.Code)
		}
	})

	t.Run("PATCH missing note returns 404", func(t *testing.T) {
		patchReq := map[string]interface{}{
			"title": "Updated Title",
		}
		body, _ := json.Marshal(patchReq)

		req := httptest.NewRequest("PATCH", "/v1/notes/does-not-exist", bytes.NewReader(body))
		req = req.WithContext(middleware.WithUserID(req.Context(), "user1"))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != 404 {
			t.Errorf("expected 404 for missing note, got %d", w.Code)
		}
	})

	t.Run("DELETE archives note", func(t *testing.T) {
		// First create a note
		createReq := map[string]interface{}{
			"title": "To Be Deleted",
		}
		body, _ := json.Marshal(createReq)

		req := httptest.NewRequest("POST", "/v1/notes", bytes.NewReader(body))
		req = req.WithContext(middleware.WithUserID(req.Context(), "user1"))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		var created model.NoteIndex
		json.Unmarshal(w.Body.Bytes(), &created)

		// Now delete (archive) the note
		req = httptest.NewRequest("DELETE", "/v1/notes/"+created.ID, nil)
		req = req.WithContext(middleware.WithUserID(req.Context(), "user1"))
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != 204 {
			t.Errorf("expected 204, got %d", w.Code)
		}

		// Verify note is still accessible but archived
		req = httptest.NewRequest("GET", "/v1/notes/"+created.ID, nil)
		req = req.WithContext(middleware.WithUserID(req.Context(), "user1"))
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != 200 {
			t.Errorf("expected 200 for archived note, got %d", w.Code)
		}

		// Verify note has archive fields set
		var noteDetail map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &noteDetail)
		if noteDetail["deleted_at"] == nil || noteDetail["deleted_at"] == "" {
			t.Errorf("expected archived note to have deleted_at set")
		}
		if noteDetail["purge_after"] == nil || noteDetail["purge_after"] == "" {
			t.Errorf("expected archived note to have purge_after set")
		}

		// Verify it's not in the active notes list
		req = httptest.NewRequest("GET", "/v1/notes", nil)
		req = req.WithContext(middleware.WithUserID(req.Context(), "user1"))
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)

		var notes []model.NoteIndex
		json.Unmarshal(w.Body.Bytes(), &notes)
		for _, note := range notes {
			if note.ID == created.ID {
				t.Errorf("archived note should not appear in active notes list")
			}
		}
	})
}

func TestNotesMatchHandler(t *testing.T) {
	store := repository.NewMemoryStore()
	objects := repository.NewMemoryObjects()
	notesService := service.NewNotesService(store, objects)
	settingsService := service.NewSettingsService(store)

	captureService := service.NewCaptureService(store, objects, nil, nil) // nil providers for handler tests
	router := handler.NewRouter(notesService, settingsService, captureService, nil, "http://localhost:3000")

	// Create some test notes
	testNotes := []map[string]interface{}{
		{"title": "Machine Learning Notes", "aliases": []string{"ml", "ai"}},
		{"title": "Go Programming", "aliases": []string{"golang"}},
		{"title": "Database Design", "aliases": []string{"db", "sql"}},
	}

	ctx := middleware.WithUserID(context.Background(), "user1")
	for _, noteData := range testNotes {
		body, _ := json.Marshal(noteData)
		req := httptest.NewRequest("POST", "/v1/notes", bytes.NewReader(body))
		req = req.WithContext(ctx)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
	}

	t.Run("high confidence match sets auto_select_id", func(t *testing.T) {
		matchReq := map[string]string{
			"query": "Machine Learning Notes", // Exact match should be high confidence
		}
		body, _ := json.Marshal(matchReq)

		req := httptest.NewRequest("POST", "/v1/notes/match", bytes.NewReader(body))
		req = req.WithContext(middleware.WithUserID(req.Context(), "user1"))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != 200 {
			t.Errorf("expected 200, got %d", w.Code)
		}

		var response map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}

		candidates, ok := response["candidates"].([]interface{})
		if !ok || len(candidates) == 0 {
			t.Fatalf("expected candidates array, got %v", response)
		}

		autoSelectID, hasAutoSelect := response["auto_select_id"]
		if !hasAutoSelect {
			t.Errorf("expected auto_select_id for high confidence match")
		}

		firstCandidate := candidates[0].(map[string]interface{})
		expectedID := firstCandidate["note_id"].(string)
		actualID := autoSelectID.(string)
		if actualID != expectedID {
			t.Errorf("auto_select_id should match first candidate ID, expected %s, got %s", expectedID, actualID)
		}
	})

	t.Run("low confidence match omits auto_select_id", func(t *testing.T) {
		matchReq := map[string]string{
			"query": "programming", // Vague query should be ambiguous
		}
		body, _ := json.Marshal(matchReq)

		req := httptest.NewRequest("POST", "/v1/notes/match", bytes.NewReader(body))
		req = req.WithContext(middleware.WithUserID(req.Context(), "user1"))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != 200 {
			t.Errorf("expected 200, got %d", w.Code)
		}

		var response map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}

		candidates, ok := response["candidates"].([]interface{})
		if !ok || len(candidates) == 0 {
			t.Fatalf("expected candidates array, got %v", response)
		}

		_, hasAutoSelect := response["auto_select_id"]
		if hasAutoSelect {
			t.Errorf("should not have auto_select_id for ambiguous match")
		}
	})

	t.Run("requires auth", func(t *testing.T) {
		matchReq := map[string]string{
			"query": "test",
		}
		body, _ := json.Marshal(matchReq)

		req := httptest.NewRequest("POST", "/v1/notes/match", bytes.NewReader(body))
		// No userID in context
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != 401 {
			t.Errorf("expected 401, got %d", w.Code)
		}
	})
}

func TestCORSHandling(t *testing.T) {
	store := repository.NewMemoryStore()
	objects := repository.NewMemoryObjects()
	notesService := service.NewNotesService(store, objects)
	settingsService := service.NewSettingsService(store)

	captureService := service.NewCaptureService(store, objects, nil, nil) // nil providers for handler tests
	router := handler.NewRouter(notesService, settingsService, captureService, nil, "http://localhost:3000")

	t.Run("preflight request", func(t *testing.T) {
		req := httptest.NewRequest("OPTIONS", "/v1/notes", nil)
		req.Header.Set("Origin", "http://localhost:3000")
		req.Header.Set("Access-Control-Request-Method", "POST")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != 200 {
			t.Errorf("expected 200 for preflight, got %d", w.Code)
		}

		corsOrigin := w.Header().Get("Access-Control-Allow-Origin")
		if corsOrigin != "http://localhost:3000" {
			t.Errorf("expected CORS origin http://localhost:3000, got %s", corsOrigin)
		}
	})

	t.Run("actual request with CORS", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/v1/health", nil)
		req.Header.Set("Origin", "http://localhost:3000")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		corsOrigin := w.Header().Get("Access-Control-Allow-Origin")
		if corsOrigin != "http://localhost:3000" {
			t.Errorf("expected CORS origin http://localhost:3000, got %s", corsOrigin)
		}
	})

	t.Run("uses ALLOWED_ORIGIN env when parameter empty", func(t *testing.T) {
		t.Setenv("ALLOWED_ORIGIN", "https://app.example.com")
		captureService := service.NewCaptureService(store, objects, nil, nil) // nil providers for handler tests
		envRouter := handler.NewRouter(notesService, settingsService, captureService, nil, "")

		req := httptest.NewRequest("OPTIONS", "/v1/notes", nil)
		req.Header.Set("Origin", "https://app.example.com")
		req.Header.Set("Access-Control-Request-Method", "POST")
		w := httptest.NewRecorder()
		envRouter.ServeHTTP(w, req)

		if w.Code != 200 {
			t.Errorf("expected 200 for preflight, got %d", w.Code)
		}

		corsOrigin := w.Header().Get("Access-Control-Allow-Origin")
		if corsOrigin != "https://app.example.com" {
			t.Errorf("expected CORS origin from env, got %s", corsOrigin)
		}
	})
}

func TestNotesArchiveHTTP(t *testing.T) {
	store := repository.NewMemoryStore()
	objects := repository.NewMemoryObjects()
	notesService := service.NewNotesService(store, objects)
	settingsService := service.NewSettingsService(store)
	captureService := service.NewCaptureService(store, objects, nil, nil)
	router := handler.NewRouter(notesService, settingsService, captureService, nil, "http://localhost:3000")

	create := func(title string) model.NoteIndex {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"title": title})
		req := httptest.NewRequest("POST", "/v1/notes", bytes.NewReader(body))
		req = req.WithContext(middleware.WithUserID(req.Context(), "user1"))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 201 {
			t.Fatalf("create: %d %s", w.Code, w.Body.String())
		}
		var note model.NoteIndex
		json.Unmarshal(w.Body.Bytes(), &note)
		return note
	}

	t.Run("archive list restore and permanent", func(t *testing.T) {
		note := create("Archive Me")

		req := httptest.NewRequest("DELETE", "/v1/notes/"+note.ID, nil)
		req = req.WithContext(middleware.WithUserID(req.Context(), "user1"))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 204 {
			t.Fatalf("archive status %d", w.Code)
		}

		req = httptest.NewRequest("GET", "/v1/notes", nil)
		req = req.WithContext(middleware.WithUserID(req.Context(), "user1"))
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)
		var active []model.NoteIndex
		json.Unmarshal(w.Body.Bytes(), &active)
		for _, n := range active {
			if n.ID == note.ID {
				t.Fatal("archived note in active list")
			}
		}

		req = httptest.NewRequest("GET", "/v1/notes?status=archived", nil)
		req = req.WithContext(middleware.WithUserID(req.Context(), "user1"))
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)
		var archived []model.NoteIndex
		json.Unmarshal(w.Body.Bytes(), &archived)
		found := false
		for _, n := range archived {
			if n.ID == note.ID {
				found = true
			}
		}
		if !found {
			t.Fatal("note missing from archived list")
		}

		req = httptest.NewRequest("POST", "/v1/notes/"+note.ID+"/restore", nil)
		req = req.WithContext(middleware.WithUserID(req.Context(), "user1"))
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("restore status %d", w.Code)
		}

		req = httptest.NewRequest("DELETE", "/v1/notes/"+note.ID+"?permanent=true", nil)
		req = req.WithContext(middleware.WithUserID(req.Context(), "user1"))
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 400 {
			t.Fatalf("permanent while active want 400 got %d", w.Code)
		}

		req = httptest.NewRequest("DELETE", "/v1/notes/"+note.ID, nil)
		req = req.WithContext(middleware.WithUserID(req.Context(), "user1"))
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)

		req = httptest.NewRequest("DELETE", "/v1/notes/"+note.ID+"?permanent=true", nil)
		req = req.WithContext(middleware.WithUserID(req.Context(), "user1"))
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 204 {
			t.Fatalf("permanent status %d", w.Code)
		}
	})

	t.Run("PATCH archived returns 409", func(t *testing.T) {
		note := create("Lock Me")
		req := httptest.NewRequest("DELETE", "/v1/notes/"+note.ID, nil)
		req = req.WithContext(middleware.WithUserID(req.Context(), "user1"))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		body, _ := json.Marshal(map[string]string{"title": "Nope"})
		req = httptest.NewRequest("PATCH", "/v1/notes/"+note.ID, bytes.NewReader(body))
		req = req.WithContext(middleware.WithUserID(req.Context(), "user1"))
		req.Header.Set("Content-Type", "application/json")
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 409 {
			t.Fatalf("want 409 got %d body %s", w.Code, w.Body.String())
		}
	})
}
