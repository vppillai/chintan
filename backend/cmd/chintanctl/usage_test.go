package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/vppillai/chintan/backend/internal/meter"
	"github.com/vppillai/chintan/backend/internal/usage"
)

// usageWriter applies internal/usage's UpdateItems to a fakePartition, so the
// rows the listing reads are the rows the real writer produces — ADD semantics
// included — rather than a hand-built imitation of them.
type usageWriter struct{ part *fakePartition }

func (w *usageWriter) UpdateItem(ctx context.Context, in *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	pk := in.Key["pk"].(*types.AttributeValueMemberS).Value
	sk := in.Key["sk"].(*types.AttributeValueMemberS).Value
	it, ok, _ := w.part.Get(ctx, pk, sk)
	if !ok {
		it = Item{"pk": StringAttr(pk), "sk": StringAttr(sk)}
	}
	expr := *in.UpdateExpression
	adds, sets, _ := strings.Cut(strings.TrimPrefix(expr, "ADD "), " SET ")
	for _, clause := range strings.Split(adds, ", ") {
		name, val, _ := strings.Cut(clause, " ")
		attr := in.ExpressionAttributeNames[name]
		n := in.ExpressionAttributeValues[val].(*types.AttributeValueMemberN).Value
		it[attr] = NumberAttr(it.Num(attr) + int64(parseFloat(n)))
	}
	for _, clause := range strings.Split(sets, ", ") {
		name, val, _ := strings.Cut(clause, " = ")
		attr := name
		if strings.HasPrefix(name, "#") {
			attr = in.ExpressionAttributeNames[name]
		}
		switch v := in.ExpressionAttributeValues[val].(type) {
		case *types.AttributeValueMemberS:
			it[attr] = StringAttr(v.Value)
		case *types.AttributeValueMemberN:
			it[attr] = NumberAttr(int64(parseFloat(v.Value)))
		}
	}
	return &dynamodb.UpdateItemOutput{}, w.part.Put(ctx, it)
}

func (w *usageWriter) Query(context.Context, *dynamodb.QueryInput, ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	return &dynamodb.QueryOutput{}, nil
}
func (w *usageWriter) PutItem(context.Context, *dynamodb.PutItemInput, ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	return &dynamodb.PutItemOutput{}, nil
}
func (w *usageWriter) GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	return &dynamodb.GetItemOutput{}, nil
}

func parseFloat(s string) float64 {
	var f float64
	for _, r := range s {
		if r == '.' {
			break
		}
		f = f*10 + float64(r-'0')
	}
	return f
}

func TestUsageListsTheMonthRowsInternalUsageWrites(t *testing.T) {
	part := newFakePartition()
	writer := usage.NewDynamo(&usageWriter{part: part}, "t")
	ctx := context.Background()
	for _, rec := range []usage.Record{
		{TenantID: "alice", Day: "2026-09-04", Provider: "groq", Op: meter.OpTranscribe, CostMicros: 3000, Usage: meter.Quantities{meter.UnitAudioSeconds: 120}},
		{TenantID: "alice", Day: "2026-09-04", Provider: "openai", Op: meter.OpAsk, CostMicros: 500},
		{TenantID: "bob", Day: "2026-09-10", Provider: "openai", Op: meter.OpCleanup, CostMicros: 700},
		{TenantID: "carol", Day: "2026-08-30", Provider: "openai", Op: meter.OpCleanup, CostMicros: 9999},
	} {
		if err := writer.Record(ctx, rec); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	for i := 0; i < 3; i++ {
		if err := writer.CountRequest(ctx, "alice", "2026-09-05"); err != nil {
			t.Fatalf("CountRequest: %v", err)
		}
	}
	// A tenant with requests and no calls still has a month row, found the
	// same way.
	if err := writer.CountRequest(ctx, "dave", "2026-09-01"); err != nil {
		t.Fatalf("CountRequest: %v", err)
	}

	e := &env{Part: part, Blobs: newFakeBlobs(), Target: target{Instance: "dev", Environment: "prod"}}
	putsBefore := part.puts
	res, err := runUsage(ctx, e, "2026-09", nil)
	if err != nil {
		t.Fatalf("runUsage: %v", err)
	}
	var ids []string
	for _, tenant := range res.Tenants {
		ids = append(ids, tenant.TenantID)
	}
	if strings.Join(ids, ",") != "alice,bob,dave" {
		t.Errorf("tenants = %v; carol has no September row and must not appear", ids)
	}
	alice := res.Tenants[0]
	if alice.CostMicros != 3500 || alice.Calls != 2 || alice.AudioSeconds != 120 || alice.APIRequests != 3 {
		t.Errorf("alice = %+v", alice)
	}
	if alice.OpCostMicros["transcribe"] != 3000 || alice.OpCostMicros["ask"] != 500 || len(alice.OpCostMicros) != 2 {
		t.Errorf("alice per-op = %v", alice.OpCostMicros)
	}
	if res.Tenants[2].APIRequests != 1 || res.Tenants[2].Calls != 0 {
		t.Errorf("dave = %+v", res.Tenants[2])
	}
	if res.CostMicros != 4200 {
		t.Errorf("total = %d, want 4200", res.CostMicros)
	}
	if part.puts != putsBefore || part.updates != 0 || part.deletes != 0 {
		t.Errorf("the listing changed the table: puts=%d updates=%d deletes=%d", part.puts-putsBefore, part.updates, part.deletes)
	}

	// --tenant skips the index and reads the named rows; an unknown one is
	// simply absent.
	res, err = runUsage(ctx, e, "2026-09", []string{"bob", "nobody"})
	if err != nil {
		t.Fatalf("runUsage(--tenant): %v", err)
	}
	if len(res.Tenants) != 1 || res.Tenants[0].TenantID != "bob" || res.Tenants[0].CostMicros != 700 {
		t.Errorf("tenants = %+v", res.Tenants)
	}
	if _, err := runUsage(ctx, e, "2026-09", []string{"../other"}); err == nil {
		t.Error("a tenant id that could escape the partition was accepted")
	}

	var out bytes.Buffer
	if err := report(&out, false, res); err != nil {
		t.Fatalf("report: %v", err)
	}
	for _, want := range []string{"usage dev (prod) for 2026-09", "bob", "$0.0007", "cleanup=$0.0007", "total (1 tenants)"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("human output lacks %q:\n%s", want, out.String())
		}
	}
	var asJSON bytes.Buffer
	if err := report(&asJSON, true, res); err != nil {
		t.Fatalf("report(json): %v", err)
	}
	if !strings.Contains(asJSON.String(), `"api_requests": 0`) || !strings.Contains(asJSON.String(), `"month": "2026-09"`) {
		t.Errorf("json output:\n%s", asJSON.String())
	}
}

func TestUsageCommandRefusesAMalformedMonth(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"usage", "--instance", "dev", "--month", "September"}, &stdout, &stderr, strings.NewReader(""))
	if err == nil || !strings.Contains(err.Error(), "yyyy-mm") {
		t.Errorf("err = %v, want a month format error", err)
	}
	if _, err := time.Parse("2006-01", "2026-09"); err != nil {
		t.Fatalf("the default month format is not what the flag says: %v", err)
	}
}
