package pipeline

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/vppillai/chintan/backend/internal/breaker"
)

// spendRetention is how long a day's counter survives. Long enough to answer
// "what did last month cost" from the table, short enough that it does not
// accumulate a row per day forever.
const spendRetention = 90 * 24 * time.Hour

// spendPK is the partition every day's counter lives in. It is not a tenant
// partition: the cap is instance-wide, and one row per day for the whole
// instance is what the breaker compares against. Keeping it out of USER#
// also keeps it out of chintanctl erase, which is right — a tenant's departure
// does not un-spend the money.
const spendPK = "INSTANCE"

// CounterAPI is the slice of DynamoDB the spend counter uses.
type CounterAPI interface {
	UpdateItem(ctx context.Context, in *dynamodb.UpdateItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
}

// DynamoCounter is the atomic per-day spend accumulator the breaker enforces
// its cap with.
//
// It is an ADD, not a read-then-write. Two concurrent captures reading 900 and
// each writing 950 would leave a day's spend understated by exactly the amount
// the cap exists to catch.
type DynamoCounter struct {
	client CounterAPI
	table  string
	now    func() time.Time
}

var _ breaker.Counter = (*DynamoCounter)(nil)

// NewDynamoCounter builds the counter over the single table.
func NewDynamoCounter(client CounterAPI, table string) *DynamoCounter {
	return &DynamoCounter{client: client, table: table, now: time.Now}
}

// Add applies deltaMicros to the day's row — pk INSTANCE, sk SPEND#<day> —
// and returns the post-increment total.
func (c *DynamoCounter) Add(ctx context.Context, day string, deltaMicros int64) (int64, error) {
	out, err := c.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(c.table),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: spendPK},
			"sk": &types.AttributeValueMemberS{Value: "SPEND#" + day},
		},
		UpdateExpression: aws.String("ADD spend_micros :d SET #t = :ttl, #ty = :type"),
		ExpressionAttributeNames: map[string]string{
			"#t":  "ttl",
			"#ty": "type",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":d":    &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", deltaMicros)},
			":ttl":  &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", c.now().Add(spendRetention).Unix())},
			":type": &types.AttributeValueMemberS{Value: "spend"},
		},
		ReturnValues: types.ReturnValueUpdatedNew,
	})
	if err != nil {
		return 0, fmt.Errorf("pipeline: spend counter update: %w", err)
	}

	total, ok := out.Attributes["spend_micros"].(*types.AttributeValueMemberN)
	if !ok {
		return 0, fmt.Errorf("pipeline: spend counter returned no total")
	}
	var parsed int64
	if _, err := fmt.Sscan(total.Value, &parsed); err != nil {
		return 0, fmt.Errorf("pipeline: spend counter total is not a number: %w", err)
	}
	return parsed, nil
}
