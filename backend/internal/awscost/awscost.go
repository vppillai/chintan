// Package awscost reads what the instance is costing in AWS, once a day, from
// the stack's own AWS Budget, and keeps the figure where GET /v1/usage can show
// it beside the provider spend.
//
// The source is deliberate. Cost Explorer answers the same question in more
// detail and charges $0.01 per request for it; a Budget already exists in the
// stack (MonthlyBudget, whenever there is an alarm address to notify), AWS
// keeps its month-to-date actual spend current up to three times a day, and
// budgets:DescribeBudget is free. So the task asks the budget, converts the
// dollars to the microdollars everything else here counts in, and writes one
// row per calendar month. Nothing here calls Cost Explorer, and nothing here
// is on any request path.
//
// An EventBridge rule invokes the worker daily with {"task":"aws-cost"}; the
// worker dispatches to Collector.Run. The budget's name reaches the worker as
// MONTHLY_BUDGET_NAME, empty when the stack has no budget — in which case the
// task says so at INFO and does nothing, and the API answers `aws: null`.
package awscost

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/budgets"

	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/usage"
)

// Task is the payload field value the EventBridge rule sends, and cmd/worker
// dispatches on. The template's rule and this constant must agree.
const Task = "aws-cost"

// Budgets is the one call this package makes. It is an interface so the task
// is tested against a canned budget rather than an account.
type Budgets interface {
	DescribeBudget(ctx context.Context, in *budgets.DescribeBudgetInput, opts ...func(*budgets.Options)) (*budgets.DescribeBudgetOutput, error)
}

// Collector runs one aws-cost task.
type Collector struct {
	budgets    Budgets
	store      usage.AWSCostStore
	accountID  string
	budgetName string
	now        func() time.Time
}

// New builds the collector. budgetName may be empty — a stack without an
// alarm address has no budget — and then Run is a logged no-op. With a name,
// the client and the account id are required: DescribeBudget addresses a
// budget by account and name, nothing else.
func New(client Budgets, store usage.AWSCostStore, accountID, budgetName string) (*Collector, error) {
	budgetName = strings.TrimSpace(budgetName)
	accountID = strings.TrimSpace(accountID)
	switch {
	case store == nil:
		return nil, errors.New("awscost: store is required")
	case budgetName != "" && client == nil:
		return nil, errors.New("awscost: budgets client is required when a budget is named")
	case budgetName != "" && accountID == "":
		return nil, errors.New("awscost: account id is required when a budget is named")
	}
	return &Collector{budgets: client, store: store, accountID: accountID, budgetName: budgetName, now: time.Now}, nil
}

// Run reads the budget and stores the current UTC month's reading. It returns
// the reading it stored, or nil when it stored nothing.
//
// The return protocol is the worker's: nil means done, an error means an
// infrastructure fault interrupted the work and Lambda should retry. Only the
// two AWS calls can produce that. A budget that has no spend figure yet (a
// budget created hours ago) or one that counts in something other than USD is
// logged and dropped — retrying cannot change either — and the API keeps
// answering null for the month until a later run succeeds.
func (c *Collector) Run(ctx context.Context) (*usage.AWSCost, error) {
	if c.budgetName == "" {
		obs.Log(ctx).Info("aws-cost: no budget in this stack (MONTHLY_BUDGET_NAME is empty); nothing to read")
		return nil, nil
	}

	out, err := c.budgets.DescribeBudget(ctx, &budgets.DescribeBudgetInput{
		AccountId:  aws.String(c.accountID),
		BudgetName: aws.String(c.budgetName),
	})
	if err != nil {
		return nil, fmt.Errorf("awscost: describe budget %q: %w", c.budgetName, err)
	}
	if out == nil || out.Budget == nil || out.Budget.CalculatedSpend == nil || out.Budget.CalculatedSpend.ActualSpend == nil {
		obs.Log(ctx).Warn("aws-cost: the budget reports no actual spend yet; nothing stored",
			slog.String("budget", c.budgetName))
		return nil, nil
	}

	actual := out.Budget.CalculatedSpend.ActualSpend
	monthMicros, err := usdToMicros(actual.Amount, actual.Unit)
	if err != nil {
		obs.Log(ctx).Error("aws-cost: the budget's actual spend is not a USD amount; nothing stored",
			slog.String("budget", c.budgetName),
			slog.String("error", err.Error()))
		return nil, nil
	}

	now := c.now().UTC()
	reading := usage.AWSCost{
		Month:       now.Format("2006-01"),
		MonthMicros: monthMicros,
		AsOf:        now.Truncate(time.Second),
	}
	if limit := out.Budget.BudgetLimit; limit != nil {
		if budgetMicros, err := usdToMicros(limit.Amount, limit.Unit); err == nil {
			reading.BudgetMicros = &budgetMicros
		} else {
			// A limit in another unit is not a reason to lose the spend figure.
			obs.Log(ctx).Warn("aws-cost: the budget's limit is not a USD amount; stored without it",
				slog.String("error", err.Error()))
		}
	}

	if err := c.store.PutAWSCost(ctx, reading); err != nil {
		return nil, err
	}
	attrs := []any{
		slog.String("month", reading.Month),
		slog.Int64("month_micros", reading.MonthMicros),
	}
	if reading.BudgetMicros != nil {
		attrs = append(attrs, slog.Int64("budget_micros", *reading.BudgetMicros))
	}
	obs.Log(ctx).Info("aws-cost: stored the month's reading", attrs...)
	return &reading, nil
}

// usdToMicros converts a Budgets amount — a decimal string in a named unit —
// to microdollars. It parses the decimal digit by digit rather than through a
// float: the amounts are small and a float would in fact be exact enough, but
// a money conversion that is exact by construction needs no such argument.
// Digits past the sixth decimal are truncated, which is below what AWS bills.
func usdToMicros(amount, unit *string) (int64, error) {
	if unit == nil || !strings.EqualFold(strings.TrimSpace(*unit), "USD") {
		return 0, fmt.Errorf("unit is %q, want USD", aws.ToString(unit))
	}
	s := strings.TrimSpace(aws.ToString(amount))
	if s == "" {
		return 0, errors.New("amount is empty")
	}
	negative := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")

	whole, frac, _ := strings.Cut(s, ".")
	if whole == "" {
		whole = "0"
	}
	if len(frac) > 6 {
		frac = frac[:6]
	}
	frac += strings.Repeat("0", 6-len(frac))
	if !allDigits(whole) || !allDigits(frac) {
		return 0, fmt.Errorf("amount %q is not a decimal number", aws.ToString(amount))
	}
	dollars, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || dollars > math.MaxInt64/1_000_000 {
		return 0, fmt.Errorf("amount %q is out of range", aws.ToString(amount))
	}
	// frac is exactly six digits here, so this cannot fail or overflow.
	micros, _ := strconv.ParseInt(frac, 10, 64)
	total := dollars*1_000_000 + micros
	if negative {
		total = -total
	}
	return total, nil
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
