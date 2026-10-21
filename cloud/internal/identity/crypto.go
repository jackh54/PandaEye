package identity

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"strings"
)

// HMACSHA256 returns HMAC-SHA256(key, parts[0] || parts[1] || ...).
func HMACSHA256(key []byte, parts ...[]byte) []byte {
	m := hmac.New(sha256.New, key)
	for _, p := range parts {
		m.Write(p)
	}
	return m.Sum(nil)
}

// DeriveKey derives a 32-byte purpose-bound key from a master key (for example the webhook
// encryption key from PE_OUTBOUND_KEY), so one secret never serves two purposes directly.
func DeriveKey(master []byte, purpose string) []byte {
	return HMACSHA256(master, []byte("pandaeye/derive/v1/"), []byte(purpose))
}

// NewServerSecret returns a new random server secret (256 bits, base64url without padding) for the
// first Welcome of a server_id. Only HashSecret(secret) is stored.
func NewServerSecret() Secret { return Secret(randomToken(32)) }

// HashSecret returns sha256(secret), the stored form of server secrets and API key secrets.
func HashSecret(secret string) []byte {
	h := sha256.Sum256([]byte(secret))
	return h[:]
}

// CheckSecret reports in constant time whether secret hashes to hash.
func CheckSecret(hash []byte, secret string) bool {
	h := HashSecret(secret)
	return len(hash) == len(h) && subtle.ConstantTimeCompare(hash, h) == 1
}

// LicenseKeyHMAC is the lookup key of a licence: HMAC-SHA256(PE_LICENSE_PEPPER, key). Raw licence keys
// are never stored.
func LicenseKeyHMAC(pepper []byte, licenseKey string) []byte {
	return HMACSHA256(pepper, []byte(licenseKey))
}

// SignOutbound returns the SignedOutbound mac: HMAC-SHA256(PE_OUTBOUND_KEY, payload).
func SignOutbound(key, payload []byte) []byte { return HMACSHA256(key, payload) }

// VerifyOutbound reports in constant time whether mac is the SignedOutbound mac of payload.
func VerifyOutbound(key, payload, mac []byte) bool {
	return hmac.Equal(SignOutbound(key, payload), mac)
}

var crockford = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)

// AppealCode returns the 10-character appeal code of a verdict (Crockford base32, 50 bits). It is
// derived from the verdict id with a key derived from PE_OUTBOUND_KEY, so re-processing a verdict
// (deterministic ids) reproduces the same code and codes cannot be guessed from verdict ids.
func AppealCode(outboundKey []byte, verdictID string) string {
	mac := HMACSHA256(DeriveKey(outboundKey, "appeal-code"), []byte(verdictID))
	return crockford.EncodeToString(mac[:7])[:10]
}

// EqualAppealCode compares a user-supplied appeal code in constant time, ignoring case and the
// Crockford look-alikes (O->0, I/L->1).
func EqualAppealCode(want, got string) bool {
	r := strings.NewReplacer("O", "0", "I", "1", "L", "1", "-", "", " ", "")
	return EqualConstantTime(want, r.Replace(strings.ToUpper(got)))
}

const (
	apiKeyPrefix    = "pe"
	apiKeyIDLen     = 16
	apiKeySecretLen = 40
)

// NewAPIKey returns a new API key: its id, its secret (store HashSecret(secret)) and the bearer
// token "pe_<id>_<secret>" shown to the user once.
func NewAPIKey() (id string, secret Secret, token Secret) {
	id = randomBase32(apiKeyIDLen)
	s := randomBase32(apiKeySecretLen)
	return id, Secret(s), Secret(apiKeyPrefix + "_" + id + "_" + s)
}

// ParseAPIKey splits a bearer token "pe_<id>_<secret>". ok is false for anything else.
func ParseAPIKey(token string) (id string, secret Secret, ok bool) {
	parts := strings.Split(token, "_")
	if len(parts) != 3 || parts[0] != apiKeyPrefix || len(parts[1]) != apiKeyIDLen || len(parts[2]) != apiKeySecretLen {
		return "", "", false
	}
	for _, p := range parts[1:] {
		if _, err := lowerBase32.DecodeString(p); err != nil {
			return "", "", false
		}
	}
	return parts[1], Secret(parts[2]), true
}

// ErrDecrypt is returned when a sealed value cannot be opened (wrong key, corrupted or truncated).
var ErrDecrypt = errors.New("identity: cannot decrypt")

const sealVersion = 1

// Seal encrypts plaintext with AES-256-GCM under key (32 bytes, e.g. DeriveKey(PE_OUTBOUND_KEY,
// "webhook")). aad binds the ciphertext to its context (e.g. the tenant id). Output:
// version(1) || nonce(12) || ciphertext+tag.
func Seal(key, plaintext, aad []byte) ([]byte, error) {
	aead, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 1+aead.NonceSize(), 1+aead.NonceSize()+len(plaintext)+aead.Overhead())
	out[0] = sealVersion
	if _, err := rand.Read(out[1:]); err != nil {
		return nil, err
	}
	return aead.Seal(out, out[1:], plaintext, aad), nil
}

// Open decrypts a value produced by Seal with the same key and aad.
func Open(key, sealed, aad []byte) ([]byte, error) {
	aead, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	n := aead.NonceSize()
	if len(sealed) < 1+n+aead.Overhead() || sealed[0] != sealVersion {
		return nil, ErrDecrypt
	}
	pt, err := aead.Open(nil, sealed[1:1+n], sealed[1+n:], aad)
	if err != nil {
		return nil, ErrDecrypt
	}
	return pt, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, errors.New("identity: AES-256-GCM needs a 32-byte key")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// RandomKey returns n random bytes (keys generated in `all` mode).
func RandomKey(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("identity: crypto/rand failed: " + err.Error())
	}
	return b
}

func randomToken(n int) string { return base64.RawURLEncoding.EncodeToString(RandomKey(n)) }
