// Command api is the HTTP API Lambda behind API Gateway: it wires the
// repositories, services and handlers together and answers requests. The slow
// half of a capture is the worker's (cmd/worker). The contract it serves is
// docs/api/openapi.yaml.
package main

import (
	"context"
	"log"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	lambdasvc "github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/awslabs/aws-lambda-go-api-proxy/httpadapter"

	"github.com/vppillai/chintan/backend/internal/auth"
	"github.com/vppillai/chintan/backend/internal/boot"
	"github.com/vppillai/chintan/backend/internal/handler"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/pipeline"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/service"
	"github.com/vppillai/chintan/backend/internal/ssmparam"
	"github.com/vppillai/chintan/backend/internal/upload"
	"github.com/vppillai/chintan/backend/internal/usage"
)

var lambdaAdapter *httpadapter.HandlerAdapterV2

// warmTimeout bounds the warm-up. Lambda allows the init phase ten seconds;
// a dependency that has not answered in five is one the first request will
// have to wait for anyway.
const warmTimeout = 5 * time.Second

// deps is everything build wires that setup reads from the environment or
// opens on AWS, as the interfaces the services take, so a test builds the
// same API over fakes (TestBuildServesTheAPIOverFakes).
type deps struct {
	store   repository.Store
	objects repository.Objects
	// uploads is the tag-aware presigner. Without it the fallback signs
	// untagged PUTs, the lifecycle rule never matches the object, and
	// RetentionDays goes back to being a setting that is stored, returned,
	// rendered in the UI, and read by nothing.
	uploads upload.Presigner
	// invoker is the one hand-off to the worker Lambda, on its live alias,
	// shared by the capture retry/target, the whole-note clean and Ask.
	// Without it a retry has nowhere to go and POST /v1/notes/{id}/clean
	// answers 503.
	invoker service.Invoker
	// spend is the atomic counter the breaker enforces against. The API does
	// not call a provider, so it cannot spend; it reads the same counter with
	// the same cap, so a capped instance is told before it uploads rather
	// than after the capture stalls.
	spend          service.SpendCounter
	spendCapMicros int64
	// usage is the one usage store for both directions: the reader behind
	// GET /v1/usage, and the counter that adds every authenticated request
	// to the same month and day rows the worker's breaker writes spend to.
	usage interface {
		usage.Reader
		usage.RequestCounter
	}
	// verifier checks bearer tokens; nil fails closed (middleware.Auth).
	verifier auth.Verifier
	// vapidPublic is the key GET /v1/push/key hands the browser, when the
	// owner has made the pair (docs/design/push.md); empty answers 404.
	vapidPublic   string
	allowedOrigin string
}

// build wires the services and the router over d. The readiness service
// comes back as well, for the warm-up and the test.
func build(d deps) (http.Handler, *service.ReadinessService) {
	notesService := service.NewNotesService(d.store, d.objects).WithInvoker(d.invoker)
	settingsService := service.NewSettingsService(d.store)
	// Ask writes the question row and hands it to the same worker; the
	// retrieval and the model call never run here.
	askService := service.NewAskService(d.store, d.invoker)
	// WithNoteCreator lets a user resolve a needs_target capture by naming a
	// new note.
	captureService := service.NewCaptureService(d.store, d.objects).
		WithUploads(d.uploads).
		WithInvoker(d.invoker).
		WithNoteCreator(notesService)
	readiness := service.NewReadinessService(d.store, d.objects)

	// There is no biometric-unlock wiring here any more. Cognito's managed
	// login does passkeys natively (SignInPolicy.AllowedFirstAuthFactors on
	// the user pool), which replaced the custom WebAuthn ceremony, the sealed
	// refresh-token vault and the SSM vault key that used to be built here.
	router := handler.New(handler.Deps{
		Notes:          notesService,
		Settings:       settingsService,
		Captures:       captureService,
		Search:         service.NewSearchService(notesService),
		Tags:           service.NewTagsService(notesService),
		Export:         service.NewExportService(notesService, captureService, settingsService, d.objects),
		Readiness:      readiness,
		Spend:          service.NewSpendGate(d.spend, d.spendCapMicros),
		Usage:          d.usage,
		Requests:       d.usage,
		Storage:        service.NewStorageService(d.store),
		Ask:            askService,
		Devices:        service.NewDeviceService(d.store),
		Push:           service.NewPushService(d.store),
		PushPublicKey:  d.vapidPublic,
		Store:          d.store,
		Verifier:       d.verifier,
		AllowedOrigin:  d.allowedOrigin,
		SpendCapMicros: d.spendCapMicros,
	})
	return router, readiness
}

// setup reads the environment, opens AWS and builds the API. It is called
// from main rather than from init so that the package is testable at all:
// an init that calls log.Fatalf on a missing TABLE_NAME kills the test
// binary before a single test runs, which is why this binary had no test
// until 2026-10-01. The fail-fast property is unchanged: Lambda's init phase
// runs main up to lambda.Start, so a missing variable still stops the cold
// start rather than the first invocation.
func setup() http.Handler {
	// Structured logging is installed before anything can log, so no startup
	// line escapes as unstructured text. cmd/worker does the same.
	obs.Setup(boot.LogLevel())

	ctx := context.Background()

	tableName := boot.MustEnv("TABLE_NAME")
	contentBucket := boot.MustEnv("CONTENT_BUCKET")
	allowedOrigin := boot.MustEnv("ALLOWED_ORIGIN")
	// A wildcard origin alongside Allow-Credentials defeats the same-origin
	// policy. Refuse to start rather than serve it.
	if allowedOrigin == "*" {
		log.Fatalf("ALLOWED_ORIGIN must be a concrete origin, not %q", allowedOrigin)
	}

	cfg, err := boot.LoadAWS(ctx)
	if err != nil {
		log.Fatalf("Failed to load AWS config: %v", err)
	}

	// Token verification is not optional. The API Gateway authorizer is not
	// guaranteed to be the only ingress, so the service verifies for itself and
	// refuses to start unconfigured — auth may not silently degrade.
	clientID := boot.MustEnv("USER_POOL_CLIENT_ID")
	issuer := strings.TrimSpace(os.Getenv("COGNITO_ISSUER"))
	if issuer == "" {
		issuer = auth.NewCognitoIssuer(os.Getenv("AWS_REGION"), os.Getenv("USER_POOL_ID"))
	}
	verifier, err := auth.NewCognitoVerifier(issuer, clientID, nil)
	if err != nil {
		log.Fatalf("Failed to build token verifier (set COGNITO_ISSUER or USER_POOL_ID, and USER_POOL_CLIENT_ID): %v", err)
	}

	stores := boot.NewStores(cfg, tableName, contentBucket)

	// The VAPID public key is the one parameter this binary reads, and
	// optional: without it the route answers 404 and the app's Notifications
	// card explains the step.
	vapidPublic, err := ssmparam.Optional(ctx, stores.SSM, "VAPID_PUBLIC_KEY_PATH")
	if err != nil {
		log.Fatalf("Failed to read the VAPID public key: %v", err)
	}

	router, readiness := build(deps{
		store:          stores.Store,
		objects:        stores.Objects,
		uploads:        upload.NewS3(stores.S3, contentBucket),
		invoker:        pipeline.NewInvoker(lambdasvc.NewFromConfig(cfg), boot.MustEnv("WORKER_FUNCTION_ARN")),
		spend:          stores.Counter,
		spendCapMicros: boot.EnvInt64("DAILY_SPEND_CAP_MICROS", 0),
		usage:          stores.Usage,
		verifier:       verifier,
		vapidPublic:    vapidPublic,
		allowedOrigin:  allowedOrigin,
	})

	// The first request on a fresh container paid about 270 ms inside the
	// handler — the JWKS fetch and the DynamoDB connection both opened there,
	// not at start-up — and Home fans out five GETs, so an idle launch paid it
	// on every container it spawned (review 2026-09-21, T26). Both are opened
	// here instead, concurrently, before the first invocation. The store half
	// is the readiness probe itself: one GetItem on its sentinel partition and
	// one S3 GetObject, which also opens the S3 client the first note open
	// and every presign would otherwise pay for. A failure is logged, not
	// fatal: the first request then pays what it always did. The VAPID read
	// above is a third round-trip on every cold start — a ParameterNotFound
	// on a dormant instance — that is not in the 270 ms and has not been
	// measured; measure it once the keys exist.
	warmCtx, cancelWarm := context.WithTimeout(ctx, warmTimeout)
	defer cancelWarm()
	var warm sync.WaitGroup
	warm.Add(2)
	go func() {
		defer warm.Done()
		if err := verifier.Warm(warmCtx); err != nil {
			slog.Warn("token verifier warm-up failed; the first request fetches the key set", slog.String("error", err.Error()))
		}
	}()
	go func() {
		defer warm.Done()
		for name, check := range readiness.Check(warmCtx).Checks {
			if !check.OK {
				slog.Warn("store warm-up failed; the first request opens the connection", slog.String("dependency", name))
			}
		}
	}()
	warm.Wait()
	return router
}

func Handler(ctx context.Context, req events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	return lambdaAdapter.ProxyWithContext(ctx, req)
}

func main() {
	lambdaAdapter = httpadapter.NewV2(setup())
	lambda.Start(Handler)
}
