// Package usage keeps per-tenant provider usage, so the question "what did
// this user's captures cost this month" has an answer that is not a log query.
//
// It is deliberately small. There is no per-tenant cap, resolver interface or
// usage sink here: the instance-wide SPEND# counter is the enforcement. What
// lives here is accounting, not enforcement: two atomic ADDs per provider
// call — onto the tenant's month row and the tenant's day row — written by the
// breaker in the same place it writes the usage log line, so a cost is never
// logged without being attributed. Nothing here refuses a call, and nothing
// here is read on the capture path. See docs/design/usage-accounting.md for the
// data model and the admin listing that is deliberately not built yet.
//
// It also keeps the one number that is not a provider's: the instance's AWS
// spend for the month (AWSCost), written once a day by the worker's aws-cost
// task from the stack's budget and shown beside the provider figure.
//
// Since 2026-09 the same rows carry two more things. Every priced call is
// also counted under its provider (provider_<name>_*), so the month can be
// read as "what did Groq cost, what did MiniMax cost" without a log query.
// And every authenticated API request the tenant makes adds one to
// api_requests on the same month and day rows (RequestCounter), written by the
// API's per-route wrapper after the handler has answered — so the request
// count and the provider spend sit on one row and one read.
package usage

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/vppillai/chintan/backend/internal/meter"
)

// Record is one priced provider call, as the breaker settled it.
type Record struct {
	TenantID string
	// Day is the UTC calendar day the call was billed to, yyyy-mm-dd — the
	// same day the SPEND# counter used, so the two agree on which side of
	// midnight a call fell.
	Day      string
	Provider string
	Op       meter.Op
	// CostMicros is the reconciled cost in microdollars: what the provider
	// reported, priced, or the estimate when it reported nothing.
	CostMicros int64
	// Usage is what the call consumed, per unit, after reconciliation.
	Usage meter.Quantities
}

// Recorder attributes one call to its tenant. Implementations must be safe to
// call from the breaker's hot path and must never make the caller's call fail:
// accounting that breaks a capture is worse than a missed row.
type Recorder interface {
	Record(ctx context.Context, r Record) error
}

// RequestCounter counts one authenticated API request against its tenant, on
// the UTC day it was served. Like Recorder it must never make the caller's
// request fail: the handler logs a failed count and answers as it would have.
type RequestCounter interface {
	CountRequest(ctx context.Context, tenantID, day string) error
}

// Totals is what accumulates on a row, whole, per op or per provider.
type Totals struct {
	CostMicros   int64   `json:"cost_micros"`
	Calls        int64   `json:"calls"`
	AudioSeconds float64 `json:"audio_seconds,omitempty"`
	InputTokens  int64   `json:"input_tokens,omitempty"`
	OutputTokens int64   `json:"output_tokens,omitempty"`
}

// Day is one day's totals inside a month, and the API requests it served.
type Day struct {
	Date string `json:"date"`
	Totals
	APIRequests int64 `json:"api_requests"`
}

// Month is a tenant's usage for one calendar month: the month row's totals,
// the same broken down by op and by provider, the authenticated API requests
// the tenant made, and one entry per day that had any usage or any request.
type Month struct {
	Month string `json:"month"`
	Totals
	Ops         map[string]Totals `json:"ops"`
	Providers   map[string]Totals `json:"providers"`
	APIRequests int64             `json:"api_requests"`
	Days        []Day             `json:"days"`
}

// Reader answers GET /v1/usage: the tenant's own month, the instance's AWS
// cost for the same month, and the instance's provider spend the AWS cost is
// apportioned by.
type Reader interface {
	Month(ctx context.Context, tenantID, month string) (Month, error)
	// AWSCost returns the AWSCOST#<month> row, or nil when nothing has been
	// recorded for that month — the stack has no budget, or the daily task
	// has not run since the month began.
	AWSCost(ctx context.Context, month string) (*AWSCost, error)
	// InstanceSpend is what every tenant together spent at the providers in
	// the month: the sum of the INSTANCE / SPEND#<day> counters the breaker
	// enforces the cap against. Zero when there are none — the counters
	// expire after ninety days, so an old month reads zero, and a share of
	// the AWS bill is not computed for it.
	InstanceSpend(ctx context.Context, month string) (int64, error)
}

// AWSCost is the instance's AWS spend for one calendar month (UTC), as last
// read from the stack's AWS Budget by the worker's daily aws-cost task
// (internal/awscost). It is instance-level: AWS bills the account, not the
// user, so there is no per-tenant split. The JSON shape is the `aws` object on
// GET /v1/usage.
type AWSCost struct {
	// Month is the yyyy-mm the reading is for. It is the row's key, not a
	// field of the wire object, which is always nested under a month already.
	Month string `json:"-"`
	// MonthMicros is the budget's month-to-date actual spend, in microdollars.
	MonthMicros int64 `json:"month_micros"`
	// AsOf is when the budget was read. Budgets refreshes its figure up to
	// three times a day and the task runs once, so this is the honest date to
	// show next to the number.
	AsOf time.Time `json:"as_of"`
	// BudgetMicros is the budget's limit, in microdollars, or nil when the
	// budget reported none.
	BudgetMicros *int64 `json:"budget_micros"`
}

// AWSCostStore is what the aws-cost task writes.
type AWSCostStore interface {
	PutAWSCost(ctx context.Context, c AWSCost) error
}

// ErrBadMonth rejects a month that is not yyyy-mm.
var ErrBadMonth = errors.New("usage: month must be yyyy-mm")

var monthRe = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)

// ValidMonth reports whether m is a yyyy-mm month.
func ValidMonth(m string) bool { return monthRe.MatchString(m) }

// MonthOf returns the yyyy-mm month a yyyy-mm-dd day belongs to.
func MonthOf(day string) string {
	if len(day) >= 7 {
		return day[:7]
	}
	return day
}

// ---------------------------------------------------------------- storage

// Row keys. Both live in the tenant's own partition (pk USER#<tenant>), so
// they are exported, backed up and erased with the tenant by chintanctl's
// partition walk without that tool learning a new kind; the month row sorts
// before its days, so one begins_with query returns the month and its days.
const (
	skPrefix = "USAGE#"

	// The month row also carries GSI1 keys so an admin listing later is a
	// Query, not a Scan: gsi1pk USAGE#<yyyy-mm>, gsi1sk TENANT#<tenant>. The
	// index projects none of this row's attributes (INCLUDE, capture fields
	// only), so that listing yields tenant ids and then one GetItem each —
	// which is fine for an admin page and costs no index change today.
	adminGSI1PKPrefix = "USAGE#"
	adminGSI1SKPrefix = "TENANT#"

	// dayRetention is how long a day row lives. Thirteen months covers "this
	// time last year" on a per-day chart; the month rows are kept for good,
	// since they are the billing history.
	dayRetention = 400 * 24 * time.Hour
)

// Attribute names. Flat rather than nested: DynamoDB cannot ADD into a nested
// map path that does not exist yet, so a nested shape would need a read or a
// two-step write per call. Flat attributes with an `op_<op>_` prefix are one
// ADD, and a reader recovers the per-op table by prefix.
const (
	attrType        = "type"
	attrTenant      = "tenant_id"
	attrPeriod      = "period"
	attrGranularity = "granularity"
	attrTTL         = "ttl"
	attrCost        = "cost_micros"
	attrCalls       = "calls"
	attrAPIRequests = "api_requests"
	opPrefix        = "op_"
	// providerPrefix is the per-provider split, same five counters as the
	// per-op one: provider_<name>_<counter>.
	providerPrefix = "provider_"

	typeUsage = "usage"
	granMonth = "month"
	granDay   = "day"
)

// unitAttrs maps a metered unit to the attribute that accumulates it.
var unitAttrs = map[meter.Unit]string{
	meter.UnitAudioSeconds: "audio_seconds",
	meter.UnitInputTokens:  "input_tokens",
	meter.UnitOutputTokens: "output_tokens",
}

// API is the slice of DynamoDB this package uses.
type API interface {
	UpdateItem(ctx context.Context, in *dynamodb.UpdateItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
	Query(ctx context.Context, in *dynamodb.QueryInput, opts ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
	PutItem(ctx context.Context, in *dynamodb.PutItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	GetItem(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
}

// Dynamo records and reads usage rows in the single table.
type Dynamo struct {
	client API
	table  string
	now    func() time.Time
}

var (
	_ Recorder       = (*Dynamo)(nil)
	_ RequestCounter = (*Dynamo)(nil)
	_ Reader         = (*Dynamo)(nil)
	_ AWSCostStore   = (*Dynamo)(nil)
)

// NewDynamo builds the DynamoDB-backed recorder and reader.
func NewDynamo(client API, table string) *Dynamo {
	return &Dynamo{client: client, table: table, now: time.Now}
}

func userPK(tenantID string) string { return "USER#" + tenantID }

// Record applies the call to the tenant's month row and day row: two
// UpdateItems, each a single ADD of every counter the call moves, so a
// concurrent capture cannot lose an increment.
//
// The two writes are not one transaction. A failure between them leaves a
// month that is one call ahead of its days, which is a discrepancy a reader
// can see and a transaction would cost double the write units on every call
// to prevent. The breaker logs a failed Record and carries on; the log line
// it already wrote is the record of last resort.
func (d *Dynamo) Record(ctx context.Context, r Record) error {
	if r.TenantID == "" {
		return errors.New("usage: record without a tenant")
	}
	if len(r.Day) != len("2006-01-02") {
		return fmt.Errorf("usage: day %q is not yyyy-mm-dd", r.Day)
	}
	month := MonthOf(r.Day)

	if err := d.add(ctx, r, month, granMonth, 0); err != nil {
		return err
	}
	return d.add(ctx, r, r.Day, granDay, d.now().Add(dayRetention).Unix())
}

func (d *Dynamo) add(ctx context.Context, r Record, period, granularity string, ttl int64) error {
	op := string(r.Op)
	prov := providerName(r.Provider)
	names := map[string]string{
		"#cost":  attrCost,
		"#calls": attrCalls,
		"#opc":   opPrefix + op + "_" + attrCost,
		"#opn":   opPrefix + op + "_" + attrCalls,
		"#prc":   providerPrefix + prov + "_" + attrCost,
		"#prn":   providerPrefix + prov + "_" + attrCalls,
	}
	values := map[string]types.AttributeValue{
		":cost": numAttr(r.CostMicros),
		":one":  numAttr(1),
	}
	adds := []string{"#cost :cost", "#calls :one", "#opc :cost", "#opn :one", "#prc :cost", "#prn :one"}

	// Units in a fixed order, so the expression is stable for a test to read.
	units := make([]string, 0, len(r.Usage))
	for unit := range r.Usage {
		if _, known := unitAttrs[unit]; known {
			units = append(units, string(unit))
		}
	}
	sort.Strings(units)
	for i, unit := range units {
		q := r.Usage[meter.Unit(unit)]
		if q <= 0 {
			continue
		}
		total := fmt.Sprintf("#u%d", i)
		perOp := fmt.Sprintf("#ou%d", i)
		perProvider := fmt.Sprintf("#pu%d", i)
		val := fmt.Sprintf(":u%d", i)
		names[total] = unitAttrs[meter.Unit(unit)]
		names[perOp] = opPrefix + op + "_" + unitAttrs[meter.Unit(unit)]
		names[perProvider] = providerPrefix + prov + "_" + unitAttrs[meter.Unit(unit)]
		values[val] = floatAttr(q)
		adds = append(adds, total+" "+val, perOp+" "+val, perProvider+" "+val)
	}
	return d.update(ctx, r.TenantID, period, granularity, ttl, adds, names, values)
}

// CountRequest adds one to api_requests on the tenant's month and day rows.
// Two ADDs, like Record, and for the same reason: the row is shared with the
// breaker's counters, and a read-then-write would lose increments to it.
func (d *Dynamo) CountRequest(ctx context.Context, tenantID, day string) error {
	if tenantID == "" {
		return errors.New("usage: count request without a tenant")
	}
	if len(day) != len("2006-01-02") {
		return fmt.Errorf("usage: day %q is not yyyy-mm-dd", day)
	}
	names := map[string]string{"#req": attrAPIRequests}
	values := map[string]types.AttributeValue{":one": numAttr(1)}
	adds := []string{"#req :one"}
	if err := d.update(ctx, tenantID, MonthOf(day), granMonth, 0, adds, names, values); err != nil {
		return err
	}
	return d.update(ctx, tenantID, day, granDay, d.now().Add(dayRetention).Unix(), adds, names, values)
}

// update is the one UpdateItem shape both writers use: ADD the counters given,
// SET the row's identity, its TTL (day rows) or its GSI1 keys (month rows).
// names and values are the caller's maps and are added to here.
func (d *Dynamo) update(ctx context.Context, tenantID, period, granularity string, ttl int64, adds []string, names map[string]string, values map[string]types.AttributeValue) error {
	names["#type"] = attrType
	values[":type"] = strAttr(typeUsage)
	values[":tenant"] = strAttr(tenantID)
	values[":period"] = strAttr(period)
	values[":gran"] = strAttr(granularity)
	sets := []string{"#type = :type", "tenant_id = :tenant", "period = :period", "granularity = :gran"}
	if ttl > 0 {
		names["#ttl"] = attrTTL
		values[":ttl"] = numAttr(ttl)
		sets = append(sets, "#ttl = :ttl")
	} else {
		// The month row is what the admin listing (chintanctl usage) queries
		// GSI1 for.
		values[":g1pk"] = strAttr(adminGSI1PKPrefix + period)
		values[":g1sk"] = strAttr(adminGSI1SKPrefix + tenantID)
		sets = append(sets, "gsi1pk = :g1pk", "gsi1sk = :g1sk")
	}

	_, err := d.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(d.table),
		Key: map[string]types.AttributeValue{
			"pk": strAttr(userPK(tenantID)),
			"sk": strAttr(skPrefix + period),
		},
		UpdateExpression:          aws.String("ADD " + strings.Join(adds, ", ") + " SET " + strings.Join(sets, ", ")),
		ExpressionAttributeNames:  names,
		ExpressionAttributeValues: values,
	})
	if err != nil {
		return fmt.Errorf("usage: record %s %s: %w", granularity, period, err)
	}
	return nil
}

// providerName is the provider as it appears in an attribute name: lowercase,
// letters and digits only, so a name can neither collide with the counter
// suffixes nor carry a character an expression attribute name refuses. An
// empty name — no pipeline stage sends one — is "unknown" rather than an
// attribute called provider__calls.
func providerName(p string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(p)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

// Month reads the month row and its day rows in one Query: the day rows'
// sort keys extend the month's, so begins_with(sk, USAGE#yyyy-mm) is exactly
// the set. A month with no usage answers zeros rather than not found — a new
// user's "You" screen is not an error.
func (d *Dynamo) Month(ctx context.Context, tenantID, month string) (Month, error) {
	if !ValidMonth(month) {
		return Month{}, ErrBadMonth
	}
	out := Month{Month: month, Ops: map[string]Totals{}, Providers: map[string]Totals{}, Days: []Day{}}

	var start map[string]types.AttributeValue
	for {
		res, err := d.client.Query(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(d.table),
			KeyConditionExpression: aws.String("pk = :pk AND begins_with(sk, :sk)"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":pk": strAttr(userPK(tenantID)),
				":sk": strAttr(skPrefix + month),
			},
			ExclusiveStartKey: start,
		})
		if err != nil {
			return Month{}, fmt.Errorf("usage: query month %s: %w", month, err)
		}
		for _, item := range res.Items {
			period := readString(item, attrPeriod)
			if period == "" {
				period = strings.TrimPrefix(readString(item, "sk"), skPrefix)
			}
			switch {
			case period == month:
				out.Totals = totalsOf(item, "")
				out.Ops = splitOf(item, opPrefix)
				out.Providers = splitOf(item, providerPrefix)
				out.APIRequests = readInt(item, attrAPIRequests)
			case strings.HasPrefix(period, month+"-"):
				out.Days = append(out.Days, Day{Date: period, Totals: totalsOf(item, ""), APIRequests: readInt(item, attrAPIRequests)})
			}
		}
		start = res.LastEvaluatedKey
		if len(start) == 0 {
			break
		}
	}
	sort.Slice(out.Days, func(i, j int) bool { return out.Days[i].Date < out.Days[j].Date })
	return out, nil
}

// ---------------------------------------------------------------- AWS cost

// The AWS cost row is not a tenant's. It lives in the INSTANCE partition next
// to the SPEND#<day> counters (internal/pipeline DynamoCounter), for the same
// reason they do: the number belongs to the account, chintanctl's per-tenant
// walk must neither export nor erase it, and there is exactly one per month.
// No TTL — twelve rows a year is billing history, not clutter.
const (
	instancePK    = "INSTANCE"
	awsCostPrefix = "AWSCOST#"

	typeAWSCost      = "aws_cost"
	attrMonth        = "month"
	attrMonthMicros  = "month_micros"
	attrBudgetMicros = "budget_micros"
	attrAsOf         = "as_of"
)

// PutAWSCost writes the month's reading, replacing the previous one: the task
// runs daily and the latest figure is the only one anybody wants, so a retry
// of the same invocation is harmless.
func (d *Dynamo) PutAWSCost(ctx context.Context, c AWSCost) error {
	if !ValidMonth(c.Month) {
		return ErrBadMonth
	}
	item := map[string]types.AttributeValue{
		"pk":            strAttr(instancePK),
		"sk":            strAttr(awsCostPrefix + c.Month),
		attrType:        strAttr(typeAWSCost),
		attrMonth:       strAttr(c.Month),
		attrMonthMicros: numAttr(c.MonthMicros),
		attrAsOf:        strAttr(c.AsOf.UTC().Format(time.RFC3339)),
	}
	if c.BudgetMicros != nil {
		item[attrBudgetMicros] = numAttr(*c.BudgetMicros)
	}
	_, err := d.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(d.table),
		Item:      item,
	})
	if err != nil {
		return fmt.Errorf("usage: put aws cost %s: %w", c.Month, err)
	}
	return nil
}

// AWSCost reads the month's row; nil, nil when there is none.
func (d *Dynamo) AWSCost(ctx context.Context, month string) (*AWSCost, error) {
	if !ValidMonth(month) {
		return nil, ErrBadMonth
	}
	res, err := d.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(d.table),
		Key: map[string]types.AttributeValue{
			"pk": strAttr(instancePK),
			"sk": strAttr(awsCostPrefix + month),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("usage: get aws cost %s: %w", month, err)
	}
	if len(res.Item) == 0 {
		return nil, nil
	}
	out := &AWSCost{Month: month, MonthMicros: readInt(res.Item, attrMonthMicros)}
	if asOf, err := time.Parse(time.RFC3339, readString(res.Item, attrAsOf)); err == nil {
		out.AsOf = asOf
	}
	if _, has := res.Item[attrBudgetMicros]; has {
		b := readInt(res.Item, attrBudgetMicros)
		out.BudgetMicros = &b
	}
	return out, nil
}

// totalsOf reads the five counters carrying prefix ("" for the row totals,
// "op_<op>_" for one op's share).
func totalsOf(item map[string]types.AttributeValue, prefix string) Totals {
	return Totals{
		CostMicros:   readInt(item, prefix+attrCost),
		Calls:        readInt(item, prefix+attrCalls),
		AudioSeconds: readFloat(item, prefix+unitAttrs[meter.UnitAudioSeconds]),
		InputTokens:  readInt(item, prefix+unitAttrs[meter.UnitInputTokens]),
		OutputTokens: readInt(item, prefix+unitAttrs[meter.UnitOutputTokens]),
	}
}

// splitOf recovers one per-key table — per op for opPrefix, per provider for
// providerPrefix — from the <prefix><key>_<counter> attributes.
func splitOf(item map[string]types.AttributeValue, prefix string) map[string]Totals {
	out := map[string]Totals{}
	for name := range item {
		rest, ok := strings.CutPrefix(name, prefix)
		if !ok {
			continue
		}
		// The key is everything before the last known counter suffix.
		for _, suffix := range []string{attrCost, attrCalls, unitAttrs[meter.UnitAudioSeconds], unitAttrs[meter.UnitInputTokens], unitAttrs[meter.UnitOutputTokens]} {
			if key, found := strings.CutSuffix(rest, "_"+suffix); found && key != "" {
				if _, seen := out[key]; !seen {
					out[key] = totalsOf(item, prefix+key+"_")
				}
				break
			}
		}
	}
	return out
}

// ---------------------------------------------------------- instance spend

// spendPrefix is the sort key prefix of the breaker's daily counters on the
// INSTANCE partition (internal/pipeline DynamoCounter). Spelled here as well
// because that package cannot be imported without dragging the pipeline into
// the API binary; the two are pinned together by TestInstanceSpendSumsTheDayCounters.
const (
	spendPrefix    = "SPEND#"
	attrSpendMicro = "spend_micros"
)

// InstanceSpend sums the month's SPEND#<day> counters. It is the denominator
// of a tenant's share of the AWS bill: what the whole instance spent at the
// providers, of which the tenant's own month total is the numerator.
func (d *Dynamo) InstanceSpend(ctx context.Context, month string) (int64, error) {
	if !ValidMonth(month) {
		return 0, ErrBadMonth
	}
	var total int64
	var start map[string]types.AttributeValue
	for {
		res, err := d.client.Query(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(d.table),
			KeyConditionExpression: aws.String("pk = :pk AND begins_with(sk, :sk)"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":pk": strAttr(instancePK),
				":sk": strAttr(spendPrefix + month + "-"),
			},
			ExclusiveStartKey: start,
		})
		if err != nil {
			return 0, fmt.Errorf("usage: query instance spend %s: %w", month, err)
		}
		for _, item := range res.Items {
			total += readInt(item, attrSpendMicro)
		}
		start = res.LastEvaluatedKey
		if len(start) == 0 {
			break
		}
	}
	return total, nil
}

func strAttr(v string) types.AttributeValue { return &types.AttributeValueMemberS{Value: v} }

func numAttr(v int64) types.AttributeValue {
	return &types.AttributeValueMemberN{Value: strconv.FormatInt(v, 10)}
}

// floatAttr renders a quantity with millisecond-class precision. Token counts
// are whole; audio seconds come from the provider as a float and three
// decimals is finer than it bills.
func floatAttr(v float64) types.AttributeValue {
	return &types.AttributeValueMemberN{Value: strconv.FormatFloat(v, 'f', 3, 64)}
}

func readString(m map[string]types.AttributeValue, name string) string {
	if v, ok := m[name].(*types.AttributeValueMemberS); ok {
		return v.Value
	}
	return ""
}

func readFloat(m map[string]types.AttributeValue, name string) float64 {
	v, ok := m[name].(*types.AttributeValueMemberN)
	if !ok {
		return 0
	}
	f, err := strconv.ParseFloat(v.Value, 64)
	if err != nil {
		return 0
	}
	return f
}

func readInt(m map[string]types.AttributeValue, name string) int64 {
	// Read through the float path: an integer counter that was ADDed a
	// fractional value once (it cannot happen for the counters declared
	// integer, but a number attribute has no type) still reads as a number.
	return int64(readFloat(m, name))
}
