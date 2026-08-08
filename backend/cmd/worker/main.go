// Command worker drains the capture queue and the table's expiry stream.
//
// It is a second Lambda because the first one cannot do this work. API Gateway's
// HTTP API caps an integration at 30 seconds and the cap is not adjustable, so a
// capture whose speech-to-text plus LLM pipeline runs longer returned 504 to the
// user while the API Lambda kept running and billing — and the retry that
// followed appended the same text again. Here the ceiling is Lambda's 900
// seconds and nobody is waiting on the other end of a socket.
//
// One binary, two event sources, and the template deploys it as two functions.
// The capture queue and the DynamoDB expiry stream want different concurrency,
// different timeouts and different dead-letter queues, which is why they are
// separate functions rather than two mappings onto one; but they want the same
// store, the same object storage and the same cascade, which is why they are one
// binary rather than a third artifact. A third artifact would have meant a new
// required template parameter and edits to build-lambda.sh, bootstrap.sh,
// ci-deploy-stack.sh and the deploy workflow — four more places for a deploy to
// fail — to save an init this process performs in single-digit milliseconds.
//
// Which handler runs is decided by the event, not by configuration: a stream
// record says `aws:dynamodb` and a queue message says `aws:sqs`. A function
// pointed at the wrong source therefore does nothing rather than the wrong
// thing.
package main

import (
	"context"
	"encoding/json"
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
	"github.com/vppillai/chintan/backend/internal/purge"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/service"
)

var (
	worker *pipeline.Worker
	purger *purge.Handler
)

// setup builds everything the handlers need. It is called from main rather
// than from init so that the package is testable at all: an init that calls
// log.Fatalf on a missing TABLE_NAME kills the test binary before a single test
// runs, which is why the dispatch below had no test until it needed one.
//
// The fail-fast property is unchanged. Lambda's init phase runs package
// initialisation *and* main up to lambda.Start, so a missing environment
// variable still stops the cold start rather than the first invocation.
func setup() {
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
	//
	// It is given the tenant cap resolver as well as the instance-wide cap.
	// Without the resolver the breaker enforces only DAILY_SPEND_CAP_MICROS,
	// which defaults to 0 and therefore enforces nothing: a tenant's own cap
	// would be honoured solely by service.SpendGate, once, at POST /v1/captures,
	// leaving the transcription and both LLM calls that follow it unbounded.
	spend := breaker.New(
		pipeline.NewDynamoCounter(dynamoClient, tableName),
		meter.SlogSink{},
		meter.DefaultPrices,
		envInt64("DAILY_SPEND_CAP_MICROS", 0),
		breaker.WithCapResolver(tenantSpendCaps{store: store}),
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

	// The expiry stream runs the same cascade a permanent delete runs, over the
	// same store and bucket. Building it here rather than lazily keeps the
	// failure at init, where the deploy can see it, instead of on the first
	// expired note thirty days after the deploy that broke it.
	purger, err = purge.New(notes, objects)
	if err != nil {
		log.Fatalf("failed to build the expiry stream handler: %v", err)
	}
}

// tenantSpendCaps answers the one question breaker.CapResolver asks, from the
// tenant's settings record.
//
// The adapter lives here rather than in the breaker so the breaker keeps
// knowing nothing about storage: it asks for a number, this reads it. A tenant
// who has saved no settings gets DefaultSettings, whose cap is 0 — "no cap of
// my own" — and the instance-wide ceiling applies.
type tenantSpendCaps struct{ store repository.Store }

func (c tenantSpendCaps) DailyCapMicros(ctx context.Context, tenantID string) (int64, error) {
	settings, err := c.store.GetSettings(ctx, tenantID)
	if err != nil {
		return 0, fmt.Errorf("read tenant spend cap: %w", err)
	}
	return settings.DailySpendCapMicros, nil
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

// eventSource names the AWS service a Lambda event came from. Both of the
// sources this binary serves stamp it on every record.
type eventSource string

const (
	sourceSQS            eventSource = "aws:sqs"
	sourceDynamoDBStream eventSource = "aws:dynamodb"
)

// sourceOf reads the event source off the first record.
//
// Sniffing the payload rather than reading an environment variable means a
// function wired to the wrong event source is inert rather than wrong: an SQS
// message arriving at the expiry function is not run through the purge cascade
// because it does not claim to be a stream record. Lambda guarantees the field
// — a record without one is not a record either of these sources produced.
//
// An event with no records at all is legal and means there is nothing to do.
func sourceOf(raw json.RawMessage) (eventSource, error) {
	var probe struct {
		Records []struct {
			EventSource string `json:"eventSource"`
		} `json:"Records"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return "", fmt.Errorf("worker: decode event: %w", err)
	}
	if len(probe.Records) == 0 {
		return "", nil
	}
	return eventSource(probe.Records[0].EventSource), nil
}

// Handler is the entry point for both event sources.
//
// The return value is whichever batch-failure report the source understands.
// They are different types with the same JSON shape, and returning the wrong
// one would tell Lambda the whole batch succeeded.
func Handler(ctx context.Context, raw json.RawMessage) (any, error) {
	source, err := sourceOf(raw)
	if err != nil {
		return nil, err
	}

	switch source {
	case sourceSQS:
		var event events.SQSEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			return nil, fmt.Errorf("worker: decode sqs event: %w", err)
		}
		return worker.Handle(ctx, event)

	case sourceDynamoDBStream:
		var event events.DynamoDBEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			return nil, fmt.Errorf("worker: decode dynamodb stream event: %w", err)
		}
		return purger.Handle(ctx, event)

	case "":
		// No records. Nothing to report and nothing to fail.
		return events.SQSEventResponse{}, nil

	default:
		// Retrying cannot make this recognisable, so it is logged and dropped
		// rather than redelivered until it dead-letters.
		obs.Log(ctx).Error("ignoring an event from an unrecognised source",
			slog.String("event_source", string(source)))
		return events.SQSEventResponse{}, nil
	}
}

func main() {
	setup()
	lambda.Start(Handler)
}
