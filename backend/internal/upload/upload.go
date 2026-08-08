// Package upload issues the presigned PUTs a client uses to hand Chintan a
// recording.
//
// It exists as its own package for one reason: the retention tag. An S3
// lifecycle filter takes one prefix and one suffix and understands no wildcards,
// so `tenants/*/captures/` is inexpressible and the rule has to match on an
// object tag instead. That tag can only be applied by the uploader, and the only
// way to stop an uploader omitting it is to put it inside the signature — which
// means the presigner has to know about tags, and repository.Objects.PresignPut
// does not.
//
// In v1 RetentionDays was stored, returned, rendered in the UI, and read by
// nothing. This is the piece that stops that being true.
package upload

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// ArtifactTagKey is the tag the S3 lifecycle rule filters on.
const ArtifactTagKey = "chintan-artifact"

// ArtifactCaptureAudio marks source audio — the only artifact retention expires.
// Cleaned text and note bodies are never expired by a retention setting.
const ArtifactCaptureAudio = "capture-audio"

// TaggingHeader is the header S3 reads object tags from. It is an x-amz-*
// header, so a presigned request that sends it unsigned is rejected outright:
// signing it is what makes the tag mandatory rather than advisory.
const TaggingHeader = "x-amz-tagging"

// Presigned is one upload the client must perform verbatim. Every entry in
// Headers is part of the signature; omitting one produces a 403, not an
// untagged object.
type Presigned struct {
	URL       string            `json:"url"`
	ExpiresAt time.Time         `json:"expires_at"`
	MaxBytes  int64             `json:"max_bytes,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
}

// Presigner issues presigned PUTs.
type Presigner interface {
	PresignPut(ctx context.Context, key, contentType string, tags map[string]string, maxBytes int64, ttl time.Duration) (Presigned, error)
}

// S3Presigner signs PUTs against a bucket.
type S3Presigner struct {
	presign *s3.PresignClient
	bucket  string
	now     func() time.Time
}

var _ Presigner = (*S3Presigner)(nil)

// NewS3 builds a presigner over an S3 client.
func NewS3(client *s3.Client, bucket string) *S3Presigner {
	return &S3Presigner{presign: s3.NewPresignClient(client), bucket: bucket, now: time.Now}
}

// PresignPut signs a PUT that carries contentType and tags.
func (p *S3Presigner) PresignPut(ctx context.Context, key, contentType string, tags map[string]string, maxBytes int64, ttl time.Duration) (Presigned, error) {
	if strings.TrimSpace(key) == "" {
		return Presigned{}, fmt.Errorf("upload: empty key")
	}

	in := &s3.PutObjectInput{
		Bucket: aws.String(p.bucket),
		Key:    aws.String(key),
	}
	if contentType != "" {
		in.ContentType = aws.String(contentType)
	}
	tagging := EncodeTags(tags)
	if tagging != "" {
		in.Tagging = aws.String(tagging)
	}

	out, err := p.presign.PresignPutObject(ctx, in, func(opts *s3.PresignOptions) {
		opts.Expires = ttl
	})
	if err != nil {
		return Presigned{}, fmt.Errorf("upload: presign put: %w", err)
	}

	headers := map[string]string{}
	if contentType != "" {
		headers["Content-Type"] = contentType
	}
	if tagging != "" {
		headers[TaggingHeader] = tagging
	}

	return Presigned{
		URL:       out.URL,
		ExpiresAt: p.now().UTC().Add(ttl),
		MaxBytes:  maxBytes,
		Headers:   headers,
	}, nil
}

// EncodeTags renders a tag set the way S3 expects it in x-amz-tagging: a
// URL-encoded query string. Keys are sorted so the value is stable and a test
// can assert on it.
func EncodeTags(tags map[string]string) string {
	if len(tags) == 0 {
		return ""
	}
	names := make([]string, 0, len(tags))
	for k := range tags {
		names = append(names, k)
	}
	sort.Strings(names)

	parts := make([]string, 0, len(names))
	for _, k := range names {
		parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(tags[k]))
	}
	return strings.Join(parts, "&")
}

// CaptureAudioTags is the tag set every recording must carry for the retention
// lifecycle rule to see it.
func CaptureAudioTags() map[string]string {
	return map[string]string{ArtifactTagKey: ArtifactCaptureAudio}
}
