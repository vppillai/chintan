package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/vppillai/chintan/backend/internal/meter"
	"github.com/vppillai/chintan/backend/internal/obs"
)

// The start-up pricing check: an unpriced model stops the worker with a
// sentence naming the missing key and where to add it; a model priced at the
// provider's wildcard starts, but is counted once so an operator can see it;
// a model with its own row is silent.
func TestCheckPricedRefusesAnUnpricedModelAndCountsAWildcard(t *testing.T) {
	prices := meter.PriceTable{
		meter.Key("groq", "*"):            {meter.UnitAudioSeconds: 10},
		meter.Key("openai", "minimax-m3"): {meter.UnitInputTokens: 1, meter.UnitOutputTokens: 4},
	}
	cases := []struct {
		name            string
		provider, model string
		wantErr         []string
		wantMetric      bool
	}{
		{"exact row", "openai", "MiniMax-M3", nil, false},
		{"wildcard row", "groq", "whisper-large-v3", nil, true},
		{"no row", "openai", "gpt-5", []string{`"openai/gpt-5"`, `"openai/*"`, "meter.DefaultPrices", "backend/internal/meter/meter.go"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var metrics bytes.Buffer
			restore := obs.SetMetricOutput(&metrics)
			defer restore()

			err := checkPriced(context.Background(), prices, tc.provider, tc.model)
			if (err != nil) != (tc.wantErr != nil) {
				t.Fatalf("checkPriced(%s, %s) = %v, want error %v", tc.provider, tc.model, err, tc.wantErr != nil)
			}
			for _, want := range tc.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not name %s", err, want)
				}
			}
			got := strings.Count(metrics.String(), `"Name":"PriceWildcardUsed"`)
			if tc.wantMetric && got != 1 {
				t.Fatalf("PriceWildcardUsed emitted %d times, want once:\n%s", got, metrics.String())
			}
			if !tc.wantMetric && got != 0 {
				t.Fatalf("PriceWildcardUsed emitted for a model that %s", tc.name)
			}
			if tc.wantMetric && !strings.Contains(metrics.String(), `"Provider":"`+tc.provider+`"`) {
				t.Errorf("PriceWildcardUsed carries no Provider dimension:\n%s", metrics.String())
			}
		})
	}
}

// The models the template deploys must pass the check the worker runs on
// them, or a default install cannot start.
func TestDeployedDefaultsPassThePricingCheck(t *testing.T) {
	for _, m := range []struct{ provider, model string }{{"groq", "whisper-large-v3-turbo"}, {"openai", "MiniMax-M3"}} {
		if err := checkPriced(context.Background(), meter.DefaultPrices, m.provider, m.model); err != nil {
			t.Errorf("%s/%s: %v", m.provider, m.model, err)
		}
	}
}
