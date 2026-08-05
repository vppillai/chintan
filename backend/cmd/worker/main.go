// Command worker is the asynchronous pipeline Lambda (§4).
//
// S3-event and schedule invoked, never externally reachable — there is no API Gateway in
// front of it, which is both a cost saving and a smaller attack surface (§10.2).
//
// Profile: higher memory than the API, longer timeout. Sized for the Phase 5
// semantic-search matrix, which must be resident: rows x dimensions x 4 bytes, so 50,000
// blocks at 1536 dimensions is ~307MB (G-061).
//
// The pipeline chains within one invocation rather than across Step Functions: ~2,000
// state transitions/month is cheap but unnecessary complexity, and §10.2 says to revisit
// only if the pipeline approaches the 15-minute Lambda ceiling.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/vppillai/chintan/backend/internal/awsclient"
	"github.com/vppillai/chintan/backend/internal/config"
	"github.com/vppillai/chintan/backend/internal/logging"
	"github.com/vppillai/chintan/backend/internal/version"
)

func main() {
	log := logging.New()

	cfg, err := loadConfig(context.Background())
	if err != nil {
		log.Error("cold start failed", slog.String("error", err.Error()))
		os.Exit(1)
	}

	log.Info("worker ready",
		slog.String("version", version.Display()),
		slog.String("instance", cfg.Instance),
		slog.Bool("stamped", version.Stamped()),
	)

	lambda.Start(newDispatcher(cfg, log).Handle)
}

func loadConfig(ctx context.Context) (*config.Config, error) {
	bucket, key := os.Getenv("CHINTAN_CONFIG_BUCKET"), os.Getenv("CHINTAN_CONFIG_KEY")
	if bucket == "" || key == "" {
		return nil, fmt.Errorf("CHINTAN_CONFIG_BUCKET and CHINTAN_CONFIG_KEY must be set")
	}
	s3c, err := awsclient.NewS3(ctx)
	if err != nil {
		return nil, err
	}
	loader := &config.Cached{Source: config.ObjectSource{Getter: s3c, Bucket: bucket, Key: key}}
	return loader.Get(ctx)
}

// dispatcher routes an invocation to the right pipeline entry point.
//
// One function, two invocation sources (§Phase 2): S3 ObjectCreated for new audio, and
// EventBridge scheduled rules for the weekly metrics and corpus-integrity sweeps. They
// arrive as different event shapes on the same handler, so the first job is telling them
// apart — and doing so by structure rather than by guessing, because a misidentified
// event would run the wrong pipeline against real data.
type dispatcher struct {
	cfg *config.Config
	log *slog.Logger
}

func newDispatcher(cfg *config.Config, log *slog.Logger) *dispatcher {
	return &dispatcher{cfg: cfg, log: log}
}

// Handle dispatches one invocation.
func (d *dispatcher) Handle(ctx context.Context, raw json.RawMessage) error {
	// Scheduled rules carry a `detail-type`; S3 notifications carry `Records`. Checked
	// in that order because an EventBridge envelope never contains Records, whereas a
	// hand-crafted test event might contain both.
	var probe struct {
		DetailType string            `json:"detail-type"`
		Resources  []string          `json:"resources"`
		Records    []json.RawMessage `json:"Records"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return fmt.Errorf("worker: unrecognisable event: %w", err)
	}

	switch {
	case probe.DetailType != "" || len(probe.Resources) > 0:
		return d.handleSchedule(ctx, raw)
	case len(probe.Records) > 0:
		return d.handleS3(ctx, raw)
	default:
		// Fail rather than silently succeed. A no-op return on an unrecognised event
		// means a lost capture that nothing reports, and I2 says audio is never lost to
		// a software bug.
		return fmt.Errorf("worker: event matched no known shape; refusing to treat it as a no-op (I2)")
	}
}

func (d *dispatcher) handleS3(ctx context.Context, raw json.RawMessage) error {
	var ev events.S3Event
	if err := json.Unmarshal(raw, &ev); err != nil {
		return fmt.Errorf("worker: decoding S3 event: %w", err)
	}
	log := logging.FromContext(ctx, d.log)
	for _, rec := range ev.Records {
		// The object key is not content, but it contains the tenant and capture IDs, so
		// it is logged as an identifier and nothing about it is treated as trusted
		// input — the tenant a request acts on comes from the key structure, which the
		// keys package is the only thing permitted to parse (I11).
		log.Info("audio object received",
			slog.String("bucket", rec.S3.Bucket.Name),
			slog.String("key", rec.S3.Object.Key),
			slog.Int64("size", rec.S3.Object.Size),
		)
	}
	// Phase 1 attaches transcription here; Phase 2 adds server-side VAD for every
	// presegmented:false source. Deliberately not stubbed with a fake success: a
	// pipeline that reports completion without doing anything is indistinguishable from
	// one that works, right up until someone looks for a transcript.
	return fmt.Errorf("worker: transcription pipeline is not implemented yet (Phase 1); refusing to acknowledge audio it cannot process (I2)")
}

func (d *dispatcher) handleSchedule(ctx context.Context, raw json.RawMessage) error {
	var ev events.CloudWatchEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		return fmt.Errorf("worker: decoding scheduled event: %w", err)
	}
	logging.FromContext(ctx, d.log).Info("scheduled invocation",
		slog.String("detail_type", ev.DetailType),
		slog.Any("resources", ev.Resources),
	)
	// The three scheduled rules (§7.4): weekly metrics (§11A.9), weekly corpus integrity
	// (§11.6), and optional nightly deferred cleanup (§10.5.6). They arrive from Phase 2.
	return nil
}
