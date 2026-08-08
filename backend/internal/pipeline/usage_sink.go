package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/google/uuid"

	"github.com/vppillai/chintan/backend/internal/meter"
)

// usageRetention is how long an individual usage record survives.
//
// Longer than the spend counter's 90 days: the counter only has to answer "am I
// over today's cap", whereas these records are what `chintanctl usage` reads to
// answer "where did last quarter's money go", and that question is asked after
// the invoice arrives.
const usageRetention = 400 * 24 * time.Hour

// UsageAPI is the slice of DynamoDB the usage sink uses.
type UsageAPI interface {
	PutItem(ctx context.Context, in *dynamodb.PutItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
}

// DynamoUsageSink persists one row per billable provider call.
//
// The spend counter (DynamoCounter) is a running total and is what the breaker
// enforces against; it cannot say which model or which pipeline stage the money
// went to. These rows can, and they are unreconstructable after the fact —
// which is the whole reason metering lands before the features that increase
// usage rather than after.
type DynamoUsageSink struct {
	client UsageAPI
	table  string
	now    func() time.Time
	newID  func() string
}

var _ meter.Sink = (*DynamoUsageSink)(nil)

// NewDynamoUsageSink builds the sink over the single table.
func NewDynamoUsageSink(client UsageAPI, table string) *DynamoUsageSink {
	return &DynamoUsageSink{
		client: client,
		table:  table,
		now:    time.Now,
		newID:  uuid.NewString,
	}
}

// Record implements meter.Sink.
//
// The sort key is USAGE#<yyyy-mm-dd>#<id>, so a date range is a key-condition
// query rather than a scan, and the id keeps two calls in the same millisecond
// from colliding. Attributes are promoted to top level as well as kept in the
// data blob: promoted so a reader can project only what it needs, blob so a
// field added later is still recoverable from an older row.
func (s *DynamoUsageSink) Record(ctx context.Context, u meter.Usage) error {
	at := u.At
	if at.IsZero() {
		at = s.now()
	}
	at = at.UTC()

	blob, err := json.Marshal(u)
	if err != nil {
		return fmt.Errorf("pipeline: marshal usage: %w", err)
	}

	item := map[string]types.AttributeValue{
		"pk":          &types.AttributeValueMemberS{Value: "USER#" + u.TenantID},
		"sk":          &types.AttributeValueMemberS{Value: "USAGE#" + at.Format("2006-01-02") + "#" + s.newID()},
		"type":        &types.AttributeValueMemberS{Value: "usage"},
		"tenant_id":   &types.AttributeValueMemberS{Value: u.TenantID},
		"provider":    &types.AttributeValueMemberS{Value: u.Provider},
		"model":       &types.AttributeValueMemberS{Value: u.Model},
		"op":          &types.AttributeValueMemberS{Value: string(u.Op)},
		"unit":        &types.AttributeValueMemberS{Value: string(u.Unit)},
		"quantity":    &types.AttributeValueMemberN{Value: fmt.Sprintf("%g", u.Quantity)},
		"cost_micros": &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", u.CostMicros)},
		"at":          &types.AttributeValueMemberS{Value: at.Format(time.RFC3339Nano)},
		"data":        &types.AttributeValueMemberS{Value: string(blob)},
		"ttl":         &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", at.Add(usageRetention).Unix())},
	}
	if u.CorrelationID != "" {
		item["correlation_id"] = &types.AttributeValueMemberS{Value: u.CorrelationID}
	}

	if _, err := s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(s.table),
		Item:      item,
	}); err != nil {
		return fmt.Errorf("pipeline: put usage record: %w", err)
	}
	return nil
}
