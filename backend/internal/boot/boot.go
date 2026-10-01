// Package boot is the bootstrap the two Lambda mains share: the log level,
// the environment helpers, the AWS config and the stores over it. Each main
// stays a composition root — it reads its own variables, builds its own
// providers and wires its own services in a build function a test can call
// over fakes — and comes here for what both do alike, so the two cannot drift
// the way they had by 2026-10-01: the worker had EnvOr and the api did not,
// and each carried its own copy of the region branch, logLevel, mustEnv and
// envInt64 (review 2026-10-01, BE-10).
//
// The helpers that refuse to start call log.Fatalf, as the mains did. Lambda's
// init phase runs main up to lambda.Start, so a missing or malformed variable
// still stops the cold start rather than the first invocation.
package boot

import (
	"context"
	"log"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/vppillai/chintan/backend/internal/pipeline"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/usage"
)

// LogLevel reads LOG_LEVEL; anything but debug, warn or error is info.
func LogLevel() slog.Level {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LOG_LEVEL"))) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// MustEnv is a variable the binary cannot run without.
func MustEnv(key string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		log.Fatalf("%s environment variable is required", key)
	}
	return v
}

// EnvOr is a variable with a default.
func EnvOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// EnvInt64 refuses to start on a malformed value. A spend cap that silently
// reads as zero because somebody typed "10 USD" is a cap that does not exist.
func EnvInt64(key string, fallback int64) int64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		log.Fatalf("%s must be a whole number of microdollars: %v", key, err)
	}
	return v
}

// LoadAWS is the default config, pinned to AWS_REGION when Lambda sets it.
func LoadAWS(ctx context.Context) (aws.Config, error) {
	if region := os.Getenv("AWS_REGION"); region != "" {
		return config.LoadDefaultConfig(ctx, config.WithRegion(region))
	}
	return config.LoadDefaultConfig(ctx)
}

// Stores is the single table and the content bucket as both binaries read
// and write them, and the clients they are built on, for the one-off reads a
// main still makes itself (the VAPID parameters, the worker's secrets).
type Stores struct {
	Dynamo  *dynamodb.Client
	S3      *s3.Client
	SSM     *ssm.Client
	Store   *repository.DynamoStore
	Objects *repository.S3Objects
	// Counter is the instance-wide daily spend row: what the worker's breaker
	// reserves against and the api's spend gate reads.
	Counter *pipeline.DynamoCounter
	// Usage is the per-tenant usage rows: the worker's breaker and daily
	// tasks write them, GET /v1/usage and the request counter read and add.
	Usage *usage.Dynamo
}

// NewStores opens the clients once and builds every store over them.
func NewStores(cfg aws.Config, table, bucket string) Stores {
	dynamo := dynamodb.NewFromConfig(cfg)
	s3Client := s3.NewFromConfig(cfg)
	return Stores{
		Dynamo:  dynamo,
		S3:      s3Client,
		SSM:     ssm.NewFromConfig(cfg),
		Store:   repository.NewDynamoStore(dynamo, table),
		Objects: repository.NewS3Objects(s3Client, bucket),
		Counter: pipeline.NewDynamoCounter(dynamo, table),
		Usage:   usage.NewDynamo(dynamo, table),
	}
}
