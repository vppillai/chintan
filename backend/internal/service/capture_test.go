package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/provider"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// mockStore implements repository.Store for testing
type mockStore struct {
	settings map[string]model.Settings
	notes    map[string]model.NoteIndex
	captures map[string]model.CaptureIndex
}

func newMockStore() *mockStore {
	return &mockStore{
		settings: make(map[string]model.Settings),
		notes:    make(map[string]model.NoteIndex),
		captures: make(map[string]model.CaptureIndex),
	}
}

func (m *mockStore) GetSettings(ctx context.Context, userID string) (model.Settings, error) {
	if s, ok := m.settings[userID]; ok {
		return s, nil
	}
	return model.Settings{CleanupMode: model.CleanupFaithful, RetentionDays: 0}, nil
}

func (m *mockStore) PutSettings(ctx context.Context, userID string, s model.Settings) error {
	m.settings[userID] = s
	return nil
}

func (m *mockStore) ListNotes(ctx context.Context, userID string) ([]model.NoteIndex, error) {
	var notes []model.NoteIndex
	for _, n := range m.notes {
		if strings.HasPrefix(n.ID, userID+"/") {
			notes = append(notes, n)
		}
	}
	return notes, nil
}

func (m *mockStore) GetNote(ctx context.Context, userID, noteID string) (model.NoteIndex, error) {
	key := userID + "/" + noteID
	if n, ok := m.notes[key]; ok {
		return n, nil
	}
	return model.NoteIndex{}, repository.ErrNotFound
}

func (m *mockStore) PutNote(ctx context.Context, userID string, n model.NoteIndex) error {
	key := userID + "/" + n.ID
	m.notes[key] = n
	return nil
}

func (m *mockStore) DeleteNote(ctx context.Context, userID, noteID string) error {
	key := userID + "/" + noteID
	delete(m.notes, key)
	return nil
}

func (m *mockStore) PutCapture(ctx context.Context, c model.CaptureIndex) error {
	key := c.UserID + "/" + c.ID
	m.captures[key] = c
	return nil
}

func (m *mockStore) GetCapture(ctx context.Context, userID, captureID string) (model.CaptureIndex, error) {
	key := userID + "/" + captureID
	if c, ok := m.captures[key]; ok {
		return c, nil
	}
	return model.CaptureIndex{}, repository.ErrNotFound
}

func (m *mockStore) ListCapturesByNote(ctx context.Context, userID, noteID string) ([]model.CaptureIndex, error) {
	var out []model.CaptureIndex
	for _, c := range m.captures {
		if c.UserID == userID && c.NoteID == noteID {
			out = append(out, c)
		}
	}
	return out, nil
}

func (m *mockStore) UpdateCaptureStatus(ctx context.Context, userID, captureID string, status model.CaptureStatus, errMsg string) error {
	key := userID + "/" + captureID
	if c, ok := m.captures[key]; ok {
		c.Status = status
		c.Error = errMsg
		m.captures[key] = c
		return nil
	}
	return repository.ErrNotFound
}

func (m *mockStore) PutWebAuthnChallenge(ctx context.Context, c model.WebAuthnChallenge) error {
	return nil
}
func (m *mockStore) GetWebAuthnChallenge(ctx context.Context, challengeID string) (model.WebAuthnChallenge, error) {
	return model.WebAuthnChallenge{}, repository.ErrNotFound
}
func (m *mockStore) DeleteWebAuthnChallenge(ctx context.Context, challengeID string) error {
	return nil
}
func (m *mockStore) PutWebAuthnCredential(ctx context.Context, c model.WebAuthnCredential) error {
	return nil
}
func (m *mockStore) GetWebAuthnCredential(ctx context.Context, credentialID string) (model.WebAuthnCredential, error) {
	return model.WebAuthnCredential{}, repository.ErrNotFound
}
func (m *mockStore) ListWebAuthnCredentials(ctx context.Context) ([]model.WebAuthnCredential, error) {
	return nil, nil
}
func (m *mockStore) ListWebAuthnCredentialsByUser(ctx context.Context, userID string) ([]model.WebAuthnCredential, error) {
	return nil, nil
}
func (m *mockStore) DeleteAllWebAuthnCredentials(ctx context.Context, userID string) error {
	return nil
}
func (m *mockStore) PutRefreshVault(ctx context.Context, v model.RefreshVault) error {
	return nil
}
func (m *mockStore) GetRefreshVault(ctx context.Context, userID string) (model.RefreshVault, error) {
	return model.RefreshVault{}, repository.ErrNotFound
}
func (m *mockStore) DeleteRefreshVault(ctx context.Context, userID string) error {
	return nil
}

// mockObjects implements repository.Objects for testing
type mockObjects struct {
	objects map[string][]byte
}

func newMockObjects() *mockObjects {
	return &mockObjects{
		objects: make(map[string][]byte),
	}
}

func (m *mockObjects) Put(ctx context.Context, key string, body []byte, contentType string) error {
	m.objects[key] = body
	return nil
}

func (m *mockObjects) Get(ctx context.Context, key string) ([]byte, error) {
	if data, ok := m.objects[key]; ok {
		return data, nil
	}
	return nil, repository.ErrNotFound
}

func (m *mockObjects) Delete(ctx context.Context, key string) error {
	delete(m.objects, key)
	return nil
}

func (m *mockObjects) PresignPut(ctx context.Context, key string, contentType string, ttl time.Duration) (url string, err error) {
	return "https://presigned.upload.url/" + key, nil
}

func (m *mockObjects) PresignGet(ctx context.Context, key string, ttl time.Duration) (url string, err error) {
	return "https://presigned.download.url/" + key, nil
}

func TestCaptureService_CreateCapture(t *testing.T) {
	store := newMockStore()
	objects := newMockObjects()
	stt := &provider.FakeSTT{}
	llm := &provider.FakeLLM{}

	service := NewCaptureService(store, objects, stt, llm)

	// Set up test note
	note := model.NoteIndex{
		ID:    "note1",
		Title: "Test Note",
	}
	store.PutNote(context.Background(), "user1", note)

	ctx := context.Background()
	capture, uploadURL, err := service.CreateCapture(ctx, "user1", "note1", "audio/wav")

	if err != nil {
		t.Fatalf("CreateCapture failed: %v", err)
	}

	if capture.ID == "" {
		t.Error("Expected capture ID to be set")
	}

	if capture.Status != model.StatusUploaded {
		t.Errorf("Expected status %v, got %v", model.StatusUploaded, capture.Status)
	}

	if capture.UserID != "user1" {
		t.Errorf("Expected UserID user1, got %v", capture.UserID)
	}

	if capture.NoteID != "note1" {
		t.Errorf("Expected NoteID note1, got %v", capture.NoteID)
	}

	if uploadURL == "" {
		t.Error("Expected upload URL to be set")
	}
}

func TestCaptureService_CompleteCapture_HappyPath(t *testing.T) {
	store := newMockStore()
	objects := newMockObjects()
	stt := &provider.FakeSTT{Response: "Hello world"}
	llm := &provider.FakeLLM{Response: "Clean: Hello world"}

	service := NewCaptureService(store, objects, stt, llm)

	// Set up test data
	note := model.NoteIndex{
		ID:            "note1",
		Title:         "Test Note",
		S3MarkdownKey: "tenants/user1/notes/note1/note.md",
		Snippet:       "Original content",
	}
	store.PutNote(context.Background(), "user1", note)

	capture := model.CaptureIndex{
		ID:       "capture1",
		UserID:   "user1",
		NoteID:   "note1",
		Status:   model.StatusUploaded,
		Mode:     model.CleanupFaithful,
		AudioKey: "tenants/user1/captures/capture1/audio.wav",
	}
	store.PutCapture(context.Background(), capture)

	// Put audio data
	objects.Put(context.Background(), capture.AudioKey, []byte("fake audio data"), "audio/wav")

	// Put existing note content
	objects.Put(context.Background(), note.S3MarkdownKey, []byte("Original note content"), "text/plain")

	ctx := context.Background()
	result, err := service.CompleteCapture(ctx, "user1", "capture1")

	if err != nil {
		t.Fatalf("CompleteCapture failed: %v", err)
	}

	if result.Status != model.StatusAppended {
		t.Errorf("Expected status %v, got %v", model.StatusAppended, result.Status)
	}

	// Check that note was updated with cleaned text
	updatedNote, err := objects.Get(ctx, note.S3MarkdownKey)
	if err != nil {
		t.Fatalf("Failed to get updated note: %v", err)
	}

	expectedContent := "Original note content\n\nClean: Hello world"
	if string(updatedNote) != expectedContent {
		t.Errorf("Expected note content %q, got %q", expectedContent, string(updatedNote))
	}

	// Check that raw and clean files were stored
	rawData, err := objects.Get(ctx, "tenants/user1/captures/capture1/raw.txt")
	if err != nil {
		t.Errorf("Expected raw.txt to be stored: %v", err)
	}
	if string(rawData) != "Hello world" {
		t.Errorf("Expected raw content 'Hello world', got %q", string(rawData))
	}

	cleanData, err := objects.Get(ctx, "tenants/user1/captures/capture1/clean.txt")
	if err != nil {
		t.Errorf("Expected clean.txt to be stored: %v", err)
	}
	if string(cleanData) != "Clean: Hello world" {
		t.Errorf("Expected clean content 'Clean: Hello world', got %q", string(cleanData))
	}
}

func TestCaptureService_CompleteCapture_STTFailure(t *testing.T) {
	store := newMockStore()
	objects := newMockObjects()
	stt := &provider.FakeSTT{ShouldFail: true}
	llm := &provider.FakeLLM{}

	service := NewCaptureService(store, objects, stt, llm)

	// Set up test data
	note := model.NoteIndex{
		ID:            "note1",
		Title:         "Test Note",
		S3MarkdownKey: "tenants/user1/notes/note1/note.md",
	}
	store.PutNote(context.Background(), "user1", note)

	capture := model.CaptureIndex{
		ID:       "capture1",
		UserID:   "user1",
		NoteID:   "note1",
		Status:   model.StatusUploaded,
		AudioKey: "tenants/user1/captures/capture1/audio.wav",
	}
	store.PutCapture(context.Background(), capture)

	// Put audio data
	objects.Put(context.Background(), capture.AudioKey, []byte("fake audio data"), "audio/wav")

	// Put original note content
	originalContent := "Original note content"
	objects.Put(context.Background(), note.S3MarkdownKey, []byte(originalContent), "text/plain")

	ctx := context.Background()
	result, err := service.CompleteCapture(ctx, "user1", "capture1")

	if err != nil {
		t.Fatalf("CompleteCapture failed: %v", err)
	}

	if result.Status != model.StatusFailed {
		t.Errorf("Expected status %v, got %v", model.StatusFailed, result.Status)
	}

	// Check that note was NOT modified
	noteContent, err := objects.Get(ctx, note.S3MarkdownKey)
	if err != nil {
		t.Fatalf("Failed to get note: %v", err)
	}

	if string(noteContent) != originalContent {
		t.Errorf("Note content should not have changed, got %q", string(noteContent))
	}

	// Check that raw.txt was NOT created
	_, err = objects.Get(ctx, "tenants/user1/captures/capture1/raw.txt")
	if err != repository.ErrNotFound {
		t.Error("raw.txt should not exist after STT failure")
	}

	// Check that clean.txt was NOT created
	_, err = objects.Get(ctx, "tenants/user1/captures/capture1/clean.txt")
	if err != repository.ErrNotFound {
		t.Error("clean.txt should not exist after STT failure")
	}
}

func TestCaptureService_CompleteCapture_CleanupFailure(t *testing.T) {
	store := newMockStore()
	objects := newMockObjects()
	stt := &provider.FakeSTT{Response: "Hello world"}
	llm := &provider.FakeLLM{ShouldFail: true}

	service := NewCaptureService(store, objects, stt, llm)

	// Set up test data
	note := model.NoteIndex{
		ID:            "note1",
		Title:         "Test Note",
		S3MarkdownKey: "tenants/user1/notes/note1/note.md",
	}
	store.PutNote(context.Background(), "user1", note)

	capture := model.CaptureIndex{
		ID:       "capture1",
		UserID:   "user1",
		NoteID:   "note1",
		Status:   model.StatusUploaded,
		AudioKey: "tenants/user1/captures/capture1/audio.wav",
	}
	store.PutCapture(context.Background(), capture)

	// Put audio data
	objects.Put(context.Background(), capture.AudioKey, []byte("fake audio data"), "audio/wav")

	// Put original note content
	originalContent := "Original note content"
	objects.Put(context.Background(), note.S3MarkdownKey, []byte(originalContent), "text/plain")

	ctx := context.Background()
	result, err := service.CompleteCapture(ctx, "user1", "capture1")

	if err != nil {
		t.Fatalf("CompleteCapture failed: %v", err)
	}

	if result.Status != model.StatusFailed {
		t.Errorf("Expected status %v, got %v", model.StatusFailed, result.Status)
	}

	// Check that note was NOT modified
	noteContent, err := objects.Get(ctx, note.S3MarkdownKey)
	if err != nil {
		t.Fatalf("Failed to get note: %v", err)
	}

	if string(noteContent) != originalContent {
		t.Errorf("Note content should not have changed, got %q", string(noteContent))
	}

	// Check that raw.txt WAS created (STT succeeded)
	rawData, err := objects.Get(ctx, "tenants/user1/captures/capture1/raw.txt")
	if err != nil {
		t.Errorf("raw.txt should exist after STT success: %v", err)
	}
	if string(rawData) != "Hello world" {
		t.Errorf("Expected raw content 'Hello world', got %q", string(rawData))
	}

	// Check that clean.txt was NOT created
	_, err = objects.Get(ctx, "tenants/user1/captures/capture1/clean.txt")
	if err != repository.ErrNotFound {
		t.Error("clean.txt should not exist after cleanup failure")
	}
}

func TestCompleteCaptureIdempotent(t *testing.T) {
	store := newMockStore()
	objects := newMockObjects()
	stt := &provider.FakeSTT{Response: "raw words"}
	llm := &provider.FakeLLM{Response: "cleaned words"}
	svc := NewCaptureService(store, objects, stt, llm)

	ctx := context.Background()
	userID := "user1"
	noteID := "n1"
	store.PutNote(ctx, userID, model.NoteIndex{
		ID: noteID, Title: "T", S3MarkdownKey: "tenants/user1/notes/n1/note.md",
	})
	objects.Put(ctx, "tenants/user1/notes/n1/note.md", []byte(""), "text/markdown")
	objects.Put(ctx, "tenants/user1/captures/c_1/audio.webm", []byte("audio"), "audio/webm")
	store.PutCapture(ctx, model.CaptureIndex{
		ID: "c_1", UserID: userID, NoteID: noteID, Status: model.StatusUploaded,
		Mode: model.CleanupFaithful, AudioKey: "tenants/user1/captures/c_1/audio.webm",
	})

	first, err := svc.CompleteCapture(ctx, userID, "c_1")
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != model.StatusAppended {
		t.Fatalf("status=%s", first.Status)
	}
	second, err := svc.CompleteCapture(ctx, userID, "c_1")
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != model.StatusAppended {
		t.Fatalf("second status=%s", second.Status)
	}
	body, _ := objects.Get(ctx, "tenants/user1/notes/n1/note.md")
	if strings.Count(string(body), "cleaned words") != 1 {
		t.Fatalf("expected single append, got %q", body)
	}
}
