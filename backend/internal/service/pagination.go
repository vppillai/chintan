package service

import (
	"encoding/base64"

	"errors"
	"fmt"
)

// ErrInvalidCursor rejects a continuation token that cannot have been issued by
// this API. It is typed so the handler answers 400 rather than 500: a malformed
// cursor is a client mistake, and reporting it as a server fault sends the
// client back to retry the same broken request.
var ErrInvalidCursor = errors.New("invalid pagination cursor")

// MaxCursorLen matches the OpenAPI bound on the cursor parameter.
const MaxCursorLen = 2048

// ValidateCursor checks a cursor's transport shape before it reaches the store.
//
// It checks only what is true of every cursor this API issues: bounded length,
// and base64url. It deliberately does not inspect the decoded payload. Each
// store encodes its continuation token differently — a DynamoDB
// LastEvaluatedKey, an in-memory sort key — and a handler that knew the
// difference would be a layering inversion that breaks the moment a store
// changes its encoding.
//
// The store still rejects a cursor belonging to another tenant's partition, so
// a forged-but-well-formed cursor is refused there rather than honoured.
func ValidateCursor(cursor string) error {
	if cursor == "" {
		return nil
	}
	if len(cursor) > MaxCursorLen {
		return fmt.Errorf("%w: too long", ErrInvalidCursor)
	}
	if _, err := base64.RawURLEncoding.DecodeString(cursor); err != nil {
		return fmt.Errorf("%w: not base64url", ErrInvalidCursor)
	}
	return nil
}
