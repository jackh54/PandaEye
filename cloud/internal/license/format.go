package license

import (
	"encoding/hex"

	"github.com/jackh54/PandaEye/cloud/internal/identity"
)

// MaxKeyLen bounds licence keys (Hello strings are truncated to 256 bytes anyway).
const MaxKeyLen = 256

// ValidFormat reports whether key can be a licence key at all: 1..256 printable ASCII characters
// without spaces. Keys failing it are invalid without asking any verifier.
func ValidFormat(key string) bool {
	if len(key) == 0 || len(key) > MaxKeyLen {
		return false
	}
	for i := 0; i < len(key); i++ {
		if c := key[i]; c <= ' ' || c > '~' {
			return false
		}
	}
	return true
}

// Fingerprint returns a short, non-reversible identifier of key for logs: the first 8 hex
// characters of HMAC-SHA256(pepper, key). It never reveals the key.
func Fingerprint(pepper []byte, key string) string {
	return hex.EncodeToString(identity.LicenseKeyHMAC(pepper, key)[:4])
}
