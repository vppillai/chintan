package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/vppillai/chintan/backend/internal/config"
	"github.com/vppillai/chintan/backend/internal/router"
)

// The first deploy failed with "handler kind ptr is not func" — lambda.Start was given
// the adapter struct rather than a function. It failed at the first REQUEST, after the
// cold start had already logged "api ready", so the function looked healthy and returned
// 500 for everything with nothing in the log about why.
//
// lambda.NewHandler panics on a non-function, which is exactly the check that was
// missing, so this asserts the contract at build time instead of in production.
func TestHandlerIsAcceptableToLambdaStart(t *testing.T) {
	h := newProxyHandler(http.NewServeMux(), "https://example.com", slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("lambda would reject this handler: %v", r)
		}
	}()
	if lambda.NewHandler(h) == nil {
		t.Fatal("lambda.NewHandler returned nil")
	}
}

// End to end through the adapter: a v2 proxy event in, the real router, a response out.
// Proves the health contract the frontend's drift check depends on (§0.6).
func TestHealthThroughTheProxyAdapter(t *testing.T) {
	cfg, err := config.Load("../../../config/instances/dev.yaml")
	if err != nil {
		t.Fatal(err)
	}
	h := newV2Handler(router.New(cfg), cfg.AllowedOrigin, slog.New(slog.NewJSONHandler(os.Stderr, nil)))

	res, err := h.Handle(context.Background(), events.APIGatewayV2HTTPRequest{
		RawPath: "/v1/health",
		RequestContext: events.APIGatewayV2HTTPRequestContext{
			RequestID: "test-req",
			HTTP:      events.APIGatewayV2HTTPRequestContextHTTPDescription{Method: http.MethodGet},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", res.StatusCode, res.Body)
	}

	var body map[string]any
	if err := json.Unmarshal([]byte(res.Body), &body); err != nil {
		t.Fatalf("health body is not JSON: %v", err)
	}
	// The frontend compares these against its own build to flag drift, so their presence
	// is the contract rather than an implementation detail (§0.6).
	for _, field := range []string{"version", "commit", "config_version", "instance", "stamped"} {
		if _, ok := body[field]; !ok {
			t.Errorf("health response is missing %q, which the version-drift check needs (§0.6)", field)
		}
	}
	if body["instance"] != "dev" {
		t.Errorf("instance = %v, want dev", body["instance"])
	}
}

// An unknown path must 404, never anything that discloses what exists (§9.1).
func TestUnknownPathReturns404(t *testing.T) {
	cfg, err := config.Load("../../../config/instances/dev.yaml")
	if err != nil {
		t.Fatal(err)
	}
	h := newV2Handler(router.New(cfg), cfg.AllowedOrigin, slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	for _, path := range []string{"/v1/captures", "/health", "/", "/v1/../v1/health"} {
		res, err := h.Handle(context.Background(), events.APIGatewayV2HTTPRequest{
			RawPath:        path,
			RequestContext: events.APIGatewayV2HTTPRequestContext{HTTP: events.APIGatewayV2HTTPRequestContextHTTPDescription{Method: http.MethodGet}},
		})
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if res.StatusCode == http.StatusOK {
			t.Errorf("%s returned 200; this phase serves only /v1/health", path)
		}
	}
}
