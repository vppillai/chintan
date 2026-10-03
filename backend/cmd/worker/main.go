// Command worker runs the capture pipeline, the weekly expiry sweep, the
// daily AWS cost reading, the daily storage snapshot and the quarter-hourly
// reconcile of stuck captures.
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
// {"task":"ask"} for a question over the tenant's notes, the API or
// chintanctl with {"task":"regenerate-note"} to clean a note's recordings
// again with the current prompts, and four
// EventBridge rules: once a week with {"task":"sweep-expired"}, once a day
// each with {"task":"aws-cost"} and {"task":"storage-snapshot"}, and every
// fifteen minutes with {"task":"reconcile-stuck"}. There is no
// queue in between. A returned error
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
	"strings"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/budgets"
	lambdasvc "github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/vppillai/chintan/backend/internal/awscost"
	"github.com/vppillai/chintan/backend/internal/boot"
	"github.com/vppillai/chintan/backend/internal/breaker"
	"github.com/vppillai/chintan/backend/internal/meter"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/pipeline"
	"github.com/vppillai/chintan/backend/internal/provider"
	"github.com/vppillai/chintan/backend/internal/purge"
	"github.com/vppillai/chintan/backend/internal/push"
	"github.com/vppillai/chintan/backend/internal/reconcile"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/service"
	"github.com/vppillai/chintan/backend/internal/ssmparam"
	"github.com/vppillai/chintan/backend/internal/storagesnap"
	"github.com/vppillai/chintan/backend/internal/usage"
)

var (
	sweeper   *purge.Sweeper
	costs     *awscost.Collector
	snapshots *storagesnap.Snapshotter
	reaper    *reconcile.Reaper
)

// deps is everything build wires that setup reads from the environment,
// resolves from SSM or opens on AWS, as the interfaces the pipeline and the
// tasks take, so a test builds the same worker over fakes
// (TestBuildWiresTheWorkerOverFakes).
type deps struct {
	store   repository.Store
	objects repository.Objects
	stt     provider.STT
	llm     provider.LLM
	router  provider.Router
	// sttModel and llmModel are what the breaker prices each call as.
	sttModel, llmModel string
	// counter is the instance-wide daily spend row the breaker reserves
	// against; spendCapMicros the cap it enforces, 0 to count only.
	counter        breaker.Counter
	spendCapMicros int64
	// usage is the per-tenant accounting: the breaker's two ADDs per settled
	// call, the daily AWS cost reading and the daily storage snapshot all
	// write the same rows.
	usage interface {
		usage.Recorder
		usage.AWSCostStore
		storagesnap.Store
	}
	// cleanInvoker hands a note back to this function, through the live
	// alias, for its cleaned view after an append; nil regenerates inline.
	cleanInvoker pipeline.NoteCleanInvoker
	// pusher sends Web Push when a recording files; nil sends nothing.
	pusher pipeline.Pusher
	// budgets, accountID and budgetName are the daily cost reading's; an
	// empty budgetName makes the task a logged no-op.
	budgets    awscost.Budgets
	accountID  string
	budgetName string
}

// build wires the pipeline and the four scheduled tasks over d into the
// package's handler variables. Building the tasks here rather than lazily
// keeps a failure at init, where the deploy can see it, instead of on the
// first sweep a week after the deploy that broke it.
func build(d deps) error {
	// The breaker owns every provider call. It is built here, once, and passed
	// in — the pipeline refuses to start without it, so there is no build of
	// this binary in which a paid API is reachable without reserving against
	// the day's counter. One counter, one cap: DAILY_SPEND_CAP_MICROS from the
	// template, compared against the instance-wide SPEND#<day> row.
	//
	// WithUsage is the per-tenant accounting: two ADDs per settled call onto
	// the tenant's USAGE#<month> and USAGE#<day> rows, in the same place the
	// breaker writes the usage log line. It enforces nothing; GET /v1/usage
	// reads it.
	spend := breaker.New(d.counter, meter.DefaultPrices, d.spendCapMicros, breaker.WithUsage(d.usage))
	notes := service.NewNotesService(d.store, d.objects)

	p, err := pipeline.New(pipeline.Config{
		Store:        d.store,
		Objects:      d.objects,
		STT:          d.stt,
		LLM:          d.llm,
		Router:       d.router,
		Notes:        notes,
		Breaker:      spend,
		CleanInvoker: d.cleanInvoker,
		Pusher:       d.pusher,
		STTProvider:  "groq",
		STTModel:     d.sttModel,
		LLMProvider:  "openai",
		LLMModel:     d.llmModel,
	})
	if err != nil {
		return fmt.Errorf("build capture pipeline: %w", err)
	}
	handleWork = pipeline.NewWorker(p).Handle

	// The expiry sweep runs the same cascade a permanent delete runs, over the
	// same store and bucket.
	if sweeper, err = purge.New(d.store, notes); err != nil {
		return fmt.Errorf("build the expiry sweeper: %w", err)
	}
	// The daily AWS cost reading. The account id is how DescribeBudget
	// addresses a budget; the template passes it so the binary need not learn
	// it from STS or the function ARN.
	if costs, err = awscost.New(d.budgets, d.usage, d.accountID, d.budgetName); err != nil {
		return fmt.Errorf("build the aws-cost task: %w", err)
	}
	// The daily storage snapshot: the same footprint GET /v1/usage computes
	// on demand, added onto each tenant's usage rows once a day so storage
	// has a figure over the month and not only a figure right now.
	if snapshots, err = storagesnap.New(d.usage, service.NewStorageService(d.store)); err != nil {
		return fmt.Errorf("build the storage-snapshot task: %w", err)
	}
	// The quarter-hourly backstop for a capture an invocation left in a
	// stage: the same pipeline, run once more, then a failed verdict.
	if reaper, err = reconcile.New(d.store, d.objects, p.Run); err != nil {
		return fmt.Errorf("build the reconcile-stuck task: %w", err)
	}
	return nil
}

// setup reads the environment and the secrets, opens AWS, builds the
// providers and hands everything to build. It is called from main rather
// than from init so that the package is testable at all: an init that calls
// log.Fatalf on a missing TABLE_NAME kills the test binary before a single
// test runs, which is why the dispatch below had no test until it needed one.
//
// The fail-fast property is unchanged. Lambda's init phase runs package
// initialisation *and* main up to lambda.Start, so a missing environment
// variable still stops the cold start rather than the first invocation.
func setup() {
	obs.Setup(boot.LogLevel())

	ctx := context.Background()

	tableName := boot.MustEnv("TABLE_NAME")
	contentBucket := boot.MustEnv("CONTENT_BUCKET")
	llmBaseURL := boot.EnvOr("LLM_BASE_URL", "https://api.minimax.io/v1")
	llmModel := boot.EnvOr("LLM_MODEL", "MiniMax-M3")
	sttModel := boot.EnvOr("GROQ_STT_MODEL", "")

	cfg, err := boot.LoadAWS(ctx)
	if err != nil {
		log.Fatalf("failed to load AWS config: %v", err)
	}
	stores := boot.NewStores(cfg, tableName, contentBucket)

	groqAPIKey, err := resolveSecret(ctx, stores.SSM, "GROQ_API_KEY", "GROQ_API_KEY_PATH")
	if err != nil {
		log.Fatalf("failed to resolve Groq API key: %v", err)
	}
	llmAPIKey, err := resolveSecret(ctx, stores.SSM, "LLM_API_KEY", "LLM_API_KEY_PATH")
	if err != nil {
		log.Fatalf("failed to resolve LLM API key: %v", err)
	}

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
	// paid is reachable. The api has no such check because it prices
	// nothing: it reads the counter this binary's breaker writes.
	for _, m := range []struct{ provider, model string }{
		{"groq", stt.Model()},
		{"openai", llm.Model()},
	} {
		if err := checkPriced(ctx, meter.DefaultPrices, m.provider, m.model); err != nil {
			log.Fatalf("%v", err)
		}
	}

	// After an append to a note with auto_clean the worker hands the note back
	// to itself, through the same live alias the API uses, so the cleaned view
	// runs as its own invocation with its own retries. The variable is
	// optional: without it the view is regenerated inline, which is still off
	// the request path.
	var cleanInvoker pipeline.NoteCleanInvoker
	if arn := strings.TrimSpace(os.Getenv("WORKER_FUNCTION_ARN")); arn != "" {
		cleanInvoker = pipeline.NewInvoker(lambdasvc.NewFromConfig(cfg), arn)
	}

	// Web Push is dormant until the VAPID pair is in SSM
	// (scripts/vapid-keys.sh --apply, docs/design/push.md). Both halves are optional
	// reads: an instance without them starts, files recordings and sends
	// nothing, and says so once here rather than on every capture.
	vapidPublic, err := ssmparam.Optional(ctx, stores.SSM, "VAPID_PUBLIC_KEY_PATH")
	if err != nil {
		log.Fatalf("failed to read the VAPID public key: %v", err)
	}
	vapidPrivate, err := ssmparam.Optional(ctx, stores.SSM, "VAPID_PRIVATE_KEY_PATH")
	if err != nil {
		log.Fatalf("failed to read the VAPID private key: %v", err)
	}
	var pusher pipeline.Pusher
	if vapidPublic != "" && vapidPrivate != "" {
		sender, err := push.New(vapidPublic, vapidPrivate, boot.EnvOr("VAPID_SUBJECT", "https://github.com/vppillai/chintan"))
		if err != nil {
			log.Fatalf("failed to build the web push sender: %v", err)
		}
		pusher = sender
	} else {
		slog.Info("web push is not configured; no notification is sent when a recording files",
			slog.String("hint", "put vapid_public_key and vapid_private_key under /chintan/<instance>/ in SSM; scripts/vapid-keys.sh --apply installs them"))
	}

	// MONTHLY_BUDGET_NAME is the stack's budget, or empty when the stack has
	// none (no alarm address), in which case the task is a logged no-op and
	// the API shows no AWS figure.
	budgetName := strings.TrimSpace(os.Getenv("MONTHLY_BUDGET_NAME"))
	var accountID string
	if budgetName != "" {
		accountID = boot.MustEnv("AWS_ACCOUNT_ID")
	}

	if err := build(deps{
		store:          stores.Store,
		objects:        stores.Objects,
		stt:            stt,
		llm:            llm,
		router:         llm,
		sttModel:       stt.Model(),
		llmModel:       llm.Model(),
		counter:        stores.Counter,
		spendCapMicros: boot.EnvInt64("DAILY_SPEND_CAP_MICROS", 0),
		usage:          stores.Usage,
		cleanInvoker:   cleanInvoker,
		pusher:         pusher,
		budgets:        budgets.NewFromConfig(cfg),
		accountID:      accountID,
		budgetName:     budgetName,
	}); err != nil {
		log.Fatalf("failed to %v", err)
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

// smokeTask is the deploy smoke's payload task, {"task":"smoke"}: an explicit
// no-op in scheduled rather than an unrecognised name, which the pipeline
// refuses with ErrUnknownTask and would fail every deploy's smoke.
const smokeTask = "smoke"

// scheduled are the tasks this binary runs itself: the EventBridge rules'
// constant inputs, whose handlers need nothing from the pipeline. Every other
// task goes to the pipeline worker, which owns the list of the tasks it
// serves and refuses any other with an error. Until R7-18 this switch named
// the pipeline's tasks too and dropped any task it did not know, so one added
// to Worker.Handle alone was logged and lost — no retry, no dead letter.
//
// A variable, not a switch, so the dispatch test can stand in for handlers
// whose real dependencies are AWS clients.
var scheduled = map[string]func(context.Context) error{
	purge.Task: func(ctx context.Context) error {
		_, err := sweeper.Sweep(ctx)
		return err
	},
	// The daily budget reading behind the AWS line on GET /v1/usage.
	awscost.Task: func(ctx context.Context) error {
		_, err := costs.Run(ctx)
		return err
	},
	// The daily storage reading behind storage.byte_days on GET /v1/usage.
	storagesnap.Task: func(ctx context.Context) error {
		_, err := snapshots.Run(ctx)
		return err
	},
	// The quarter-hourly reconcile of captures left in a pipeline stage.
	reconcile.Task: func(ctx context.Context) error {
		_, err := reaper.Run(ctx)
		return err
	},
	// scripts/deploy.sh's worker smoke. Reaching here means setup() ran —
	// the environment, the secrets and the clients a bad deploy breaks
	// first — and that is all the smoke asks; nothing is read or written.
	smokeTask: func(context.Context) error { return nil },
}

// handleWork is the pipeline worker's Handle, set by setup; a variable for the
// same reason as scheduled.
var handleWork func(context.Context, json.RawMessage) error

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
	case inv.task != "":
		if run, ok := scheduled[inv.task]; ok {
			return run(ctx)
		}
		// clean-note, ask, regenerate-note, and any task nobody knows.
		return handleWork(ctx, raw)

	case inv.source == sourceS3, inv.source == "":
		// A recording landing in the bucket, or the API naming a capture.
		return handleWork(ctx, raw)

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
