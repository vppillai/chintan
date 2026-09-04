package usage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/vppillai/chintan/backend/internal/meter"
)

// fakeAPI records UpdateItems and answers Query from a canned item list.
type fakeAPI struct {
	updates []*dynamodb.UpdateItemInput
	items   []map[string]types.AttributeValue
	err     error
}

func (f *fakeAPI) UpdateItem(_ context.Context, in *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.updates = append(f.updates, in)
	return &dynamodb.UpdateItemOutput{}, nil
}

func (f *fakeAPI) Query(_ context.Context, _ *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
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
