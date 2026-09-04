// Package usage keeps per-tenant provider usage, so the question "what did
// this user's captures cost this month" has an answer that is not a log query.
//
// It is deliberately small. The 2026-09-03 review removed the per-tenant USAGE
// rows, the resolver interface and the sink nothing constructed, because they
// guarded a cap that the instance-wide SPEND# counter already enforced. What
// comes back here is accounting, not enforcement: two atomic ADDs per provider
// call — onto the tenant's month row and the tenant's day row — written by the
// breaker in the same place it writes the usage log line, so a cost is never
// logged without being attributed. Nothing here refuses a call, and nothing
// here is read on the capture path. See docs/design/usage-accounting.md for the
// data model and the admin listing that is deliberately not built yet.
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

// Totals is what accumulates on a row, whole or per op.
type Totals struct {
	CostMicros   int64   `json:"cost_micros"`
	Calls        int64   `json:"calls"`
	AudioSeconds float64 `json:"audio_seconds,omitempty"`
	InputTokens  int64   `json:"input_tokens,omitempty"`
	OutputTokens int64   `json:"output_tokens,omitempty"`
}

// Day is one day's totals inside a month.
type Day struct {
	Date string `json:"date"`
	Totals
}

// Month is a tenant's usage for one calendar month: the month row's totals,
// the same broken down by op, and one entry per day that had any usage.
type Month struct {
	Month string `json:"month"`
	Totals
	Ops  map[string]Totals `json:"ops"`
	Days []Day             `json:"days"`
}

// Reader answers GET /v1/usage.
type Reader interface {
	Month(ctx context.Context, tenantID, month string) (Month, error)
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
	opPrefix        = "op_"

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
}

// Dynamo records and reads usage rows in the single table.
type Dynamo struct {
	client API
	table  string
	now    func() time.Time
}

var (
	_ Recorder = (*Dynamo)(nil)
	_ Reader   = (*Dynamo)(nil)
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
	names := map[string]string{
		"#type":  attrType,
		"#cost":  attrCost,
		"#calls": attrCalls,
		"#opc":   opPrefix + op + "_" + attrCost,
		"#opn":   opPrefix + op + "_" + attrCalls,
	}
	values := map[string]types.AttributeValue{
		":cost":   numAttr(r.CostMicros),
		":one":    numAttr(1),
		":type":   strAttr(typeUsage),
		":tenant": strAttr(r.TenantID),
		":period": strAttr(period),
		":gran":   strAttr(granularity),
	}
	adds := []string{"#cost :cost", "#calls :one", "#opc :cost", "#opn :one"}

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
		val := fmt.Sprintf(":u%d", i)
		names[total] = unitAttrs[meter.Unit(unit)]
		names[perOp] = opPrefix + op + "_" + unitAttrs[meter.Unit(unit)]
		values[val] = floatAttr(q)
		adds = append(adds, total+" "+val, perOp+" "+val)
	}

	sets := []string{"#type = :type", "tenant_id = :tenant", "period = :period", "granularity = :gran"}
	if ttl > 0 {
		names["#ttl"] = attrTTL
		values[":ttl"] = numAttr(ttl)
		sets = append(sets, "#ttl = :ttl")
	} else {
		// The month row is what a later admin listing queries GSI1 for.
		values[":g1pk"] = strAttr(adminGSI1PKPrefix + period)
		values[":g1sk"] = strAttr(adminGSI1SKPrefix + r.TenantID)
		sets = append(sets, "gsi1pk = :g1pk", "gsi1sk = :g1sk")
	}

	_, err := d.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(d.table),
		Key: map[string]types.AttributeValue{
			"pk": strAttr(userPK(r.TenantID)),
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

// Month reads the month row and its day rows in one Query: the day rows'
// sort keys extend the month's, so begins_with(sk, USAGE#yyyy-mm) is exactly
// the set. A month with no usage answers zeros rather than not found — a new
// user's "You" screen is not an error.
func (d *Dynamo) Month(ctx context.Context, tenantID, month string) (Month, error) {
	if !ValidMonth(month) {
		return Month{}, ErrBadMonth
	}
	out := Month{Month: month, Ops: map[string]Totals{}, Days: []Day{}}

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
				out.Ops = opsOf(item)
			case strings.HasPrefix(period, month+"-"):
				out.Days = append(out.Days, Day{Date: period, Totals: totalsOf(item, "")})
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

// opsOf recovers the per-op table from the op_<op>_<counter> attributes.
func opsOf(item map[string]types.AttributeValue) map[string]Totals {
	ops := map[string]Totals{}
	for name := range item {
		rest, ok := strings.CutPrefix(name, opPrefix)
		if !ok {
			continue
		}
		// The op name is everything before the last known counter suffix.
		for _, suffix := range []string{attrCost, attrCalls, unitAttrs[meter.UnitAudioSeconds], unitAttrs[meter.UnitInputTokens], unitAttrs[meter.UnitOutputTokens]} {
			if op, found := strings.CutSuffix(rest, "_"+suffix); found && op != "" {
				if _, seen := ops[op]; !seen {
					ops[op] = totalsOf(item, opPrefix+op+"_")
				}
				break
			}
		}
	}
	return ops
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
