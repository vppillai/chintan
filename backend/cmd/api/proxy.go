package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/aws/aws-lambda-go/events"
	"github.com/vppillai/chintan/backend/internal/logging"
)

// v2Handler adapts an API Gateway HTTP API v2 proxy event to an http.Handler.
//
// The alternative — writing handlers against the event struct directly — would make
// every handler untestable without constructing a synthetic Lambda event, and would
// couple business logic to the invocation transport. This adapter is the only code that
// knows what a Lambda event looks like.
type v2Handler struct {
	mux           http.Handler
	allowedOrigin string
	log           *slog.Logger
}

func newV2Handler(mux http.Handler, allowedOrigin string, log *slog.Logger) *v2Handler {
	return &v2Handler{mux: mux, allowedOrigin: allowedOrigin, log: log}
}

// Handle serves one request.
func (h *v2Handler) Handle(ctx context.Context, ev events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	// One correlation ID per request, taken from the API Gateway request ID so a log
	// line here can be joined to the access log and — once an upload triggers the
	// worker — to the pipeline that followed (§Phase 0's structured logging with
	// correlation IDs).
	ctx = logging.WithCorrelationID(ctx, ev.RequestContext.RequestID)

	req, err := h.toRequest(ctx, ev)
	if err != nil {
		logging.FromContext(ctx, h.log).Warn("malformed request", logging.ErrorAttr(err))
		return events.APIGatewayV2HTTPResponse{
			StatusCode: http.StatusBadRequest,
			Headers:    map[string]string{"Content-Type": "application/json"},
			Body:       `{"error":"bad_request"}`,
		}, nil
	}

	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	res := rec.Result()
	defer func() { _ = res.Body.Close() }()

	body, _ := io.ReadAll(res.Body)

	headers := map[string]string{}
	for k, v := range res.Header {
		if len(v) > 0 {
			headers[k] = v[0]
		}
	}

	return events.APIGatewayV2HTTPResponse{
		StatusCode: res.StatusCode,
		Headers:    headers,
		Body:       string(body),
	}, nil
}

// toRequest builds an http.Request from the event.
func (h *v2Handler) toRequest(ctx context.Context, ev events.APIGatewayV2HTTPRequest) (*http.Request, error) {
	method := ev.RequestContext.HTTP.Method
	if method == "" {
		method = http.MethodGet
	}

	// RawPath is the un-decoded path, which is what routing must use. The decoded form
	// would let an encoded "%2F" become a path separator after routing had already
	// decided, which is a classic path-confusion bug.
	path := ev.RawPath
	if path == "" {
		path = "/"
	}
	target := path
	if ev.RawQueryString != "" {
		target += "?" + ev.RawQueryString
	}

	var body io.Reader = strings.NewReader("")
	if ev.Body != "" {
		if ev.IsBase64Encoded {
			// API Gateway base64-encodes binary bodies. Audio never arrives this way —
			// I3 forbids audio bytes transiting API Gateway at all, and clients upload
			// to S3 via presigned PUT — so a base64 body here is small, and decoding it
			// wholesale is safe.
			decoded, err := decodeBase64(ev.Body)
			if err != nil {
				return nil, err
			}
			body = strings.NewReader(decoded)
		} else {
			body = strings.NewReader(ev.Body)
		}
	}

	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	for k, v := range ev.Headers {
		req.Header.Set(k, v)
	}
	return req, nil
}
