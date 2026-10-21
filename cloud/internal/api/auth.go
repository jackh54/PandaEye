package api

import (
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"time"
	"unicode"

	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/store"
)

// Dashboard roles, ordered (ARCHITECTURE 7: viewer reads; staff confirm/overturn/decide appeals;
// admin settings; owner users and API keys).
type role int

const (
	roleNone role = iota
	roleViewer
	roleStaff
	roleAdmin
	roleOwner
)

var roleNames = map[string]role{"viewer": roleViewer, "staff": roleStaff, "admin": roleAdmin, "owner": roleOwner}

func (r role) String() string {
	for n, v := range roleNames {
		if v == r {
			return n
		}
	}
	return "none"
}

// access is the authentication and authorisation rule of one route.
type access struct {
	public  bool   // no authentication at all (GET /status)
	service bool   // service token without assertion (auth/license, public/appeals)
	role    role   // minimum dashboard role
	scope   string // API key scope; "" refuses API keys
}

// Principal kinds.
const (
	kindPublic  = "public"
	kindService = "service"
	kindUser    = "user"
	kindAPIKey  = "apikey"
)

// principal is who makes a request. It is derived from authentication only, never from bodies.
type principal struct {
	kind     string
	name     string // audit principal: "user:<username>", "apikey:<id>", "service", "public"
	tenantID string
	role     role
	userID   string
	keyID    string
	clientIP netip.Addr
}

func (p *principal) limitKey() string {
	switch p.kind {
	case kindUser:
		return "user:" + p.tenantID + ":" + p.userID
	case kindAPIKey:
		return "apikey:" + p.keyID
	}
	return p.kind + ":" + ipKey(p.clientIP)
}

// Assertion validity (ARCHITECTURE 5.8): exp at most 60 s ahead, with 5 s of clock leeway in both
// directions.
const (
	assertionMaxTTL = 60 * time.Second
	assertionLeeway = 5 * time.Second
	maxAssertionLen = 4096
)

type assertionClaims struct {
	Tenant   string `json:"tenant"`
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	Method   string `json:"method"`
	Path     string `json:"path"`
	Exp      int64  `json:"exp"`
}

var errAssertion = errors.New("invalid assertion")

var b64 = base64.RawURLEncoding.Strict()

// verifyAssertion checks an X-PandaEye-Assertion: base64url(json) "." base64url(HMAC-SHA256(key,
// base64url(json))) without padding, under any of keys (PE_ASSERTION_KEY, then _NEXT); exp not
// expired and at most 60 s ahead (5 s leeway); method and path equal to the request's.
func verifyAssertion(keys [][]byte, value string, r *http.Request, now time.Time) (*assertionClaims, string) {
	if len(value) > maxAssertionLen {
		return nil, "assertion too long"
	}
	body, macText, ok := strings.Cut(value, ".")
	if !ok || body == "" || macText == "" || strings.Contains(macText, ".") {
		return nil, "malformed assertion"
	}
	mac, err := b64.DecodeString(macText)
	if err != nil || len(mac) != 32 {
		return nil, "malformed assertion"
	}
	valid := false
	for _, k := range keys {
		if len(k) > 0 && hmac.Equal(identity.HMACSHA256(k, []byte(body)), mac) {
			valid = true
		}
	}
	if !valid {
		return nil, "assertion signature mismatch"
	}
	raw, err := b64.DecodeString(body)
	if err != nil {
		return nil, "malformed assertion"
	}
	var c assertionClaims
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, "malformed assertion"
	}
	exp := time.Unix(c.Exp, 0)
	switch {
	case c.Exp == 0 || exp.Before(now.Add(-assertionLeeway)):
		return nil, "assertion expired"
	case exp.After(now.Add(assertionMaxTTL + assertionLeeway)):
		return nil, "assertion expires too far in the future"
	case c.Method != r.Method:
		return nil, "assertion method does not match the request"
	case c.Path != r.URL.EscapedPath() && c.Path != r.URL.Path:
		return nil, "assertion path does not match the request"
	case !identity.ValidTenantID(c.Tenant):
		return nil, "assertion tenant is invalid"
	case roleNames[c.Role] == roleNone:
		return nil, "assertion role is invalid"
	case !printable(c.Username, 64) || !printable(c.UserID, 128):
		return nil, "assertion user is invalid"
	}
	return &c, ""
}

func printable(s string, max int) bool {
	if s == "" || len(s) > max {
		return false
	}
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	scheme, tok, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	tok = strings.TrimSpace(tok)
	return tok, tok != ""
}

// serviceTokenOK compares the bearer token with PE_SERVICE_TOKEN and PE_SERVICE_TOKEN_NEXT in
// constant time. An empty PE_SERVICE_TOKEN disables dashboard access.
func (s *Service) serviceTokenOK(tok string) bool {
	cfg := s.d.Config
	ok := false
	if !cfg.ServiceToken.IsZero() && cfg.ServiceToken.Equal(tok) {
		ok = true
	}
	if !cfg.ServiceTokenNext.IsZero() && cfg.ServiceTokenNext.Equal(tok) {
		ok = true
	}
	return ok
}

func (s *Service) assertionKeys() [][]byte {
	cfg := s.d.Config
	var keys [][]byte
	if !cfg.AssertionKey.IsZero() {
		keys = append(keys, cfg.AssertionKey.Bytes())
	}
	if !cfg.AssertionKeyNext.IsZero() {
		keys = append(keys, cfg.AssertionKeyNext.Bytes())
	}
	return keys
}

// clientIP is the X-PandaEye-Client-IP header when trusted (a valid service token or assertion),
// else the address derived from the TCP peer and trusted proxies.
func (s *Service) clientIP(r *http.Request, trustHeader bool) netip.Addr {
	if trustHeader {
		if a, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("X-PandaEye-Client-IP"))); err == nil {
			return a.Unmap().WithZone("")
		}
	}
	a, _ := s.d.Config.TrustedProxies.RequestClientIP(r)
	return a
}

// authenticate applies acc to r. X-PandaEye-Tenant is never read: the tenant comes from the
// assertion or the API key.
func (s *Service) authenticate(r *http.Request, acc access, now time.Time) (*principal, *apiError) {
	if acc.public {
		return &principal{kind: kindPublic, name: kindPublic, clientIP: s.clientIP(r, false)}, nil
	}
	tok, ok := bearerToken(r)
	if !ok {
		return nil, errf(http.StatusUnauthorized, codeUnauthenticated, "missing bearer token")
	}
	if strings.HasPrefix(tok, "pe_") {
		id, secret, ok := identity.ParseAPIKey(tok)
		if !ok {
			return nil, errf(http.StatusUnauthorized, codeInvalidToken, "invalid API key")
		}
		k, err := s.d.Store.VerifyAPIKey(r.Context(), id, secret, now)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil, errf(http.StatusUnauthorized, codeInvalidToken, "invalid API key")
			}
			s.log.Warn("api key verification failed", "err", err)
			return nil, unavailable("cannot verify the API key right now")
		}
		if acc.service || acc.scope == "" {
			return nil, forbidden("this endpoint is not available to API keys")
		}
		if !k.HasScope(acc.scope) {
			return nil, errf(http.StatusForbidden, codeScopeRequired, "the API key lacks the %s scope", acc.scope)
		}
		return &principal{kind: kindAPIKey, name: "apikey:" + k.ID, tenantID: k.TenantID, keyID: k.ID,
			clientIP: s.clientIP(r, false)}, nil
	}
	if !s.serviceTokenOK(tok) {
		return nil, errf(http.StatusUnauthorized, codeInvalidToken, "invalid token")
	}
	if acc.service {
		return &principal{kind: kindService, name: kindService, clientIP: s.clientIP(r, true)}, nil
	}
	value := r.Header.Get("X-PandaEye-Assertion")
	if value == "" {
		return nil, errf(http.StatusUnauthorized, codeAssertionMissing, "missing user assertion")
	}
	c, why := verifyAssertion(s.assertionKeys(), value, r, now)
	if c == nil {
		return nil, errf(http.StatusUnauthorized, codeInvalidAssertion, "%s", why)
	}
	rl := roleNames[c.Role]
	if rl < acc.role {
		return nil, forbidden("requires the %s role", acc.role)
	}
	return &principal{kind: kindUser, name: "user:" + c.Username, tenantID: c.Tenant, role: rl, userID: c.UserID,
		clientIP: s.clientIP(r, true)}, nil
}

// isDashboardAdmin reports whether p is a dashboard user with role owner or admin (required for
// enforcement, autoban and autokick changes).
func (p *principal) isDashboardAdmin() bool { return p.kind == kindUser && p.role >= roleAdmin }
