package pipeline

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/service"
)

// SQSAPI is the seam that keeps the queue testable. A concrete *sqs.Client here
// would make every caller untestable by construction, which is the mistake the
// v1 repository made.
type SQSAPI interface {
	SendMessage(ctx context.Context, in *sqs.SendMessageInput, opts ...func(*sqs.Options)) (*sqs.SendMessageOutput, error)
}

// Queue hands captures to the worker.
type Queue struct {
	client SQSAPI
	url    string
}

var _ service.Enqueuer = (*Queue)(nil)

// NewQueue builds an enqueuer over an SQS queue URL.
func NewQueue(client SQSAPI, queueURL string) *Queue {
	return &Queue{client: client, url: queueURL}
}

// EnqueueCapture puts one capture on the queue, carrying the request's
// correlation id so the worker's logs join the API's.
func (q *Queue) EnqueueCapture(ctx context.Context, tenantID, captureID, reason string) error {
	body, err := json.Marshal(Message{TenantID: tenantID, CaptureID: captureID, Reason: reason})
	if err != nil {
		return fmt.Errorf("pipeline: encode queue message: %w", err)
	}

	in := &sqs.SendMessageInput{
		QueueUrl:    aws.String(q.url),
		MessageBody: aws.String(string(body)),
	}
	if id := obs.CorrelationID(ctx); id != "" {
		in.MessageAttributes = map[string]types.MessageAttributeValue{
			CorrelationAttribute: {DataType: aws.String("String"), StringValue: aws.String(id)},
		}
	}

	if _, err := q.client.SendMessage(ctx, in); err != nil {
		return fmt.Errorf("pipeline: send queue message: %w", err)
	}
	return nil
}
