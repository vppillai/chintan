package upload

import (
	"context"
	"time"

	"github.com/vppillai/chintan/backend/internal/repository"
)

// ObjectsPresigner is the fallback used when no tag-aware presigner is wired: it
// signs through repository.Objects, which has no way to bind object tags.
//
// It deliberately does NOT advertise the tagging header. Returning a header the
// signature does not cover would produce a 403 on every upload; returning it and
// having S3 ignore it would silently lose retention, which is the v1 defect this
// work exists to close. Failing to tag loudly beats failing to tag quietly, so
// this type is for the in-memory store in tests and for local development only.
type ObjectsPresigner struct {
	objects repository.Objects
	now     func() time.Time
}

var _ Presigner = (*ObjectsPresigner)(nil)

// NewObjects wraps an object store.
func NewObjects(objects repository.Objects) *ObjectsPresigner {
	return &ObjectsPresigner{objects: objects, now: time.Now}
}

// PresignPut implements Presigner. tags are accepted and dropped.
func (p *ObjectsPresigner) PresignPut(ctx context.Context, key, contentType string, tags map[string]string, maxBytes int64, ttl time.Duration) (Presigned, error) {
	url, err := p.objects.PresignPut(ctx, key, contentType, ttl)
	if err != nil {
		return Presigned{}, err
	}
	headers := map[string]string{}
	if contentType != "" {
		headers["Content-Type"] = contentType
	}
	return Presigned{
		URL:       url,
		ExpiresAt: p.now().UTC().Add(ttl),
		MaxBytes:  maxBytes,
		Headers:   headers,
	}, nil
}
