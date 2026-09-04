package meter

import (
	"math"
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
	// A sub-microdollar unit price, which the real MiniMax rows have.
	p = PriceTable{Key("openai", "*"): {UnitInputTokens: 0.3}}
	if got := p.CostMicros("openai", "m", UnitInputTokens, 10); got != 3 {
		t.Fatalf("10 tokens at 0.3 µ$ = %d, want 3", got)
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

// A completion is charged for what it read and what it wrote. Pricing input
// alone understates cleanup — long output, priced higher — by roughly 5×.
func TestCostSumsEveryUnitTheCallConsumed(t *testing.T) {
	p := PriceTable{Key("openai", "*"): {UnitInputTokens: 1, UnitOutputTokens: 4}}
	got := p.Cost("openai", "m", Quantities{UnitInputTokens: 1000, UnitOutputTokens: 500})
	if got != 1000+2000 {
		t.Fatalf("cost = %d, want 3000 (1000 in at 1 + 500 out at 4)", got)
	}
	if got := p.Cost("openai", "m", nil); got != 0 {
		t.Fatalf("an empty call priced at %d", got)
	}
}

func TestDefaultPricesCoverBothProvidersAndBothTokenKinds(t *testing.T) {
	if DefaultPrices.CostMicros("groq", "whisper-large-v3-turbo", UnitAudioSeconds, 3600) <= 0 {
		t.Fatal("groq audio is unpriced in DefaultPrices")
	}
	// The models the template deploys have their own rows, and the rows are
	// the list prices: an hour of turbo is $0.04, a million MiniMax-M3 input
	// tokens is $0.30 and a million output tokens is $1.20.
	if got := DefaultPrices.CostMicros("groq", "whisper-large-v3-turbo", UnitAudioSeconds, 3600); got != 40_000 {
		t.Fatalf("an hour of whisper-large-v3-turbo = %d µ$, want 40000", got)
	}
	if got := DefaultPrices.CostMicros("openai", "MiniMax-M3", UnitInputTokens, 1_000_000); got != 300_000 {
		t.Fatalf("1M MiniMax-M3 input tokens = %d µ$, want 300000", got)
	}
	if got := DefaultPrices.CostMicros("openai", "MiniMax-M3", UnitOutputTokens, 1_000_000); got != 1_200_000 {
		t.Fatalf("1M MiniMax-M3 output tokens = %d µ$, want 1200000", got)
	}
	// An unrecognised model on either provider still prices, and no cheaper
	// than the recognised ones.
	if DefaultPrices.CostMicros("groq", "whisper-unknown", UnitAudioSeconds, 60) < DefaultPrices.CostMicros("groq", "whisper-large-v3-turbo", UnitAudioSeconds, 60) {
		t.Fatal("the groq wildcard is cheaper than the model it stands in for")
	}
	if DefaultPrices.CostMicros("openai", "gpt", UnitOutputTokens, 1000) <= 0 {
		t.Fatal("openai output tokens are unpriced in DefaultPrices")
	}
}
