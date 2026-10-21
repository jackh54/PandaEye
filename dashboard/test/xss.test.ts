import { env } from "cloudflare:workers";
import { describe, expect, it } from "vitest";
import { Client, MockApi, seedUser, signIn, uniq } from "./helpers";

// Breaks out of attributes and text alike if anything is not escaped.
const P = `"'><img src=x onerror=alert(1)>`;
const ESCAPED_TEXT = "&lt;img src=x onerror=alert(1)&gt;";
const UUID = "8667ba71-b85a-4004-af54-457a9734eed7";

function assertSafe(html: string, where: string) {
  expect(html, where).not.toMatch(/<img/i);
  expect(html, where).not.toContain("<script>alert");
  // No tag may carry an event-handler attribute. Attribute values are always double-quoted and any quote
  // inside them is escaped, so dropping quoted values leaves only attribute names to inspect.
  const unquoted = html.replace(/"[^"]*"/g, '""');
  expect(unquoted, where).not.toMatch(/<[^>]*\son[a-z]+\s*=/i);
  // Escaped payloads may only appear as text or inside quoted values.
  expect(html.includes("'><img"), where).toBe(false);
}

function evilVerdict(id = "v1") {
  return {
    id,
    server_id: P,
    server_name: P,
    network_id: P,
    player_uuid: UUID,
    player_name: P,
    session_id: P,
    action: "ban",
    category: P,
    score: 99,
    confidence: 0.9,
    reason: P,
    checks: [P, "killaura.hitbox"],
    shadow: false,
    status: P,
    supersedes: P,
    created_at: "2026-10-21T12:00:00Z",
    executed: false,
    ack_note: P,
    clip_ids: [P, "clip-1"],
    mitigations: [P],
    evidence: {
      score: 99,
      thresholds: { [P]: 1 },
      families: [{ family: P, contribution: 5 }],
      checks: [{ check: P, contribution: 5 }],
      signals: [{ check: P, category: P, points: 5, confidence: 0.5, info: P, data: { [P]: 1, k: P }, time: P, clip_id: P }],
      session: { [P]: P, brand: P },
    },
  };
}

function evilApi(): MockApi {
  const ban = { verdict_id: P, server_id: P, player_uuid: UUID, player_name: P, active: true, reason: P, overturned_by: P, overturn_note: P, unban_state: "none" };
  const appeal = { id: P, verdict_id: P, player_uuid: UUID, player_name: P, message: P, contact: P, status: "open", decided_by: P, decision_note: P };
  return new MockApi()
    .json("GET", "/api/v1/tenant", { id: P, name: P, slug: P, plan: P, leniency: 4, discord_webhook: P, discord_min_action: "mitigate", over_limit_since: "2026-10-01T00:00:00Z" })
    .json("GET", "/api/v1/servers", { servers: [{ id: P, name: P, network_id: P, platform: P, platform_version: P, plugin_version: P, minecraft_version: P, online: true, online_players: 3 }] })
    .json("GET", "/api/v1/stats/overview", { last_24h: { by_action: { [P]: 1, ban: 1 }, by_category: { [P]: 2 } } })
    .json("GET", "/api/v1/stats/latency", { last_1h: { p95_us: P } })
    .json("GET", "/api/v1/verdicts", { verdicts: [evilVerdict()], next_cursor: P })
    .json("GET", /^\/api\/v1\/verdicts\/[^/]+$/, evilVerdict())
    .json("GET", "/api/v1/players", { players: [{ uuid: P, last_name: P, last_server_id: P }], next_cursor: P })
    .json("GET", `/api/v1/players/${UUID}`, {
      uuid: UUID,
      last_name: P,
      names: [P, { name: P }],
      brand: P,
      client_version: P,
      last_server_id: P,
      verdicts: [evilVerdict()],
      bans: [ban],
      reputation: { risk: 0.5, networks_flagged: 2, confirmed_bans: 1 },
    })
    .json("GET", "/api/v1/bans", { bans: [ban] })
    .json("GET", "/api/v1/appeals", { appeals: [appeal, { ...appeal, status: "denied" }] })
    .json("GET", "/api/v1/usage", { plan: P, free_tier_limit: 30, over_limit_since: P, days: [{ day: "2026-10-01", peak_players: 3 }, { day: P, peak_players: 1 }] })
    .json("GET", "/api/v1/apikeys", { api_keys: [{ id: P, name: P, scopes: [P] }] })
    .json("GET", "/api/v1/checks", { checks: [{ id: P, enabled: true, overrides: { [P]: 1 } }, { id: "aim.snap", enabled: true, overrides: { k: 2 } }] })
    .json("GET", "/api/v1/audit", { entries: [{ id: P, principal: P, action: P, target: P, detail: { [P]: P } }] })
    .json("GET", "/status", { region: P, version: P, probe_rtt: { p95_us: 1 } })
    .json("GET", /^\/api\/v1\/clips\//, { player_name: P, events: [{ time_us: 1, flag: { check_id: P, info: P } }] });
}

describe("XSS: API data is always escaped", () => {
  it("renders every signed-in page with payloads in every field, escaped", async () => {
    const api = evilApi();
    const client = new Client(api);
    const owner = await seedUser({ role: "owner" });
    // A username and audit entry with the payload, written straight into D1.
    await env.DB.prepare(
      "INSERT INTO users (id, tenant_id, username, password_hash, role, created_at, disabled, password_changed_at) VALUES (?1, ?2, ?3, 'x', 'viewer', 1, 0, 1)",
    )
      .bind(uniq("u_"), owner.tenantId, P.toLowerCase())
      .run();
    await env.DB.prepare("INSERT INTO audit_log (tenant_id, principal, action, target, detail, created_at) VALUES (?1, ?2, ?2, ?2, ?2, 1)").bind(owner.tenantId, P).run();
    await signIn(client, owner);

    const pages = ["/", "/servers", "/verdicts", "/verdicts/v1", "/players?q=abc", `/players/${UUID}`, "/bans", "/appeals?status=all", "/settings", "/checks", "/users", "/apikeys", "/audit", "/usage", "/account"];
    for (const path of pages) {
      const res = await client.get(path);
      expect(res.status, path).toBe(200);
      const html = await res.text();
      assertSafe(html, path);
      if (path !== "/account") expect(html, path).toContain(ESCAPED_TEXT);
    }
  });

  it("escapes the payload in player names, brand, signal info and appeal messages specifically", async () => {
    const api = evilApi();
    const client = new Client(api);
    await signIn(client, await seedUser({ role: "staff" }));
    const player = await (await client.get(`/players/${UUID}`)).text();
    expect(player).toContain(`<code>&quot;&#39;&gt;${ESCAPED_TEXT}</code>`); // brand
    expect(player).toContain(`<h1>&quot;&#39;&gt;${ESCAPED_TEXT}</h1>`); // player name
    const verdict = await (await client.get("/verdicts/v1")).text();
    expect(verdict).toContain(`<td class="info">&quot;&#39;&gt;${ESCAPED_TEXT}</td>`); // signal info
    const appeals = await (await client.get("/appeals?status=all")).text();
    expect(appeals).toContain(`<blockquote class="appeal-message">&quot;&#39;&gt;${ESCAPED_TEXT}</blockquote>`); // appeal message
  });

  it("escapes payloads in public pages and reflected form values", async () => {
    const api = evilApi();
    const client = new Client(api);
    const status = await (await client.get("/status")).text();
    assertSafe(status, "/status");
    expect(status).toContain(ESCAPED_TEXT);

    // Query parameters that are not valid ids are not reflected at all.
    const appealPage = await (await client.get(`/appeal?verdict=${encodeURIComponent(P)}&code=${encodeURIComponent(P)}`)).text();
    assertSafe(appealPage, "/appeal");
    expect(appealPage).not.toContain("onerror");

    // A rejected appeal re-renders the form with the visitor's own input, escaped.
    const csrf = await client.csrfFrom("/appeal");
    const res = await client.post("/appeal", { _csrf: csrf, verdict_id: "v1", appeal_code: "ABCD", message: P, contact: P });
    const html = await res.text();
    expect(res.status).toBe(400);
    assertSafe(html, "POST /appeal");
    expect(html).toContain(ESCAPED_TEXT);

    const lcsrf = await client.csrfFrom("/login");
    const login = await (await client.post("/login", { _csrf: lcsrf, username: P, password: "x" })).text();
    assertSafe(login, "POST /login");
    const next = await (await client.get(`/login?next=${encodeURIComponent(`/"><img src=x onerror=alert(1)>`)}`)).text();
    assertSafe(next, "/login?next");
  });

  it("serves clip JSON as application/json with nosniff (the viewer only uses textContent/fillText)", async () => {
    const client = new Client(evilApi());
    await signIn(client, await seedUser({ role: "viewer" }));
    const res = await client.get("/verdicts/v1/clip/clip-1");
    expect(res.headers.get("Content-Type")).toMatch(/^application\/json/);
    expect(res.headers.get("X-Content-Type-Options")).toBe("nosniff");
  });
});

describe("static guards", () => {
  const sources = JSON.parse(env.TEST_SOURCES) as Record<string, string>;

  it("never uses raw HTML APIs in server code", () => {
    const server = Object.entries(sources).filter(([p]) => p.startsWith("src"));
    expect(server.length).toBeGreaterThan(10);
    for (const [path, text] of server) {
      expect(text, path).not.toMatch(/\braw\(/);
      expect(text, path).not.toMatch(/dangerouslySetInnerHTML/);
      expect(text, path).not.toMatch(/from "hono\/html"/);
      expect(text, path).not.toMatch(/\sstyle=\{/);
    }
  });

  it("the replay viewer never writes HTML", () => {
    const js = sources["public/static/replay.js"]!;
    expect(js).toBeTruthy();
    for (const bad of ["innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval(", "new Function"]) {
      expect(js).not.toContain(bad);
    }
    expect(js).toContain("textContent");
    expect(js).toContain("fillText");
  });
});
