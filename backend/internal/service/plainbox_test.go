package service

import "context"

// PlainBox is an identity SealBox: it stores the refresh token unencrypted.
//
// It lives in a _test.go file on purpose, and must stay here. It is a plausible
// thing to reach for when a key is missing — "just fall back to plaintext so
// the feature still works" — and doing that would silently store every Cognito
// refresh token in DynamoDB in the clear, which is the exact separation the
// vault exists to provide. Compiled only into the test binary, it cannot be
// selected by any configuration, missing or otherwise, because it does not
// exist in the deployed artifact.
//
// The production path fails closed instead: no vault key, no biometric unlock.
type PlainBox struct{}

func (PlainBox) Seal(_ context.Context, plaintext []byte) ([]byte, error) {
	return append([]byte(nil), plaintext...), nil
}

func (PlainBox) Open(_ context.Context, ciphertext []byte) ([]byte, error) {
	return append([]byte(nil), ciphertext...), nil
}
