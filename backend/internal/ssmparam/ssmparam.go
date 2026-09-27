// Package ssmparam reads an optional SecureString from SSM Parameter Store by
// the path an environment variable names. It exists for the VAPID key pair
// (docs/design/push.md), which an instance runs without until its owner makes
// one: the API and the worker both start either way, and the feature stays
// dormant rather than the deploy failing on a parameter that is not there.
// The provider keys are different — they are required, and cmd/worker fails
// its start on them by design.
package ssmparam

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

// Client is the one SSM call this package makes.
type Client interface {
	GetParameter(ctx context.Context, in *ssm.GetParameterInput, opts ...func(*ssm.Options)) (*ssm.GetParameterOutput, error)
}

// Optional reads the parameter at the path the environment variable names,
// decrypted, and returns "" when the variable is unset, the parameter does
// not exist or its value is blank. Any other failure — a denied read, a
// throttle — is an error, since the deploy granted the read and a silent
// "" would hide a broken grant behind a dormant feature.
func Optional(ctx context.Context, client Client, pathEnv string) (string, error) {
	path := strings.TrimSpace(os.Getenv(pathEnv))
	if path == "" {
		return "", nil
	}
	out, err := client.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(path),
		WithDecryption: aws.Bool(true),
	})
	var missing *types.ParameterNotFound
	if errors.As(err, &missing) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("ssm get %s: %w", path, err)
	}
	if out.Parameter == nil || out.Parameter.Value == nil {
		return "", nil
	}
	return strings.TrimSpace(*out.Parameter.Value), nil
}
