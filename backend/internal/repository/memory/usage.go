package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/vppillai/chintan/backend/internal/meter"
	"github.com/vppillai/chintan/backend/internal/usage"
)

// Usage is an in-memory usage.Recorder and usage.Reader for tests and local
// development. It accumulates the same shape the DynamoDB rows hold — a month
// total, its per-op split, and one total per day — so a handler test reads
// back what the breaker would have written.
type Usage struct {
	mu      sync.Mutex
	records []usage.Record
	// requests counts authenticated API requests per tenant per day.
	requests map[string]map[string]int64
	// snapshots is the storage reading per tenant per day, one at most, as
	// the month row's storage_snapshot_day condition allows.
	snapshots map[string]map[string]usage.StorageSnapshot
	awsCost   map[string]usage.AWSCost
}

var (
	_ usage.Recorder        = (*Usage)(nil)
	_ usage.RequestCounter  = (*Usage)(nil)
	_ usage.Reader          = (*Usage)(nil)
	_ usage.AWSCostStore    = (*Usage)(nil)
	_ usage.StorageDayStore = (*Usage)(nil)
	_ usage.TenantLister    = (*Usage)(nil)
)

// NewUsage returns an empty in-memory usage store.
func NewUsage() *Usage { return &Usage{} }

// AddStorageDay keeps one reading per tenant per day, as the DynamoDB month
// row's condition does; a second reading for the same day is not added.
func (u *Usage) AddStorageDay(ctx context.Context, s usage.StorageSnapshot) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if s.TenantID == "" || len(s.Day) != len("2006-01-02") {
		return false, fmt.Errorf("usage: storage snapshot for %q on %q", s.TenantID, s.Day)
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.snapshots == nil {
		u.snapshots = map[string]map[string]usage.StorageSnapshot{}
	}
	if u.snapshots[s.TenantID] == nil {
		u.snapshots[s.TenantID] = map[string]usage.StorageSnapshot{}
	}
	if _, taken := u.snapshots[s.TenantID][s.Day]; taken {
		return false, nil
	}
	u.snapshots[s.TenantID][s.Day] = s
	return true, nil
}

// TenantsWithUsage is every tenant with a record, a request or a snapshot in
// any of the months, sorted — what the month rows' GSI1 keys answer on DynamoDB.
func (u *Usage) TenantsWithUsage(ctx context.Context, months []string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	inMonths := func(day string) bool {
		for _, m := range months {
			if strings.HasPrefix(day, m+"-") {
				return true
			}
		}
		return false
	}
	seen := map[string]bool{}
	for _, r := range u.records {
		if inMonths(r.Day) {
			seen[r.TenantID] = true
		}
	}
	for tenant, days := range u.requests {
		for day := range days {
			if inMonths(day) {
				seen[tenant] = true
			}
		}
	}
	for tenant, days := range u.snapshots {
		for day := range days {
			if inMonths(day) {
				seen[tenant] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for tenant := range seen {
		out = append(out, tenant)
	}
	sort.Strings(out)
	return out, nil
}

func (u *Usage) Record(ctx context.Context, r usage.Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.records = append(u.records, r)
	return nil
}

// CountRequest counts one request on the tenant's day, as the DynamoDB rows
// would. A malformed day is refused the same way.
func (u *Usage) CountRequest(ctx context.Context, tenantID, day string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if tenantID == "" || len(day) != len("2006-01-02") {
		return fmt.Errorf("usage: count request for %q on %q", tenantID, day)
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.requests == nil {
		u.requests = map[string]map[string]int64{}
	}
	if u.requests[tenantID] == nil {
		u.requests[tenantID] = map[string]int64{}
	}
	u.requests[tenantID][day]++
	return nil
}

// Requests returns the tenant's request count for one day, for tests.
func (u *Usage) Requests(tenantID, day string) int64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.requests[tenantID][day]
}

// Records returns everything recorded, in order, for tests to assert on.
func (u *Usage) Records() []usage.Record {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]usage.Record(nil), u.records...)
}

func (u *Usage) Month(ctx context.Context, tenantID, month string) (usage.Month, error) {
	if err := ctx.Err(); err != nil {
		return usage.Month{}, err
	}
	if !usage.ValidMonth(month) {
		return usage.Month{}, usage.ErrBadMonth
	}
	u.mu.Lock()
	defer u.mu.Unlock()

	out := usage.Month{Month: month, Ops: map[string]usage.Totals{}, Providers: map[string]usage.Totals{}, Days: []usage.Day{}}
	days := map[string]*usage.Day{}
	dayOf := func(date string) *usage.Day {
		if days[date] == nil {
			days[date] = &usage.Day{Date: date}
		}
		return days[date]
	}
	for _, r := range u.records {
		if r.TenantID != tenantID || !strings.HasPrefix(r.Day, month+"-") {
			continue
		}
		apply(&out.Totals, r)
		op := out.Ops[string(r.Op)]
		apply(&op, r)
		out.Ops[string(r.Op)] = op
		provider := strings.ToLower(strings.TrimSpace(r.Provider))
		prov := out.Providers[provider]
		apply(&prov, r)
		out.Providers[provider] = prov
		apply(&dayOf(r.Day).Totals, r)
	}
	for day, n := range u.requests[tenantID] {
		if strings.HasPrefix(day, month+"-") {
			dayOf(day).APIRequests += n
			out.APIRequests += n
		}
	}
	for day, s := range u.snapshots[tenantID] {
		if strings.HasPrefix(day, month+"-") {
			dayOf(day).StorageByteDays = s.AudioBytes
			out.StorageByteDays += s.AudioBytes
			out.StorageNoteDays += s.Notes
		}
	}
	for _, d := range days {
		out.Days = append(out.Days, *d)
	}
	sort.Slice(out.Days, func(i, j int) bool { return out.Days[i].Date < out.Days[j].Date })
	return out, nil
}

// InstanceSpend is every tenant's recorded cost in the month together, which
// is what the breaker's SPEND#<day> counters add up to in production.
func (u *Usage) InstanceSpend(ctx context.Context, month string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if !usage.ValidMonth(month) {
		return 0, usage.ErrBadMonth
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	var total int64
	for _, r := range u.records {
		if strings.HasPrefix(r.Day, month+"-") {
			total += r.CostMicros
		}
	}
	return total, nil
}

// PutAWSCost keeps the latest reading per month, as the DynamoDB row does.
func (u *Usage) PutAWSCost(ctx context.Context, c usage.AWSCost) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !usage.ValidMonth(c.Month) {
		return usage.ErrBadMonth
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.awsCost == nil {
		u.awsCost = map[string]usage.AWSCost{}
	}
	u.awsCost[c.Month] = c
	return nil
}

// AWSCost returns the month's reading, or nil when none was recorded.
func (u *Usage) AWSCost(ctx context.Context, month string) (*usage.AWSCost, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !usage.ValidMonth(month) {
		return nil, usage.ErrBadMonth
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	c, ok := u.awsCost[month]
	if !ok {
		return nil, nil
	}
	return &c, nil
}

func apply(t *usage.Totals, r usage.Record) {
	t.CostMicros += r.CostMicros
	t.Calls++
	t.AudioSeconds += r.Usage[meter.UnitAudioSeconds]
	t.InputTokens += int64(r.Usage[meter.UnitInputTokens])
	t.OutputTokens += int64(r.Usage[meter.UnitOutputTokens])
}
