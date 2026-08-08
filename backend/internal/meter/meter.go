// Package meter records what each third-party call cost.
//
// v1 had no visibility into STT seconds or LLM tokens at all, and that data
// cannot be reconstructed after the fact — which is why this lands before the
// features that increase usage rather than after.
//
// It is also the prerequisite for internal/breaker: you cannot cap spend you do
// not measure.
package meter

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/vppillai/chintan/backend/internal/obs"
)

// Unit is what a provider bills for.
type Unit string

const (
	UnitAudioSeconds Unit = "audio_seconds"
	UnitInputTokens  Unit = "input_tokens"
	UnitOutputTokens Unit = "output_tokens"
	UnitRequests     Unit = "requests"
)

// Op names the pipeline stage that incurred the cost.
type Op string

const (
	OpTranscribe Op = "transcribe"
	OpRoute      Op = "route"
	OpCleanup    Op = "cleanup"
)

// Usage is one billable event.
//
// It deliberately carries no transcript, prompt, or completion text — only
// shape and cost. Nothing in this record may reveal user content.
type Usage struct {
	TenantID      string    `json:"tenant_id"`
	Provider      string    `json:"provider"`
	Model         string    `json:"model"`
	Op            Op        `json:"op"`
	Unit          Unit      `json:"unit"`
	Quantity      float64   `json:"quantity"`
	CostMicros    int64     `json:"cost_micros"`
	CorrelationID string    `json:"correlation_id"`
	At            time.Time `json:"at"`
}

// Sink persists usage records.
type Sink interface {
	Record(ctx context.Context, u Usage) error
}

// Pricer converts a quantity into whole microdollars.
type Pricer interface {
	CostMicros(provider, model string, unit Unit, quantity float64) int64
}

// PriceTable maps "provider/model" to a per-unit price in microdollars.
//
// Prices are configuration, not constants: they change without notice and
// differ per account. A missing entry falls back to the provider-wide "*" row,
// and a missing provider prices at zero rather than blocking the call — an
// unpriced model must not become an outage.
type PriceTable map[string]map[Unit]int64

// Key builds the lookup key for a provider and model.
func Key(provider, model string) string {
	return strings.ToLower(strings.TrimSpace(provider)) + "/" + strings.ToLower(strings.TrimSpace(model))
}

// CostMicros implements Pricer.
func (p PriceTable) CostMicros(provider, model string, unit Unit, quantity float64) int64 {
	if quantity <= 0 || math.IsNaN(quantity) || math.IsInf(quantity, 0) {
		return 0
	}
	row, ok := p[Key(provider, model)]
	if !ok {
		row, ok = p[Key(provider, "*")]
	}
	if !ok {
		return 0
	}
	per, ok := row[unit]
	if !ok {
		return 0
	}
	// Round up: a fractional microdollar that rounds to zero would let a high
	// call rate accumulate real spend that the breaker never sees.
	return int64(math.Ceil(quantity * float64(per)))
}

// DefaultPrices is a starting table in microdollars per unit.
//
// These are estimates for budgeting, not billing. Override them per instance
// via configuration once real invoices are available.
var DefaultPrices = PriceTable{
	Key("groq", "*"):   {UnitAudioSeconds: 2},                     // ~$0.007/min
	Key("openai", "*"): {UnitInputTokens: 1, UnitOutputTokens: 4}, // ~$1/$4 per Mtok
}

// SlogSink writes usage to the structured log.
//
// Useful before the DynamoDB sink is wired, and as a durable audit trail
// afterwards: log retention outlives a table row.
type SlogSink struct{}

// Record implements Sink.
func (SlogSink) Record(ctx context.Context, u Usage) error {
	obs.Log(ctx).Info("provider usage",
		slog.String("provider", u.Provider),
		slog.String("model", u.Model),
		slog.String("op", string(u.Op)),
		slog.String("unit", string(u.Unit)),
		slog.Float64("quantity", u.Quantity),
		slog.Int64("cost_micros", u.CostMicros),
	)
	obs.Emit(ctx,
		map[string]string{"Provider": u.Provider, "Op": string(u.Op)},
		obs.Metric{Name: "ProviderCostMicros", Value: float64(u.CostMicros), Unit: obs.UnitCount},
		obs.Metric{Name: "ProviderCalls", Value: 1, Unit: obs.UnitCount},
	)
	return nil
}

// MultiSink fans a record out to several sinks, returning the first failure but
// always attempting all of them.
type MultiSink []Sink

// Record implements Sink.
func (m MultiSink) Record(ctx context.Context, u Usage) error {
	var firstErr error
	for _, s := range m {
		if err := s.Record(ctx, u); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("meter: sink failed: %w", err)
		}
	}
	return firstErr
}
