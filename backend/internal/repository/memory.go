package repository

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
)

type memoryObject struct {
	body        []byte
	contentType string
}

// MemoryStore is an in-memory Store for tests and local development.
type MemoryStore struct {
	mu       sync.RWMutex
	settings map[string]model.Settings
	notes    map[string]map[string]model.NoteIndex
	captures map[string]map[string]model.CaptureIndex
}

// NewMemoryStore returns an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		settings: make(map[string]model.Settings),
		notes:    make(map[string]map[string]model.NoteIndex),
		captures: make(map[string]map[string]model.CaptureIndex),
	}
}

func (s *MemoryStore) checkCtx(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (s *MemoryStore) GetSettings(ctx context.Context, userID string) (model.Settings, error) {
	if err := s.checkCtx(ctx); err != nil {
		return model.Settings{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s, ok := s.settings[userID]; ok {
		return s, nil
	}
	return defaultSettings(), nil
}

func (s *MemoryStore) PutSettings(ctx context.Context, userID string, settings model.Settings) error {
	if err := s.checkCtx(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings[userID] = settings
	return nil
}

func (s *MemoryStore) ListNotes(ctx context.Context, userID string) ([]model.NoteIndex, error) {
	if err := s.checkCtx(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	userNotes := s.notes[userID]
	out := make([]model.NoteIndex, 0, len(userNotes))
	for _, n := range userNotes {
		out = append(out, copyNote(n))
	}
	return out, nil
}

func (s *MemoryStore) GetNote(ctx context.Context, userID, noteID string) (model.NoteIndex, error) {
	if err := s.checkCtx(ctx); err != nil {
		return model.NoteIndex{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	n, ok := s.notes[userID][noteID]
	if !ok {
		return model.NoteIndex{}, ErrNotFound
	}
	return copyNote(n), nil
}

func (s *MemoryStore) PutNote(ctx context.Context, userID string, n model.NoteIndex) error {
	if err := s.checkCtx(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.notes[userID] == nil {
		s.notes[userID] = make(map[string]model.NoteIndex)
	}
	s.notes[userID][n.ID] = copyNote(n)
	return nil
}

func (s *MemoryStore) DeleteNote(ctx context.Context, userID, noteID string) error {
	if err := s.checkCtx(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	userNotes, ok := s.notes[userID]
	if !ok {
		return ErrNotFound
	}
	if _, ok := userNotes[noteID]; !ok {
		return ErrNotFound
	}
	delete(userNotes, noteID)
	return nil
}

func (s *MemoryStore) PutCapture(ctx context.Context, c model.CaptureIndex) error {
	if err := s.checkCtx(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.captures[c.UserID] == nil {
		s.captures[c.UserID] = make(map[string]model.CaptureIndex)
	}
	s.captures[c.UserID][c.ID] = c
	return nil
}

func (s *MemoryStore) GetCapture(ctx context.Context, userID, captureID string) (model.CaptureIndex, error) {
	if err := s.checkCtx(ctx); err != nil {
		return model.CaptureIndex{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.captures[userID][captureID]
	if !ok {
		return model.CaptureIndex{}, ErrNotFound
	}
	return c, nil
}

func (s *MemoryStore) ListCapturesByNote(ctx context.Context, userID, noteID string) ([]model.CaptureIndex, error) {
	if err := s.checkCtx(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.CaptureIndex, 0)
	for _, c := range s.captures[userID] {
		if c.NoteID == noteID {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt > out[j].CreatedAt
	})
	return out, nil
}

func (s *MemoryStore) UpdateCaptureStatus(ctx context.Context, userID, captureID string, status model.CaptureStatus, errMsg string) error {
	if err := s.checkCtx(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	userCaptures, ok := s.captures[userID]
	if !ok {
		return ErrNotFound
	}
	c, ok := userCaptures[captureID]
	if !ok {
		return ErrNotFound
	}
	c.Status = status
	c.Error = errMsg
	userCaptures[captureID] = c
	return nil
}

func copyNote(n model.NoteIndex) model.NoteIndex {
	out := n
	if n.Aliases != nil {
		out.Aliases = append([]string(nil), n.Aliases...)
	}
	return out
}

// MemoryObjects is an in-memory Objects implementation for tests.
type MemoryObjects struct {
	mu      sync.RWMutex
	objects map[string]memoryObject
}

// NewMemoryObjects returns an empty in-memory object store.
func NewMemoryObjects() *MemoryObjects {
	return &MemoryObjects{
		objects: make(map[string]memoryObject),
	}
}

func (o *MemoryObjects) checkCtx(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (o *MemoryObjects) Put(ctx context.Context, key string, body []byte, contentType string) error {
	if err := o.checkCtx(ctx); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	stored := append([]byte(nil), body...)
	o.objects[key] = memoryObject{body: stored, contentType: contentType}
	return nil
}

func (o *MemoryObjects) Get(ctx context.Context, key string) ([]byte, error) {
	if err := o.checkCtx(ctx); err != nil {
		return nil, err
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	obj, ok := o.objects[key]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte(nil), obj.body...), nil
}

func (o *MemoryObjects) Delete(ctx context.Context, key string) error {
	if err := o.checkCtx(ctx); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.objects[key]; !ok {
		return ErrNotFound
	}
	delete(o.objects, key)
	return nil
}

func (o *MemoryObjects) PresignPut(ctx context.Context, key string, contentType string, ttl time.Duration) (string, error) {
	if err := o.checkCtx(ctx); err != nil {
		return "", err
	}
	return fmt.Sprintf("memory://put/%s?contentType=%s&ttl=%s",
		url.PathEscape(key),
		url.QueryEscape(contentType),
		url.QueryEscape(ttl.String()),
	), nil
}

func (o *MemoryObjects) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if err := o.checkCtx(ctx); err != nil {
		return "", err
	}
	return fmt.Sprintf("memory://get/%s?ttl=%s",
		url.PathEscape(key),
		url.QueryEscape(ttl.String()),
	), nil
}
