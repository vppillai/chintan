// Package meter prices what a third-party call cost.
//
// It is the arithmetic half of internal/breaker: the breaker reserves and
// enforces, this converts "so many audio seconds" or "so many tokens" into
// microdollars. Nothing here persists anything — the breaker's own log line is
// the only record of a call, and the SPEND# counter in the table is the only
// running total.
package meter

import (
	"math"
	"strings"
)

// Unit is what a provider bills for.
type Unit string

const (
	UnitAudioSeconds Unit = "audio_seconds"
	UnitInputTokens  Unit = "input_tokens"
	UnitOutputTokens Unit = "output_tokens"
)

// Op names the pipeline stage that incurred the cost.
type Op string

const (
	OpTranscribe Op = "transcribe"
	OpRoute      Op = "route"
	OpCleanup    Op = "cleanup"
	// OpCleanNote is the whole-note cleaned view: one call over the entire
	// body, priced like cleanup (input and output tokens on the LLM model).
	OpCleanNote Op = "clean_note"
	// OpAsk is a question answered over the tenant's notes (backlog D5): one
	// call carrying the packed notes and the question, priced on the LLM
	// model's input and output tokens like every other completion.
	OpAsk Op = "ask"
)

// Quantities is what one call consumed, per unit. A call is priced as the sum
// over its units, which is how a completion is charged for the tokens it read
// AND the tokens it wrote: pricing input alone understated cleanup by roughly
// the output-to-input price ratio, four to five times.
type Quantities map[Unit]float64

// PriceTable maps "provider/model" to a per-unit price in microdollars.
//
// Prices are configuration, not constants: they change without notice and
// differ per account. A missing entry falls back to the provider-wide "*" row,
// and a missing provider prices at zero rather than blocking the call — an
// unpriced model must not become an outage at runtime. It must not be silent
// either: pricing at zero means the breaker enforces nothing and the cap the
// operator set is a fiction, so the worker checks the configured models with
// Resolve at start-up and refuses to run when neither row exists
// (cmd/worker). The runtime fallback then only covers a table edited out from
// under a running process, which is the case it was written for.
//
// The per-unit price is a float because the real numbers are fractions of a
// microdollar: a MiniMax input token is 0.3 µ$. The total is still whole
// microdollars, rounded up.
type PriceTable map[string]map[Unit]float64

// Key builds the lookup key for a provider and model.
func Key(provider, model string) string {
	return strings.ToLower(strings.TrimSpace(provider)) + "/" + strings.ToLower(strings.TrimSpace(model))
}

// Resolution says which row, if any, prices a provider and model.
type Resolution int

const (
	// ResolvedNone means neither the exact row nor the provider's "*" row
	// exists: every call would price at zero.
	ResolvedNone Resolution = iota
	// ResolvedWildcard means the provider's "*" row stands in for a model
	// that has no row of its own.
	ResolvedWildcard
	// ResolvedExact means the model has its own row.
	ResolvedExact
)

// Resolve reports which row would price calls on provider and model. It is
// the start-up check's question; CostMicros answers the per-call one with the
// same lookup.
func (p PriceTable) Resolve(provider, model string) Resolution {
	if _, ok := p[Key(provider, model)]; ok {
		return ResolvedExact
	}
	if _, ok := p[Key(provider, "*")]; ok {
		return ResolvedWildcard
	}
	return ResolvedNone
}

// Resolves reports whether calls on provider and model would be priced at all.
func (p PriceTable) Resolves(provider, model string) bool {
	return p.Resolve(provider, model) != ResolvedNone
}

// row is the lookup CostMicros and Resolve share, so the two cannot disagree
// about which row a call is priced from.
func (p PriceTable) row(provider, model string) (map[Unit]float64, bool) {
	if row, ok := p[Key(provider, model)]; ok {
		return row, true
	}
	row, ok := p[Key(provider, "*")]
	return row, ok
}

// CostMicros prices one unit of one call.
func (p PriceTable) CostMicros(provider, model string, unit Unit, quantity float64) int64 {
	if quantity <= 0 || math.IsNaN(quantity) || math.IsInf(quantity, 0) {
		return 0
	}
	row, ok := p.row(provider, model)
	if !ok {
		return 0
	}
	per, ok := row[unit]
	if !ok {
		return 0
	}
	// Round up: a fractional microdollar that rounds to zero would let a high
	// call rate accumulate real spend that the breaker never sees.
	return int64(math.Ceil(quantity * per))
}

// Cost prices a whole call: the sum over every unit it consumed.
func (p PriceTable) Cost(provider, model string, q Quantities) int64 {
	var total int64
	for unit, quantity := range q {
		total += p.CostMicros(provider, model, unit, quantity)
	}
	return total
}

// DefaultPrices is the table the worker ships with, in microdollars per unit.
//
// These are list prices for budgeting, not an invoice. Each row says where its
// number came from and when it was read; a price that could not be verified
// says so rather than pretending.
var DefaultPrices = PriceTable{
	// Groq speech-to-text, groq.com/pricing read 2026-09-03. Billed per hour
	// of audio with a 10-second minimum per request, which the estimate below
	// ignores (a 3-second clip is priced as 3 seconds, not 10; at $0.04/hour
	// the difference is a tenth of a microdollar).
	//
	//   whisper-large-v3-turbo  $0.040 / hour = 11.11 µ$/s
	//   whisper-large-v3        $0.111 / hour = 30.83 µ$/s
	//
	// The wildcard is the dearer model, so an unrecognised Groq model is
	// over-reserved rather than under.
	Key("groq", "whisper-large-v3-turbo"): {UnitAudioSeconds: 0.04 * 1_000_000 / 3600},
	Key("groq", "*"):                      {UnitAudioSeconds: 0.111 * 1_000_000 / 3600},

	// The OpenAI-compatible endpoint the worker points at is MiniMax by default
	// (LLM_BASE_URL in the template), so the provider key is "openai" and the
	// model is what the endpoint bills. MiniMax-M3, platform.minimax.io
	// pricing read 2026-09-03, standard tier (≤ 512K input): $0.30 per million
	// input tokens, $1.20 per million output tokens. That is a "permanent 50%
	// off" of a $0.60 / $2.40 list price; if the promotion ends the row is
	// 2× low, which the wildcard below covers.
	Key("openai", "minimax-m3"): {UnitInputTokens: 0.30, UnitOutputTokens: 1.20},
	// Any other model on the endpoint: the pre-2026-09-03 numbers ($1 / $4 per
	// million), kept because they were never tied to a price sheet and there
	// is no invoice to correct them from. They are 3–4× MiniMax's list price,
	// so an unrecognised model over-reserves.
	Key("openai", "*"): {UnitInputTokens: 1, UnitOutputTokens: 4},
}
