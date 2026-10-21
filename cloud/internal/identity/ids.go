package identity

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"strings"
	"unicode"

	"github.com/google/uuid"
	"golang.org/x/text/unicode/norm"
)

// UUID is a 16-byte UUID, big-endian (most significant 64 bits first), as carried on the wire.
type UUID [16]byte

// ErrInvalidUUID is returned for malformed UUIDs.
var ErrInvalidUUID = errors.New("identity: invalid uuid")

// NewUUID returns a random (version 4) UUID.
func NewUUID() UUID { return UUID(uuid.New()) }

// String returns the canonical lowercase form (8-4-4-4-12).
func (u UUID) String() string { return uuid.UUID(u).String() }

// IsZero reports whether u is the nil UUID.
func (u UUID) IsZero() bool { return u == UUID{} }

// Bytes returns a copy of the 16 bytes.
func (u UUID) Bytes() []byte { b := u; return b[:] }

// UUIDFromBytes converts the wire form (exactly 16 bytes).
func UUIDFromBytes(b []byte) (UUID, error) {
	var u UUID
	if len(b) != len(u) {
		return u, ErrInvalidUUID
	}
	copy(u[:], b)
	return u, nil
}

// ParseCanonicalUUID parses the canonical lowercase 8-4-4-4-12 form only. It is used for every
// identifier that ends up in NATS subjects or KV keys.
func ParseCanonicalUUID(s string) (UUID, error) {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return UUID{}, ErrInvalidUUID
	}
	var digits [32]byte
	n := 0
	for i := 0; i < len(s); i++ {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return UUID{}, ErrInvalidUUID
		}
		digits[n] = c
		n++
	}
	var u UUID
	if _, err := hex.Decode(u[:], digits[:]); err != nil {
		return UUID{}, ErrInvalidUUID
	}
	return u, nil
}

// ParseUUID parses a UUID in canonical form, any letter case, with or without hyphens. It is for
// human input (API paths); results are always handled in canonical form afterwards.
func ParseUUID(s string) (UUID, error) {
	s = strings.ToLower(s)
	if len(s) == 32 {
		s = s[0:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
	}
	return ParseCanonicalUUID(s)
}

// ValidServerID reports whether s is a canonical lowercase UUID other than the nil UUID, the only
// accepted form of Hello.server_id.
func ValidServerID(s string) bool {
	u, err := ParseCanonicalUUID(s)
	return err == nil && !u.IsZero()
}

// ServerIDBytes returns the 16 bytes of a valid server id.
func ServerIDBytes(serverID string) (UUID, error) {
	if !ValidServerID(serverID) {
		return UUID{}, ErrInvalidUUID
	}
	return ParseCanonicalUUID(serverID)
}

const tenantIDLen = 2 + 20

var lowerBase32 = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// randomBase32 returns n random characters of the lowercase RFC 4648 base32 alphabet.
func randomBase32(n int) string {
	b := make([]byte, (n*5+7)/8)
	if _, err := rand.Read(b); err != nil {
		panic("identity: crypto/rand failed: " + err.Error())
	}
	return lowerBase32.EncodeToString(b)[:n]
}

// NewTenantID returns a random tenant id: "t_" + 20 lowercase base32 characters (100 bits).
func NewTenantID() string { return "t_" + randomBase32(20) }

// ValidTenantID reports whether s has the tenant id format. Tenant ids are safe NATS subject tokens
// and KV key tokens.
func ValidTenantID(s string) bool {
	if len(s) != tenantIDLen || s[:2] != "t_" {
		return false
	}
	for i := 2; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '2' || c > '7') {
			return false
		}
	}
	return true
}

const maxSlugLen = 40

// Slug derives a URL-safe slug from a display name: lowercase ASCII letters, digits and single
// hyphens, at most 40 characters. Accents are folded ("Café" -> "cafe"); an empty result becomes
// "tenant".
func Slug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range norm.NFKD.String(name) {
		switch {
		case unicode.Is(unicode.Mn, r):
			continue
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case r >= 'A' && r <= 'Z':
			b.WriteRune(unicode.ToLower(r))
			dash = false
		default:
			if b.Len() > 0 && !dash {
				b.WriteByte('-')
				dash = true
			}
		}
		if b.Len() >= maxSlugLen {
			break
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > maxSlugLen {
		s = strings.TrimRight(s[:maxSlugLen], "-")
	}
	if s == "" {
		return "tenant"
	}
	return s
}
