// Command worker drains the capture queue.
//
// It is a second Lambda because the first one cannot do this work. API Gateway's
// HTTP API caps an integration at 30 seconds and the cap is not adjustable, so a
// capture whose speech-to-text plus LLM pipeline runs longer returned 504 to the
// user while the API Lambda kept running and billing — and the retry that
// followed appended the same text again. Here the ceiling is Lambda's 900
// seconds and nobody is waiting on the other end of a socket.
package main

import (
	"context"
	"fmt"
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
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/vppillai/chintan/backend/internal/breaker"
	"github.com/vppillai/chintan/backend/internal/meter"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/pipeline"
	"github.com/vppillai/chintan/backend/internal/provider"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/service"
)

var worker *pipeline.Worker

func init() {
	obs.Setup(logLevel())

	ctx := context.Background()

	tableName := mustEnv("TABLE_NAME")
	contentBucket := mustEnv("CONTENT_BUCKET")
	llmBaseURL := envOr("LLM_BASE_URL", "https://api.minimax.io/v1")
	llmModel := envOr("LLM_MODEL", "MiniMax-M3")
	sttModel := envOr("GROQ_STT_MODEL", "")
	awsRegion := os.Getenv("AWS_REGION")

	var cfg aws.Config
	var err error
	if awsRegion != "" {
		cfg, err = config.LoadDefaultConfig(ctx, config.WithRegion(awsRegion))
	} else {
		cfg, err = config.LoadDefaultConfig(ctx)
	}
	if err != nil {
		log.Fatalf("failed to load AWS config: %v", err)
	}

	ssmClient := ssm.NewFromConfig(cfg)
	groqAPIKey, err := resolveSecret(ctx, ssmClient, "GROQ_API_KEY", "GROQ_API_KEY_PATH")
	if err != nil {
		log.Fatalf("failed to resolve Groq API key: %v", err)
	}
	llmAPIKey, err := resolveSecret(ctx, ssmClient, "LLM_API_KEY", "LLM_API_KEY_PATH")
	if err != nil {
		log.Fatalf("failed to resolve LLM API key: %v", err)
	}

	dynamoClient := dynamodb.NewFromConfig(cfg)
	s3Client := s3.NewFromConfig(cfg)

	store := repository.NewDynamoStore(dynamoClient, tableName)
	objects := repository.NewS3Objects(s3Client, contentBucket)

	stt, err := provider.NewGroqSTT(groqAPIKey, "", sttModel, nil)
	if err != nil {
		log.Fatalf("failed to create Groq STT: %v", err)
	}
	llm, err := provider.NewOpenAICleanup(llmAPIKey, llmBaseURL, llmModel, nil)
	if err != nil {
		log.Fatalf("failed to create OpenAI Cleanup: %v", err)
	}

	// The breaker owns every provider call. It is built here, once, and passed in
	// — the pipeline refuses to start without it, so there is no build of this
	// binary in which a paid API is reachable unmetered.
	spend := breaker.New(
		pipeline.NewDynamoCounter(dynamoClient, tableName),
		meter.SlogSink{},
		meter.DefaultPrices,
		envInt64("DAILY_SPEND_CAP_MICROS", 0),
	)

	notes := service.NewNotesService(store, objects)

	p, err := pipeline.New(pipeline.Config{
		Store:       store,
		Objects:     objects,
		STT:         stt,
		LLM:         llm,
		Router:      llm,
		Notes:       notes,
		Breaker:     spend,
		STTProvider: "groq",
		STTModel:    stt.Model(),
		LLMProvider: "openai",
		LLMModel:    llm.Model(),
	})
	if err != nil {
		log.Fatalf("failed to build capture pipeline: %v", err)
	}

	worker = pipeline.NewWorker(p)
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

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
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

// resolveSecret prefers a direct env value (local/dev), else fetches SecureString from SSM path env.
func resolveSecret(ctx context.Context, client *ssm.Client, valueEnv, pathEnv string) (string, error) {
	if v := strings.TrimSpace(os.Getenv(valueEnv)); v != "" {
		return v, nil
	}
	path := strings.TrimSpace(os.Getenv(pathEnv))
	if path == "" {
		return "", fmt.Errorf("%s or %s is required", valueEnv, pathEnv)
	}
	out, err := client.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(path),
		WithDecryption: aws.Bool(true),
	})
	if err != nil {
		return "", fmt.Errorf("ssm get %s: %w", path, err)
	}
	if out.Parameter == nil || out.Parameter.Value == nil || strings.TrimSpace(*out.Parameter.Value) == "" {
		return "", fmt.Errorf("ssm parameter %s is empty", path)
	}
	return *out.Parameter.Value, nil
}

// Handler is the SQS entry point.
func Handler(ctx context.Context, event events.SQSEvent) (events.SQSEventResponse, error) {
	return worker.Handle(ctx, event)
}

func main() {
	lambda.Start(Handler)
}
