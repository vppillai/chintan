package pipeline

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"

	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/service"
)

// LambdaAPI is the seam that keeps the invoker testable. A concrete
// *lambda.Client here would make every caller untestable by construction,
// which is the mistake the v1 repository made.
type LambdaAPI interface {
	Invoke(ctx context.Context, in *lambda.InvokeInput, opts ...func(*lambda.Options)) (*lambda.InvokeOutput, error)
}

// Invoker hands captures to the worker Lambda.
type Invoker struct {
	client   LambdaAPI
	function string
}

var _ service.Invoker = (*Invoker)(nil)

// NewInvoker builds an invoker over the worker's live alias ARN.
//
// The alias, not the function: a rollback that moves the alias back to the
// previous version must move the API's retries with it, or a retry would run
// code the deploy just rolled away from.
func NewInvoker(client LambdaAPI, functionARN string) *Invoker {
	return &Invoker{client: client, function: functionARN}
}

// InvokeCapture invokes the worker asynchronously for one capture, carrying
// the request's correlation id so the worker's logs join the API's.
//
// InvocationType Event returns as soon as Lambda has queued the event, which is
// what the request path wants: the work runs for minutes and nothing here can
// wait for it. Lambda retries a failed invocation twice on its own, and sends
// one that fails all three attempts to the dead-letter queue, exactly as it
// does for the S3 notification that starts a capture.
func (i *Invoker) InvokeCapture(ctx context.Context, tenantID, captureID, reason string) error {
	payload, err := json.Marshal(Invocation{
		TenantID:      tenantID,
		CaptureID:     captureID,
		Reason:        reason,
		CorrelationID: obs.CorrelationID(ctx),
	})
	if err != nil {
		return fmt.Errorf("pipeline: encode invocation: %w", err)
	}

	out, err := i.client.Invoke(ctx, &lambda.InvokeInput{
		FunctionName:   aws.String(i.function),
		InvocationType: types.InvocationTypeEvent,
		Payload:        payload,
	})
	if err != nil {
		return fmt.Errorf("pipeline: invoke worker: %w", err)
	}
	// Event invocations answer 202 once queued. Anything else means the
	// request was not accepted, whatever the error field says.
	if out.StatusCode != 202 {
		return fmt.Errorf("pipeline: invoke worker: unexpected status %d", out.StatusCode)
	}
	return nil
}
