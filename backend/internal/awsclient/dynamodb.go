package awsclient

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

// NewDynamoDB builds a DynamoDB client from the ambient Lambda configuration.
//
// Unlike NewS3 this returns the SDK client rather than a wrapper, and the reason is
// worth stating because it is a deliberate exception to this package's rule that no
// other package imports the SDK. repository.Dynamo declares the four-operation
// interface it needs (*dynamodb.Client satisfies it), and the alternative — wrapping
// every operation here in SDK-free types — would put the attribute-value translation
// in two packages instead of one. That translation is where money is encoded as an
// exact integer rather than a float (see repository/dynamodb.go); duplicating it means
// two chances to get it wrong and one of them untested.
//
// Both reasons this package exists still hold: region and retry policy are configured
// once, here, and no test needs AWS credentials because the interface repository
// depends on is fakeable with a struct literal.
func NewDynamoDB(ctx context.Context) (*dynamodb.Client, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("awsclient: loading AWS config: %w", err)
	}
	return dynamodb.NewFromConfig(cfg), nil
}
