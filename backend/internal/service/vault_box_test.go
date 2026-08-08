package service

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func testVaultKey(fill byte) []byte {
	k := make([]byte, VaultKeySize)
	for i := range k {
		k[i] = fill
	}
	return k
}

func TestNewAESVaultBoxRefusesAKeyThatIsNotAES256(t *testing.T) {
	for _, size := range []int{0, 1, 16, 31, 33, 64} {
		if _, err := NewAESVaultBox(make([]byte, size), "ssm:1"); err == nil {
			t.Errorf("NewAESVaultBox accepted a %d-byte key; only %d bytes is AES-256", size, VaultKeySize)
		}
	}
	if _, err := NewAESVaultBox(testVaultKey(0x11), "ssm:1"); err != nil {
		t.Fatalf("NewAESVaultBox rejected a %d-byte key: %v", VaultKeySize, err)
	}
}

func TestAESVaultBoxRoundTripsTheRefreshToken(t *testing.T) {
	ctx := context.Background()
	box, err := NewAESVaultBox(testVaultKey(0x11), "ssm:1")
	if err != nil {
		t.Fatalf("NewAESVaultBox: %v", err)
	}

	token := []byte("eyJraWQiOiJyZWZyZXNoIn0.a-real-looking-cognito-refresh-token")
	sealed, err := box.Seal(ctx, token)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bytes.Contains(sealed, token) {
		t.Fatal("the sealed blob contains the plaintext token verbatim")
	}
	opened, err := box.Open(ctx, sealed)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(opened, token) {
		t.Fatalf("Open returned %q, want %q", opened, token)
	}
}

// The header is what makes a future rotation possible. Without it there is no
// way to tell which key sealed a blob, and rotating would mean every vault
// entry becomes indistinguishable rubbish. The CMK gave rotation for free.
func TestSealedBlobNamesTheKeyThatSealedIt(t *testing.T) {
	ctx := context.Background()
	box, err := NewAESVaultBox(testVaultKey(0x11), "ssm:7")
	if err != nil {
		t.Fatalf("NewAESVaultBox: %v", err)
	}
	sealed, err := box.Seal(ctx, []byte("token"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	label, err := VaultBlobKeyLabel(sealed)
	if err != nil {
		t.Fatalf("VaultBlobKeyLabel: %v", err)
	}
	if label != "ssm:7" {
		t.Fatalf("blob names key %q, want %q", label, "ssm:7")
	}
}

// GCM fails catastrophically on nonce reuse rather than gracefully, so this is
// the assertion that matters most in this file.
func TestEverySealUsesAFreshNonce(t *testing.T) {
	ctx := context.Background()
	box, err := NewAESVaultBox(testVaultKey(0x11), "ssm:1")
	if err != nil {
		t.Fatalf("NewAESVaultBox: %v", err)
	}

	const runs = 200
	seen := make(map[string]bool, runs)
	for i := 0; i < runs; i++ {
		// The same plaintext every time: if the nonce were fixed, or derived
		// from the plaintext, every blob here would be byte-identical.
		sealed, err := box.Seal(ctx, []byte("the same refresh token every time"))
		if err != nil {
			t.Fatalf("Seal %d: %v", i, err)
		}
		nonce, err := vaultBlobNonce(sealed)
		if err != nil {
			t.Fatalf("nonce %d: %v", i, err)
		}
		if len(nonce) != vaultNonceSize {
			t.Fatalf("nonce is %d bytes, want %d", len(nonce), vaultNonceSize)
		}
		if seen[string(nonce)] {
			t.Fatalf("nonce reused after %d seals; AES-GCM loses confidentiality and integrity on reuse", i)
		}
		seen[string(nonce)] = true
	}
}

func TestOpenRefusesATamperedBlob(t *testing.T) {
	ctx := context.Background()
	box, err := NewAESVaultBox(testVaultKey(0x11), "ssm:1")
	if err != nil {
		t.Fatalf("NewAESVaultBox: %v", err)
	}
	sealed, err := box.Seal(ctx, []byte("token"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	for _, i := range []int{len(sealed) - 1, len(sealed) / 2} {
		tampered := append([]byte(nil), sealed...)
		tampered[i] ^= 0x01
		if _, err := box.Open(ctx, tampered); err == nil {
			t.Fatalf("Open accepted a blob with byte %d flipped", i)
		}
	}
}

func TestOpenRefusesABlobSealedUnderADifferentKey(t *testing.T) {
	ctx := context.Background()
	mine, err := NewAESVaultBox(testVaultKey(0x11), "ssm:1")
	if err != nil {
		t.Fatalf("NewAESVaultBox: %v", err)
	}
	theirs, err := NewAESVaultBox(testVaultKey(0x22), "ssm:1")
	if err != nil {
		t.Fatalf("NewAESVaultBox: %v", err)
	}
	sealed, err := theirs.Seal(ctx, []byte("token"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := mine.Open(ctx, sealed); err == nil {
		t.Fatal("Open accepted a blob sealed under another key")
	}
}

// A vault entry written by the retired KMS CMK cannot be opened by this box,
// and must say so distinguishably — the caller deletes it and asks the user to
// re-enrol rather than surfacing an opaque decrypt failure forever.
func TestOpenReportsABlobItCannotEverOpenAsUnreadable(t *testing.T) {
	ctx := context.Background()
	box, err := NewAESVaultBox(testVaultKey(0x11), "ssm:1")
	if err != nil {
		t.Fatalf("NewAESVaultBox: %v", err)
	}

	// A real KMS CiphertextBlob begins 0x01 0x02 0x03 0x00. It must not be
	// mistaken for a v1 blob of ours, which is why the header is a magic
	// string rather than a version byte.
	kmsBlob := append([]byte{0x01, 0x02, 0x03, 0x00}, bytes.Repeat([]byte{0xAB}, 180)...)

	for name, blob := range map[string][]byte{
		"kms ciphertext blob": kmsBlob,
		"empty":               {},
		"truncated header":    []byte("CV"),
		"unknown magic":       append([]byte("ZZZZ"), bytes.Repeat([]byte{0x00}, 40)...),
	} {
		_, err := box.Open(ctx, blob)
		if !errors.Is(err, ErrVaultUnreadable) {
			t.Errorf("%s: Open err = %v, want ErrVaultUnreadable", name, err)
		}
	}
}

// A blob sealed by a key version this box does not hold is unreadable rather
// than corrupt, and the message names the version so an operator can tell the
// two apart.
func TestOpenReportsAnUnknownKeyVersionAsUnreadableAndNamesIt(t *testing.T) {
	ctx := context.Background()
	old, err := NewAESVaultBox(testVaultKey(0x11), "ssm:1")
	if err != nil {
		t.Fatalf("NewAESVaultBox: %v", err)
	}
	current, err := NewAESVaultBox(testVaultKey(0x11), "ssm:2")
	if err != nil {
		t.Fatalf("NewAESVaultBox: %v", err)
	}
	sealed, err := old.Seal(ctx, []byte("token"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	_, err = current.Open(ctx, sealed)
	if !errors.Is(err, ErrVaultUnreadable) {
		t.Fatalf("Open err = %v, want ErrVaultUnreadable", err)
	}
	if !strings.Contains(err.Error(), "ssm:1") {
		t.Errorf("error %q does not name the key version that sealed the blob", err)
	}
}
