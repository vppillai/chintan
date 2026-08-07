package repository

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
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
	defer result.Body.Close()

	body, err := io.ReadAll(result.Body)
	if err != nil {
		return nil, fmt.Errorf("s3 read object body: %w", err)
	}

	return body, nil
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
