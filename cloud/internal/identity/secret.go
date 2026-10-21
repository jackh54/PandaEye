package identity

import (
	"crypto/sha256"
	"crypto/subtle"
	"log/slog"
)

// Redacted is what a Secret prints instead of its value.
const Redacted = "[redacted]"

// Secret is a credential (token, key, DSN, licence key). Its String, GoString, LogValue and
// MarshalJSON methods never reveal the value; use Reveal or Bytes where the value is needed.
type Secret string

// Reveal returns the secret value.
func (s Secret) Reveal() string { return string(s) }

// Bytes returns the secret value as bytes.
func (s Secret) Bytes() []byte { return []byte(s) }

// Len returns the length of the secret in bytes.
func (s Secret) Len() int { return len(s) }

// IsZero reports whether the secret is empty.
func (s Secret) IsZero() bool { return s == "" }

// String implements fmt.Stringer without revealing the value.
func (s Secret) String() string {
	if s == "" {
		return ""
	}
	return Redacted
}

// GoString implements fmt.GoStringer without revealing the value.
func (s Secret) GoString() string { return `identity.Secret("` + s.String() + `")` }

// LogValue implements slog.LogValuer without revealing the value.
func (s Secret) LogValue() slog.Value { return slog.StringValue(s.String()) }

// MarshalJSON never reveals the value.
func (s Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + s.String() + `"`), nil }

// Equal compares two secrets in constant time (independent of where they differ and of their
// lengths).
func (s Secret) Equal(other string) bool { return EqualConstantTime(string(s), other) }

// EqualConstantTime compares a and b in time that depends on neither their contents nor their
// lengths (both are hashed first).
func EqualConstantTime(a, b string) bool {
	ha := sha256.Sum256([]byte(a))
	hb := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}
