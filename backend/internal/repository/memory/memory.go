// Package memory holds in-memory implementations of the repository interfaces
// for tests and local development.
//
// It is a separate package so it is never linked into the API binary: nothing
// under cmd/ imports it, and a guard test asserts that stays true. In v1 these
// doubles lived in the production package alongside the real DynamoDB and S3
// implementations.
package memory

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

type memoryObject struct {
	body        []byte
	contentType string
	etag        string
	tags        map[string]string
}

type idemEntry struct {
	record  repository.IdemRecord
	attempt string
}

// Store is an in-memory repository.Store for tests and local development.
type Store struct {
	mu       sync.RWMutex
	settings map[string]model.Settings
	notes    map[string]map[string]model.NoteIndex
	captures map[string]map[string]model.CaptureIndex
	idem     map[string]map[string]idemEntry
}

var _ repository.Store = (*Store)(nil)

// NewStore returns an empty in-memory store.
func NewStore() *Store {
	return &Store{
		settings: make(map[string]model.Settings),
		notes:    make(map[string]map[string]model.NoteIndex),
		captures: make(map[string]map[string]model.CaptureIndex),
		idem:     make(map[string]map[string]idemEntry),
	}
}

func (s *Store) checkCtx(ctx context.Context) error {
	return ctx.Err()
}

// paginate applies the cursor and limit to an already-ordered list of keys.
// The cursor is the last key of the previous page, so it survives insertions
// the same way a DynamoDB LastEvaluatedKey does. It is namespaced by partition
// so one tenant's cursor cannot address another tenant's page.
func paginate[T any](partition string, keys []string, opts repository.ListOptions, load func(string) T) (repository.Page[T], error) {
	limit := int(opts.Limit)
	switch {
	case limit <= 0:
		limit = int(repository.DefaultListLimit)
	case limit > int(repository.MaxListLimit):
		limit = int(repository.MaxListLimit)
	}

	start := 0
	if opts.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(opts.Cursor)
		if err != nil {
			return repository.Page[T]{}, fmt.Errorf("cursor: not valid base64: %w", err)
		}
		gotPartition, last, ok := strings.Cut(string(raw), "\x00")
		if !ok {
			return repository.Page[T]{}, fmt.Errorf("cursor: unexpected key shape")
		}
		if gotPartition != partition {
			return repository.Page[T]{}, fmt.Errorf("cursor: does not belong to this partition")
		}
		start = sort.SearchStrings(keys, last)
		if start < len(keys) && keys[start] == last {
			start++
		}
	}

	end := start + limit
	if end > len(keys) {
		end = len(keys)
	}
	if start > len(keys) {
		start = len(keys)
	}

	items := make([]T, 0, end-start)
	for _, k := range keys[start:end] {
		items = append(items, load(k))
	}

	cursor := ""
	if end < len(keys) && end > start {
		cursor = base64.RawURLEncoding.EncodeToString([]byte(partition + "\x00" + keys[end-1]))
	}
	return repository.Page[T]{Items: items, Cursor: cursor}, nil
}

func (s *Store) GetSettings(ctx context.Context, tenantID string) (model.Settings, error) {
	if err := s.checkCtx(ctx); err != nil {
		return model.Settings{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if found, ok := s.settings[tenantID]; ok {
		return found, nil
	}
	return repository.DefaultSettings(), nil
}

func (s *Store) PutSettings(ctx context.Context, tenantID string, settings model.Settings) error {
	if err := s.checkCtx(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings[tenantID] = settings
	return nil
}

func (s *Store) listNotes(ctx context.Context, tenantID string, opts repository.ListOptions, keep func(model.NoteIndex) bool) (repository.Page[model.NoteIndex], error) {
	if err := s.checkCtx(ctx); err != nil {
		return repository.Page[model.NoteIndex]{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Most recently touched first, matching the order the DynamoDB store
	// builds. Ordering by note id here instead would order by CREATION, and the
	// double would then quietly disagree with production about the one thing
	// this list is for — every service and handler test runs against this store.
	type entry struct{ sortKey, id string }
	entries := make([]entry, 0, len(s.notes[tenantID]))
	for id, n := range s.notes[tenantID] {
		if !keep(n) {
			continue
		}
		// Fixed-width instant, then id to break a tie deterministically. The
		// width matters for the same reason it does in the real store:
		// RFC3339Nano trims trailing zeros and stops sorting chronologically.
		entries = append(entries, entry{sortKey: noteTouchedSortKey(n) + "\x00" + id, id: id})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].sortKey > entries[j].sortKey })

	keys := make([]string, 0, len(entries))
	byKey := make(map[string]string, len(entries))
	for i, e := range entries {
		// paginate walks keys in ascending order, so index the chosen order.
		k := fmt.Sprintf("%08d", i)
		keys = append(keys, k)
		byKey[k] = e.id
	}

	return paginate(tenantID, keys, opts, func(k string) model.NoteIndex {
		return copyNote(s.notes[tenantID][byKey[k]])
	})
}

// noteTouchedSortKey renders a note's update time the way the DynamoDB store
// orders on, so the double orders notes the way production does.
func noteTouchedSortKey(n model.NoteIndex) string {
	if t, err := model.ParseTime(n.UpdatedAt); err == nil {
		return model.FormatTime(t)
	}
	return n.UpdatedAt
}

func (s *Store) ListNotes(ctx context.Context, tenantID string, opts repository.ListOptions) (repository.Page[model.NoteIndex], error) {
	return s.listNotes(ctx, tenantID, opts, func(n model.NoteIndex) bool {
		return strings.TrimSpace(n.DeletedAt) == ""
	})
}

func (s *Store) ListArchivedNotes(ctx context.Context, tenantID string, opts repository.ListOptions) (repository.Page[model.NoteIndex], error) {
	now := time.Now().Unix()
	return s.listNotes(ctx, tenantID, opts, func(n model.NoteIndex) bool {
		return strings.TrimSpace(n.DeletedAt) != "" && n.PurgeAfterEpoch > now
	})
}

func (s *Store) ExpiredNotes(ctx context.Context, asOf int64) ([]repository.TenantNote, error) {
	if err := s.checkCtx(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []repository.TenantNote
	for tenantID, notes := range s.notes {
		for _, n := range notes {
			if n.PurgeAfterEpoch > 0 && n.PurgeAfterEpoch < asOf {
				out = append(out, repository.TenantNote{TenantID: tenantID, Note: copyNote(n)})
			}
		}
	}
	// Deterministic for tests: by tenant, then id.
	sort.Slice(out, func(i, j int) bool {
		if out[i].TenantID != out[j].TenantID {
			return out[i].TenantID < out[j].TenantID
		}
		return out[i].Note.ID < out[j].Note.ID
	})
	return out, nil
}

func (s *Store) GetNote(ctx context.Context, tenantID, noteID string) (model.NoteIndex, error) {
	if err := s.checkCtx(ctx); err != nil {
		return model.NoteIndex{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	n, ok := s.notes[tenantID][noteID]
	if !ok {
		return model.NoteIndex{}, repository.ErrNotFound
	}
	return copyNote(n), nil
}

func (s *Store) PutNote(ctx context.Context, tenantID string, n model.NoteIndex) (model.NoteIndex, error) {
	if err := s.checkCtx(ctx); err != nil {
		return model.NoteIndex{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.notes[tenantID] == nil {
		s.notes[tenantID] = make(map[string]model.NoteIndex)
	}
	if existing, ok := s.notes[tenantID][n.ID]; ok && existing.Version != n.Version {
		return model.NoteIndex{}, repository.ErrVersionConflict
	}
	next := copyNote(n)
	next.Version = n.Version + 1
	s.notes[tenantID][n.ID] = next
	return copyNote(next), nil
}

func (s *Store) DeleteNote(ctx context.Context, tenantID, noteID string) error {
	if err := s.checkCtx(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tenantNotes, ok := s.notes[tenantID]
	if !ok {
		return repository.ErrNotFound
	}
	if _, ok := tenantNotes[noteID]; !ok {
		return repository.ErrNotFound
	}
	delete(tenantNotes, noteID)
	return nil
}

func (s *Store) PutCapture(ctx context.Context, c model.CaptureIndex) (model.CaptureIndex, error) {
	if err := s.checkCtx(ctx); err != nil {
		return model.CaptureIndex{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.captures[c.UserID] == nil {
		s.captures[c.UserID] = make(map[string]model.CaptureIndex)
	}
	if existing, ok := s.captures[c.UserID][c.ID]; ok && existing.Version != c.Version {
		return model.CaptureIndex{}, repository.ErrVersionConflict
	}
	next := c
	next.Version = c.Version + 1
	s.captures[c.UserID][c.ID] = next
	return next, nil
}

func (s *Store) GetCapture(ctx context.Context, tenantID, captureID string) (model.CaptureIndex, error) {
	if err := s.checkCtx(ctx); err != nil {
		return model.CaptureIndex{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.captures[tenantID][captureID]
	if !ok {
		return model.CaptureIndex{}, repository.ErrNotFound
	}
	return c, nil
}

// ListCaptures mirrors DynamoStore.ListCaptures: the tenant's whole capture
// partition, keyed by capture id, newest first.
//
// It must include a capture with no destination note. That is the case the
// note-walking fallback got wrong, and a double that quietly did the right
// thing here is how the difference stayed invisible.
func (s *Store) ListCaptures(ctx context.Context, tenantID string, opts repository.ListOptions) (repository.Page[model.CaptureIndex], error) {
	if err := s.checkCtx(ctx); err != nil {
		return repository.Page[model.CaptureIndex]{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Descending capture id, matching a base-table query with
	// ScanIndexForward: false.
	ids := make([]string, 0, len(s.captures[tenantID]))
	for id := range s.captures[tenantID] {
		ids = append(ids, id)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))

	keys := make([]string, 0, len(ids))
	byKey := make(map[string]string, len(ids))
	for i, id := range ids {
		// paginate walks keys in ascending order, so index the chosen order.
		k := fmt.Sprintf("%08d", i)
		keys = append(keys, k)
		byKey[k] = id
	}

	page, err := paginate(tenantID+"#captures", keys, opts, func(k string) model.CaptureIndex {
		return s.captures[tenantID][byKey[k]]
	})
	if err != nil {
		return repository.Page[model.CaptureIndex]{}, err
	}
	sortCapturesNewestFirst(page.Items)
	return page, nil
}

// sortCapturesNewestFirst matches the ordering DynamoStore applies to a page.
func sortCapturesNewestFirst(captures []model.CaptureIndex) {
	sort.SliceStable(captures, func(i, j int) bool {
		if captures[i].CreatedAt != captures[j].CreatedAt {
			return captures[i].CreatedAt > captures[j].CreatedAt
		}
		return captures[i].ID > captures[j].ID
	})
}

func (s *Store) ListCapturesByNote(ctx context.Context, tenantID, noteID string, opts repository.ListOptions) (repository.Page[model.CaptureIndex], error) {
	if err := s.checkCtx(ctx); err != nil {
		return repository.Page[model.CaptureIndex]{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Sort key mirrors GSI1: CAPTURE#<createdAt> descending, so the page order
	// matches what DynamoDB returns.
	type entry struct{ sortKey, id string }
	entries := make([]entry, 0)
	for id, c := range s.captures[tenantID] {
		if c.NoteID == noteID {
			entries = append(entries, entry{sortKey: c.CreatedAt + "\x00" + id, id: id})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].sortKey > entries[j].sortKey })

	keys := make([]string, 0, len(entries))
	byKey := make(map[string]string, len(entries))
	for i, e := range entries {
		// paginate assumes ascending keys, so index the descending order.
		k := fmt.Sprintf("%08d", i)
		keys = append(keys, k)
		byKey[k] = e.id
	}

	return paginate(tenantID+"#"+noteID, keys, opts, func(k string) model.CaptureIndex {
		return s.captures[tenantID][byKey[k]]
	})
}

func (s *Store) UpdateCaptureStatus(ctx context.Context, tenantID, captureID string, status model.CaptureStatus, errMsg string) error {
	if err := s.checkCtx(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tenantCaptures, ok := s.captures[tenantID]
	if !ok {
		return repository.ErrNotFound
	}
	c, ok := tenantCaptures[captureID]
	if !ok {
		return repository.ErrNotFound
	}
	c.Status = status
	c.Error = errMsg
	c.Version++
	tenantCaptures[captureID] = c
	return nil
}

func (s *Store) DeleteCapture(ctx context.Context, tenantID, captureID string) error {
	if err := s.checkCtx(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tenantCaptures, ok := s.captures[tenantID]
	if !ok {
		return repository.ErrNotFound
	}
	if _, ok := tenantCaptures[captureID]; !ok {
		return repository.ErrNotFound
	}
	delete(tenantCaptures, captureID)
	return nil
}

func (s *Store) ClaimCaptureAppend(ctx context.Context, tenantID, captureID, token string) (bool, model.CaptureIndex, error) {
	if err := s.checkCtx(ctx); err != nil {
		return false, model.CaptureIndex{}, err
	}
	if token == "" {
		return false, model.CaptureIndex{}, fmt.Errorf("repository: empty append token")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	c, ok := s.captures[tenantID][captureID]
	if !ok {
		return false, model.CaptureIndex{}, repository.ErrNotFound
	}

	now := time.Now()
	stale := now.Add(-repository.AppendClaimLease).Unix()
	// Same claimability test as DynamoStore.ClaimCaptureAppend, stated the same
	// way round: unclaimed, or claimed by somebody who never finished and whose
	// lease has run out.
	claimable := c.AppendToken == "" ||
		(c.AppendedAt == 0 && c.AppendClaimedAt < stale)
	if !claimable {
		return false, c, nil
	}

	c.AppendToken = token
	c.AppendClaimedAt = now.Unix()
	c.Version++
	s.captures[tenantID][captureID] = c
	return true, c, nil
}

func (s *Store) CompleteCaptureAppend(ctx context.Context, tenantID, captureID, token string) (model.CaptureIndex, error) {
	if err := s.checkCtx(ctx); err != nil {
		return model.CaptureIndex{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	c, ok := s.captures[tenantID][captureID]
	if !ok {
		return model.CaptureIndex{}, repository.ErrNotFound
	}
	if c.AppendToken != token {
		return model.CaptureIndex{}, repository.ErrVersionConflict
	}
	if c.AppendedAt > 0 {
		return c, nil
	}
	c.Status = model.StatusAppended
	c.Error = ""
	c.AppendedAt = time.Now().Unix()
	c.Version++
	s.captures[tenantID][captureID] = c
	return c, nil
}

func (s *Store) BeginIdempotent(ctx context.Context, tenantID, key, fingerprint string) (*repository.IdemRecord, error) {
	if err := s.checkCtx(ctx); err != nil {
		return nil, err
	}
	if key == "" {
		return nil, fmt.Errorf("repository: empty idempotency key")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.idem[tenantID] == nil {
		s.idem[tenantID] = make(map[string]idemEntry)
	}
	now := time.Now()
	if existing, ok := s.idem[tenantID][key]; ok && existing.record.ExpiresAt > now.Unix() {
		if existing.record.Fingerprint != fingerprint {
			return nil, repository.ErrIdempotencyKeyReused
		}
		if existing.record.Done {
			rec := existing.record
			rec.Response = append([]byte(nil), existing.record.Response...)
			return &rec, nil
		}
		return nil, repository.ErrIdempotencyInFlight
	}

	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, err
	}
	s.idem[tenantID][key] = idemEntry{
		attempt: hex.EncodeToString(tokenBytes),
		record: repository.IdemRecord{
			Key:         key,
			TenantID:    tenantID,
			Fingerprint: fingerprint,
			// A claim is honoured only for the lease; the full TTL starts at
			// completion. See repository.IdemClaimLease.
			ExpiresAt: now.Add(repository.IdemClaimLease).Unix(),
		},
	}
	return nil, nil
}

func (s *Store) CompleteIdempotent(ctx context.Context, tenantID, key string, status int, response []byte) error {
	if err := s.checkCtx(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.idem[tenantID][key]
	if !ok {
		return repository.ErrNotFound
	}
	entry.record.Done = true
	entry.record.Status = status
	entry.record.Response = append([]byte(nil), response...)
	entry.record.ExpiresAt = time.Now().Add(repository.IdemTTL).Unix()
	s.idem[tenantID][key] = entry
	return nil
}

func (s *Store) AbandonIdempotent(ctx context.Context, tenantID, key string) error {
	if err := s.checkCtx(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.idem[tenantID][key]
	if !ok || entry.record.Done {
		// Nothing claimed, or already completed: a completed record is the
		// caller's answer and must not be thrown away.
		return nil
	}
	delete(s.idem[tenantID], key)
	return nil
}

func copyNote(n model.NoteIndex) model.NoteIndex {
	out := n
	if n.Aliases != nil {
		out.Aliases = append([]string(nil), n.Aliases...)
	}
	if n.Tags != nil {
		out.Tags = append([]string(nil), n.Tags...)
	}
	return out
}

// Objects is an in-memory repository.Objects implementation for tests.
type Objects struct {
	mu      sync.RWMutex
	objects map[string]memoryObject
}

var _ repository.Objects = (*Objects)(nil)

// NewObjects returns an empty in-memory object store.
func NewObjects() *Objects {
	return &Objects{objects: make(map[string]memoryObject)}
}

func (o *Objects) checkCtx(ctx context.Context) error {
	return ctx.Err()
}

func etagOf(body []byte) string {
	sum := sha256.Sum256(body)
	return `"` + hex.EncodeToString(sum[:8]) + `"`
}

func (o *Objects) Put(ctx context.Context, key string, body []byte, contentType string) error {
	if err := o.checkCtx(ctx); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	stored := append([]byte(nil), body...)
	// Tags survive a rewrite of the same key. Nothing in the real pipeline
	// re-Puts an object after MarkProcessed, but a test double that silently
	// dropped them on the next Put would make that assumption load-bearing
	// rather than merely true.
	o.objects[key] = memoryObject{
		body: stored, contentType: contentType, etag: etagOf(stored),
		tags: o.objects[key].tags,
	}
	return nil
}

func (o *Objects) Get(ctx context.Context, key string) ([]byte, error) {
	body, _, err := o.GetWithETag(ctx, key)
	return body, err
}

func (o *Objects) GetWithETag(ctx context.Context, key string) ([]byte, string, error) {
	if err := o.checkCtx(ctx); err != nil {
		return nil, "", err
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	obj, ok := o.objects[key]
	if !ok {
		return nil, "", repository.ErrNotFound
	}
	return append([]byte(nil), obj.body...), obj.etag, nil
}

func (o *Objects) PutIfMatch(ctx context.Context, key string, body []byte, contentType, etag string) error {
	if err := o.checkCtx(ctx); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	obj, exists := o.objects[key]
	if etag == "" {
		if exists {
			return repository.ErrPreconditionFailed
		}
	} else if !exists || obj.etag != etag {
		return repository.ErrPreconditionFailed
	}
	stored := append([]byte(nil), body...)
	o.objects[key] = memoryObject{
		body: stored, contentType: contentType, etag: etagOf(stored),
		tags: o.objects[key].tags,
	}
	return nil
}

func (o *Objects) Exists(ctx context.Context, key string) (bool, error) {
	if err := o.checkCtx(ctx); err != nil {
		return false, err
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	_, ok := o.objects[key]
	return ok, nil
}

// MarkProcessed sets the tag the retention lifecycle rule requires before it
// will expire an object. A missing key is not an error: there is nothing left
// to protect or to expire, matching S3Objects' behaviour on a NoSuchKey.
func (o *Objects) MarkProcessed(ctx context.Context, key string) error {
	if err := o.checkCtx(ctx); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	obj, ok := o.objects[key]
	if !ok {
		return nil
	}
	if obj.tags == nil {
		obj.tags = make(map[string]string)
	}
	obj.tags[repository.ProcessedTagKey] = repository.ProcessedTagValue
	o.objects[key] = obj
	return nil
}

// Tags returns the object's current tags, for tests to assert on. A missing
// object returns nil.
func (o *Objects) Tags(key string) map[string]string {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.objects[key].tags
}

func (o *Objects) Delete(ctx context.Context, key string) error {
	if err := o.checkCtx(ctx); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.objects[key]; !ok {
		return repository.ErrNotFound
	}
	delete(o.objects, key)
	return nil
}

func (o *Objects) PresignPut(ctx context.Context, key string, contentType string, ttl time.Duration) (string, error) {
	if err := o.checkCtx(ctx); err != nil {
		return "", err
	}
	return fmt.Sprintf("memory://put/%s?contentType=%s&ttl=%s",
		url.PathEscape(key),
		url.QueryEscape(contentType),
		url.QueryEscape(ttl.String()),
	), nil
}

func (o *Objects) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if err := o.checkCtx(ctx); err != nil {
		return "", err
	}
	return fmt.Sprintf("memory://get/%s?ttl=%s",
		url.PathEscape(key),
		url.QueryEscape(ttl.String()),
	), nil
}
