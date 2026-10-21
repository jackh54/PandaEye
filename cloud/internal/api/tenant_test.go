package api

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/discord"
	"github.com/jackh54/PandaEye/cloud/internal/license"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	"github.com/jackh54/PandaEye/cloud/internal/stream"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

func TestPatchTenantWebhookAndConfigPush(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.connect(tenantA, srv1, stream.ConnectionInfo{Platform: pb.Platform_PLATFORM_PAPER})
	e.connect(tenantA, srv2, stream.ConnectionInfo{Platform: pb.Platform_PLATFORM_PAPER})
	e.connect(tenantB, srvB, stream.ConnectionInfo{Platform: pb.Platform_PLATFORM_PAPER})
	obA, obB := e.outbox(tenantA), e.outbox(tenantB)
	owner := as(tenantA, "owner")

	for _, bad := range []string{"http://discord.com/api/webhooks/123456789012345678/" + testWebhookToken,
		"https://discord.com.evil.tld/api/webhooks/123456789012345678/" + testWebhookToken, "https://example.com/hook"} {
		if r := e.do("PATCH", "/api/v1/tenant", map[string]any{"discord_webhook": bad}, owner); r.status != 400 || r.code(t) != codeInvalidWebhook {
			t.Errorf("%s: %d %s", bad, r.status, r.body)
		}
	}
	if e.webhooks.count() != 0 {
		t.Fatal("test message sent to an invalid webhook")
	}
	// A failing test message refuses the save.
	e.webhooks.err = errors.New("discord: webhook 123456789012345678: HTTP 404")
	if r := e.do("PATCH", "/api/v1/tenant", map[string]any{"discord_webhook": testWebhookURL}, owner); r.status != 400 || r.code(t) != codeWebhookTest {
		t.Fatalf("failed test message: %d %s", r.status, r.body)
	}
	e.webhooks.err = nil

	r := e.must(200, "PATCH", "/api/v1/tenant", map[string]any{"discord_webhook": testWebhookURL, "leniency": 2, "autokick": true}, owner)
	m := r.json(t)
	if m["discord_webhook"] != "https://discord.com/api/webhooks/123456789012345678/****" || m["leniency"] != float64(2) {
		t.Fatalf("tenant: %v", m)
	}
	if strings.Contains(string(r.body), testWebhookToken) {
		t.Fatal("webhook token returned")
	}
	tn, err := e.d.Store.GetTenant(ctx, tenantA)
	check(t, err)
	if bytes.Contains(tn.DiscordWebhookEnc, []byte(testWebhookToken)) || len(tn.DiscordWebhookEnc) == 0 {
		t.Fatal("webhook not stored encrypted")
	}
	if u, err := discord.Open([][]byte{[]byte(testOutboundKey)}, tenantA, tn.DiscordWebhookEnc); err != nil || u != testWebhookURL {
		t.Fatalf("sealed webhook: %q %v", u, err)
	}
	// KV PE_TENANTS carries the new settings; every connected server of the tenant got the config.
	st, _, err := e.d.Bus.GetTenant(ctx, tenantA)
	check(t, err)
	if st.Leniency != 2 || !st.Autokick || len(st.DiscordWebhookEnc) == 0 {
		t.Fatalf("PE_TENANTS: %+v", st)
	}
	servers := map[string]bool{}
	for _, o := range obA.all(500 * time.Millisecond) {
		if c := o.GetConfigUpdate(); c != nil && c.Leniency == 2 && c.Autokick {
			servers[o.ServerId] = true
		}
	}
	if !servers[srv1] || !servers[srv2] {
		t.Fatalf("config_update not pushed to every connected server: %v", servers)
	}
	if o := obB.next(200 * time.Millisecond); o != nil {
		t.Fatalf("another tenant's server got the config: %v", o)
	}
	// Saving the same webhook again sends no new test message; "" removes it.
	sent := e.webhooks.count()
	e.must(200, "PATCH", "/api/v1/tenant", map[string]any{"discord_webhook": testWebhookURL}, owner)
	if e.webhooks.count() != sent {
		t.Fatal("unchanged webhook tested again")
	}
	r = e.must(200, "PATCH", "/api/v1/tenant", map[string]any{"discord_webhook": ""}, owner)
	if r.json(t)["discord_webhook"] != "" {
		t.Fatalf("webhook not removed: %s", r.body)
	}
	if tn, _ := e.d.Store.GetTenant(ctx, tenantA); len(tn.DiscordWebhookEnc) != 0 {
		t.Fatal("webhook still stored")
	}
}

// TestSecretsNeverLogged runs requests carrying every secret at debug level and asserts that none of
// them reaches the log.
func TestSecretsNeverLogged(t *testing.T) {
	e := newEnv(t)
	e.seed()
	e.verifier.valid[testLicenseKey] = license.License{Valid: true, MaxServers: 1}
	_, apiKey := e.apiKey(tenantA, store.ScopeRead, store.ScopeSettingsWrite)
	e.must(200, "POST", "/api/v1/auth/license", map[string]string{"license_key": testLicenseKey}, service())
	e.do("POST", "/api/v1/auth/license", map[string]string{"license_key": testLicenseKey + "-wrong"}, service())
	e.must(200, "PATCH", "/api/v1/tenant", map[string]any{"discord_webhook": testWebhookURL}, as(tenantA, "owner"))
	e.webhooks.err = errors.New("discord: webhook 123456789012345678: HTTP 500")
	e.do("PATCH", "/api/v1/tenant", map[string]any{"discord_webhook": strings.Replace(testWebhookURL, "Tok3n", "Tok4n", 1)}, as(tenantA, "owner"))
	e.must(200, "GET", "/api/v1/tenant", nil, bearer(apiKey))
	e.do("GET", "/api/v1/tenant", nil, bearer(apiKey+"x"))
	e.do("GET", "/api/v1/tenant", nil, bearer(testServiceToken+"x"))
	e.do("GET", "/api/v1/tenant", nil, withClaims(testAssertionKeyNext, claimsFor(tenantA, "owner", "GET", "/api/v1/tenant", fixedNow.Add(time.Minute))))
	e.do("GET", "/api/v1/tenant", nil, bearer(testServiceTokenNext), header("X-PandaEye-Assertion", "garbage"))
	created := e.must(201, "POST", "/api/v1/apikeys", map[string]any{"name": "n"}, as(tenantA, "owner")).json(t)["key"].(string)
	e.must(202, "POST", "/api/v1/public/appeals", appealBody(vBan, appealCodeFor(vBan)), service())
	e.must(200, "GET", "/api/v1/verdicts/"+vBan, nil, as(tenantA, "viewer"))

	logs := e.logs.String()
	if !strings.Contains(logs, `"msg":"api request"`) {
		t.Fatalf("debug request log missing; got %q", logs)
	}
	for name, secret := range map[string]string{
		"service token": testServiceToken, "service token next": testServiceTokenNext, "assertion key": testAssertionKey,
		"assertion key next": testAssertionKeyNext, "outbound key": testOutboundKey, "pepper": testPepper,
		"licence key": testLicenseKey, "webhook token": testWebhookToken[:20], "api key": apiKey, "api key secret": apiKey[20:],
		"created api key": created, "appeal code": appealCodeFor(vBan),
	} {
		if strings.Contains(logs, secret) {
			t.Errorf("%s appears in the log", name)
		}
	}
	if strings.Contains(logs, "X-PandaEye-Assertion") || strings.Contains(logs, "Authorization") {
		t.Error("headers logged")
	}
}
