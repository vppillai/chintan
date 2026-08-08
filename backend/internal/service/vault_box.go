package service

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
)

// The refresh-token vault is sealed with AES-256-GCM under a key held as an SSM
// SecureString, not with a customer-managed KMS key.
//
// What the CMK bought was never encryption at rest — DynamoDB already encrypts
// the table with an AWS-owned key, for free. It bought SEPARATION: a principal
// that can read the table still cannot use a refresh token without a distinct
// kms:Decrypt. That separation is narrower than it looks, because the Lambda
// role holds both dynamodb:GetItem and the decrypt right, so compromising that
// role defeats it either way — but it still stands between the token and a
// chintanctl backup, a PITR restore, or someone reading the table in the
// console.
//
// A SecureString keeps exactly that property. It is encrypted under the
// AWS-managed aws/ssm key, which is free, and reading it needs ssm:GetParameter
// on one path plus kms:Decrypt on alias/aws/ssm — neither of which comes with
// dynamodb:GetItem. A customer-managed key is $1/month, which on an instance
// whose entire idle bill is that key is the whole bill.
//
// Format. A sealed blob is:
//
//	"CVK1" | uint8 len(label) | label | nonce(12) | AES-256-GCM ciphertext+tag
//
// The magic is four bytes rather than a single version byte on purpose. A KMS
// CiphertextBlob begins 0x01 0x02 0x03 0x00, so a bare version byte of 1 would
// make a CMK-era blob look like a v1 blob of ours and turn a clean "this
// predates the migration" into an authentication failure that reads like
// corruption.
//
// The label names the key version that sealed the blob, so a later rotation can
// still tell which key an old entry needs. The CMK gave that for free; losing
// it silently is how a rotation turns every vault entry into rubbish.
const (
	// VaultKeySize is the only accepted key length: AES-256.
	VaultKeySize = 32

	vaultNonceSize = 12 // GCM standard; crypto/cipher's NewGCM default.
	vaultMagic     = "CVK1"
)

// ErrVaultUnreadable means the blob cannot be opened by this key and never
// will be — it predates the migration off KMS, or it was sealed by a key
// version this process does not hold. It is distinct from an authentication
// failure so the caller can discard the entry and ask the user to re-enrol
// instead of failing biometric unlock forever with an opaque error.
var ErrVaultUnreadable = errors.New("service: refresh vault was sealed by a key this instance does not hold")

// AESVaultBox seals small secrets with AES-256-GCM.
type AESVaultBox struct {
	aead  cipher.AEAD
	label string
}

var _ SealBox = (*AESVaultBox)(nil)

// NewAESVaultBox builds a box over a 32-byte key. label names the key version
// and is recorded in every blob it seals; "ssm:<n>" is the SSM parameter
// version the key was read from.
func NewAESVaultBox(key []byte, label string) (*AESVaultBox, error) {
	if len(key) != VaultKeySize {
		// Not "at least": AES-192 and AES-128 are silently accepted by
		// aes.NewCipher, and a short key from a truncated parameter would
		// otherwise downgrade the cipher without anything saying so.
		return nil, fmt.Errorf("service: vault key is %d bytes, want exactly %d (AES-256)", len(key), VaultKeySize)
	}
	if label == "" {
		return nil, errors.New("service: vault key label is required, so a rotation can tell key versions apart")
	}
	if len(label) > 255 {
		return nil, fmt.Errorf("service: vault key label is %d bytes, want at most 255", len(label))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("service: vault cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("service: vault aead: %w", err)
	}
	return &AESVaultBox{aead: aead, label: label}, nil
}

// Seal encrypts plaintext under a freshly generated nonce.
func (b *AESVaultBox) Seal(_ context.Context, plaintext []byte) ([]byte, error) {
	nonce := make([]byte, vaultNonceSize)
	// crypto/rand every time. A counter, a timestamp, or anything derived from
	// the plaintext repeats a nonce eventually, and GCM does not degrade
	// gracefully on reuse: it leaks the XOR of the two plaintexts and, worse,
	// the authentication key.
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("service: vault nonce: %w", err)
	}

	header := make([]byte, 0, len(vaultMagic)+1+len(b.label))
	header = append(header, vaultMagic...)
	header = append(header, byte(len(b.label)))
	header = append(header, b.label...)

	out := make([]byte, 0, len(header)+len(nonce)+len(plaintext)+b.aead.Overhead())
	out = append(out, header...)
	out = append(out, nonce...)
	// The header is authenticated as additional data, so the recorded key
	// label cannot be rewritten without the tag failing.
	return b.aead.Seal(out, nonce, plaintext, header), nil
}

// Open decrypts a blob this box sealed.
func (b *AESVaultBox) Open(_ context.Context, blob []byte) ([]byte, error) {
	header, label, rest, err := splitVaultBlob(blob)
	if err != nil {
		return nil, err
	}
	if label != b.label {
		return nil, fmt.Errorf("%w: blob names key version %q, this instance holds %q", ErrVaultUnreadable, label, b.label)
	}
	nonce, ciphertext := rest[:vaultNonceSize], rest[vaultNonceSize:]
	plain, err := b.aead.Open(nil, nonce, ciphertext, header)
	if err != nil {
		// Deliberately not ErrVaultUnreadable: the key version matched, so
		// this is a tampered or corrupt blob, not one that predates the key.
		return nil, fmt.Errorf("service: vault authentication failed: %w", err)
	}
	return plain, nil
}

// VaultBlobKeyLabel reports which key version sealed a blob, without needing
// the key. It is what lets an operator tell "sealed by the retired CMK" from
// "sealed by a key version we no longer hold".
func VaultBlobKeyLabel(blob []byte) (string, error) {
	_, label, _, err := splitVaultBlob(blob)
	return label, err
}

// vaultBlobNonce returns the nonce a blob carries.
func vaultBlobNonce(blob []byte) ([]byte, error) {
	_, _, rest, err := splitVaultBlob(blob)
	if err != nil {
		return nil, err
	}
	return rest[:vaultNonceSize], nil
}

// splitVaultBlob parses the framing and returns the authenticated header, the
// key label, and everything after the header (nonce followed by ciphertext).
func splitVaultBlob(blob []byte) (header []byte, label string, rest []byte, err error) {
	if len(blob) < len(vaultMagic)+1 || string(blob[:len(vaultMagic)]) != vaultMagic {
		// Anything that is not ours — a KMS CiphertextBlob from before the
		// migration, an empty entry, a truncated one.
		return nil, "", nil, fmt.Errorf("%w: not a %s blob", ErrVaultUnreadable, vaultMagic)
	}
	labelLen := int(blob[len(vaultMagic)])
	headerLen := len(vaultMagic) + 1 + labelLen
	if len(blob) < headerLen+vaultNonceSize {
		return nil, "", nil, fmt.Errorf("%w: blob is %d bytes, too short for its own header", ErrVaultUnreadable, len(blob))
	}
	return blob[:headerLen], string(blob[headerLen-labelLen : headerLen]), blob[headerLen:], nil
}
