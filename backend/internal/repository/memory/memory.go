// Package memory holds the in-memory implementations of the repository's
// object and usage interfaces for tests: Objects stands in for the S3 bucket
// and Usage for the usage rows. The table has no double here — every test
// runs the real DynamoStore over repository/dynamofake, so it meets the
// store's own conditions and projections.
//
// It is a separate package so it is never linked into the API binary: nothing
// under cmd/ imports it, and a guard test asserts that stays true. Doubles
// living in the production package alongside the real DynamoDB and S3
// implementations would be one wiring mistake away from serving production.
package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"net/url"
	"sync"
	"time"

	"github.com/vppillai/chintan/backend/internal/repository"
)

type memoryObject struct {
	body        []byte
	contentType string
	etag        string
	tags        map[string]string
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

// PutTagged is Put with the object's tag set replaced, as S3 does on a PUT
// that carries x-amz-tagging.
func (o *Objects) PutTagged(ctx context.Context, key string, body []byte, contentType string, tags map[string]string) error {
	if err := o.Put(ctx, key, body, contentType); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	obj := o.objects[key]
	obj.tags = maps.Clone(tags)
	o.objects[key] = obj
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
