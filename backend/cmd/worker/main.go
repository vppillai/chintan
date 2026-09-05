// Command worker runs the capture pipeline, the weekly expiry sweep and the
// daily AWS cost reading.
//
// It is a second Lambda because the first one cannot do this work. API Gateway's
// HTTP API caps an integration at 30 seconds and the cap is not adjustable, so a
// capture whose speech-to-text plus LLM pipeline runs longer returned 504 to the
// user while the API Lambda kept running and billing — and the retry that
// followed appended the same text again. Here the ceiling is Lambda's 900
// seconds and nobody is waiting on the other end of a socket.
//
// Everything reaches it asynchronously, through the `live` alias: S3 when a
// recording lands in the content bucket, the API when the user retries a
// capture or picks its destination, the API or this function itself with
// {"task":"clean-note"} for a note's whole-note cleaned view, the API with
// {"task":"ask"} for a question over the tenant's notes, and two
// EventBridge rules: once a week with {"task":"sweep-expired"} and once a day
// with {"task":"aws-cost"}. There is no queue in between. A returned error
// makes Lambda retry the same payload twice, and an invocation that fails all
// three attempts is written to the dead-letter queue, which is what the alarm
// watches.
//
// Which handler runs is decided by the event, not by configuration: a payload
// naming a task is that task; a payload with records says `aws:s3` on each; the
// API's payload has neither. A function fed something else does nothing rather
// than the wrong thing. Until 2026-09 this binary was also deployed as a third
// function consuming the table's DynamoDB stream to cascade S3 deletes after
// TTL removed a note; the sweep replaced that function, its event-source
// mapping, its dead-letter queue and the stream.
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

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/budgets"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	lambdasvc "github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/vppillai/chintan/backend/internal/awscost"
	"github.com/vppillai/chintan/backend/internal/breaker"
	"github.com/vppillai/chintan/backend/internal/meter"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/pipeline"
	"github.com/vppillai/chintan/backend/internal/provider"
	"github.com/vppillai/chintan/backend/internal/purge"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/service"
	"github.com/vppillai/chintan/backend/internal/usage"
)

var (
	worker  *pipeline.Worker
	sweeper *purge.Sweeper
	costs   *awscost.Collector
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

	// A model the price table cannot price is a cap that enforces nothing:
	// meter prices an unknown provider at zero rather than failing the call,
	// which is right at runtime and wrong at deploy time, where refusing to
	// start is what gets the row added. Checked here, once, before anything
	// paid is reachable.
	for _, m := range []struct{ provider, model string }{
		{"groq", stt.Model()},
		{"openai", llm.Model()},
	} {
		if err := checkPriced(ctx, meter.DefaultPrices, m.provider, m.model); err != nil {
			log.Fatalf("%v", err)
		}
	}

	// The breaker owns every provider call. It is built here, once, and passed in
	// — the pipeline refuses to start without it, so there is no build of this
	// binary in which a paid API is reachable without reserving against the
	// day's counter. One counter, one cap: DAILY_SPEND_CAP_MICROS from the
	// template, compared against the instance-wide SPEND#<day> row.
	//
	// WithUsage is the per-tenant accounting: two ADDs per settled call onto
	// the tenant's USAGE#<month> and USAGE#<day> rows, in the same place the
	// breaker writes the usage log line. It enforces nothing; GET /v1/usage
	// reads it.
	usageStore := usage.NewDynamo(dynamoClient, tableName)
	spend := breaker.New(
		pipeline.NewDynamoCounter(dynamoClient, tableName),
		meter.DefaultPrices,
		envInt64("DAILY_SPEND_CAP_MICROS", 0),
		breaker.WithUsage(usageStore),
	)

	notes := service.NewNotesService(store, objects)

	// After an append to a note with auto_clean the worker hands the note back
	// to itself, through the same live alias the API uses, so the cleaned view
	// runs as its own invocation with its own retries. The variable is
	// optional: without it the view is regenerated inline, which is still off
	// the request path.
	var cleanInvoker pipeline.NoteCleanInvoker
	if arn := strings.TrimSpace(os.Getenv("WORKER_FUNCTION_ARN")); arn != "" {
		cleanInvoker = pipeline.NewInvoker(lambdasvc.NewFromConfig(cfg), arn)
	}

	p, err := pipeline.New(pipeline.Config{
		Store:        store,
		Objects:      objects,
		STT:          stt,
		LLM:          llm,
		Router:       llm,
		Notes:        notes,
		Breaker:      spend,
		CleanInvoker: cleanInvoker,
		STTProvider:  "groq",
		STTModel:     stt.Model(),
		LLMProvider:  "openai",
		LLMModel:     llm.Model(),
	})
	if err != nil {
		log.Fatalf("failed to build capture pipeline: %v", err)
	}

	worker = pipeline.NewWorker(p)

	// The expiry sweep runs the same cascade a permanent delete runs, over the
	// same store and bucket. Building it here rather than lazily keeps the
	// failure at init, where the deploy can see it, instead of on the first
	// sweep a week after the deploy that broke it.
	sweeper, err = purge.New(store, notes)
	if err != nil {
		log.Fatalf("failed to build the expiry sweeper: %v", err)
	}

	// The daily AWS cost reading. MONTHLY_BUDGET_NAME is the stack's budget,
	// or empty when the stack has none (no alarm address), in which case the
	// task is a logged no-op and the API shows no AWS figure. The account id
	// is how DescribeBudget addresses a budget; the template passes it so the
	// binary need not learn it from STS or the function ARN.
	budgetName := strings.TrimSpace(os.Getenv("MONTHLY_BUDGET_NAME"))
	var accountID string
	if budgetName != "" {
		accountID = mustEnv("AWS_ACCOUNT_ID")
	}
	costs, err = awscost.New(budgets.NewFromConfig(cfg), usageStore, accountID, budgetName)
	if err != nil {
		log.Fatalf("failed to build the aws-cost task: %v", err)
	}
}

// checkPriced refuses a provider and model the price table has no row for,
// exact or wildcard. When the wildcard stands in for the model it says so
// once, in the log and as PriceWildcardUsed{Provider}, so an operator can see
// that the instance is being priced at the provider's stand-in rate rather
// than the model's own — over-reserved by design, but not what the cap was
// set against.
func checkPriced(ctx context.Context, prices meter.PriceTable, provider, model string) error {
	switch prices.Resolve(provider, model) {
	case meter.ResolvedNone:
		return fmt.Errorf("no price for %q: add a row for it, or a %q wildcard, to meter.DefaultPrices in backend/internal/meter/meter.go — an unpriced model would make the daily spend cap enforce nothing",
			meter.Key(provider, model), meter.Key(provider, "*"))
	case meter.ResolvedWildcard:
		obs.Log(ctx).Warn("model has no price row of its own; priced at the provider wildcard",
			slog.String("provider", provider),
			slog.String("model", model),
			slog.String("wildcard", meter.Key(provider, "*")))
		obs.Count(ctx, "PriceWildcardUsed", map[string]string{"Provider": provider})
	}
	return nil
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

// eventSource names the AWS service a Lambda event came from. S3 stamps it on
// every record it delivers.
type eventSource string

const sourceS3 eventSource = "aws:s3"

// invocation is what the payload sniff decides: a task by name, or the event
// source of a record-bearing event ("" for a payload with no records, which is
// the API's).
type invocation struct {
	task   string
	source eventSource
}

// sniff reads the task name and the first record's event source off a payload.
//
// Sniffing the payload rather than reading an environment variable means a
// function wired to the wrong trigger is inert rather than wrong. The task is
// checked first: the sweep's payload has no records and no capture, and read as
// the API's shape it would reach the pipeline as "addressed no capture" — a
// silent no-op where a whole week's expiries are concerned.
func sniff(raw json.RawMessage) (invocation, error) {
	var probe struct {
		Task    string `json:"task"`
		Records []struct {
			EventSource string `json:"eventSource"`
		} `json:"Records"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return invocation{}, fmt.Errorf("worker: decode event: %w", err)
	}
	inv := invocation{task: probe.Task}
	if len(probe.Records) > 0 {
		inv.source = eventSource(probe.Records[0].EventSource)
	}
	return inv, nil
}

// Handler is the entry point for every asynchronous invocation.
//
// The return value is the whole protocol: nil for done, an error for "retry
// this payload". Every handler — a capture, the sweep, the cost reading — is
// idempotent, so a retry re-does only what did not finish.
func Handler(ctx context.Context, raw json.RawMessage) error {
	inv, err := sniff(raw)
	if err != nil {
		return err
	}

	switch {
	case inv.task == purge.Task:
		_, err := sweeper.Sweep(ctx)
		return err

	case inv.task == awscost.Task:
		// The daily budget reading behind the AWS line on GET /v1/usage.
		_, err := costs.Run(ctx)
		return err

	case inv.task == pipeline.TaskCleanNote:
		// The whole-note cleaned view. Sent by the API for POST
		// /v1/notes/{id}/clean and for a recording moved or deleted from a
		// note with auto_clean, and by this function to itself after an
		// append to one.
		return worker.Handle(ctx, raw)

	case inv.task == pipeline.TaskAsk:
		// A question over the tenant's notes, sent by the API for POST
		// /v1/ask. Retrieval and the one model call run here; the API only
		// wrote the row.
		return worker.Handle(ctx, raw)

	case inv.task != "":
		// Retrying cannot make this recognisable, so it is logged and dropped
		// rather than retried until it dead-letters.
		obs.Log(ctx).Error("ignoring an unrecognised task",
			slog.String("task", inv.task))
		return nil

	case inv.source == sourceS3, inv.source == "":
		// A recording landing in the bucket, or the API naming a capture.
		return worker.Handle(ctx, raw)

	default:
		obs.Log(ctx).Error("ignoring an event from an unrecognised source",
			slog.String("event_source", string(inv.source)))
		return nil
	}
}

func main() {
	setup()
	lambda.Start(Handler)
}
