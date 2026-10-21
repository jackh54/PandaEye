import { describe, expect, it } from "vitest";
import { Client, MockApi, signedInClient, tenantFixture, verdictFixture } from "./helpers";

const V = verdictFixture();

describe("dashboard pages against the mocked API", () => {
  it("overview degrades per section when parts of the API fail", async () => {
    const { client, api } = await signedInClient("viewer");
    api
      .json("GET", "/api/v1/tenant", tenantFixture({ over_limit_since: new Date(Date.now() - 4 * 86_400_000).toISOString() }))
      .json("GET", "/api/v1/stats/overview", { last_24h: { by_action: { ban: 2, ALERT: 5 }, by_category: { HEURISTICS: 7 }, shadow: 3 }, last_7d: { by_action: { ban: 9 } } })
      .json("GET", "/api/v1/stats/latency", { error: "boom" }, 500)
      .json("GET", "/api/v1/servers", { servers: [{ id: "s1", name: "SMP", online: true, online_players: 12 }] })
      .json("GET", "/api/v1/verdicts", { verdicts: [V, verdictFixture({ id: "v-shadow", shadow: true, action: "kick" })] })
      .json("GET", "/api/v1/appeals", { appeals: [{ id: "a1" }] });
    const res = await client.get("/");
    expect(res.status).toBe(200);
    const html = await res.text();
    expect(html).toContain("Verdicts are capped at ALERT");
    expect(html).toContain("Shadow mode");
    expect(html).toContain("1 / 1");
    expect(html).toContain("12 players online");
    expect(html).toMatch(/Verdict latency p95 \(1 h\)<\/div><div class="stat-value">—<\/div><div class="stat-hint">unavailable/);
    expect(html).toContain("SHADOW – not executed");
    // Every call carried an assertion.
    for (const call of api.calls) expect(call.headers.get("X-PandaEye-Assertion")).toBeTruthy();
  });

  it("shows a friendly error page when the API is unreachable", async () => {
    const { client, api } = await signedInClient("viewer");
    api.down = true;
    for (const path of ["/servers", "/verdicts", "/verdicts/abc", "/bans", "/appeals", "/usage", "/players?q=abc"]) {
      const res = await client.get(path);
      expect(res.status, path).toBe(503);
      const html = await res.text();
      expect(html).toContain("PandaEye API unavailable");
      expect(html).not.toContain("TypeError");
    }
    const overview = await client.get("/");
    expect(overview.status).toBe(200);
  });

  it("maps API 404 and 401 to readable pages", async () => {
    const { client, api } = await signedInClient("viewer");
    api.json("GET", "/api/v1/verdicts/missing", { error: "not found" }, 404).json("GET", "/api/v1/servers", { error: "bad assertion" }, 401);
    const nf = await client.get("/verdicts/missing");
    expect(nf.status).toBe(404);
    const unauth = await client.get("/servers");
    expect(unauth.status).toBe(503);
    expect(await unauth.text()).toContain("rejected the dashboard");
  });

  it("verdict list passes filters to the API and builds a next-page link", async () => {
    const { client, api } = await signedInClient("viewer");
    api.json("GET", "/api/v1/verdicts", { verdicts: [V], next_cursor: "CUR1" });
    const res = await client.get("/verdicts?action=BAN&status=open&shadow=true&player=Suspect&server=s1&since=2026-10-01&until=2026-10-02&junk=1");
    expect(res.status).toBe(200);
    const q = api.calls[0]!.url.searchParams;
    expect(Object.fromEntries(q)).toEqual({
      action: "ban",
      status: "open",
      shadow: "true",
      player: "Suspect",
      server: "s1",
      since: "2026-10-01T00:00:00Z",
      until: "2026-10-03T00:00:00Z",
      limit: "50",
    });
    const html = await res.text();
    expect(html).toContain("cursor=CUR1");
  });

  it("verdict detail renders evidence, shadow label and the replay viewer", async () => {
    const { client, api } = await signedInClient("staff");
    api.json("GET", `/api/v1/verdicts/${V.id}`, verdictFixture({ shadow: true, clip_ids: ["clip-1", "clip-2"] }));
    const res = await client.get(`/verdicts/${V.id}?clip=clip-2`);
    const html = await res.text();
    expect(res.status).toBe(200);
    expect(html).toContain("SHADOW – not executed");
    expect(html).toContain("killaura.hitbox");
    expect(html).toContain("hit outside hitbox by 0.21");
    expect(html).toContain(`data-clip-url="/verdicts/${V.id}/clip/clip-2"`);
    expect(html).toContain("Confirm: cheating");
  });

  it("clip proxy returns the API's clip JSON and maps errors to JSON", async () => {
    const { client, api } = await signedInClient("viewer");
    api.json("GET", "/api/v1/clips/c1", { player: "8667ba71-b85a-4004-af54-457a9734eed7", events: [{ time_us: "1", move: { x: 1 } }] });
    const ok = await client.get("/verdicts/v1/clip/c1");
    expect(ok.status).toBe(200);
    expect(await ok.json()).toEqual({ player: "8667ba71-b85a-4004-af54-457a9734eed7", events: [{ time_us: "1", move: { x: 1 } }] });
    const missing = await client.get("/verdicts/v1/clip/c2");
    expect(missing.status).toBe(404);
    expect(await missing.json()).toEqual({ error: "clip not found" });
    api.down = true;
    expect((await client.get("/verdicts/v1/clip/c1")).status).toBe(502);
    expect((await client.get("/verdicts/v1/clip/bad%20id")).status).toBe(400);
  });

  it("staff confirm/overturn and decide appeals; viewers cannot", async () => {
    const { client, csrf, api } = await signedInClient("staff");
    api.json("POST", /\/api\/v1\/verdicts\/.+\/(confirm|overturn)$/, {}).json("POST", "/api/v1/appeals/a1/decide", {});
    expect((await client.post("/verdicts/v1/overturn", { _csrf: csrf, note: "false positive" })).headers.get("Location")).toBe("/verdicts/v1?ok=overturned");
    expect(api.calls[0]!.body).toEqual({ note: "false positive" });
    expect((await client.post("/verdicts/v1/overturn", { _csrf: csrf, return: "/bans" })).headers.get("Location")).toBe("/bans?ok=overturned");
    expect((await client.post("/appeals/a1/decide", { _csrf: csrf, decision: "accept", note: "ok" })).headers.get("Location")).toBe("/appeals?ok=appeal_accepted");
    expect(api.callsTo("POST", "/api/v1/appeals/a1/decide")[0]!.body).toEqual({ decision: "accept", note: "ok" });
    expect((await client.post("/appeals/a1/decide", { _csrf: csrf, decision: "maybe" })).headers.get("Location")).toBe("/appeals?err=invalid");

    const viewer = await signedInClient("viewer");
    viewer.api.json("POST", /confirm$/, {});
    expect((await viewer.client.post("/verdicts/v1/confirm", { _csrf: viewer.csrf })).status).toBe(403);
    expect((await viewer.client.post("/appeals/a1/decide", { _csrf: viewer.csrf, decision: "deny" })).status).toBe(403);
    expect(viewer.api.calls).toHaveLength(0);
  });

  it("API 4xx on an action shows the API's message, escaped", async () => {
    const { client, csrf, api } = await signedInClient("staff");
    api.json("POST", "/api/v1/verdicts/v1/confirm", { error: "verdict is already overturned <b>", code: "conflict" }, 409);
    const res = await client.post("/verdicts/v1/confirm", { _csrf: csrf });
    expect(res.status).toBe(400);
    const html = await res.text();
    expect(html).toContain("verdict is already overturned &lt;b&gt;");
  });

  it("bans page shows unban state and an overturn button for active bans", async () => {
    const { client, api } = await signedInClient("staff");
    api.json("GET", "/api/v1/bans", {
      bans: [
        { verdict_id: "b1", player_uuid: "8667ba71-b85a-4004-af54-457a9734eed7", player_name: "One", active: true, unban_state: "none" },
        { verdict_id: "b2", player_uuid: "8667ba71-b85a-4004-af54-457a9734eed8", player_name: "Two", active: true, unban_state: "pending" },
        { verdict_id: "b3", player_uuid: "8667ba71-b85a-4004-af54-457a9734eed9", player_name: "Three", active: false, unban_state: "done" },
      ],
    });
    const html = await (await client.get("/bans")).text();
    expect(api.calls[0]!.url.searchParams.get("active")).toBe("true");
    expect(html).toContain("Unban pending");
    expect(html).toContain("Unbanned");
    expect(html.match(/Overturn and unban/g)).toHaveLength(1);
  });

  it("players: UUID search redirects, name search lists, watch is staff-only and bounded", async () => {
    const { client, csrf, api } = await signedInClient("staff");
    const uuid = "8667ba71-b85a-4004-af54-457a9734eed7";
    expect((await client.get(`/players?q=${uuid.toUpperCase()}`)).headers.get("Location")).toBe(`/players/${uuid}`);
    api.json("GET", "/api/v1/players", { players: [{ uuid, last_name: "Suspect" }] }).json("POST", `/api/v1/players/${uuid}/watch`, {});
    const list = await client.get("/players?q=Susp");
    expect(api.calls[0]!.url.searchParams.get("name")).toBe("Susp");
    expect(await list.text()).toContain("Suspect");
    expect((await client.post(`/players/${uuid}/watch`, { _csrf: csrf, seconds: "300" })).headers.get("Location")).toBe(`/players/${uuid}?ok=watch_requested`);
    expect(api.callsTo("POST", `/api/v1/players/${uuid}/watch`)[0]!.body).toEqual({ seconds: 300 });
    expect((await client.post(`/players/${uuid}/watch`, { _csrf: csrf, seconds: "9999" })).headers.get("Location")).toBe(`/players/${uuid}?err=invalid`);
    expect((await client.post(`/players/${uuid}/erase`, { _csrf: csrf, confirm: uuid })).status).toBe(403);
  });

  it("erasure requires admin and the typed UUID", async () => {
    const { client, csrf, api } = await signedInClient("admin");
    const uuid = "8667ba71-b85a-4004-af54-457a9734eed7";
    api.json("DELETE", `/api/v1/players/${uuid}`, { counts: { verdicts: 3, clips: 1 } });
    expect((await client.post(`/players/${uuid}/erase`, { _csrf: csrf, confirm: "nope" })).headers.get("Location")).toBe(`/players/${uuid}?err=invalid`);
    expect(api.calls).toHaveLength(0);
    const res = await client.post(`/players/${uuid}/erase`, { _csrf: csrf, confirm: uuid });
    expect(res.status).toBe(200);
    expect(await res.text()).toContain("verdicts 3, clips 1");
  });
});

describe("settings and checks", () => {
  it("PATCHes only changed fields and requires confirmation to enable enforcement", async () => {
    const { client, csrf, api } = await signedInClient("admin");
    api.json("GET", "/api/v1/tenant", tenantFixture()).json("PATCH", "/api/v1/tenant", {});
    const base = { _csrf: csrf, leniency: "4", mitigations: "on", discord_min_action: "mitigate" };
    expect((await client.post("/settings", base)).headers.get("Location")).toBe("/settings?ok=settings_saved");
    expect(api.callsTo("PATCH", "/api/v1/tenant")).toHaveLength(0);

    const noConfirm = await client.post("/settings", { ...base, enforcement: "on" });
    expect(noConfirm.status).toBe(400);
    expect(api.callsTo("PATCH", "/api/v1/tenant")).toHaveLength(0);

    const webhook = `https://discord.com/api/webhooks/123456789012345678/${"a".repeat(68)}`;
    const ok = await client.post("/settings", { ...base, leniency: "2", enforcement: "on", confirm_enforcement: "on", autoban: "on", discord_webhook: webhook, discord_min_action: "ban" });
    expect(ok.status).toBe(303);
    expect(api.callsTo("PATCH", "/api/v1/tenant")[0]!.body).toEqual({ leniency: 2, enforcement: true, autoban: true, discord_webhook: webhook, discord_min_action: "ban" });

    const badHook = await client.post("/settings", { ...base, discord_webhook: "https://evil.example/api/webhooks/1/2" });
    expect(badHook.status).toBe(400);
    expect((await client.post("/settings", { ...base, leniency: "9" })).status).toBe(400);
  });

  it("shows the enforcement warning and the masked webhook", async () => {
    const { client, api } = await signedInClient("admin");
    api.json("GET", "/api/v1/tenant", tenantFixture({ discord_webhook: "https://discord.com/api/webhooks/1234…/****" }));
    const html = await (await client.get("/settings")).text();
    expect(html).toContain("Read before enabling");
    expect(html).toContain("https://discord.com/api/webhooks/1234…/****");
    expect(html).not.toMatch(/name="discord_webhook"[^>]*value="https/);
  });

  it("settings and checks are admin-only", async () => {
    const { client } = await signedInClient("staff");
    expect((await client.get("/settings")).status).toBe(403);
    expect((await client.get("/checks")).status).toBe(403);
  });

  it("PUTs the full cloud check list and keeps unknown checks", async () => {
    const { client, csrf, api } = await signedInClient("admin");
    api.json("GET", "/api/v1/checks", { checks: [{ id: "reach.distance", enabled: true, overrides: { max_reach: 3.1 } }, { id: "ml.gbm1", enabled: false, overrides: {} }] }).json("PUT", "/api/v1/checks", {});
    const page = await (await client.get("/checks")).text();
    expect(page).toContain("dig.ray");
    expect(page).toContain("movement.noslow");
    expect(page).toContain('value="max_reach=3.1"');
    const res = await client.post("/checks", { _csrf: csrf, "enabled:reach.distance": "on", "overrides:reach.distance": "max_reach=3.2, buffer=0.1", "enabled:dig.ray": "on" });
    expect(res.status).toBe(303);
    const body = api.callsTo("PUT", "/api/v1/checks")[0]!.body as { checks: { id: string; enabled: boolean; overrides: Record<string, number> }[] };
    const byId = Object.fromEntries(body.checks.map((c) => [c.id, c]));
    expect(byId["reach.distance"]).toEqual({ id: "reach.distance", enabled: true, overrides: { max_reach: 3.2, buffer: 0.1 } });
    expect(byId["dig.ray"]!.enabled).toBe(true);
    expect(byId["aim.snap"]!.enabled).toBe(false);
    expect(byId["ml.gbm1"]).toEqual({ id: "ml.gbm1", enabled: false, overrides: {} });
    expect(byId["movement.speed"]).toBeUndefined();
    const bad = await client.post("/checks", { _csrf: csrf, "overrides:aim.snap": "x=abc" });
    expect(bad.status).toBe(400);
  });
});

describe("API keys", () => {
  it("owner creates a key and sees the plaintext once", async () => {
    const { client, csrf, api } = await signedInClient("owner");
    api
      .json("GET", "/api/v1/apikeys", { api_keys: [{ id: "k1", name: "bot", scopes: ["read"] }] })
      .json("POST", "/api/v1/apikeys", { id: "k2", name: "bot2", scopes: ["read", "verdicts:write"], key: "pe_k2_SECRETSECRET" });
    const res = await client.post("/apikeys", { _csrf: csrf, name: "bot2", "scope:read": "on", "scope:verdicts:write": "on", "scope:bogus": "on" });
    expect(res.status).toBe(200);
    expect(res.headers.get("Cache-Control")).toBe("no-store");
    expect(await res.text()).toContain("pe_k2_SECRETSECRET");
    expect(api.callsTo("POST", "/api/v1/apikeys")[0]!.body).toEqual({ name: "bot2", scopes: ["read", "verdicts:write"] });
    expect(await (await client.get("/apikeys")).text()).not.toContain("pe_k2_SECRETSECRET");
  });

  it("is owner-only", async () => {
    const { client } = await signedInClient("admin");
    expect((await client.get("/apikeys")).status).toBe(403);
  });
});

describe("public pages", () => {
  it("status page renders the API status document and survives the API being down", async () => {
    const api = new MockApi().json("GET", "/status", { region: "eu-central", version: "1.0.0", uptime_s: 7200, connected_servers: 42, probe_rtt: { p50_us: 21000, p95_us: 48000, p99_us: 90000 }, slo: { target_p95_ms: 100, met: true } });
    const client = new Client(api);
    const res = await client.get("/status");
    const html = await res.text();
    expect(res.status).toBe(200);
    expect(html).toContain("All systems operational");
    expect(html).toContain("eu-central");
    expect(html).toContain("48 ms");
    expect(api.calls[0]!.headers.get("X-PandaEye-Assertion")).toBeNull();
    api.down = true;
    const down = await client.get("/status");
    expect(down.status).toBe(503);
    expect(await down.text()).toContain("Status unavailable");
  });

  it("appeal form verifies Turnstile and forwards to the public appeals endpoint", async () => {
    const api = new MockApi().json("POST", "/api/v1/public/appeals", { ok: true }, 201);
    const client = new Client(api, "198.51.100.77");
    const csrf = await client.csrfFrom("/appeal?verdict=3f0c2c9e-1111-4a2b-9c3d-0123456789ab&code=ABCD-1234");
    const form = { _csrf: csrf, verdict_id: "3f0c2c9e-1111-4a2b-9c3d-0123456789ab", appeal_code: "ABCD-1234", message: "I was lagging, not cheating.", contact: "me#1234", "cf-turnstile-response": "tok" };
    const res = await client.post("/appeal", form);
    expect(res.status).toBe(200);
    expect(await res.text()).toContain("Appeal received");
    const call = api.callsTo("POST", "/api/v1/public/appeals")[0]!;
    expect(call.body).toEqual({ verdict_id: form.verdict_id, appeal_code: "ABCD-1234", message: form.message, contact: "me#1234" });
    expect(call.headers.get("X-PandaEye-Client-IP")).toBe("198.51.100.77");
    expect(call.headers.get("X-PandaEye-Assertion")).toBeNull();

    api.json("POST", "/api/v1/public/appeals", { error: "invalid" }, 404);
    const wrong = await client.post("/appeal", form);
    expect(wrong.status).toBe(400);
    expect(await wrong.text()).toContain("could not be accepted");

    api.turnstileOk = false;
    const ts = await client.post("/appeal", form);
    expect(ts.status).toBe(400);
  });

  it("privacy page summarises the privacy points", async () => {
    const html = await (await new Client().get("/privacy")).text();
    for (const s of ["processor", "controller", "No IP addresses", "Retention", "appeal", "Minors", "erase"]) expect(html).toContain(s);
  });

  it("healthz needs no auth", async () => {
    expect(await (await new Client().get("/healthz")).text()).toBe("ok");
  });
});
