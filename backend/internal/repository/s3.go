package repository

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// ProcessedTagKey and ProcessedTagValue mark an object the pipeline has
// finished with — reached a terminal capture status, not merely aged since
// upload. See Objects.MarkProcessed.
const (
	ProcessedTagKey   = "chintan-processed"
	ProcessedTagValue = "true"
)

// S3API is the slice of the S3 client the adapter calls, named so a test can
// stand an in-memory bucket in for it (s3fake_test.go) and prove the If-Match
// contract against something other than the memory store, which implements
// the semantics this adapter is supposed to have. DynamoAPI is the same seam
// for the table.
type S3API interface {
	GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	HeadObject(ctx context.Context, in *s3.HeadObjectInput, opts ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	DeleteObject(ctx context.Context, in *s3.DeleteObjectInput, opts ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	GetObjectTagging(ctx context.Context, in *s3.GetObjectTaggingInput, opts ...func(*s3.Options)) (*s3.GetObjectTaggingOutput, error)
	PutObjectTagging(ctx context.Context, in *s3.PutObjectTaggingInput, opts ...func(*s3.Options)) (*s3.PutObjectTaggingOutput, error)
}

// S3Objects implements the Objects interface using AWS S3.
type S3Objects struct {
	client S3API
	// presign signs URLs, which only the concrete client can do; it is built
	// once here rather than per call.
	presign *s3.PresignClient
	bucket  string
}

// NewS3Objects creates a new S3-backed object store.
func NewS3Objects(client *s3.Client, bucket string) *S3Objects {
	return &S3Objects{
		client:  client,
		presign: s3.NewPresignClient(client),
		bucket:  bucket,
	}
}

func (o *S3Objects) Put(ctx context.Context, key string, body []byte, contentType string) error {
	return o.PutTagged(ctx, key, body, contentType, nil)
}

// PutTagged binds tags on the write itself, the way the presigned upload does
// through x-amz-tagging: S3 encodes the set as a query string in the Tagging
// parameter.
func (o *S3Objects) PutTagged(ctx context.Context, key string, body []byte, contentType string, tags map[string]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	in := &s3.PutObjectInput{
		Bucket:      aws.String(o.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(body),
		ContentType: aws.String(contentType),
	}
	if len(tags) > 0 {
		tagging := url.Values{}
		for k, v := range tags {
			tagging.Set(k, v)
		}
		in.Tagging = aws.String(tagging.Encode())
	}
	if _, err := o.client.PutObject(ctx, in); err != nil {
		return fmt.Errorf("s3 put object: %w", err)
	}

	return nil
}

func (o *S3Objects) Get(ctx context.Context, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	result, err := o.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(o.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var noSuchKey *types.NoSuchKey
		if errors.As(err, &noSuchKey) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("s3 get object: %w", err)
	}
	defer func() { _ = result.Body.Close() }()

	body, err := io.ReadAll(result.Body)
	if err != nil {
		return nil, fmt.Errorf("s3 read object body: %w", err)
	}

	return body, nil
}

// GetWithETag returns the body together with the ETag a later PutIfMatch must
// present. A missing object returns ErrNotFound with an empty ETag, which
// PutIfMatch then reads as "must not exist".
func (o *S3Objects) GetWithETag(ctx context.Context, key string) ([]byte, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}

	result, err := o.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(o.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var noSuchKey *types.NoSuchKey
		if errors.As(err, &noSuchKey) {
			return nil, "", ErrNotFound
		}
		return nil, "", fmt.Errorf("s3 get object: %w", err)
	}
	defer func() { _ = result.Body.Close() }()

	body, err := io.ReadAll(result.Body)
	if err != nil {
		return nil, "", fmt.Errorf("s3 read object body: %w", err)
	}
	return body, aws.ToString(result.ETag), nil
}

// PutIfMatch writes body only if the object still carries etag. An empty etag
// means the object must not exist. A lost race returns ErrPreconditionFailed so
// the caller can re-read and retry rather than silently discarding a concurrent
// write.
func (o *S3Objects) PutIfMatch(ctx context.Context, key string, body []byte, contentType, etag string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	in := &s3.PutObjectInput{
		Bucket:      aws.String(o.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(body),
		ContentType: aws.String(contentType),
	}
	if etag == "" {
		in.IfNoneMatch = aws.String("*")
	} else {
		in.IfMatch = aws.String(etag)
	}

	if _, err := o.client.PutObject(ctx, in); err != nil {
		if isS3PreconditionFailure(err) {
			return ErrPreconditionFailed
		}
		return fmt.Errorf("s3 put object: %w", err)
	}
	return nil
}

// isS3PreconditionFailure recognises the two statuses S3 uses to reject a
// conditional write: 412 for a failed If-Match and 409 for a lost If-None-Match
// race.
func isS3PreconditionFailure(err error) bool {
	var respErr *awshttp.ResponseError
	if errors.As(err, &respErr) {
		switch respErr.HTTPStatusCode() {
		case http.StatusPreconditionFailed, http.StatusConflict:
			return true
		}
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "PreconditionFailed", "ConditionalRequestConflict":
			return true
		}
	}
	return false
}

// Exists answers with a HEAD, so the check costs one request and no transfer.
// S3 reports a missing key on HEAD as a bare 404 rather than a NoSuchKey error
// document, so both shapes are read as "absent".
func (o *S3Objects) Exists(ctx context.Context, key string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}

	_, err := o.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(o.bucket),
		Key:    aws.String(key),
	})
	if err == nil {
		return true, nil
	}
	var notFound *types.NotFound
	var noSuchKey *types.NoSuchKey
	if errors.As(err, &notFound) || errors.As(err, &noSuchKey) {
		return false, nil
	}
	var respErr *awshttp.ResponseError
	if errors.As(err, &respErr) && respErr.HTTPStatusCode() == http.StatusNotFound {
		return false, nil
	}
	return false, fmt.Errorf("s3 head object: %w", err)
}

func (o *S3Objects) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	_, err := o.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(o.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("s3 delete object: %w", err)
	}

	return nil
}

// MarkProcessed reads the object's current tags and writes them back with
// ProcessedTagKey added, since S3's tagging API has no merge operation of its
// own — a plain PutObjectTagging would silently drop the retention tier tag
// that PresignPut signed in at upload.
func (o *S3Objects) MarkProcessed(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	got, err := o.client.GetObjectTagging(ctx, &s3.GetObjectTaggingInput{
		Bucket: aws.String(o.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var noSuchKey *types.NoSuchKey
		if errors.As(err, &noSuchKey) {
			return nil
		}
		return fmt.Errorf("s3 get object tagging: %w", err)
	}

	tags := make([]types.Tag, 0, len(got.TagSet)+1)
	for _, tag := range got.TagSet {
		if aws.ToString(tag.Key) == ProcessedTagKey {
			continue
		}
		tags = append(tags, tag)
	}
	tags = append(tags, types.Tag{Key: aws.String(ProcessedTagKey), Value: aws.String(ProcessedTagValue)})

	if _, err := o.client.PutObjectTagging(ctx, &s3.PutObjectTaggingInput{
		Bucket:  aws.String(o.bucket),
		Key:     aws.String(key),
		Tagging: &types.Tagging{TagSet: tags},
	}); err != nil {
		var noSuchKey *types.NoSuchKey
		if errors.As(err, &noSuchKey) {
			return nil
		}
		return fmt.Errorf("s3 put object tagging: %w", err)
	}
	return nil
}

func (o *S3Objects) PresignPut(ctx context.Context, key string, contentType string, ttl time.Duration) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	result, err := o.presign.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(o.bucket),
		Key:         aws.String(key),
		ContentType: aws.String(contentType),
	}, func(opts *s3.PresignOptions) {
		opts.Expires = ttl
	})
	if err != nil {
		return "", fmt.Errorf("s3 presign put: %w", err)
	}

	return result.URL, nil
}

func (o *S3Objects) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	result, err := o.presign.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(o.bucket),
		Key:    aws.String(key),
	}, func(opts *s3.PresignOptions) {
		opts.Expires = ttl
	})
	if err != nil {
		return "", fmt.Errorf("s3 presign get: %w", err)
	}

	return result.URL, nil
}
