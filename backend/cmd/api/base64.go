package main

import (
	"encoding/base64"
	"fmt"
)

// decodeBase64 decodes an API Gateway base64-encoded body.
//
// Split out so the encoding import stays visible at one call site, and so the error
// wraps with context rather than surfacing a bare "illegal base64 data" that says
// nothing about where it came from.
func decodeBase64(s string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return "", fmt.Errorf("decoding base64 request body: %w", err)
	}
	return string(b), nil
}
