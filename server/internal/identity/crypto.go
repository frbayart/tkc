package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// Domain-separation prefixes for signed messages (protocol/device-auth.md).
const (
	proofDomain     = "tkc/v1/device-key-proof\x00"
	challengeDomain = "tkc/v1/auth-challenge\x00"
)

// NonceSize is the size in bytes of authentication nonces, enrollment codes
// and session tokens.
const NonceSize = 32

// ProofMessage is the message a device signs with its identity key to prove
// possession of that key and bind its encryption key to it.
func ProofMessage(encryptionPublicKey []byte) []byte {
	return append([]byte(proofDomain), encryptionPublicKey...)
}

// ChallengeMessage is the message a device signs to authenticate.
func ChallengeMessage(deviceID string, nonce []byte) ([]byte, error) {
	id, err := uuidBytes(deviceID)
	if err != nil {
		return nil, err
	}
	msg := append([]byte(challengeDomain), id...)
	return append(msg, nonce...), nil
}

func verify(pub, msg, sig []byte) bool {
	return len(pub) == ed25519.PublicKeySize && len(sig) == ed25519.SignatureSize &&
		ed25519.Verify(ed25519.PublicKey(pub), msg, sig)
}

func randomBytes() []byte {
	b := make([]byte, NonceSize)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("identity: crypto/rand failed: %v", err))
	}
	return b
}

// newSecret returns a random secret encoded for transport and its hash for
// storage.
func newSecret() (string, []byte) {
	b := randomBytes()
	return base64.RawURLEncoding.EncodeToString(b), hashBytes(b)
}

// hashSecret decodes and hashes a transported secret. Malformed input still
// yields a hash, which simply matches nothing.
func hashSecret(s string) []byte {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(b) != NonceSize {
		return hashBytes([]byte("invalid:" + s))
	}
	return hashBytes(b)
}

func hashBytes(b []byte) []byte {
	h := sha256.Sum256(b)
	return h[:]
}

// uuidBytes parses a canonical UUID string into its 16 bytes.
func uuidBytes(s string) ([]byte, error) {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return nil, fmt.Errorf("%w: malformed id", ErrInvalid)
	}
	b, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	if err != nil {
		return nil, fmt.Errorf("%w: malformed id", ErrInvalid)
	}
	return b, nil
}

func validID(s string) bool {
	_, err := uuidBytes(s)
	return err == nil
}
