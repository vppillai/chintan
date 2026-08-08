package service

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

// A malformed cursor is a client mistake. Typing it as ErrInvalidCursor is what
// lets the handler answer 400 rather than 500, and a 500 sends the client back
// to retry the same broken request forever.
func TestValidateCursorRejectsWhatThisAPICouldNotHaveIssued(t *testing.T) {
	cases := map[string]string{
		"not base64url": "not base64!!",
		"standard base64 padding": base64.StdEncoding.EncodeToString(
			[]byte("user1\x00note_007")),
		"past the declared bound": strings.Repeat("a", MaxCursorLen+1),
	}
	for name, cursor := range cases {
		t.Run(name, func(t *testing.T) {
			if err := ValidateCursor(cursor); !errors.Is(err, ErrInvalidCursor) {
				t.Fatalf("ValidateCursor err = %v, want ErrInvalidCursor", err)
			}
		})
	}
}

// It checks only what is true of every cursor this API issues: bounded length
// and base64url. Inspecting the decoded payload would mean the handler knew how
// each store encodes its continuation token, which breaks the moment a store
// changes its encoding. The store still refuses a cursor from another tenant's
// partition.
func TestValidateCursorAcceptsAnyWellFormedToken(t *testing.T) {
	cases := map[string]string{
		"absent":                "",
		"a store continuation":  base64.RawURLEncoding.EncodeToString([]byte("user1\x00note_007")),
		"a walk offset":         encodeWalkCursor(50),
		"at the declared bound": strings.Repeat("a", MaxCursorLen),
	}
	for name, cursor := range cases {
		t.Run(name, func(t *testing.T) {
			if err := ValidateCursor(cursor); err != nil {
				t.Fatalf("ValidateCursor(%q) = %v, want it accepted at this layer", cursor, err)
			}
		})
	}
}
