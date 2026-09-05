package usage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/vppillai/chintan/backend/internal/meter"
)

// fakeAPI records UpdateItems and PutItems, answers Query from a canned item
// list and GetItem from the last PutItem with the same key.
type fakeAPI struct {
	updates []*dynamodb.UpdateItemInput
	puts    []*dynamodb.PutItemInput
	queries []*dynamodb.QueryInput
	items   []map[string]types.AttributeValue
	err     error
	// conditionFails makes every conditional UpdateItem fail its condition,
	// the way the month row answers a second storage snapshot in one day.
	conditionFails bool
	// requests is api_requests per row key, so UpdateItem can report the
	// counter's new value the way the service does (ReturnValues UPDATED_NEW).
	requests map[string]int64
}

func (f *fakeAPI) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.puts = append(f.puts, in)
	return &dynamodb.PutItemOutput{}, nil
}

func (f *fakeAPI) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	for i := len(f.puts) - 1; i >= 0; i-- {
		item := f.puts[i].Item
		if item["pk"].(*types.AttributeValueMemberS).Value == in.Key["pk"].(*types.AttributeValueMemberS).Value &&
			item["sk"].(*types.AttributeValueMemberS).Value == in.Key["sk"].(*types.AttributeValueMemberS).Value {
			return &dynamodb.GetItemOutput{Item: item}, nil
		}
	}
	return &dynamodb.GetItemOutput{}, nil
}

func (f *fakeAPI) UpdateItem(_ context.Context, in *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	// DynamoDB rejects a placeholder that the expression never uses, and the
	// production outage behind this check was exactly that: a value carried
	// over from a previous write. The fake enforces the same rule so a test
	// cannot pass an UpdateItem the service would refuse.
	if err := validatePlaceholders(in); err != nil {
		return nil, err
	}
	f.updates = append(f.updates, in)
	if f.conditionFails && in.ConditionExpression != nil {
		return nil, &types.ConditionalCheckFailedException{Message: aws.String("The conditional request failed")}
	}
	out := &dynamodb.UpdateItemOutput{Attributes: map[string]types.AttributeValue{}}
	if in.ExpressionAttributeNames["#req"] == attrAPIRequests {
		if f.requests == nil {
			f.requests = map[string]int64{}
		}
		key := keyOf(in, "pk") + "|" + keyOf(in, "sk")
		if strings.Contains(*in.UpdateExpression, "#req :one") {
			f.requests[key]++
		}
		out.Attributes[attrAPIRequests] = numAttr(f.requests[key])
	}
	return out, nil
}

// validatePlaceholders mirrors the ValidationException DynamoDB raises for an
// ExpressionAttributeNames or ExpressionAttributeValues entry that appears in
// no expression of the request.
func validatePlaceholders(in *dynamodb.UpdateItemInput) error {
	expr := ""
	if in.UpdateExpression != nil {
		expr += *in.UpdateExpression
	}
	if in.ConditionExpression != nil {
		expr += " " + *in.ConditionExpression
	}
	for name := range in.ExpressionAttributeNames {
		if !strings.Contains(expr, name) {
			return fmt.Errorf("ValidationException: value provided in ExpressionAttributeNames unused in expressions: keys: {%s}", name)
		}
	}
	for value := range in.ExpressionAttributeValues {
		if !strings.Contains(expr, value) {
			return fmt.Errorf("ValidationException: value provided in ExpressionAttributeValues unused in expressions: keys: {%s}", value)
		}
	}
	return nil
}

func (f *fakeAPI) Query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.queries = append(f.queries, in)
	return &dynamodb.QueryOutput{Items: f.items}, nil
}

func keyOf(in *dynamodb.UpdateItemInput, name string) string {
	return in.Key[name].(*types.AttributeValueMemberS).Value
}

// One call is two rows — the month and the day — each written as a single ADD
// so concurrent captures cannot lose an increment, and each carrying the
// tenant-visible totals and the per-op share in the same expression.
func TestRecordWritesMonthAndDayRowsAsOneADDEach(t *testing.T) {
	api := &fakeAPI{}
	d := NewDynamo(api, "t")
	d.now = func() time.Time { return time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC) }

	err := d.Record(context.Background(), Record{
		TenantID: "tenant-a", Day: "2026-09-04", Provider: "groq", Op: meter.OpTranscribe,
		CostMicros: 311, Usage: meter.Quantities{meter.UnitAudioSeconds: 28.5},
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if len(api.updates) != 2 {
		t.Fatalf("updates = %d, want 2 (month and day)", len(api.updates))
	}

	month, day := api.updates[0], api.updates[1]
	if got := keyOf(month, "pk"); got != "USER#tenant-a" {
		t.Errorf("month pk = %q", got)
	}
	if got := keyOf(month, "sk"); got != "USAGE#2026-09" {
		t.Errorf("month sk = %q", got)
	}
	if got := keyOf(day, "sk"); got != "USAGE#2026-09-04" {
		t.Errorf("day sk = %q", got)
	}

	for _, in := range api.updates {
		expr := *in.UpdateExpression
		if !strings.HasPrefix(expr, "ADD ") || !strings.Contains(expr, " SET ") {
			t.Errorf("expression is not one ADD plus SET: %q", expr)
		}
		if in.ConditionExpression != nil {
			t.Errorf("an accounting ADD must be unconditional: %q", *in.ConditionExpression)
		}
		// Every counter the call moves is in the one expression.
		names := in.ExpressionAttributeNames
		want := map[string]bool{"cost_micros": false, "calls": false, "audio_seconds": false,
			"op_transcribe_cost_micros": false, "op_transcribe_calls": false, "op_transcribe_audio_seconds": false}
		for _, attr := range names {
			if _, ok := want[attr]; ok {
				want[attr] = true
			}
		}
		for attr, seen := range want {
			if !seen {
				t.Errorf("%s: attribute %s is not in the ADD: %v", keyOf(in, "sk"), attr, names)
			}
		}
		if v, ok := in.ExpressionAttributeValues[":cost"].(*types.AttributeValueMemberN); !ok || v.Value != "311" {
			t.Errorf("cost value = %v", in.ExpressionAttributeValues[":cost"])
		}
	}

	// Only the day row expires; the month row is billing history and carries
	// the GSI1 keys a later admin listing queries.
	if _, ok := month.ExpressionAttributeValues[":ttl"]; ok {
		t.Error("the month row was given a TTL")
	}
	if _, ok := day.ExpressionAttributeValues[":ttl"]; !ok {
		t.Error("the day row was given no TTL")
	}
	if v, ok := month.ExpressionAttributeValues[":g1pk"].(*types.AttributeValueMemberS); !ok || v.Value != "USAGE#2026-09" {
		t.Errorf("month gsi1pk = %v", month.ExpressionAttributeValues[":g1pk"])
	}
	if v, ok := month.ExpressionAttributeValues[":g1sk"].(*types.AttributeValueMemberS); !ok || v.Value != "TENANT#tenant-a" {
		t.Errorf("month gsi1sk = %v", month.ExpressionAttributeValues[":g1sk"])
	}
	if _, ok := day.ExpressionAttributeValues[":g1pk"]; ok {
		t.Error("the day row was indexed; only the month row belongs in the admin listing")
	}
}

func TestRecordRefusesAnAnonymousOrMalformedRecord(t *testing.T) {
	api := &fakeAPI{}
	d := NewDynamo(api, "t")
	if err := d.Record(context.Background(), Record{Day: "2026-09-04", Op: meter.OpRoute}); err == nil {
		t.Error("a record without a tenant was accepted")
	}
	if err := d.Record(context.Background(), Record{TenantID: "x", Day: "2026-9-4", Op: meter.OpRoute}); err == nil {
		t.Error("a malformed day was accepted")
	}
	if len(api.updates) != 0 {
		t.Errorf("refused records still wrote %d updates", len(api.updates))
	}
}

func TestRecordReturnsTheStoreError(t *testing.T) {
	boom := errors.New("throttled")
	d := NewDynamo(&fakeAPI{err: boom}, "t")
	err := d.Record(context.Background(), Record{TenantID: "x", Day: "2026-09-04", Op: meter.OpRoute, CostMicros: 1})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the store's error wrapped", err)
	}
}

func n(v string) types.AttributeValue { return &types.AttributeValueMemberN{Value: v} }
func s(v string) types.AttributeValue { return &types.AttributeValueMemberS{Value: v} }

// Month rebuilds the response from the flat row shape: totals, the op_<op>_
// counters folded back into a per-op table, and the day rows in date order.
func TestMonthReadsTotalsOpsAndDays(t *testing.T) {
	api := &fakeAPI{items: []map[string]types.AttributeValue{
		{
			"sk": s("USAGE#2026-09"), "period": s("2026-09"), "granularity": s("month"),
			"cost_micros": n("951"), "calls": n("2"), "audio_seconds": n("28.500"),
			"input_tokens": n("900"), "output_tokens": n("300"),
			"op_transcribe_cost_micros": n("311"), "op_transcribe_calls": n("1"), "op_transcribe_audio_seconds": n("28.500"),
			"op_cleanup_cost_micros": n("640"), "op_cleanup_calls": n("1"),
			"op_cleanup_input_tokens": n("900"), "op_cleanup_output_tokens": n("300"),
		},
		// Out of order on purpose: the reader sorts.
		{"sk": s("USAGE#2026-09-05"), "period": s("2026-09-05"), "cost_micros": n("640"), "calls": n("1")},
		{"sk": s("USAGE#2026-09-04"), "period": s("2026-09-04"), "cost_micros": n("311"), "calls": n("1"), "audio_seconds": n("28.5")},
	}}
	d := NewDynamo(api, "t")

	got, err := d.Month(context.Background(), "tenant-a", "2026-09")
	if err != nil {
		t.Fatalf("Month: %v", err)
	}
	if got.CostMicros != 951 || got.Calls != 2 || got.AudioSeconds != 28.5 || got.InputTokens != 900 || got.OutputTokens != 300 {
		t.Errorf("totals = %+v", got.Totals)
	}
	if len(got.Ops) != 2 {
		t.Fatalf("ops = %v, want transcribe and cleanup", got.Ops)
	}
	if tr := got.Ops["transcribe"]; tr.CostMicros != 311 || tr.Calls != 1 || tr.AudioSeconds != 28.5 {
		t.Errorf("transcribe = %+v", tr)
	}
	if cl := got.Ops["cleanup"]; cl.CostMicros != 640 || cl.InputTokens != 900 || cl.OutputTokens != 300 {
		t.Errorf("cleanup = %+v", cl)
	}
	if len(got.Days) != 2 || got.Days[0].Date != "2026-09-04" || got.Days[1].Date != "2026-09-05" {
		t.Errorf("days = %+v, want two in date order", got.Days)
	}
}

func TestMonthWithNoRowsIsZerosNotAnError(t *testing.T) {
	d := NewDynamo(&fakeAPI{}, "t")
	got, err := d.Month(context.Background(), "tenant-a", "2026-09")
	if err != nil {
		t.Fatalf("Month: %v", err)
	}
	if got.Month != "2026-09" || got.CostMicros != 0 || got.Ops == nil || got.Days == nil {
		t.Errorf("empty month = %+v; want zeros with non-nil collections so the JSON is {} and []", got)
	}
}

func TestMonthRejectsAMalformedMonth(t *testing.T) {
	d := NewDynamo(&fakeAPI{}, "t")
	for _, bad := range []string{"2026", "2026-13", "2026-9", "09-2026", "2026-09-04"} {
		if _, err := d.Month(context.Background(), "tenant-a", bad); !errors.Is(err, ErrBadMonth) {
			t.Errorf("%q: err = %v, want ErrBadMonth", bad, err)
		}
	}
}

// The AWS cost row is the instance's, not a tenant's: it sits in the INSTANCE
// partition beside SPEND#, keyed by month, and reads back exactly what was
// written — including the absence of a budget limit.
func TestAWSCostRowLivesOnTheInstancePartition(t *testing.T) {
	api := &fakeAPI{}
	d := NewDynamo(api, "t")
	ctx := context.Background()

	got, err := d.AWSCost(ctx, "2026-09")
	if err != nil || got != nil {
		t.Fatalf("unrecorded month = %+v, %v; want nil, nil", got, err)
	}

	limit := int64(10_000_000)
	asOf := time.Date(2026, 9, 4, 6, 15, 0, 0, time.UTC)
	if err := d.PutAWSCost(ctx, AWSCost{Month: "2026-09", MonthMicros: 2_345_678, AsOf: asOf, BudgetMicros: &limit}); err != nil {
		t.Fatalf("PutAWSCost: %v", err)
	}
	if len(api.puts) != 1 {
		t.Fatalf("puts = %d, want 1", len(api.puts))
	}
	item := api.puts[0].Item
	if pk := item["pk"].(*types.AttributeValueMemberS).Value; pk != "INSTANCE" {
		t.Errorf("pk = %q, want INSTANCE (never a tenant's partition)", pk)
	}
	if sk := item["sk"].(*types.AttributeValueMemberS).Value; sk != "AWSCOST#2026-09" {
		t.Errorf("sk = %q", sk)
	}
	if _, hasTTL := item["ttl"]; hasTTL {
		t.Error("the month reading must not expire: it is billing history")
	}

	got, err = d.AWSCost(ctx, "2026-09")
	if err != nil || got == nil {
		t.Fatalf("AWSCost = %+v, %v", got, err)
	}
	if got.Month != "2026-09" || got.MonthMicros != 2_345_678 || !got.AsOf.Equal(asOf) || got.BudgetMicros == nil || *got.BudgetMicros != limit {
		t.Errorf("read back %+v", got)
	}

	// A later reading replaces the earlier one, and a budget without a limit
	// reads back as no limit rather than zero.
	if err := d.PutAWSCost(ctx, AWSCost{Month: "2026-09", MonthMicros: 3_000_000, AsOf: asOf.Add(24 * time.Hour)}); err != nil {
		t.Fatalf("PutAWSCost: %v", err)
	}
	got, err = d.AWSCost(ctx, "2026-09")
	if err != nil || got == nil || got.MonthMicros != 3_000_000 || got.BudgetMicros != nil {
		t.Errorf("after second put: %+v, %v", got, err)
	}

	if err := d.PutAWSCost(ctx, AWSCost{Month: "September"}); !errors.Is(err, ErrBadMonth) {
		t.Errorf("malformed month: err = %v, want ErrBadMonth", err)
	}
	if _, err := d.AWSCost(ctx, "2026-9"); !errors.Is(err, ErrBadMonth) {
		t.Errorf("malformed month on read: err = %v, want ErrBadMonth", err)
	}
}

// The same ADD also counts the call under its provider, so the month can be
// read per provider without a second row.
func TestRecordCountsTheCallUnderItsProvider(t *testing.T) {
	api := &fakeAPI{}
	d := NewDynamo(api, "t")
	err := d.Record(context.Background(), Record{
		TenantID: "tenant-a", Day: "2026-09-04", Provider: "OpenAI", Op: meter.OpAsk,
		CostMicros: 90, Usage: meter.Quantities{meter.UnitInputTokens: 800, meter.UnitOutputTokens: 60},
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	for _, in := range api.updates {
		want := map[string]bool{"provider_openai_cost_micros": false, "provider_openai_calls": false,
			"provider_openai_input_tokens": false, "provider_openai_output_tokens": false,
			"op_ask_cost_micros": false, "op_ask_calls": false}
		for _, attr := range in.ExpressionAttributeNames {
			if _, ok := want[attr]; ok {
				want[attr] = true
			}
		}
		for attr, seen := range want {
			if !seen {
				t.Errorf("%s: attribute %s is not in the ADD: %v", keyOf(in, "sk"), attr, in.ExpressionAttributeNames)
			}
		}
	}
	for _, tc := range []struct{ in, want string }{
		{"groq", "groq"}, {" OpenAI ", "openai"}, {"my-provider.v2", "myproviderv2"}, {"", "unknown"},
	} {
		if got := providerName(tc.in); got != tc.want {
			t.Errorf("providerName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A request is one ADD of api_requests on the day row, carrying the row's
// identity and TTL. The day's first request also writes the month row — an ADD
// of zero with the GSI1 keys — so a tenant with requests and no calls is still
// listed; every later request of the day is the one write.
func TestCountRequestWritesTheDayRowAndTheMonthRowOnlyOnTheDaysFirstRequest(t *testing.T) {
	api := &fakeAPI{}
	d := NewDynamo(api, "t")
	d.now = func() time.Time { return time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC) }
	if err := d.CountRequest(context.Background(), "tenant-a", "2026-09-04"); err != nil {
		t.Fatalf("CountRequest: %v", err)
	}
	if len(api.updates) != 2 {
		t.Fatalf("updates after the day's first request = %d, want the day row and then the month row", len(api.updates))
	}
	day, month := api.updates[0], api.updates[1]
	if keyOf(day, "sk") != "USAGE#2026-09-04" || keyOf(month, "sk") != "USAGE#2026-09" {
		t.Errorf("keys = %s, %s; the day row is written first so its count decides the month write", keyOf(day, "sk"), keyOf(month, "sk"))
	}
	if expr := *day.UpdateExpression; !strings.HasPrefix(expr, "ADD #req :one SET ") || day.ExpressionAttributeNames["#req"] != "api_requests" {
		t.Errorf("day expression = %q names = %v", expr, day.ExpressionAttributeNames)
	}
	if expr := *month.UpdateExpression; !strings.HasPrefix(expr, "ADD #req :zero SET ") {
		t.Errorf("month expression = %q, want an ADD of zero that creates the row without counting on it", expr)
	}
	for _, in := range api.updates {
		if strings.Contains(*in.UpdateExpression, "cost") {
			t.Errorf("a request count moved a cost counter: %q", *in.UpdateExpression)
		}
	}
	if _, has := month.ExpressionAttributeValues[":g1pk"]; !has {
		t.Error("the month row lost its GSI1 key")
	}
	if _, has := day.ExpressionAttributeValues[":ttl"]; !has {
		t.Error("the day row lost its TTL")
	}

	for i := 0; i < 3; i++ {
		if err := d.CountRequest(context.Background(), "tenant-a", "2026-09-04"); err != nil {
			t.Fatalf("CountRequest %d: %v", i+2, err)
		}
	}
	if len(api.updates) != 5 {
		t.Fatalf("updates after four requests = %d, want 5: the month row is written on the day's first request only", len(api.updates))
	}
	for _, in := range api.updates[2:] {
		if keyOf(in, "sk") != "USAGE#2026-09-04" {
			t.Errorf("a later request wrote %s; only the day row moves", keyOf(in, "sk"))
		}
	}
	// A new day, or another tenant, starts its own count and its own month write.
	if err := d.CountRequest(context.Background(), "tenant-a", "2026-09-05"); err != nil {
		t.Fatalf("CountRequest (next day): %v", err)
	}
	if len(api.updates) != 7 || keyOf(api.updates[6], "sk") != "USAGE#2026-09" {
		t.Errorf("updates after the next day's first request = %d (last %s), want the month row touched again", len(api.updates), keyOf(api.updates[len(api.updates)-1], "sk"))
	}
	if err := d.CountRequest(context.Background(), "", "2026-09-04"); err == nil {
		t.Error("an anonymous request was counted")
	}
	if err := d.CountRequest(context.Background(), "tenant-a", "2026-09"); err == nil {
		t.Error("a malformed day was accepted")
	}
}

// The read recovers the per-provider split from the month row and the request
// count as the sum of the day rows; the month row's own api_requests — a count
// from before 2026-09-05, when both rows were written — is ignored.
func TestMonthReadsProvidersAndRequests(t *testing.T) {
	api := &fakeAPI{items: []map[string]types.AttributeValue{
		{
			"sk": strAttr("USAGE#2026-09"), attrPeriod: strAttr("2026-09"),
			attrCost: numAttr(1000), attrCalls: numAttr(3), attrAPIRequests: numAttr(999),
			"op_ask_cost_micros": numAttr(400), "op_ask_calls": numAttr(1),
			"provider_groq_cost_micros": numAttr(600), "provider_groq_calls": numAttr(2), "provider_groq_audio_seconds": floatAttr(41.5),
			"provider_openai_cost_micros": numAttr(400), "provider_openai_calls": numAttr(1), "provider_openai_input_tokens": numAttr(900),
		},
		{"sk": strAttr("USAGE#2026-09-04"), attrPeriod: strAttr("2026-09-04"), attrCost: numAttr(1000), attrCalls: numAttr(3), attrAPIRequests: numAttr(12)},
		{"sk": strAttr("USAGE#2026-09-05"), attrPeriod: strAttr("2026-09-05"), attrAPIRequests: numAttr(300)},
	}}
	got, err := NewDynamo(api, "t").Month(context.Background(), "tenant-a", "2026-09")
	if err != nil {
		t.Fatalf("Month: %v", err)
	}
	if got.APIRequests != 312 || got.Ops["ask"].CostMicros != 400 {
		t.Errorf("month = %+v; api_requests is the sum of the days (12 + 300), never the month row's own attribute", got)
	}
	if got.Providers["groq"].CostMicros != 600 || got.Providers["groq"].AudioSeconds != 41.5 || got.Providers["openai"].InputTokens != 900 || len(got.Providers) != 2 {
		t.Errorf("providers = %+v", got.Providers)
	}
	if len(got.Days) != 2 || got.Days[0].APIRequests != 12 || got.Days[1].APIRequests != 300 || got.Days[1].CostMicros != 0 {
		t.Errorf("days = %+v; a day with requests and no calls is still a day", got.Days)
	}
}

// The instance's provider spend for a month is the breaker's day counters
// summed: INSTANCE / SPEND#<day>, spend_micros — the row and attribute
// internal/pipeline's DynamoCounter writes.
func TestInstanceSpendSumsTheDayCounters(t *testing.T) {
	api := &fakeAPI{items: []map[string]types.AttributeValue{
		{"pk": strAttr("INSTANCE"), "sk": strAttr("SPEND#2026-09-01"), "spend_micros": numAttr(1500)},
		{"pk": strAttr("INSTANCE"), "sk": strAttr("SPEND#2026-09-02"), "spend_micros": numAttr(2500)},
	}}
	d := NewDynamo(api, "t")
	got, err := d.InstanceSpend(context.Background(), "2026-09")
	if err != nil {
		t.Fatalf("InstanceSpend: %v", err)
	}
	if got != 4000 {
		t.Errorf("InstanceSpend = %d, want 4000", got)
	}
	if _, err := d.InstanceSpend(context.Background(), "September"); !errors.Is(err, ErrBadMonth) {
		t.Errorf("malformed month: err = %v", err)
	}
	if got, _ := NewDynamo(&fakeAPI{}, "t").InstanceSpend(context.Background(), "2026-01"); got != 0 {
		t.Errorf("no counters: %d, want 0", got)
	}
}

// The storage snapshot is two writes with fresh maps each: an ADD onto the
// month row under a condition on storage_snapshot_day, and a SET on the day
// row. The condition is what makes a second run on the same day add nothing.
func TestAddStorageDayAddsToTheMonthOnceADay(t *testing.T) {
	api := &fakeAPI{}
	d := NewDynamo(api, "t")
	d.now = func() time.Time { return time.Date(2026, 9, 5, 3, 0, 0, 0, time.UTC) }

	added, err := d.AddStorageDay(context.Background(), StorageSnapshot{TenantID: "tenant-a", Day: "2026-09-05", AudioBytes: 9_123_456, Notes: 12})
	if err != nil {
		t.Fatalf("AddStorageDay: %v", err)
	}
	if !added || len(api.updates) != 2 {
		t.Fatalf("added = %v, updates = %d; want the month and the day written", added, len(api.updates))
	}
	month, day := api.updates[0], api.updates[1]
	if keyOf(month, "sk") != "USAGE#2026-09" || keyOf(day, "sk") != "USAGE#2026-09-05" {
		t.Errorf("keys = %q, %q", keyOf(month, "sk"), keyOf(day, "sk"))
	}
	if month.ConditionExpression == nil || !strings.Contains(*month.ConditionExpression, "attribute_not_exists(#snap) OR #snap < :day") {
		t.Errorf("month condition = %v, want one on the last snapshot day", month.ConditionExpression)
	}
	if !strings.HasPrefix(*month.UpdateExpression, "ADD #bd :bytes, #nd :notes") || !strings.Contains(*month.UpdateExpression, "#snap = :day") {
		t.Errorf("month expression = %q", *month.UpdateExpression)
	}
	if month.ExpressionAttributeNames["#bd"] != "storage_byte_days" || month.ExpressionAttributeNames["#nd"] != "storage_note_days" ||
		month.ExpressionAttributeNames["#snap"] != "storage_snapshot_day" {
		t.Errorf("month names = %v", month.ExpressionAttributeNames)
	}
	if got := month.ExpressionAttributeValues[":bytes"].(*types.AttributeValueMemberN).Value; got != "9123456" {
		t.Errorf("bytes = %q", got)
	}
	// The month row is what the admin listing and the snapshot task find
	// tenants through, so a month created by a snapshot alone is listed too.
	if got := month.ExpressionAttributeValues[":g1pk"].(*types.AttributeValueMemberS).Value; got != "USAGE#2026-09" {
		t.Errorf("month gsi1pk = %q", got)
	}
	if day.ConditionExpression != nil {
		t.Errorf("the day row is a plain SET; condition = %q", *day.ConditionExpression)
	}
	if !strings.HasPrefix(*day.UpdateExpression, "SET #bd = :bytes, #nd = :notes") || !strings.Contains(*day.UpdateExpression, "#ttl = :ttl") {
		t.Errorf("day expression = %q", *day.UpdateExpression)
	}
	// Fresh maps: nothing from the month write leaks into the day write.
	for name := range day.ExpressionAttributeNames {
		if name == "#snap" {
			continue
		}
		if !strings.Contains(*day.UpdateExpression, name) {
			t.Errorf("day names carry %s, which its expression does not use", name)
		}
	}

	// The same day again: the month's condition fails, nothing is added, and
	// the day row is still written so a retry can finish an interrupted run.
	api.updates = nil
	api.conditionFails = true
	added, err = d.AddStorageDay(context.Background(), StorageSnapshot{TenantID: "tenant-a", Day: "2026-09-05", AudioBytes: 9_123_456, Notes: 12})
	if err != nil {
		t.Fatalf("AddStorageDay again: %v", err)
	}
	if added {
		t.Error("added = true on the second run of the day")
	}
	if len(api.updates) != 2 || keyOf(api.updates[1], "sk") != "USAGE#2026-09-05" {
		t.Errorf("updates after the failed condition = %d, want the day row still written", len(api.updates))
	}
}

func TestAddStorageDayRefusesAMalformedSnapshot(t *testing.T) {
	d := NewDynamo(&fakeAPI{}, "t")
	for _, s := range []StorageSnapshot{
		{Day: "2026-09-05"},
		{TenantID: "t", Day: "2026-09"},
		{TenantID: "t", Day: "2026-09-05", AudioBytes: -1},
	} {
		if _, err := d.AddStorageDay(context.Background(), s); err == nil {
			t.Errorf("AddStorageDay(%+v) accepted", s)
		}
	}
}

func TestMonthReadsTheStorageDays(t *testing.T) {
	api := &fakeAPI{items: []map[string]types.AttributeValue{
		{"sk": strAttr("USAGE#2026-09"), "period": strAttr("2026-09"), "storage_byte_days": numAttr(27_370_368), "storage_note_days": numAttr(36)},
		{"sk": strAttr("USAGE#2026-09-05"), "period": strAttr("2026-09-05"), "storage_byte_days": numAttr(9_123_456)},
	}}
	got, err := NewDynamo(api, "t").Month(context.Background(), "tenant-a", "2026-09")
	if err != nil {
		t.Fatalf("Month: %v", err)
	}
	if got.StorageByteDays != 27_370_368 || got.StorageNoteDays != 36 {
		t.Errorf("month storage days = %d bytes, %d notes", got.StorageByteDays, got.StorageNoteDays)
	}
	if len(got.Days) != 1 || got.Days[0].StorageByteDays != 9_123_456 {
		t.Errorf("days = %+v", got.Days)
	}
}

// The snapshot task finds its tenants the way the admin listing does: one
// index query per month, tenants read off gsi1sk, the union returned once.
func TestTenantsWithUsageQueriesTheIndexPerMonth(t *testing.T) {
	api := &fakeAPI{items: []map[string]types.AttributeValue{
		{"gsi1pk": strAttr("USAGE#2026-09"), "gsi1sk": strAttr("TENANT#tenant-b")},
		{"gsi1pk": strAttr("USAGE#2026-09"), "gsi1sk": strAttr("TENANT#tenant-a")},
	}}
	got, err := NewDynamo(api, "t").TenantsWithUsage(context.Background(), []string{"2026-09", "2026-08"})
	if err != nil {
		t.Fatalf("TenantsWithUsage: %v", err)
	}
	if len(got) != 2 || got[0] != "tenant-a" || got[1] != "tenant-b" {
		t.Errorf("tenants = %v, want the two, once each, sorted", got)
	}
	if len(api.queries) != 2 {
		t.Fatalf("queries = %d, want one per month", len(api.queries))
	}
	for _, q := range api.queries {
		if q.IndexName == nil || *q.IndexName != "gsi1" || *q.KeyConditionExpression != "gsi1pk = :pk" {
			t.Errorf("query = %+v, want a GSI1 query on gsi1pk", q)
		}
	}
	if _, err := NewDynamo(api, "t").TenantsWithUsage(context.Background(), []string{"09-2026"}); err == nil {
		t.Error("a malformed month was accepted")
	}
}
