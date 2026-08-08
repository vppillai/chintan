package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/vppillai/chintan/backend/internal/meter"
)

type fakeUsageAPI struct {
	items []map[string]types.AttributeValue
	err   error
}

func (f *fakeUsageAPI) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.items = append(f.items, in.Item)
	return &dynamodb.PutItemOutput{}, nil
}

func s(t *testing.T, item map[string]types.AttributeValue, k string) string {
	t.Helper()
	v, ok := item[k]
	if !ok {
		t.Fatalf("attribute %q absent; item has %v", k, keysOf(item))
	}
	sv, ok := v.(*types.AttributeValueMemberS)
	if !ok {
		t.Fatalf("attribute %q is not a string", k)
	}
	return sv.Value
}

func n(t *testing.T, item map[string]types.AttributeValue, k string) string {
	t.Helper()
	v, ok := item[k]
	if !ok {
		t.Fatalf("attribute %q absent; item has %v", k, keysOf(item))
	}
	nv, ok := v.(*types.AttributeValueMemberN)
	if !ok {
		t.Fatalf("attribute %q is not a number", k)
	}
	return nv.Value
}

func keysOf(item map[string]types.AttributeValue) []string {
	out := make([]string, 0, len(item))
	for k := range item {
		out = append(out, k)
	}
	return out
}

func sampleUsage() meter.Usage {
	return meter.Usage{
		TenantID:      "tenant-a",
		Provider:      "groq",
		Model:         "whisper-large-v3",
		Op:            meter.OpTranscribe,
		Unit:          meter.UnitAudioSeconds,
		Quantity:      42,
		CostMicros:    84,
		CorrelationID: "corr-1",
		At:            time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC),
	}
}

// chintanctl's `usage` command reads these rows. If the key shape drifts, the
// command silently reports nothing rather than failing.
func TestUsageRecordUsesTheKeyShapeTheCLIReads(t *testing.T) {
	api := &fakeUsageAPI{}
	sink := NewDynamoUsageSink(api, "tbl")
	sink.newID = func() string { return "fixed-id" }

	if err := sink.Record(context.Background(), sampleUsage()); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if len(api.items) != 1 {
		t.Fatalf("wrote %d items", len(api.items))
	}
	item := api.items[0]

	if got := s(t, item, "pk"); got != "USER#tenant-a" {
		t.Fatalf("pk=%q", got)
	}
	// The date must be inside the sort key so a range query is a key condition
	// rather than a scan.
	if got := s(t, item, "sk"); got != "USAGE#2026-08-07#fixed-id" {
		t.Fatalf("sk=%q", got)
	}
}

func TestUsageRecordPromotesTheAttributesTheCLIProjects(t *testing.T) {
	api := &fakeUsageAPI{}
	if err := NewDynamoUsageSink(api, "tbl").Record(context.Background(), sampleUsage()); err != nil {
		t.Fatalf("Record: %v", err)
	}
	item := api.items[0]

	for k, want := range map[string]string{
		"tenant_id":      "tenant-a",
		"provider":       "groq",
		"model":          "whisper-large-v3",
		"op":             "transcribe",
		"unit":           "audio_seconds",
		"correlation_id": "corr-1",
		"type":           "usage",
	} {
		if got := s(t, item, k); got != want {
			t.Fatalf("%s=%q want %q", k, got, want)
		}
	}
	if got := n(t, item, "cost_micros"); got != "84" {
		t.Fatalf("cost_micros=%q", got)
	}
	if got := n(t, item, "quantity"); got != "42" {
		t.Fatalf("quantity=%q", got)
	}
}

// A field added to meter.Usage later must still be recoverable from a row
// written today, which is what the blob is for.
func TestUsageRecordKeepsAFullBlobAlongsideThePromotedFields(t *testing.T) {
	api := &fakeUsageAPI{}
	if err := NewDynamoUsageSink(api, "tbl").Record(context.Background(), sampleUsage()); err != nil {
		t.Fatalf("Record: %v", err)
	}

	var round meter.Usage
	if err := json.Unmarshal([]byte(s(t, api.items[0], "data")), &round); err != nil {
		t.Fatalf("blob is not valid JSON: %v", err)
	}
	if round.Model != "whisper-large-v3" || round.CostMicros != 84 || round.Quantity != 42 {
		t.Fatalf("blob lost fidelity: %+v", round)
	}
}

func TestUsageRecordSetsATTLBeyondTheSpendCounters(t *testing.T) {
	api := &fakeUsageAPI{}
	if err := NewDynamoUsageSink(api, "tbl").Record(context.Background(), sampleUsage()); err != nil {
		t.Fatalf("Record: %v", err)
	}
	ttl := n(t, api.items[0], "ttl")
	want := time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC).Add(usageRetention).Unix()
	if ttl != strconv.FormatInt(want, 10) {
		t.Fatalf("ttl=%s want %d", ttl, want)
	}
	// The invoice arrives after the counter has expired, so these must outlive it.
	if usageRetention <= spendRetention {
		t.Fatalf("usage retention (%v) must exceed spend counter retention (%v)", usageRetention, spendRetention)
	}
}

func TestUsageRecordFallsBackToNowWhenTimestampIsZero(t *testing.T) {
	api := &fakeUsageAPI{}
	sink := NewDynamoUsageSink(api, "tbl")
	sink.now = func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }
	sink.newID = func() string { return "id" }

	u := sampleUsage()
	u.At = time.Time{}
	if err := sink.Record(context.Background(), u); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if got := s(t, api.items[0], "sk"); got != "USAGE#2026-01-02#id" {
		t.Fatalf("sk=%q", got)
	}
}

func TestUsageRecordOmitsCorrelationIDWhenAbsent(t *testing.T) {
	api := &fakeUsageAPI{}
	u := sampleUsage()
	u.CorrelationID = ""
	if err := NewDynamoUsageSink(api, "tbl").Record(context.Background(), u); err != nil {
		t.Fatalf("Record: %v", err)
	}
	// An empty string attribute is legal in DynamoDB but pointless; absence is
	// cheaper and reads more honestly.
	if _, present := api.items[0]["correlation_id"]; present {
		t.Fatal("empty correlation id should be omitted rather than written blank")
	}
}

func TestUsageRecordSurfacesWriteFailure(t *testing.T) {
	api := &fakeUsageAPI{err: errors.New("throttled")}
	err := NewDynamoUsageSink(api, "tbl").Record(context.Background(), sampleUsage())
	if err == nil {
		t.Fatal("expected the write failure to surface")
	}
	// The breaker logs and swallows this — metering must not fail a call the
	// provider already completed and billed for — but the sink itself must report it.
	if !strings.Contains(err.Error(), "throttled") {
		t.Fatalf("error lost its cause: %v", err)
	}
}

// A usage row is written to a table an operator reads. It must never carry
// transcript, prompt, or completion text.
func TestUsageRecordCarriesNoContent(t *testing.T) {
	api := &fakeUsageAPI{}
	if err := NewDynamoUsageSink(api, "tbl").Record(context.Background(), sampleUsage()); err != nil {
		t.Fatalf("Record: %v", err)
	}
	permitted := map[string]bool{
		"pk": true, "sk": true, "type": true, "tenant_id": true, "provider": true,
		"model": true, "op": true, "unit": true, "quantity": true,
		"cost_micros": true, "at": true, "data": true, "ttl": true,
		"correlation_id": true,
	}
	for k := range api.items[0] {
		if !permitted[k] {
			t.Fatalf("usage row gained attribute %q — these rows must never carry content", k)
		}
	}
}
