package repository

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
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

// S3Objects implements the Objects interface using AWS S3.
type S3Objects struct {
	client *s3.Client
	bucket string
}

// NewS3Objects creates a new S3-backed object store.
func NewS3Objects(client *s3.Client, bucket string) *S3Objects {
	return &S3Objects{
		client: client,
		bucket: bucket,
	}
}

func (o *S3Objects) Put(ctx context.Context, key string, body []byte, contentType string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	_, err := o.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(o.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(body),
		ContentType: aws.String(contentType),
	})
	if err != nil {
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

	presignClient := s3.NewPresignClient(o.client)

	result, err := presignClient.PresignPutObject(ctx, &s3.PutObjectInput{
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

	presignClient := s3.NewPresignClient(o.client)

	result, err := presignClient.PresignGetObject(ctx, &s3.GetObjectInput{
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
