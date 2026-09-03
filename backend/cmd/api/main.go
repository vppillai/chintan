package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/awslabs/aws-lambda-go-api-proxy/httpadapter"

	"github.com/vppillai/chintan/backend/internal/auth"
	"github.com/vppillai/chintan/backend/internal/handler"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/pipeline"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/service"
	"github.com/vppillai/chintan/backend/internal/upload"
)

var lambdaAdapter *httpadapter.HandlerAdapterV2

func init() {
	// Structured logging is installed before anything can log, so no startup
	// line escapes as unstructured text. cmd/worker does the same.
	obs.Setup(logLevel())

	ctx := context.Background()

	tableName := mustEnv("TABLE_NAME")
	contentBucket := mustEnv("CONTENT_BUCKET")
	allowedOrigin := mustEnv("ALLOWED_ORIGIN")
	// A wildcard origin alongside Allow-Credentials defeats the same-origin
	// policy. Refuse to start rather than serve it.
	if allowedOrigin == "*" {
		log.Fatalf("ALLOWED_ORIGIN must be a concrete origin, not %q", allowedOrigin)
	}

	awsRegion := os.Getenv("AWS_REGION")

	var cfg aws.Config
	var err error
	if awsRegion != "" {
		cfg, err = config.LoadDefaultConfig(ctx, config.WithRegion(awsRegion))
	} else {
		cfg, err = config.LoadDefaultConfig(ctx)
	}
	if err != nil {
		log.Fatalf("Failed to load AWS config: %v", err)
	}

	// Token verification is not optional. The API Gateway authorizer is not
	// guaranteed to be the only ingress, so the service verifies for itself and
	// refuses to start unconfigured — auth may not silently degrade.
	clientID := mustEnv("USER_POOL_CLIENT_ID")
	issuer := strings.TrimSpace(os.Getenv("COGNITO_ISSUER"))
	if issuer == "" {
		issuer = auth.NewCognitoIssuer(awsRegion, os.Getenv("USER_POOL_ID"))
	}
	verifier, err := auth.NewCognitoVerifier(issuer, clientID, nil)
	if err != nil {
		log.Fatalf("Failed to build token verifier (set COGNITO_ISSUER or USER_POOL_ID, and USER_POOL_CLIENT_ID): %v", err)
	}

	dynamoClient := dynamodb.NewFromConfig(cfg)
	s3Client := s3.NewFromConfig(cfg)

	store := repository.NewDynamoStore(dynamoClient, tableName)
	objects := repository.NewS3Objects(s3Client, contentBucket)

	notesService := service.NewNotesService(store, objects)
	settingsService := service.NewSettingsService(store)

	// Three wirings, each of which is the difference between a feature working
	// and a feature that only looks like it works:
	//
	//  - WithUploads installs the tag-aware presigner. Without it the fallback
	//    signs untagged PUTs, the lifecycle rule never matches the object, and
	//    RetentionDays goes back to being a setting that is stored, returned,
	//    rendered in the UI, and read by nothing.
	//  - WithQueue hands captures to the worker. Without it a retry has nowhere
	//    to go.
	//  - WithNoteCreator lets a user resolve a needs_target capture by naming a
	//    new note.
	captureService := service.NewCaptureService(store, objects).
		WithUploads(upload.NewS3(s3Client, contentBucket)).
		WithQueue(pipeline.NewQueue(sqs.NewFromConfig(cfg), mustEnv("CAPTURE_QUEUE_URL"))).
		WithNoteCreator(notesService)

	// The API does not call a provider, so it cannot spend. It reads the same
	// atomic counter the breaker enforces against, so a capped tenant is told
	// before it uploads rather than after the capture stalls.
	spendGate := service.NewSpendGate(
		pipeline.NewDynamoCounter(dynamoClient, tableName),
		settingsService,
		envInt64("DAILY_SPEND_CAP_MICROS", 0),
	)

	// There is no biometric-unlock wiring here any more. Cognito's managed
	// login does passkeys natively (SignInPolicy.AllowedFirstAuthFactors on the
	// user pool), which replaced the custom WebAuthn ceremony, the sealed
	// refresh-token vault and the SSM vault key that used to be built here.
	router := handler.New(handler.Deps{
		Notes:                 notesService,
		Settings:              settingsService,
		Captures:              captureService,
		Search:                service.NewSearchService(notesService),
		Tags:                  service.NewTagsService(notesService),
		Export:                service.NewExportService(notesService, captureService, settingsService, objects),
		Readiness:             service.NewReadinessService(store, objects),
		Spend:                 spendGate,
		Store:                 store,
		Verifier:              verifier,
		AllowedOrigin:         allowedOrigin,
		DefaultSpendCapMicros: envInt64("DAILY_SPEND_CAP_MICROS", 0),
	})
	lambdaAdapter = httpadapter.NewV2(router)
}

func logLevel() slog.Level {
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

func mustEnv(key string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		log.Fatalf("%s environment variable is required", key)
	}
	return v
}

// envInt64 refuses to start on a malformed value. A spend cap that silently
// reads as zero because somebody typed "10 USD" is a cap that does not exist.
func envInt64(key string, fallback int64) int64 {
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

func Handler(ctx context.Context, req events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	return lambdaAdapter.ProxyWithContext(ctx, req)
}

func main() {
	lambda.Start(Handler)
}
