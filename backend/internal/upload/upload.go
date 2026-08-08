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
// nothing. This is the piece that stops that being true — for the TENANT's
// setting, not merely for a deploy-time stack parameter. The first version of
// this package tagged every object with one constant value and took the expiry
// period from CloudFormation, which fixed the defect one level up and left it
// exactly as it was one level down.
package upload

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go/middleware"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/vppillai/chintan/backend/internal/model"
)

// ArtifactTagKey is the tag the S3 lifecycle rule filters on.
const ArtifactTagKey = "chintan-artifact"

// ArtifactCaptureAudio marks source audio — the only artifact retention expires.
// Cleaned text and note bodies are never expired by a retention setting.
const ArtifactCaptureAudio = "capture-audio"

// RetentionTagKey carries the tenant's chosen retention onto the object.
//
// This is the tag that makes the per-user setting mean something. The artifact
// tag above says "this is expirable audio"; it cannot say for how long, because
// it is the same on every object. A lifecycle rule cannot read a number out of
// a DynamoDB item either, so the only way a per-user period can reach S3 is as
// a tag VALUE that a rule matches on — one rule per value, which is why
// model.RetentionTiers is a fixed set rather than any integer.
//
// An object with no retention tag matches no expiry rule and is kept
// indefinitely, which is what a retention of 0 means.
const RetentionTagKey = "chintan-retention"

// TaggingHeader is the header S3 reads object tags from. It is an x-amz-*
// header, so a presigned request that sends it unsigned is rejected outright:
// signing it is what makes the tag mandatory rather than advisory.
const TaggingHeader = "x-amz-tagging"

// Presigned is one upload the client must perform verbatim. Every entry in
// Headers is part of the signature; omitting one produces a 403, not an
// untagged object.
//
// MaxBytes is the exception and is advisory. A presigned PUT cannot carry a
// length condition — SigV4 has nowhere to put one — so the URL accepts an
// object of any size, and the bucket's versioning accepts another on every
// replay until the URL expires. The size is enforced afterwards, by the worker,
// from what S3 reports it actually wrote. Only a presigned POST with a
// Content-Length-Range policy would refuse the bytes at the edge.
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
		if contentType != "" {
			opts.ClientOptions = append(opts.ClientOptions, func(o *s3.Options) {
				o.APIOptions = append(o.APIOptions, signContentType(contentType))
			})
		}
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

// signContentType puts Content-Type back on the request immediately before it
// is signed.
//
// PutObjectInput.ContentType alone is not enough. A presign carries no payload,
// so the SDK's RemoveDefaultContentType middleware strips the header during
// serialization and the signature covers only host and x-amz-tagging — leaving
// the content type as advice in the response body that the client is free to
// ignore. Restoring it in the Finalize step, after that middleware and before
// signing, is what puts it in X-Amz-SignedHeaders and makes the declared
// container binding. TestPresignedAudioPutSignsTheContentType pins it, because
// this depends on SDK middleware ordering and would fail silently if that
// changed.
//
// This bounds the *type*, never the *length*. SigV4 cannot sign Content-Length
// on a PUT at all: only a presigned POST with a Content-Length-Range policy
// condition can bound the size at the edge. Until that trade is made, the
// worker's after-the-fact size check is the enforcement.
func signContentType(contentType string) func(*middleware.Stack) error {
	return func(stack *middleware.Stack) error {
		// Added at the head of Finalize rather than relative to "Signing": the
		// presign stack swaps the signing middleware out for its own, so naming
		// it here would bind this to an id the SDK does not promise to keep.
		return stack.Finalize.Add(
			middleware.FinalizeMiddlewareFunc("ChintanSignContentType", func(
				ctx context.Context, in middleware.FinalizeInput, next middleware.FinalizeHandler,
			) (middleware.FinalizeOutput, middleware.Metadata, error) {
				if req, ok := in.Request.(*smithyhttp.Request); ok {
					req.Header.Set("Content-Type", contentType)
				}
				return next.HandleFinalize(ctx, in)
			}),
			middleware.Before)
	}
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

// CaptureAudioTags is the tag set a recording must carry for the retention
// lifecycle rules to see it, for a tenant whose retention is retentionDays.
//
// retentionDays is resolved to a tier here as well as at the point it is
// stored, so a settings record written before tiers existed — or by anything
// that bypassed the validator — still produces a tag some rule matches, rather
// than one no rule matches, which reads identically to "keep forever".
func CaptureAudioTags(retentionDays int) map[string]string {
	tags := map[string]string{ArtifactTagKey: ArtifactCaptureAudio}
	if tier := model.RetentionTierFor(retentionDays); tier > 0 {
		tags[RetentionTagKey] = strconv.Itoa(tier)
	}
	return tags
}
