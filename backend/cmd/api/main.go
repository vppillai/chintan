// Command api is the synchronous HTTP API Lambda (§4).
//
// One function handling every HTTP route through internal routing — a "Lambdalith" per
// execution profile, never one function per endpoint, which multiplies cold starts and
// duplicated init for no benefit (§4).
//
// Profile: 256MB, 10s timeout, behind API Gateway HTTP API v2 with an AWS_PROXY
// integration on the `$default` route.
//
// **Cold-start order matters and is deliberate.** Config is loaded and validated before
// the handler is registered, so an invalid config fails the cold start rather than the
// first request that needed a missing value (§Phase 0). A Lambda that starts and then
// errors per-request looks like an application fault; one that fails to initialise looks
// like what it is.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/vppillai/chintan/backend/internal/awsclient"
	"github.com/vppillai/chintan/backend/internal/config"
	"github.com/vppillai/chintan/backend/internal/logging"
	"github.com/vppillai/chintan/backend/internal/router"
	"github.com/vppillai/chintan/backend/internal/version"
)

func main() {
	log := logging.New()

	// The handler is built here, at cold start, and any failure exits. Deliberate: a
	// process that cannot construct its dependencies must not serve, and Lambda will
	// report the init failure rather than returning a confusing 500 per request.
	handler, err := build(context.Background(), log)
	if err != nil {
		log.Error("cold start failed", slog.String("error", err.Error()))
		// Exit non-zero so the platform records an init failure. Returning a handler
		// that always 500s would hide a config problem behind an application error.
		os.Exit(1)
	}

	log.Info("api ready",
		slog.String("version", version.Display()),
		slog.String("commit", version.Commit),
		slog.Bool("stamped", version.Stamped()),
	)

	lambda.Start(handler)
}

// build loads config and returns the Lambda handler.
func build(ctx context.Context, log *slog.Logger) (any, error) {
	instance := os.Getenv("CHINTAN_INSTANCE")
	if instance == "" {
		return nil, fmt.Errorf("CHINTAN_INSTANCE is not set")
	}

	bucket := os.Getenv("CHINTAN_CONFIG_BUCKET")
	key := os.Getenv("CHINTAN_CONFIG_KEY")
	if bucket == "" || key == "" {
		return nil, fmt.Errorf("CHINTAN_CONFIG_BUCKET and CHINTAN_CONFIG_KEY must be set; config is read at cold start rather than embedded, so a threshold change needs no rebuild (§7.4)")
	}

	s3c, err := awsclient.NewS3(ctx)
	if err != nil {
		return nil, fmt.Errorf("building S3 client: %w", err)
	}

	loader := &config.Cached{Source: config.ObjectSource{Getter: s3c, Bucket: bucket, Key: key}}
	cfg, err := loader.Get(ctx)
	if err != nil {
		return nil, err
	}
	if cfg.Instance != instance {
		// A mismatch means the function is serving another instance's configuration —
		// the most confusing possible deployment fault, because every threshold is
		// plausible and simply belongs to the wrong environment.
		return nil, fmt.Errorf("config declares instance %q but CHINTAN_INSTANCE is %q", cfg.Instance, instance)
	}

	log.Info("config loaded",
		slog.String("instance", cfg.Instance),
		slog.Int("config_version", cfg.Version),
		slog.String("active_stt", cfg.Providers.STT.Active),
		// Worth logging at start-up: when false, Phase 4's corrections must route to the
		// LLM cleanup layer instead of silently producing none (§7.1, G-042).
		slog.Bool("prompt_biasing", cfg.PromptBiasingAvailable()),
	)

	mux := router.New(cfg)
	return newProxyHandler(mux, cfg.AllowedOrigin, log), nil
}

// newProxyHandler adapts API Gateway HTTP API v2 events to net/http, returning the
// handler FUNCTION lambda.Start expects.
//
// Returning the adapter struct instead fails at the first request, not at start-up, with
// "handler kind ptr is not func" — after the cold start has already logged "api ready".
// So the function reported healthy, served a 500 on everything, and the log said nothing
// about configuration or permissions. Returning the bound method value is what
// lambda.Start actually accepts.
//
// Written against net/http rather than the raw event shape so handlers stay testable with
// httptest and uncoupled from the invocation transport — the same handler serves a unit
// test and a Lambda.
func newProxyHandler(mux http.Handler, allowedOrigin string, log *slog.Logger) any {
	return newV2Handler(mux, allowedOrigin, log).Handle
}
