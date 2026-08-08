package meter

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"
)

func TestPriceTableLooksUpExactModelFirst(t *testing.T) {
	p := PriceTable{
		Key("groq", "*"):             {UnitAudioSeconds: 10},
		Key("groq", "whisper-large"): {UnitAudioSeconds: 25},
	}
	if got := p.CostMicros("groq", "whisper-large", UnitAudioSeconds, 4); got != 100 {
		t.Fatalf("exact model price not used: %d", got)
	}
	if got := p.CostMicros("groq", "whisper-tiny", UnitAudioSeconds, 4); got != 40 {
		t.Fatalf("wildcard fallback not used: %d", got)
	}
}

func TestPriceTableIsCaseAndSpaceInsensitive(t *testing.T) {
	p := PriceTable{Key("groq", "*"): {UnitAudioSeconds: 10}}
	if got := p.CostMicros("  GROQ ", "Whisper", UnitAudioSeconds, 1); got != 10 {
		t.Fatalf("got %d", got)
	}
}

// An unpriced model must not become an outage.
func TestPriceTableReturnsZeroForUnknownProviderOrUnit(t *testing.T) {
	p := PriceTable{Key("groq", "*"): {UnitAudioSeconds: 10}}
	if got := p.CostMicros("anthropic", "x", UnitInputTokens, 100); got != 0 {
		t.Fatalf("unknown provider should price at zero, got %d", got)
	}
	if got := p.CostMicros("groq", "x", UnitInputTokens, 100); got != 0 {
		t.Fatalf("unknown unit should price at zero, got %d", got)
	}
}

// A fractional microdollar that rounded down to zero would let a high call rate
// accumulate real spend the breaker never sees.
func TestPriceTableRoundsUp(t *testing.T) {
	p := PriceTable{Key("openai", "*"): {UnitInputTokens: 1}}
	if got := p.CostMicros("openai", "m", UnitInputTokens, 0.2); got != 1 {
		t.Fatalf("sub-unit quantity must round up, got %d", got)
	}
}

func TestPriceTableRejectsNonsenseQuantities(t *testing.T) {
	p := PriceTable{Key("groq", "*"): {UnitAudioSeconds: 10}}
	for _, q := range []float64{0, -5, math.NaN(), math.Inf(1), math.Inf(-1)} {
		if got := p.CostMicros("groq", "m", UnitAudioSeconds, q); got != 0 {
			t.Fatalf("quantity %v priced at %d, want 0", q, got)
		}
	}
}

func TestDefaultPricesCoverBothProviders(t *testing.T) {
	if DefaultPrices.CostMicros("groq", "whisper", UnitAudioSeconds, 60) <= 0 {
		t.Fatal("groq audio is unpriced in DefaultPrices")
	}
	if DefaultPrices.CostMicros("openai", "gpt", UnitInputTokens, 1000) <= 0 {
		t.Fatal("openai input tokens are unpriced in DefaultPrices")
	}
	if DefaultPrices.CostMicros("openai", "gpt", UnitOutputTokens, 1000) <= 0 {
		t.Fatal("openai output tokens are unpriced in DefaultPrices")
	}
}

type recordingSink struct {
	n   int
	err error
}

func (r *recordingSink) Record(context.Context, Usage) error {
	r.n++
	return r.err
}

func TestMultiSinkAttemptsAllEvenAfterFailure(t *testing.T) {
	a := &recordingSink{err: errors.New("first down")}
	b := &recordingSink{}
	c := &recordingSink{err: errors.New("third down")}

	err := MultiSink{a, b, c}.Record(context.Background(), Usage{})
	if err == nil {
		t.Fatal("expected the first failure to surface")
	}
	if a.n != 1 || b.n != 1 || c.n != 1 {
		t.Fatalf("all sinks must be attempted: a=%d b=%d c=%d", a.n, b.n, c.n)
	}
}

func TestMultiSinkSucceedsWhenAllSucceed(t *testing.T) {
	a, b := &recordingSink{}, &recordingSink{}
	if err := (MultiSink{a, b}).Record(context.Background(), Usage{}); err != nil {
		t.Fatalf("got %v", err)
	}
}

// Usage is written to logs and to DynamoDB; it must never carry content.
func TestUsageCarriesNoContentFields(t *testing.T) {
	u := Usage{TenantID: "t", Provider: "groq", Model: "m", Op: OpTranscribe}
	_ = u
	// Compile-time intent: if someone adds a Transcript/Prompt/Text field to
	// Usage, this test is the place that should stop them. Enumerate the
	// permitted field names explicitly.
	permitted := map[string]bool{
		"TenantID": true, "Provider": true, "Model": true, "Op": true,
		"Unit": true, "Quantity": true, "CostMicros": true,
		"CorrelationID": true, "At": true,
	}
	typ := typeOfUsage()
	for i := 0; i < typ.NumField(); i++ {
		if name := typ.Field(i).Name; !permitted[name] {
			t.Fatalf("Usage gained field %q — usage records must never carry content", name)
		}
	}
}

func typeOfUsage() reflect.Type { return reflect.TypeOf(Usage{}) }
