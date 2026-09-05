package awscost

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/budgets"
	"github.com/aws/aws-sdk-go-v2/service/budgets/types"

	"github.com/vppillai/chintan/backend/internal/repository/memory"
	"github.com/vppillai/chintan/backend/internal/usage"
)

// fakeBudgets answers DescribeBudget with one canned budget and records what
// it was asked for.
type fakeBudgets struct {
	budget *types.Budget
	err    error
	calls  []*budgets.DescribeBudgetInput
}

func (f *fakeBudgets) DescribeBudget(_ context.Context, in *budgets.DescribeBudgetInput, _ ...func(*budgets.Options)) (*budgets.DescribeBudgetOutput, error) {
	f.calls = append(f.calls, in)
	if f.err != nil {
		return nil, f.err
	}
	return &budgets.DescribeBudgetOutput{Budget: f.budget}, nil
}

func spend(amount, unit string) *types.Spend {
	return &types.Spend{Amount: aws.String(amount), Unit: aws.String(unit)}
}

func newCollector(t *testing.T, client Budgets, name string, now time.Time) (*Collector, *memory.Usage) {
	t.Helper()
	store := memory.NewUsage()
	c, err := New(client, store, "123456789012", name)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c.now = func() time.Time { return now }
	return c, store
}

// The whole task: ask the budget by account and name, convert its dollars to
// microdollars exactly, and store the reading under the current UTC month.
func TestRunStoresTheBudgetsActualSpendForTheMonth(t *testing.T) {
	client := &fakeBudgets{budget: &types.Budget{
		BudgetName:      aws.String("chintan-dev-prod-MonthlyBudget-ABC"),
		BudgetLimit:     spend("10", "USD"),
		CalculatedSpend: &types.CalculatedSpend{ActualSpend: spend("2.3456789", "USD")},
	}}
	now := time.Date(2026, 9, 4, 6, 15, 9, 500, time.UTC)
	c, store := newCollector(t, client, "chintan-dev-prod-MonthlyBudget-ABC", now)

	got, err := c.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(client.calls) != 1 {
		t.Fatalf("DescribeBudget calls = %d, want 1", len(client.calls))
	}
	if in := client.calls[0]; aws.ToString(in.AccountId) != "123456789012" || aws.ToString(in.BudgetName) != "chintan-dev-prod-MonthlyBudget-ABC" {
		t.Errorf("asked for %+v", in)
	}

	stored, err := store.AWSCost(context.Background(), "2026-09")
	if err != nil || stored == nil {
		t.Fatalf("stored = %+v, %v", stored, err)
	}
	if stored.MonthMicros != 2_345_678 {
		t.Errorf("month_micros = %d, want 2345678 (truncated to the microdollar)", stored.MonthMicros)
	}
	if stored.BudgetMicros == nil || *stored.BudgetMicros != 10_000_000 {
		t.Errorf("budget_micros = %v, want 10000000", stored.BudgetMicros)
	}
	if !stored.AsOf.Equal(time.Date(2026, 9, 4, 6, 15, 9, 0, time.UTC)) {
		t.Errorf("as_of = %v, want the read time to the second", stored.AsOf)
	}
	if got == nil || *got != *stored {
		t.Errorf("Run returned %+v, stored %+v", got, stored)
	}
}

// Without a budget name there is nothing to ask; the task says so and stops.
// This is a stack without an alarm address, not a fault, so it must not reach
// the client and must not return an error for Lambda to retry.
func TestRunIsANoOpWithoutABudgetName(t *testing.T) {
	client := &fakeBudgets{err: errors.New("must not be called")}
	c, store := newCollector(t, client, "", time.Date(2026, 9, 4, 6, 0, 0, 0, time.UTC))

	got, err := c.Run(context.Background())
	if err != nil || got != nil {
		t.Fatalf("Run = %+v, %v; want nil, nil", got, err)
	}
	if len(client.calls) != 0 {
		t.Errorf("DescribeBudget was called %d times", len(client.calls))
	}
	if stored, _ := store.AWSCost(context.Background(), "2026-09"); stored != nil {
		t.Errorf("stored %+v without a budget", stored)
	}

	// No client at all is fine when there is no budget; a budget without a
	// client or an account is a build error.
	if _, err := New(nil, store, "", ""); err != nil {
		t.Errorf("New without a budget: %v", err)
	}
	if _, err := New(nil, store, "123456789012", "b"); err == nil {
		t.Error("New accepted a budget name with no client")
	}
	if _, err := New(client, store, "", "b"); err == nil {
		t.Error("New accepted a budget name with no account id")
	}
}

// The row is per calendar month in UTC. The reading a minute before midnight
// on the 30th belongs to September; the one a minute after belongs to October,
// whatever the local calendar says.
func TestRunKeysTheRowByTheUTCMonth(t *testing.T) {
	client := &fakeBudgets{budget: &types.Budget{
		CalculatedSpend: &types.CalculatedSpend{ActualSpend: spend("0.5", "USD")},
	}}
	// 2026-09-30 23:59 UTC is already 2026-10-01 in Kolkata (UTC+5:30).
	kolkata := time.FixedZone("IST", 5*3600+1800)
	before := time.Date(2026, 10, 1, 5, 29, 0, 0, kolkata) // 2026-09-30T23:59Z
	after := before.Add(2 * time.Minute)                   // 2026-10-01T00:01Z

	c, store := newCollector(t, client, "b", before)
	if _, err := c.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	c.now = func() time.Time { return after }
	if _, err := c.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	sep, _ := store.AWSCost(context.Background(), "2026-09")
	oct, _ := store.AWSCost(context.Background(), "2026-10")
	if sep == nil || oct == nil {
		t.Fatalf("september = %+v, october = %+v; want one row each", sep, oct)
	}
	if !sep.AsOf.Equal(before.Truncate(time.Second)) || !oct.AsOf.Equal(after.Truncate(time.Second)) {
		t.Errorf("as_of: september %v, october %v", sep.AsOf, oct.AsOf)
	}
	if sep.BudgetMicros != nil {
		t.Errorf("a budget without a limit stored budget_micros = %d", *sep.BudgetMicros)
	}
}

// Only an AWS call failing is retryable. A budget with no figure yet, or one
// in another currency, is dropped with a log line: retrying cannot change it.
func TestRunRetriesOnlyOnAnAWSFault(t *testing.T) {
	now := time.Date(2026, 9, 4, 6, 0, 0, 0, time.UTC)

	c, _ := newCollector(t, &fakeBudgets{err: errors.New("throttled")}, "b", now)
	if _, err := c.Run(context.Background()); err == nil {
		t.Error("a failed DescribeBudget must be returned for Lambda to retry")
	}

	for name, budget := range map[string]*types.Budget{
		"no calculated spend yet": {BudgetLimit: spend("10", "USD")},
		"not in USD":              {CalculatedSpend: &types.CalculatedSpend{ActualSpend: spend("3", "EUR")}},
		"not a number":            {CalculatedSpend: &types.CalculatedSpend{ActualSpend: spend("3 dollars", "USD")}},
	} {
		c, store := newCollector(t, &fakeBudgets{budget: budget}, "b", now)
		got, err := c.Run(context.Background())
		if err != nil || got != nil {
			t.Errorf("%s: Run = %+v, %v; want nil, nil", name, got, err)
		}
		if stored, _ := store.AWSCost(context.Background(), "2026-09"); stored != nil {
			t.Errorf("%s: stored %+v", name, stored)
		}
	}
}

func TestUSDToMicrosIsExact(t *testing.T) {
	cases := []struct {
		amount string
		want   int64
		bad    bool
	}{
		{"0", 0, false},
		{"10", 10_000_000, false},
		{"2.3456789", 2_345_678, false},
		{"0.000001", 1, false},
		{".5", 500_000, false},
		{"12.", 12_000_000, false},
		{"-0.25", -250_000, false},
		{"1e3", 0, true},
		{"", 0, true},
		{"1,000", 0, true},
		{"99999999999999", 0, true},
	}
	for _, tc := range cases {
		got, err := usdToMicros(aws.String(tc.amount), aws.String("USD"))
		if tc.bad {
			if err == nil {
				t.Errorf("%q: accepted as %d", tc.amount, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%q = %d, %v; want %d", tc.amount, got, err, tc.want)
		}
	}
	if _, err := usdToMicros(aws.String("1"), aws.String("EUR")); err == nil {
		t.Error("EUR accepted as USD")
	}
	if _, err := usdToMicros(aws.String("1"), nil); err == nil {
		t.Error("a missing unit accepted as USD")
	}
	if _, err := usdToMicros(aws.String("1"), aws.String("usd")); err != nil {
		t.Errorf("lower-case usd rejected: %v", err)
	}
}

var _ usage.AWSCostStore = (*memory.Usage)(nil)
