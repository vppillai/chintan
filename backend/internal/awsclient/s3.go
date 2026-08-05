// Package awsclient builds the AWS SDK clients, in one place.
//
// Isolated so that no other package imports the SDK directly. Two reasons that matter
// beyond tidiness:
//
//   - The interfaces the rest of the code depends on are narrow and locally declared
//     (config.ObjectGetter is one method), so tests need no AWS fakes and no
//     credentials — which is what lets the whole check suite run in a job that holds
//     none (§0.5A).
//   - Region and retry policy are configured once. A client built ad hoc somewhere else
//     would pick up the ambient region, and the ambient region on a developer machine
//     is not necessarily the deploy region.
package awsclient

import (
	"context"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3 wraps the S3 client with the narrow surface this project uses.
type S3 struct{ api *s3.Client }

// NewS3 builds an S3 client from the ambient Lambda configuration.
func NewS3(ctx context.Context) (*S3, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("awsclient: loading AWS config: %w", err)
	}
	return &S3{api: s3.NewFromConfig(cfg)}, nil
}

// GetObject fetches one object's body. Satisfies config.ObjectGetter.
//
// The caller closes the body. Returning the reader rather than the bytes matters for the
// paths that must not buffer a whole object in memory — I3's carve-out for the Telegram
// download requires streaming straight to S3 "without buffering the whole object in
// memory".
func (c *S3) GetObject(ctx context.Context, bucket, key string) (io.ReadCloser, error) {
	out, err := c.api.GetObject(ctx, &s3.GetObjectInput{Bucket: &bucket, Key: &key})
	if err != nil {
		return nil, fmt.Errorf("awsclient: get s3://%s/%s: %w", bucket, key, err)
	}
	return out.Body, nil
}
