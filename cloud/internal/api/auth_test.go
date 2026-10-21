package api

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/store"
)

func claimsFor(tenant, role, method, path string, exp time.Time) assertionClaims {
	return assertionClaims{Tenant: tenant, UserID: "u_1", Username: "alice", Role: role, Method: method, Path: path, Exp: exp.Unix()}
}

func TestServiceTokenAndAssertion(t *testing.T) {
	e := newEnv(t)
	const path = "/api/v1/tenant"
	exp := fixedNow.Add(45 * time.Second)
	cases := []struct {
		name   string
		opts   []reqOpt
		status int
		code   string
	}{
		{"no token", nil, 401, codeUnauthenticated},
		{"basic auth", []reqOpt{header("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("a:b")))}, 401, codeUnauthenticated},
		{"wrong service token", []reqOpt{bearer(testServiceToken + "x"), header("X-PandaEye-Assertion", sign(testAssertionKey, claimsFor(tenantA, "owner", "GET", path, exp)))}, 401, codeInvalidToken},
		{"service token without assertion", []reqOpt{service()}, 401, codeAssertionMissing},
		{"valid", []reqOpt{withClaims(testAssertionKey, claimsFor(tenantA, "viewer", "GET", path, exp))}, 200, ""},
		{"NEXT service token", []reqOpt{withClaims(testAssertionKey, claimsFor(tenantA, "viewer", "GET", path, exp)), bearer(testServiceTokenNext)}, 200, ""},
		{"NEXT assertion key", []reqOpt{withClaims(testAssertionKeyNext, claimsFor(tenantA, "viewer", "GET", path, exp))}, 200, ""},
		{"wrong assertion key", []reqOpt{withClaims("some-other-key-0123456789abcdef0123456", claimsFor(tenantA, "viewer", "GET", path, exp))}, 401, codeInvalidAssertion},
		{"expired beyond leeway", []reqOpt{withClaims(testAssertionKey, claimsFor(tenantA, "viewer", "GET", path, fixedNow.Add(-6*time.Second)))}, 401, codeInvalidAssertion},
		{"expired within leeway", []reqOpt{withClaims(testAssertionKey, claimsFor(tenantA, "viewer", "GET", path, fixedNow.Add(-4*time.Second)))}, 200, ""},
		{"exp 64 s ahead (leeway)", []reqOpt{withClaims(testAssertionKey, claimsFor(tenantA, "viewer", "GET", path, fixedNow.Add(64*time.Second)))}, 200, ""},
		{"exp too far ahead", []reqOpt{withClaims(testAssertionKey, claimsFor(tenantA, "viewer", "GET", path, fixedNow.Add(66*time.Second)))}, 401, codeInvalidAssertion},
		{"exp missing", []reqOpt{withClaims(testAssertionKey, assertionClaims{Tenant: tenantA, UserID: "u", Username: "a", Role: "viewer", Method: "GET", Path: path})}, 401, codeInvalidAssertion},
		{"wrong path", []reqOpt{withClaims(testAssertionKey, claimsFor(tenantA, "viewer", "GET", "/api/v1/servers", exp))}, 401, codeInvalidAssertion},
		{"path with query", []reqOpt{withClaims(testAssertionKey, claimsFor(tenantA, "viewer", "GET", path+"?x=1", exp))}, 401, codeInvalidAssertion},
		{"wrong method", []reqOpt{withClaims(testAssertionKey, claimsFor(tenantA, "viewer", "POST", path, exp))}, 401, codeInvalidAssertion},
		{"lower-case method", []reqOpt{withClaims(testAssertionKey, claimsFor(tenantA, "viewer", "get", path, exp))}, 401, codeInvalidAssertion},
		{"unknown role", []reqOpt{withClaims(testAssertionKey, claimsFor(tenantA, "root", "GET", path, exp))}, 401, codeInvalidAssertion},
		{"invalid tenant", []reqOpt{withClaims(testAssertionKey, claimsFor("t_x", "viewer", "GET", path, exp))}, 401, codeInvalidAssertion},
		{"garbage assertion", []reqOpt{service(), header("X-PandaEye-Assertion", "abc")}, 401, codeInvalidAssertion},
	}
	for _, c := range cases {
		r := e.do("GET", path, nil, c.opts...)
		if r.status != c.status {
			t.Errorf("%s: status %d, want %d: %s", c.name, r.status, c.status, r.body)
			continue
		}
		if c.code != "" && r.code(t) != c.code {
			t.Errorf("%s: code %q, want %q", c.name, r.code(t), c.code)
		}
	}

	// A tampered body (role raised) keeps the old MAC and fails; so does padding.
	good := sign(testAssertionKey, claimsFor(tenantA, "viewer", "GET", path, exp))
	body, mac, _ := strings.Cut(good, ".")
	forged := sign("x", claimsFor(tenantA, "owner", "GET", path, exp))
	fbody, _, _ := strings.Cut(forged, ".")
	for name, a := range map[string]string{"tampered": fbody + "." + mac, "padded mac": body + "." + mac + "=", "three parts": good + ".x"} {
		if r := e.do("GET", path, nil, service(), header("X-PandaEye-Assertion", a)); r.status != 401 {
			t.Errorf("%s assertion: status %d", name, r.status)
		}
	}
}

func TestRoles(t *testing.T) {
	e := newEnv(t)
	e.seed()
	cases := []struct {
		method, path string
		body         any
		min          string // lowest role allowed
	}{
		{"GET", "/api/v1/verdicts", nil, "viewer"},
		{"GET", "/api/v1/appeals", nil, "viewer"},
		{"POST", "/api/v1/verdicts/" + vAlert + "/confirm", map[string]any{}, "staff"},
		{"POST", "/api/v1/appeals/" + appeal1 + "/decide", map[string]any{"decision": "deny"}, "staff"},
		{"PATCH", "/api/v1/tenant", map[string]any{"leniency": 3}, "admin"},
		{"GET", "/api/v1/checks", nil, "admin"},
		{"GET", "/api/v1/audit", nil, "admin"},
		{"DELETE", "/api/v1/players/" + player2, nil, "admin"},
		{"GET", "/api/v1/apikeys", nil, "owner"},
		{"POST", "/api/v1/auth/rebind", map[string]any{"license_key": "k"}, "owner"},
	}
	order := []string{"viewer", "staff", "admin", "owner"}
	for _, c := range cases {
		allowed := false
		for _, role := range order {
			if role == c.min {
				allowed = true
			}
			r := e.do(c.method, c.path, c.body, as(tenantA, role))
			if !allowed && (r.status != 403 || r.code(t) != codeForbidden) {
				t.Errorf("%s %s as %s: status %d, want 403", c.method, c.path, role, r.status)
			}
			if allowed && (r.status == 401 || r.status == 403) {
				t.Errorf("%s %s as %s: refused %d: %s", c.method, c.path, role, r.status, r.body)
			}
		}
	}
	// Enforcement, autoban and autokick need an owner/admin assertion (admin passes, see above).
	for _, f := range []string{"enforcement", "autoban", "autokick"} {
		if r := e.do("PATCH", "/api/v1/tenant", map[string]any{f: true}, as(tenantA, "staff")); r.status != 403 {
			t.Errorf("staff changing %s: %d", f, r.status)
		}
		e.must(200, "PATCH", "/api/v1/tenant", map[string]any{f: true}, as(tenantA, "admin"))
	}
}

func TestAPIKeyScopes(t *testing.T) {
	e := newEnv(t)
	e.seed()
	_, read := e.apiKey(tenantA)
	_, vw := e.apiKey(tenantA, store.ScopeVerdictsWrite, store.ScopeRead)
	_, sw := e.apiKey(tenantA, store.ScopeSettingsWrite)
	_, erase := e.apiKey(tenantA, store.ScopeErase)
	revokedID, revoked := e.apiKey(tenantA, store.ScopeRead)
	check(t, e.d.Store.RevokeAPIKey(context.Background(), tenantA, revokedID, fixedNow))
	_, keyB := e.apiKey(tenantB, store.ScopeRead)

	cases := []struct {
		key, method, path string
		body              any
		status            int
		code              string
	}{
		{read, "GET", "/api/v1/verdicts", nil, 200, ""}, // no scopes = read
		{read, "GET", "/api/v1/tenant", nil, 200, ""},
		{read, "POST", "/api/v1/verdicts/" + vAlert + "/confirm", map[string]any{}, 403, codeScopeRequired},
		{vw, "POST", "/api/v1/verdicts/" + vAlert + "/confirm", map[string]any{}, 200, ""},
		{sw, "GET", "/api/v1/verdicts", nil, 403, codeScopeRequired},
		{sw, "PATCH", "/api/v1/tenant", map[string]any{"leniency": 2}, 200, ""},
		{sw, "PATCH", "/api/v1/tenant", map[string]any{"enforcement": true}, 403, codeForbidden},
		{sw, "PATCH", "/api/v1/tenant", map[string]any{"autoban": false}, 403, codeForbidden},
		{sw, "PATCH", "/api/v1/tenant", map[string]any{"autokick": true}, 403, codeForbidden},
		{sw, "PUT", "/api/v1/checks", map[string]any{"checks": []any{}}, 200, ""},
		{read, "DELETE", "/api/v1/players/" + player2, nil, 403, codeScopeRequired},
		{erase, "DELETE", "/api/v1/players/" + player2, nil, 200, ""},
		// Key management, rebind, audit, appeal decisions and watches are dashboard-only.
		{sw, "GET", "/api/v1/apikeys", nil, 403, codeForbidden},
		{sw, "POST", "/api/v1/apikeys", map[string]any{"name": "x"}, 403, codeForbidden},
		{vw, "POST", "/api/v1/auth/rebind", map[string]any{"license_key": "x"}, 403, codeForbidden},
		{sw, "GET", "/api/v1/audit", nil, 403, codeForbidden},
		{vw, "POST", "/api/v1/appeals/" + appeal1 + "/decide", map[string]any{"decision": "deny"}, 403, codeForbidden},
		{vw, "POST", "/api/v1/players/" + player1 + "/watch", map[string]any{"seconds": 60}, 403, codeForbidden},
		{read, "POST", "/api/v1/public/appeals", map[string]any{}, 403, codeForbidden},
		{read, "POST", "/api/v1/auth/license", map[string]any{"license_key": "x"}, 403, codeForbidden},
		{revoked, "GET", "/api/v1/verdicts", nil, 401, codeInvalidToken},
		{"pe_aaaaaaaaaaaaaaaa_" + strings.Repeat("a", 40), "GET", "/api/v1/verdicts", nil, 401, codeInvalidToken},
		{"pe_short", "GET", "/api/v1/verdicts", nil, 401, codeInvalidToken},
	}
	for _, c := range cases {
		r := e.do(c.method, c.path, c.body, bearer(c.key))
		if r.status != c.status {
			t.Errorf("%s %s: status %d, want %d: %s", c.method, c.path, r.status, c.status, r.body)
			continue
		}
		if c.code != "" && r.code(t) != c.code {
			t.Errorf("%s %s: code %q, want %q", c.method, c.path, r.code(t), c.code)
		}
	}

	// X-PandaEye-Tenant never selects the tenant: the key does.
	r := e.must(200, "GET", "/api/v1/tenant", nil, bearer(keyB), header("X-PandaEye-Tenant", tenantA))
	if r.json(t)["id"] != tenantB {
		t.Fatalf("X-PandaEye-Tenant honoured: %s", r.body)
	}
	r = e.must(200, "GET", "/api/v1/tenant", nil, as(tenantA, "viewer"), header("X-PandaEye-Tenant", tenantB))
	if r.json(t)["id"] != tenantA {
		t.Fatalf("X-PandaEye-Tenant honoured for the dashboard: %s", r.body)
	}
}

// TestCrossTenant404 requests tenant B's ids as tenant A on every id route.
func TestCrossTenant404(t *testing.T) {
	e := newEnv(t)
	e.seed()
	keyBID, _ := e.apiKey(tenantB, store.ScopeRead)
	for _, c := range []struct {
		method, path string
		body         any
		role         string
	}{
		{"GET", "/api/v1/verdicts/" + vB, nil, "viewer"},
		{"POST", "/api/v1/verdicts/" + vB + "/confirm", map[string]any{}, "staff"},
		{"POST", "/api/v1/verdicts/" + vB + "/overturn", map[string]any{}, "staff"},
		{"GET", "/api/v1/clips/" + clipB, nil, "viewer"},
		{"GET", "/api/v1/players/" + playerB, nil, "viewer"},
		{"POST", "/api/v1/players/" + playerB + "/watch", map[string]any{"seconds": 60}, "staff"},
		{"POST", "/api/v1/appeals/" + appealB + "/decide", map[string]any{"decision": "accept"}, "staff"},
		{"DELETE", "/api/v1/apikeys/" + keyBID, nil, "owner"},
		{"GET", "/api/v1/verdicts/no-such-verdict", nil, "viewer"},
	} {
		r := e.do(c.method, c.path, c.body, as(tenantA, c.role))
		if r.status != 404 || r.code(t) != codeNotFound {
			t.Errorf("%s %s as tenant A: status %d, want 404: %s", c.method, c.path, r.status, r.body)
		}
	}
	// Erasing tenant B's player id as tenant A touches only tenant A (nothing there) and never B.
	r := e.must(200, "DELETE", "/api/v1/players/"+playerB, nil, as(tenantA, "admin"))
	for k, n := range r.json(t)["counts"].(map[string]any) {
		if n.(float64) != 0 {
			t.Errorf("cross-tenant erasure deleted %v %s", n, k)
		}
	}
	ctx := context.Background()
	if _, err := e.d.Store.GetVerdict(ctx, tenantB, vB); err != nil {
		t.Fatalf("tenant B's verdict gone: %v", err)
	}
	if _, err := e.d.Store.GetPlayer(ctx, tenantB, playerB); err != nil {
		t.Fatalf("tenant B's player gone: %v", err)
	}
	if v, _ := e.d.Store.GetVerdict(ctx, tenantB, vB); v.Status != store.StatusOpen {
		t.Fatalf("tenant B's verdict changed: %s", v.Status)
	}
	// Lists never show the other tenant's rows.
	for _, p := range []string{"/api/v1/verdicts", "/api/v1/bans", "/api/v1/appeals", "/api/v1/players", "/api/v1/servers"} {
		if body := string(e.must(200, "GET", p, nil, as(tenantA, "viewer")).body); strings.Contains(body, tenantB) ||
			strings.Contains(body, vB) || strings.Contains(body, playerB) || strings.Contains(body, srvB) {
			t.Errorf("%s leaks tenant B", p)
		}
	}
}

func TestDisabledServiceTokenAndTrustedProxy(t *testing.T) {
	e := newEnv(t, "PE_SERVICE_TOKEN=", "PE_SERVICE_TOKEN_NEXT=")
	if r := e.do("GET", "/api/v1/tenant", nil, as(tenantA, "owner")); r.status != 401 {
		t.Fatalf("empty PE_SERVICE_TOKEN must disable dashboard access: %d", r.status)
	}
	if r := e.do("POST", "/api/v1/auth/license", map[string]string{"license_key": "x"}, bearer("")); r.status != 401 {
		t.Fatalf("empty bearer: %d", r.status)
	}

	p := newEnv(t, "PE_REQUIRE_TRUSTED_PROXY=true", "PE_TRUSTED_PROXIES=203.0.113.0/24")
	if r := p.do("GET", "/status", nil); r.status != http.StatusForbidden || r.code(t) != codeUntrustedPeer {
		t.Fatalf("peer outside PE_TRUSTED_PROXIES: %d %s", r.status, r.body)
	}
	q := newEnv(t, "PE_REQUIRE_TRUSTED_PROXY=true", "PE_TRUSTED_PROXIES=127.0.0.1/32,::1/128")
	q.must(200, "GET", "/status", nil)
}

func TestErrorsAreJSON(t *testing.T) {
	e := newEnv(t)
	for _, c := range []struct {
		method, path string
		body         any
		status       int
		code         string
	}{
		{"GET", "/api/v1/nope", nil, 404, codeNotFound},
		{"DELETE", "/api/v1/tenant", nil, 405, codeMethodNotAllowed},
		{"PATCH", "/api/v1/tenant", "{not json", 400, codeBadRequest},
		{"PATCH", "/api/v1/tenant", map[string]any{"plan": "pro"}, 400, codeBadRequest},
		{"PATCH", "/api/v1/tenant", map[string]any{"leniency": 9}, 400, codeBadRequest},
		{"PATCH", "/api/v1/tenant", map[string]any{"discord_min_action": "unban"}, 400, codeBadRequest},
		{"PATCH", "/api/v1/tenant", strings.Repeat(" ", maxBody+10) + "{}", 413, codeTooLarge},
	} {
		r := e.do(c.method, c.path, c.body, as(tenantA, "owner"))
		if r.status != c.status || r.code(t) != c.code || !strings.HasPrefix(r.header.Get("Content-Type"), "application/json") {
			t.Errorf("%s %s: %d %s", c.method, c.path, r.status, r.body)
		}
	}
}

func TestRateLimit(t *testing.T) {
	e := newEnv(t)
	_, key := e.apiKey(tenantA, store.ScopeRead)
	for i := 0; i < requestsPerKeyPerMinute; i++ {
		if ok, _ := e.s.keyLimits.allow("apikey:"+strings.Split(key, "_")[1], fixedNow); !ok {
			t.Fatalf("limited after %d", i)
		}
	}
	r := e.do("GET", "/api/v1/verdicts", nil, bearer(key))
	if r.status != 429 || r.code(t) != codeRateLimited || r.header.Get("Retry-After") == "" {
		t.Fatalf("601st request in a minute: %d %v", r.status, r.header)
	}
	// Another key is unaffected.
	_, other := e.apiKey(tenantA, store.ScopeRead)
	e.must(200, "GET", "/api/v1/verdicts", nil, bearer(other))
}
