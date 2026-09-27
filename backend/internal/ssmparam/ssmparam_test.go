package ssmparam

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

type fakeSSM struct {
	values map[string]string
	err    error
	asked  []string
}

func (f *fakeSSM) GetParameter(_ context.Context, in *ssm.GetParameterInput, _ ...func(*ssm.Options)) (*ssm.GetParameterOutput, error) {
	f.asked = append(f.asked, aws.ToString(in.Name))
	if f.err != nil {
		return nil, f.err
	}
	v, ok := f.values[aws.ToString(in.Name)]
	if !ok {
		return nil, &types.ParameterNotFound{}
	}
	return &ssm.GetParameterOutput{Parameter: &types.Parameter{Value: aws.String(v)}}, nil
}

func TestOptionalIsEmptyWhenUnsetOrAbsentAndFailsOnAnythingElse(t *testing.T) {
	ctx := context.Background()
	client := &fakeSSM{values: map[string]string{"/chintan/dev/vapid_public_key": " BPUBLIC \n"}}

	if v, err := Optional(ctx, client, "SSMPARAM_TEST_UNSET"); v != "" || err != nil {
		t.Fatalf("unset variable: %q, %v", v, err)
	}
	if len(client.asked) != 0 {
		t.Fatalf("an unset variable asked SSM for %v", client.asked)
	}

	t.Setenv("SSMPARAM_TEST_PATH", "/chintan/dev/vapid_public_key")
	if v, err := Optional(ctx, client, "SSMPARAM_TEST_PATH"); v != "BPUBLIC" || err != nil {
		t.Fatalf("present parameter: %q, %v", v, err)
	}

	t.Setenv("SSMPARAM_TEST_PATH", "/chintan/dev/vapid_private_key")
	if v, err := Optional(ctx, client, "SSMPARAM_TEST_PATH"); v != "" || err != nil {
		t.Fatalf("absent parameter: %q, %v, want empty and no error", v, err)
	}

	client.err = errors.New("AccessDeniedException")
	if _, err := Optional(ctx, client, "SSMPARAM_TEST_PATH"); err == nil {
		t.Fatal("a denied read was reported as an absent parameter")
	}
}
