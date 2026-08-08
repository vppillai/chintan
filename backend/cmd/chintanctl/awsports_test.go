package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

// recordingSources counts what resolveContentBucket asked for, so a test can
// assert that a cheaper answer stopped it looking further.
type recordingSources struct {
	output     string
	outputErr  error
	account    string
	accountErr error

	stackCalls   []string
	accountCalls int
}

func (r *recordingSources) sources() bucketSources {
	return bucketSources{
		stackOutput: func(_ context.Context, stackName, outputKey string) (string, error) {
			r.stackCalls = append(r.stackCalls, stackName+"/"+outputKey)
			return r.output, r.outputErr
		},
		accountID: func(context.Context) (string, error) {
			r.accountCalls++
			return r.account, r.accountErr
		},
	}
}

// The bucket name is derived in three places already — the template, deploy.sh,
// and here. The stack knows the answer; nothing else should be guessing it.
func TestContentBucketResolutionPrefersTheExplicitOverride(t *testing.T) {
	src := &recordingSources{output: "chintan-content-dev-prod-000000000000"}
	var warn bytes.Buffer

	got, err := resolveContentBucket(context.Background(),
		globalFlags{instance: "dev", environment: "staging", bucket: "scratch-bucket"},
		src.sources(), &warn)
	if err != nil {
		t.Fatalf("resolveContentBucket: %v", err)
	}
	if got != "scratch-bucket" {
		t.Fatalf("bucket = %q, want the explicit --bucket", got)
	}
	if len(src.stackCalls) != 0 || src.accountCalls != 0 {
		t.Fatalf("an explicit --bucket still went looking: stack=%v sts=%d", src.stackCalls, src.accountCalls)
	}
	if warn.Len() != 0 {
		t.Fatalf("unexpected warning: %s", warn.String())
	}
}

// --environment staging must read staging's blobs. Deriving the name without
// the environment reads prod's.
func TestContentBucketComesFromTheStackOutput(t *testing.T) {
	src := &recordingSources{output: "chintan-content-dev-staging-000000000000"}
	var warn bytes.Buffer

	got, err := resolveContentBucket(context.Background(),
		globalFlags{instance: "dev", environment: "staging"}, src.sources(), &warn)
	if err != nil {
		t.Fatalf("resolveContentBucket: %v", err)
	}
	if got != "chintan-content-dev-staging-000000000000" {
		t.Fatalf("bucket = %q, want the stack's ContentBucketName output", got)
	}
	if len(src.stackCalls) != 1 || src.stackCalls[0] != "chintan-dev-staging/ContentBucketName" {
		t.Fatalf("stack lookups = %v, want one describe of chintan-dev-staging for ContentBucketName", src.stackCalls)
	}
	if src.accountCalls != 0 {
		t.Fatal("the stack answered, so STS should not have been called")
	}
	if warn.Len() != 0 {
		t.Fatalf("unexpected warning when the stack answered: %s", warn.String())
	}
}

// A stack that cannot be described — no permission, a different account, a
// bucket kept after the stack was deleted — still has to leave the operator
// with a working command, but never silently.
func TestContentBucketFallsBackToTheConventionWithAWarning(t *testing.T) {
	src := &recordingSources{outputErr: errors.New("AccessDenied"), account: "000000000000"}
	var warn bytes.Buffer

	got, err := resolveContentBucket(context.Background(),
		globalFlags{instance: "dev", environment: "staging"}, src.sources(), &warn)
	if err != nil {
		t.Fatalf("resolveContentBucket: %v", err)
	}
	// The environment is the whole defect: without it, staging reads prod.
	if got != "chintan-content-dev-staging-000000000000" {
		t.Fatalf("bucket = %q, want the environment-qualified convention", got)
	}
	if warn.Len() == 0 {
		t.Fatal("the convention was used with no warning; a guessed bucket that reads nothing is the failure mode this fix exists for")
	}
	if !strings.Contains(warn.String(), "chintan-dev-staging") {
		t.Fatalf("warning = %q, want it to name the stack that could not be described", warn.String())
	}
}

// The zero that made H3 invisible: index rows pointing at objects, and an
// object listing that came back empty. Whatever the cause — the wrong bucket,
// the wrong account, a bucket policy — reporting "objects: 0" and exiting 0 is
// the one thing this must not do.
func TestZeroObjectsAgainstReferencedKeysIsAnError(t *testing.T) {
	tgt := target{Instance: "dev", Environment: "staging", Bucket: "chintan-content-dev-000000000000"}
	refs := map[string]string{
		"tenants/u1/notes/n1/note.md":       "NOTE#n1",
		"tenants/u1/captures/c1/audio.webm": "CAPTURE#c1",
	}

	err := requireObjectsForReferencedKeys(tgt, "u1", 0, refs)
	if err == nil {
		t.Fatal("a tenant whose index rows reference objects reported zero objects and no error")
	}
	msg := err.Error()
	for _, want := range []string{"chintan-content-dev-000000000000", "u1"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error = %q, want it to name %q", msg, want)
		}
	}

	// The honest cases stay quiet: objects found, or nothing referenced.
	if err := requireObjectsForReferencedKeys(tgt, "u1", 2, refs); err != nil {
		t.Fatalf("objects were found and it still failed: %v", err)
	}
	if err := requireObjectsForReferencedKeys(tgt, "u1", 0, nil); err != nil {
		t.Fatalf("a tenant with no referenced keys is legitimately empty: %v", err)
	}
}
