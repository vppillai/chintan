package service

import (
	"context"
	"fmt"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
)

// The doubles below embed the real interface and override exactly one method.
// A hand-written stub for a forty-method interface goes stale the day the
// interface grows a method; an embedded one only has to be right about the one
// call the test is about, and the embedded nil panics loudly if a test reaches
// a method it did not mean to.

// errSettingsStore fails the settings read that both the readiness probe and
// the spend gate make.
type errSettingsStore struct {
	repository.Store
	err error
}

func (s errSettingsStore) GetSettings(context.Context, string) (model.Settings, error) {
	return model.Settings{}, s.err
}

// wrappedNotFoundStore reports "no settings row for this tenant" the way a
// store that annotates its errors on the way out does. GetSettings must still
// answer with defaults, which is only true if it compares with errors.Is.
type wrappedNotFoundStore struct{ repository.Store }

func (wrappedNotFoundStore) GetSettings(context.Context, string) (model.Settings, error) {
	return model.Settings{}, fmt.Errorf("dynamodb: GetItem TENANT#u1: %w", repository.ErrNotFound)
}

// bareNotFoundStore returns the sentinel unwrapped, which is what the DynamoDB
// store does today.
type bareNotFoundStore struct{ repository.Store }

func (bareNotFoundStore) GetSettings(context.Context, string) (model.Settings, error) {
	return model.Settings{}, repository.ErrNotFound
}

// errGetObjects fails every object read, which is how the S3 half of the
// readiness probe is made to fail without an S3.
type errGetObjects struct {
	repository.Objects
	err error
}

func (o errGetObjects) Get(context.Context, string) ([]byte, error) { return nil, o.err }

// recordingObjects remembers which keys were read, so a test can assert which
// partition a probe addressed rather than only that it succeeded.
type recordingObjects struct {
	repository.Objects
	reads []string
}

func (o *recordingObjects) Get(ctx context.Context, key string) ([]byte, error) {
	o.reads = append(o.reads, key)
	return o.Objects.Get(ctx, key)
}

// stubCounter stands in for the DynamoDB spend counter and records the day
// partitions it was asked about.
type stubCounter struct {
	total int64
	err   error
	days  []string
}

func (c *stubCounter) Add(_ context.Context, _, day string, deltaMicros int64) (int64, error) {
	c.days = append(c.days, day)
	if c.err != nil {
		return 0, c.err
	}
	c.total += deltaMicros
	return c.total, nil
}
