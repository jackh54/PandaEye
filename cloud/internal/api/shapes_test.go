package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/license"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	"github.com/jackh54/PandaEye/cloud/internal/stream"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// TestReadShapes checks every read endpoint against docs/API.md: exact key sets and golden values.
func TestReadShapes(t *testing.T) {
	e := newEnv(t)
	e.seed()
	e.connect(tenantA, srv1, stream.ConnectionInfo{OnlinePlayers: 12, NetworkID: "net-a", Platform: pb.Platform_PLATFORM_PAPER,
		ShadowMode: true, LastFrameUnixMs: fixedNow.UnixMilli()})
	viewer := as(tenantA, "viewer")
	admin := as(tenantA, "admin")

	t.Run("tenant", func(t *testing.T) {
		r := e.must(200, "GET", "/api/v1/tenant", nil, viewer)
		keys(t, "tenant", r.json(t), tenantKeys)
		golden(t, "tenant_get", r.body)
	})
	t.Run("servers", func(t *testing.T) {
		r := e.must(200, "GET", "/api/v1/servers", nil, viewer)
		m := r.json(t)
		keys(t, "servers", m, []string{"servers", "next_cursor"})
		for _, s := range m["servers"].([]any) {
			keys(t, "server", s, []string{"id", "name", "network_id", "platform", "platform_version", "plugin_version",
				"minecraft_version", "online", "online_players", "last_seen_at", "shadow_mode"})
		}
		golden(t, "servers", r.body)
	})
	t.Run("verdicts", func(t *testing.T) {
		r := e.must(200, "GET", "/api/v1/verdicts", nil, viewer)
		m := r.json(t)
		keys(t, "verdict list", m, []string{"verdicts", "next_cursor"})
		vs := m["verdicts"].([]any)
		if len(vs) != 4 {
			t.Fatalf("want 4 verdicts of tenant A, got %d", len(vs))
		}
		for _, v := range vs {
			keys(t, "verdict", v, verdictKeys, "server_name")
		}
		golden(t, "verdicts_list", r.body)

		for q, want := range map[string]int{
			"?action=ban": 2, "?shadow=true": 1, "?shadow=false": 3, "?player=" + player2: 2, "?server=" + srv2: 2,
			"?status=open": 4, "?since=" + fixedNow.Add(-150*time.Minute).Format(time.RFC3339): 1,
			"?until=" + fixedNow.Add(-150*time.Minute).Format(time.RFC3339): 3, "?action=BAN&shadow=false": 1,
		} {
			got := len(e.must(200, "GET", "/api/v1/verdicts"+q, nil, viewer).json(t)["verdicts"].([]any))
			if got != want {
				t.Errorf("filter %s: %d verdicts, want %d", q, got, want)
			}
		}
		// Cursor pagination walks every verdict exactly once.
		seen := map[string]bool{}
		cursor := ""
		for i := 0; i < 10; i++ {
			m := e.must(200, "GET", "/api/v1/verdicts?limit=1&cursor="+cursor, nil, viewer).json(t)
			for _, v := range m["verdicts"].([]any) {
				seen[v.(map[string]any)["id"].(string)] = true
			}
			if cursor = m["next_cursor"].(string); cursor == "" {
				break
			}
		}
		if len(seen) != 4 {
			t.Fatalf("pagination saw %d verdicts", len(seen))
		}
		for _, bad := range []string{"?action=nuke", "?status=x", "?shadow=maybe", "?player=x", "?since=yesterday", "?limit=-1", "?cursor=***"} {
			if r := e.do("GET", "/api/v1/verdicts"+bad, nil, viewer); r.status != 400 {
				t.Errorf("%s: status %d", bad, r.status)
			}
		}
	})
	t.Run("verdict detail", func(t *testing.T) {
		r := e.must(200, "GET", "/api/v1/verdicts/"+vBan, nil, viewer)
		m := r.json(t)
		keys(t, "verdict detail", m, append(append([]string{}, verdictKeys...), "evidence"), "server_name")
		keys(t, "evidence", m["evidence"], []string{"score", "confidence", "leniency", "thresholds", "families", "checks", "signals", "session"})
		golden(t, "verdict_detail", r.body)
	})
	t.Run("clip", func(t *testing.T) {
		r := e.must(200, "GET", "/api/v1/clips/"+clip1, nil, viewer)
		m := r.json(t)
		keys(t, "clip", m, []string{"player", "start_us", "end_us", "events"}, "player_name")
		for _, ev := range m["events"].([]any) {
			em := ev.(map[string]any)
			for _, k := range []string{"player", "time_us", "tick"} {
				if _, ok := em[k]; !ok {
					t.Errorf("event without %s: %v", k, em)
				}
			}
		}
		if att := m["events"].([]any)[2].(map[string]any)["attack"].(map[string]any); att["target_player"] != player2 {
			t.Errorf("target_player not a canonical UUID: %v", att["target_player"])
		}
		golden(t, "clip", r.body)
	})
	t.Run("players", func(t *testing.T) {
		r := e.must(200, "GET", "/api/v1/players", nil, viewer)
		m := r.json(t)
		keys(t, "players", m, []string{"players", "next_cursor"})
		for _, p := range m["players"].([]any) {
			keys(t, "player", p, playerKeys)
		}
		golden(t, "players_list", r.body)
		if n := len(e.must(200, "GET", "/api/v1/players?name=steve_o", nil, viewer).json(t)["players"].([]any)); n != 1 {
			t.Errorf("name prefix search (old name, case-insensitive): %d", n)
		}
		if n := len(e.must(200, "GET", "/api/v1/players?name=bob", nil, viewer).json(t)["players"].([]any)); n != 0 {
			t.Errorf("another tenant's player found by name: %d", n)
		}
	})
	t.Run("player detail", func(t *testing.T) {
		r := e.must(200, "GET", "/api/v1/players/"+strings.ToUpper(player1), nil, viewer)
		m := r.json(t)
		keys(t, "player detail", m, append(append([]string{}, playerKeys...), "names", "verdicts", "bans"), "brand", "client_version", "reputation")
		for _, b := range m["bans"].([]any) {
			keys(t, "ban", b, banKeys, "player_name")
		}
		golden(t, "player_detail", r.body)
	})
	t.Run("bans", func(t *testing.T) {
		r := e.must(200, "GET", "/api/v1/bans?active=true", nil, viewer)
		m := r.json(t)
		keys(t, "bans", m, []string{"bans", "next_cursor"})
		for _, b := range m["bans"].([]any) {
			keys(t, "ban", b, banKeys, "player_name")
		}
		golden(t, "bans", r.body)
		if n := len(e.must(200, "GET", "/api/v1/bans?active=false", nil, viewer).json(t)["bans"].([]any)); n != 0 {
			t.Errorf("inactive bans: %d", n)
		}
	})
	t.Run("appeals", func(t *testing.T) {
		r := e.must(200, "GET", "/api/v1/appeals?status=open", nil, viewer)
		m := r.json(t)
		keys(t, "appeals", m, []string{"appeals", "next_cursor"})
		for _, a := range m["appeals"].([]any) {
			keys(t, "appeal", a, appealKeys, "player_name")
		}
		golden(t, "appeals", r.body)
	})
	t.Run("stats overview", func(t *testing.T) {
		r := e.must(200, "GET", "/api/v1/stats/overview", nil, viewer)
		m := r.json(t)
		keys(t, "overview", m, []string{"last_24h", "last_7d"})
		keys(t, "last_24h", m["last_24h"], []string{"by_action", "by_category", "shadow"})
		golden(t, "stats_overview", r.body)
	})
	t.Run("stats latency", func(t *testing.T) {
		r := e.must(200, "GET", "/api/v1/stats/latency", nil, viewer)
		m := r.json(t)
		keys(t, "latency", m, []string{"last_1h", "last_24h", "slo"})
		keys(t, "last_1h", m["last_1h"], []string{"p50_us", "p95_us", "p99_us", "count"})
		keys(t, "slo", m["slo"], []string{"target_p95_ms", "met"})
		golden(t, "stats_latency", r.body)
	})
	t.Run("usage", func(t *testing.T) {
		r := e.must(200, "GET", "/api/v1/usage", nil, viewer)
		keys(t, "usage", r.json(t), []string{"plan", "free_tier_limit", "over_limit_since", "days"})
		golden(t, "usage", r.body)
	})
	t.Run("checks", func(t *testing.T) {
		r := e.must(200, "GET", "/api/v1/checks", nil, admin)
		m := r.json(t)
		keys(t, "checks", m, []string{"checks"})
		for _, c := range m["checks"].([]any) {
			keys(t, "check", c, []string{"id", "enabled", "overrides"})
		}
		golden(t, "checks_get", r.body)
	})
	t.Run("status", func(t *testing.T) {
		var h store.ProbeHistogram
		for _, us := range []int64{30_000, 30_000, 45_000, 250_000} {
			h.Observe(us)
		}
		check(t, e.d.Store.RecordProbeStats(context.Background(), tenantB, srvB, store.ProbeSourcePlugin, time.Now(), h, 250_000))
		r := e.must(200, "GET", "/status", nil)
		m := r.json(t)
		for _, k := range []string{"region", "version", "uptime_s", "connected_servers", "probe_rtt", "slo"} {
			if _, ok := m[k]; !ok {
				t.Errorf("status without %s", k)
			}
		}
		for _, k := range []string{"p50_us", "p95_us", "p99_us"} {
			if _, ok := m["probe_rtt"].(map[string]any)[k]; !ok {
				t.Errorf("probe_rtt without %s", k)
			}
		}
		for _, k := range []string{"target_p95_ms", "met"} {
			if _, ok := m["slo"].(map[string]any)[k]; !ok {
				t.Errorf("slo without %s", k)
			}
		}
		golden(t, "status", r.body, "uptime_s", "generated_at")
	})
	t.Run("export labels", func(t *testing.T) {
		ctx := context.Background()
		check(t, e.d.Store.InsertLabel(ctx, &store.Label{ID: "1abe1000-0000-4000-8000-000000000001", TenantID: tenantA, PlayerUUID: player1,
			VerdictID: vBan, Label: store.LabelCheat, Source: store.SourceConfirmed, WindowStart: fixedNow.Add(-130 * time.Minute),
			WindowEnd: fixedNow.Add(-2 * time.Hour), CreatedAt: fixedNow.Add(-30 * time.Minute)}))
		check(t, e.d.Store.InsertLabel(ctx, &store.Label{ID: "1abe1000-0000-4000-8000-000000000002", TenantID: tenantA, PlayerUUID: player2,
			Label: store.LabelLegit, Source: store.SourceManual, WindowStart: fixedNow.Add(-5 * time.Hour),
			WindowEnd: fixedNow.Add(-4 * time.Hour), CreatedAt: fixedNow.Add(-20 * time.Minute)}))
		_, key := e.apiKey(tenantA, store.ScopeRead)
		r := e.must(200, "GET", "/api/v1/export/labels?since="+fixedNow.Add(-24*time.Hour).Format(time.RFC3339), nil, bearer(key))
		if ct := r.header.Get("Content-Type"); !strings.HasPrefix(ct, "application/x-ndjson") {
			t.Fatalf("content type %q", ct)
		}
		var lines []any
		sc := bufio.NewScanner(bytes.NewReader(r.body))
		for sc.Scan() {
			var v map[string]any
			check(t, json.Unmarshal(sc.Bytes(), &v))
			for _, k := range []string{"tenant_id", "player_uuid", "label", "window_start", "window_end", "source", "verdict_id", "created_at"} {
				if _, ok := v[k]; !ok {
					t.Errorf("label line without %s (ml/FEATURES.md 7): %v", k, v)
				}
			}
			lines = append(lines, v)
		}
		if len(lines) != 2 {
			t.Fatalf("want 2 labels, got %d", len(lines))
		}
		b, _ := json.Marshal(lines)
		golden(t, "export_labels", b)
		// Paging: limit=1 ends with a {"next_cursor"} line, which the ML client follows.
		page := e.must(200, "GET", "/api/v1/export/labels?limit=1", nil, bearer(key))
		ls := strings.Split(strings.TrimSpace(string(page.body)), "\n")
		var last map[string]any
		check(t, json.Unmarshal([]byte(ls[len(ls)-1]), &last))
		if len(ls) != 2 || len(last) != 1 || last["next_cursor"] == "" {
			t.Fatalf("paging lines: %q", ls)
		}
		// An erased player's labels are excluded.
		e.must(200, "DELETE", "/api/v1/players/"+player2, nil, admin)
		all := e.must(200, "GET", "/api/v1/export/labels", nil, bearer(key))
		if strings.Contains(string(all.body), player2) {
			t.Fatal("erased subject exported")
		}
	})
}

// TestWriteShapes checks the mutating endpoints' responses against docs/API.md.
func TestWriteShapes(t *testing.T) {
	e := newEnv(t)
	e.seed()
	owner := as(tenantA, "owner")
	staff := as(tenantA, "staff")

	t.Run("auth license", func(t *testing.T) {
		e.verifier.set(testLicenseKey, license.License{Valid: true, MaxServers: 5, Plan: "pro"}, nil)
		r := e.must(200, "POST", "/api/v1/auth/license", map[string]string{"license_key": testLicenseKey}, service())
		m := r.json(t)
		keys(t, "auth/license", m, []string{"tenant_id", "slug", "created"})
		if m["created"] != true {
			t.Fatal("first use must create the tenant")
		}
		golden(t, "auth_license", r.body, "tenant_id", "slug")
		again := e.must(200, "POST", "/api/v1/auth/license", map[string]string{"license_key": testLicenseKey}, service()).json(t)
		if again["created"] != false || again["tenant_id"] != m["tenant_id"] {
			t.Fatalf("second use: %v", again)
		}
		tn, err := e.d.Store.GetTenant(context.Background(), m["tenant_id"].(string))
		check(t, err)
		if tn.Plan != "pro" || tn.MaxServers != 5 || tn.LicenseValidUntil.Sub(tn.LicenseVerifiedAt) != 24*time.Hour {
			t.Fatalf("licence check not recorded: %+v", tn)
		}
		if _, _, err := e.d.Bus.GetTenant(context.Background(), tn.ID); err != nil {
			t.Fatalf("new tenant not in PE_TENANTS: %v", err)
		}
		if r := e.do("POST", "/api/v1/auth/license", map[string]string{"license_key": "nope"}, service()); r.status != 401 || r.code(t) != codeInvalidLicense {
			t.Fatalf("invalid licence: %d %s", r.status, r.body)
		}
		e.verifier.set("", license.License{}, errors.New("verifier unreachable"))
		if r := e.do("POST", "/api/v1/auth/license", map[string]string{"license_key": testLicenseKey}, service()); r.status != 503 {
			t.Fatalf("verifier down must be 503 (retry), got %d", r.status)
		}
		e.verifier.set("", license.License{}, nil)
	})
	t.Run("auth rebind", func(t *testing.T) {
		e.verifier.set("NEW-KEY", license.License{Valid: true, MaxServers: 2}, nil)
		r := e.must(200, "POST", "/api/v1/auth/rebind", map[string]any{"license_key": "NEW-KEY", "revoke_others": true}, owner)
		keys(t, "rebind", r.json(t), []string{"tenant_id", "slug", "revoked"})
		golden(t, "auth_rebind", r.body)
		e.must(403, "POST", "/api/v1/auth/rebind", map[string]any{"license_key": "NEW-KEY"}, as(tenantA, "admin"))
		if r := e.do("POST", "/api/v1/auth/rebind", map[string]any{"license_key": testLicenseKey}, owner); r.status != 409 {
			t.Fatalf("binding another tenant's key: %d %s", r.status, r.body)
		}
	})
	t.Run("tenant patch", func(t *testing.T) {
		r := e.must(200, "PATCH", "/api/v1/tenant", map[string]any{"leniency": 3, "enforcement": true, "discord_min_action": "kick",
			"discord_webhook": testWebhookURL}, owner)
		keys(t, "tenant", r.json(t), tenantKeys)
		golden(t, "tenant_patch", r.body)
	})
	t.Run("confirm", func(t *testing.T) {
		r := e.must(200, "POST", "/api/v1/verdicts/"+vAlert+"/confirm", map[string]string{"note": "seen it"}, staff)
		keys(t, "verdict", r.json(t), verdictKeys, "server_name")
		golden(t, "verdict_confirm", r.body)
	})
	t.Run("overturn", func(t *testing.T) {
		r := e.must(200, "POST", "/api/v1/verdicts/"+vShadow+"/overturn", map[string]string{}, staff)
		keys(t, "verdict", r.json(t), verdictKeys, "server_name")
		golden(t, "verdict_overturn", r.body)
	})
	t.Run("watch", func(t *testing.T) {
		e.connect(tenantA, srv1, stream.ConnectionInfo{Platform: pb.Platform_PLATFORM_PAPER})
		ob := e.outbox(tenantA)
		r := e.must(200, "POST", "/api/v1/players/"+player1+"/watch", map[string]int{"seconds": 120}, staff)
		golden(t, "player_watch", r.body)
		out := ob.next(2 * time.Second)
		if out == nil || out.ServerId != srv1 || out.GetWatch().GetSeconds() != 120 || !bytes.Equal(out.GetWatch().Player, uuidBytes(player1)) {
			t.Fatalf("watch request not sent to the player's server: %v", out)
		}
		e.must(400, "POST", "/api/v1/players/"+player1+"/watch", map[string]int{"seconds": 30}, staff)
		e.must(400, "POST", "/api/v1/players/"+player1+"/watch", map[string]int{"seconds": 301}, staff)
		if r := e.do("POST", "/api/v1/players/"+player2+"/watch", map[string]int{"seconds": 60}, staff); r.status != 409 || r.code(t) != codeNotConnected {
			t.Fatalf("offline server: %d %s", r.status, r.body)
		}
	})
	t.Run("appeal decide", func(t *testing.T) {
		r := e.must(200, "POST", "/api/v1/appeals/"+appeal1+"/decide", map[string]string{"decision": "deny", "note": "evidence is clear"}, staff)
		keys(t, "appeal", r.json(t), appealKeys, "player_name")
		golden(t, "appeal_decide", r.body)
		if r := e.do("POST", "/api/v1/appeals/"+appeal1+"/decide", map[string]string{"decision": "accept"}, staff); r.status != 409 {
			t.Fatalf("deciding twice: %d", r.status)
		}
	})
	t.Run("public appeal", func(t *testing.T) {
		r := e.must(202, "POST", "/api/v1/public/appeals", map[string]string{"verdict_id": vBan,
			"appeal_code": appealCodeFor(vBan), "message": "please", "contact": ""}, service())
		golden(t, "public_appeal", r.body)
	})
	t.Run("apikeys", func(t *testing.T) {
		r := e.must(http.StatusCreated, "POST", "/api/v1/apikeys", map[string]any{"name": "ci", "scopes": []string{"read", "verdicts:write"}}, owner)
		m := r.json(t)
		keys(t, "created key", m, []string{"id", "name", "scopes", "created_at", "key"})
		if !strings.HasPrefix(m["key"].(string), "pe_"+m["id"].(string)+"_") {
			t.Fatalf("key format %q", m["key"])
		}
		golden(t, "apikey_create", r.body, "id", "key")
		// The new key works.
		e.must(200, "GET", "/api/v1/verdicts", nil, bearer(m["key"].(string)))
		l := e.must(200, "GET", "/api/v1/apikeys", nil, owner)
		lm := l.json(t)
		keys(t, "key list", lm, []string{"api_keys"})
		for _, k := range lm["api_keys"].([]any) {
			keys(t, "key", k, []string{"id", "name", "scopes", "created_at", "last_used_at", "revoked_at"})
		}
		golden(t, "apikeys_list", l.body, "id")
		d := e.must(200, "DELETE", "/api/v1/apikeys/"+m["id"].(string), nil, owner)
		golden(t, "apikey_delete", d.body)
		if r := e.do("GET", "/api/v1/verdicts", nil, bearer(m["key"].(string))); r.status != 401 {
			t.Fatalf("revoked key still works: %d", r.status)
		}
		e.must(400, "POST", "/api/v1/apikeys", map[string]any{"name": "x", "scopes": []string{"admin"}}, owner)
	})
	t.Run("checks put", func(t *testing.T) {
		r := e.must(200, "PUT", "/api/v1/checks", map[string]any{"checks": []any{
			map[string]any{"id": "reach.distance", "enabled": false, "overrides": map[string]float64{"max_reach": 3.2}},
			map[string]any{"id": "autoclicker.cps", "enabled": true},
			map[string]any{"id": "timer.balance", "enabled": false}, // local check: ignored
			map[string]any{"id": "no.such.check", "enabled": false}, // unknown: ignored
		}}, as(tenantA, "admin"))
		golden(t, "checks_put", r.body)
		st, _, err := e.d.Bus.GetTenant(context.Background(), tenantA)
		check(t, err)
		if c := st.Checks["reach.distance"]; c.Enabled || c.Overrides["max_reach"] != 3.2 || !st.Checks["autoclicker.cps"].Enabled {
			t.Fatalf("checks not in PE_TENANTS: %+v", st.Checks)
		}
		if _, ok := st.Checks["timer.balance"]; ok {
			t.Fatal("local check stored")
		}
	})
	t.Run("erase", func(t *testing.T) {
		r := e.must(200, "DELETE", "/api/v1/players/"+player2, nil, as(tenantA, "admin"))
		keys(t, "erase", r.json(t), []string{"counts"})
		golden(t, "player_erase", r.body)
	})
	t.Run("audit", func(t *testing.T) {
		r := e.must(200, "GET", "/api/v1/audit?limit=200", nil, as(tenantA, "admin"))
		m := r.json(t)
		keys(t, "audit", m, []string{"entries", "next_cursor"})
		actions := map[string]bool{}
		for _, en := range m["entries"].([]any) {
			keys(t, "audit entry", en, []string{"id", "principal", "action", "target", "detail", "created_at"})
			actions[en.(map[string]any)["action"].(string)] = true
		}
		for _, a := range []string{"license.rebind", "tenant.update", "verdict.confirm", "verdict.overturn", "player.watch",
			"appeal.decide", "appeal.create", "apikey.create", "apikey.revoke", "checks.update", "player.erase"} {
			if !actions[a] {
				t.Errorf("no audit entry %s (have %v)", a, actions)
			}
		}
		entries := m["entries"].([]any)
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].(map[string]any)["action"].(string) < entries[j].(map[string]any)["action"].(string)
		})
		b, _ := json.Marshal(m)
		golden(t, "audit", b, "id", "target", "detail")
	})
}
