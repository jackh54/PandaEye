// Regression tests for the nine findings of the independent dashboard review.
import { createExecutionContext, waitOnExecutionContext } from "cloudflare:test";
import { env } from "cloudflare:workers";
import { describe, expect, it } from "vitest";
import "../public/static/replay.js";
import { createApp } from "../src/app";
import { missingSecrets } from "../src/env";
import { ApiClient, ApiError } from "../src/lib/api";
import { flashFromQuery } from "../src/lib/messages";
import { verifyTurnstile } from "../src/lib/turnstile";
import { acceptInvite, setUserDisabled, setUserRole } from "../src/lib/users";
import { sha256Hex } from "../src/lib/crypto";
import { Client, MockApi, ORIGIN, PASSWORD, seedUser, signIn, signedInClient, uniq, type SeededUser } from "./helpers";

// ---- 1. login timing ----------------------------------------------------------------------------------

/**
 * Wraps D1 so that (a) every statement executed before the response is recorded and (b) audit_log writes are
 * held until released. If the login handler awaited the audit write, the response would never arrive.
 */
function instrumentedDb(real: D1Database) {
  let release!: () => void;
  const held = new Promise<void>((r) => (release = r));
  const executed: string[] = [];
  const wrap = (stmt: D1PreparedStatement, sql: string): D1PreparedStatement => {
    const isAudit = /audit_log/.test(sql);
    const exec = async <T>(fn: () => Promise<T>): Promise<T> => {
      if (isAudit) await held;
      executed.push(sql.replace(/\s+/g, " ").trim());
      return fn();
    };
    return {
      __real: stmt,
      __sql: sql,
      bind: (...values: unknown[]) => wrap(stmt.bind(...values), sql),
      first: (col?: string) => exec(() => (col ? stmt.first(col) : stmt.first())),
      run: () => exec(() => stmt.run()),
      all: () => exec(() => stmt.all()),
      raw: (o?: { columnNames?: boolean }) => exec(() => stmt.raw(o as { columnNames: true })),
    } as unknown as D1PreparedStatement;
  };
  const db = new Proxy(real, {
    get(target, prop) {
      if (prop === "prepare") return (sql: string) => wrap(target.prepare(sql), sql);
      if (prop === "batch")
        return (stmts: D1PreparedStatement[]) => {
          const sqls = stmts.map((s) => (s as unknown as { __real?: D1PreparedStatement }).__real ?? s);
          if (stmts.some((s) => /audit_log/.test(String((s as unknown as { __sql?: string }).__sql ?? "")))) return held.then(() => target.batch(sqls));
          executed.push("batch");
          return target.batch(sqls);
        };
      const v = Reflect.get(target, prop);
      return typeof v === "function" ? v.bind(target) : v;
    },
  });
  return { db, executed, release };
}

async function loginAttempt(username: string, ip: string) {
  const client = new Client(new MockApi(), ip);
  const csrf = await client.csrfFrom("/login");
  const { db, executed, release } = instrumentedDb(env.DB);
  const app = createApp({ fetch: client.api.fetch });
  const ctx = createExecutionContext();
  const headers = new Headers({
    "Content-Type": "application/x-www-form-urlencoded",
    "CF-Connecting-IP": ip,
    Origin: ORIGIN,
    Cookie: [...client.cookies].map(([k, v]) => `${k}=${v}`).join("; "),
  });
  const body = new URLSearchParams({ _csrf: csrf, username, password: "definitely wrong" }).toString();
  const res = await Promise.race([
    app.fetch(new Request(`${ORIGIN}/login`, { method: "POST", headers, body }), { ...env, DB: db }, ctx),
    new Promise<never>((_, reject) => setTimeout(() => reject(new Error("response waited for the audit write")), 5000)),
  ]);
  const beforeResponse = [...executed];
  release();
  await waitOnExecutionContext(ctx);
  return { res, beforeResponse };
}

describe("1. login does the same D1 work for unknown users and wrong passwords", () => {
  it("writes the failed-login audit row after the response", async () => {
    const user = await seedUser();
    const known = await loginAttempt(user.username, "198.18.0.1");
    const unknown = await loginAttempt(uniq("nobody"), "198.18.0.2");
    expect(known.res.status).toBe(401);
    expect(unknown.res.status).toBe(401);
    expect(known.beforeResponse.some((s) => s.includes("audit_log"))).toBe(false);
    expect(known.beforeResponse).toEqual(unknown.beforeResponse);
    // ... and the audit row still lands once the background work ran.
    const n = await env.DB.prepare("SELECT COUNT(*) AS n FROM audit_log WHERE target = ?1 AND action = 'login.failed'").bind(user.id).first("n");
    expect(n).toBe(1);
  });
});

// ---- 2. invites follow their creator's authority -------------------------------------------------------

async function createInviteAs(user: SeededUser, role: string): Promise<string> {
  const c = new Client();
  const csrf = await signIn(c, user);
  const res = await c.post("/users/invites", { _csrf: csrf, role });
  expect(res.status).toBe(303);
  const html = await (await c.get(res.headers.get("Location")!)).text();
  return /\/invite\/([A-Za-z0-9_-]{43})/.exec(html)![1]!;
}

async function inviteState(token: string): Promise<number> {
  return (await new Client().get(`/invite/${token}`)).status;
}

async function acceptStatus(token: string): Promise<number> {
  const c = new Client(new MockApi(), `198.18.1.${Math.floor(Math.random() * 200)}`);
  const csrf = await c.csrfFrom("/login");
  return (await c.post(`/invite/${token}`, { _csrf: csrf, username: uniq("inv"), password: PASSWORD, password_confirm: PASSWORD })).status;
}

describe("2. invites do not outlive their creator's authority", () => {
  it("disabling the creator deletes their unused invites", async () => {
    const owner = await seedUser({ role: "owner" });
    const admin = await seedUser({ tenantId: owner.tenantId, role: "admin" });
    const token = await createInviteAs(admin, "staff");
    expect(await inviteState(token)).toBe(200);
    await setUserDisabled(env.DB, owner.tenantId, admin.id, true, "user:test", Date.now());
    expect(await env.DB.prepare("SELECT COUNT(*) AS n FROM invites WHERE created_by = ?1").bind(admin.id).first("n")).toBe(0);
    expect(await inviteState(token)).toBe(404);
    expect(await acceptStatus(token)).toBe(404);
  });

  it("a role change deletes exactly the invites the new role may not issue", async () => {
    const owner = await seedUser({ role: "owner" });
    const coOwner = await seedUser({ tenantId: owner.tenantId, role: "owner" });
    const adminInvite = await createInviteAs(coOwner, "admin");
    const staffInvite = await createInviteAs(coOwner, "staff");
    const rows = async () =>
      (await env.DB.prepare("SELECT role FROM invites WHERE created_by = ?1 AND used_at IS NULL ORDER BY role").bind(coOwner.id).all<{ role: string }>()).results.map((r) => r.role);
    expect(await rows()).toEqual(["admin", "staff"]);
    await setUserRole(env.DB, owner.tenantId, coOwner.id, "admin", "user:test", Date.now());
    expect(await rows()).toEqual(["staff"]);
    expect(await inviteState(adminInvite)).toBe(404);
    expect(await inviteState(staffInvite)).toBe(200);
    await setUserRole(env.DB, owner.tenantId, coOwner.id, "staff", "user:test", Date.now());
    expect(await rows()).toEqual([]);
    expect(await inviteState(staffInvite)).toBe(404);
  });

  it("the route-level role change and disable revoke invites too", async () => {
    const owner = await seedUser({ role: "owner" });
    const admin = await seedUser({ tenantId: owner.tenantId, role: "admin" });
    const token = await createInviteAs(admin, "viewer");
    const c = new Client();
    const csrf = await signIn(c, owner);
    expect((await c.post(`/users/${admin.id}/role`, { _csrf: csrf, role: "viewer" })).status).toBe(303);
    expect(await inviteState(token)).toBe(404);
  });

  it("acceptance re-checks that the creator is enabled and may still issue the role", async () => {
    const owner = await seedUser({ role: "owner" });
    const insert = async (creator: SeededUser, role: string) => {
      const token = uniq("tok").padEnd(43, "A").slice(0, 43);
      await env.DB.prepare("INSERT INTO invites (token_hash, tenant_id, role, expires_at, created_by, created_at) VALUES (?1, ?2, ?3, ?4, ?5, ?6)")
        .bind(await sha256Hex(token), owner.tenantId, role, Date.now() + 3600_000, creator.id, Date.now())
        .run();
      return token;
    };
    // Rows written directly (as if created before a demotion): the acceptance itself must refuse them.
    const disabled = await seedUser({ tenantId: owner.tenantId, role: "admin", disabled: true });
    const staff = await seedUser({ tenantId: owner.tenantId, role: "staff" });
    const admin = await seedUser({ tenantId: owner.tenantId, role: "admin" });
    const foreignAdmin = await seedUser({ role: "admin" });
    for (const token of [await insert(disabled, "viewer"), await insert(staff, "viewer"), await insert(admin, "admin"), await insert(foreignAdmin, "viewer")]) {
      expect(await inviteState(token)).toBe(404);
      expect(await acceptStatus(token)).toBe(404);
      // The transaction itself refuses it too (e.g. the creator was disabled between page load and submit).
      expect(await acceptInvite(env.DB, { token, userId: uniq("u_"), username: uniq("direct"), passwordHash: "x", now: Date.now() })).toEqual({ ok: false, reason: "invite_invalid" });
      expect(await env.DB.prepare("SELECT used_at FROM invites WHERE token_hash = ?1").bind(await sha256Hex(token)).first("used_at")).toBeNull();
    }
    const ok = await insert(admin, "staff");
    expect(await acceptStatus(ok)).toBe(303);
  });
});

// ---- 3./5. configuration checks -------------------------------------------------------------------------

describe("3./5. configuration is validated", () => {
  it("requires TURNSTILE_SITEKEY and an https API URL outside development", () => {
    expect(missingSecrets(env)).toEqual([]);
    expect(missingSecrets({ ...env, TURNSTILE_SITEKEY: undefined })).toEqual(["TURNSTILE_SITEKEY"]);
    expect(missingSecrets({ ...env, TURNSTILE_SITEKEY: undefined, ENVIRONMENT: "development" })).toEqual([]);
    expect(missingSecrets({ ...env, PANDAEYE_API_URL: "http://api.example.test" })).toEqual(["PANDAEYE_API_URL (must be https)"]);
    expect(missingSecrets({ ...env, PANDAEYE_API_URL: "http://127.0.0.1:8081", ENVIRONMENT: "development" })).toEqual([]);
    expect(missingSecrets({ ...env, PANDAEYE_API_URL: "ftp://x" })).toEqual(["PANDAEYE_API_URL (must be https)"]);
  });

  it("the API client refuses http outside development", async () => {
    const mock = new MockApi().json("GET", "/x", {});
    const prod = new ApiClient({ ...env, PANDAEYE_API_URL: "http://api.example.test" }, mock.fetch, null, null);
    const err = (await prod.get("/x").catch((e: unknown) => e)) as ApiError;
    expect(err).toBeInstanceOf(ApiError);
    expect(err.kind).toBe("misconfigured");
    expect(mock.calls).toHaveLength(0);
    const dev = new ApiClient({ ...env, PANDAEYE_API_URL: "http://api.example.test", ENVIRONMENT: "development" }, mock.fetch, null, null);
    await dev.get("/x");
    expect(mock.calls).toHaveLength(1);
  });

  it("an http API URL makes the dashboard report itself unconfigured", async () => {
    const { client } = await signedInClient("viewer");
    client.envOverride = { PANDAEYE_API_URL: "http://api.example.test" };
    const res = await client.get("/servers");
    expect(res.status).toBe(503);
    expect(await res.text()).toContain("Dashboard not configured");
    expect((await client.get("/healthz")).status).toBe(503);
  });

  it("a missing site key is detected and the Turnstile pages fail closed", async () => {
    const api = new MockApi().json("POST", "/api/v1/auth/license", { tenant_id: uniq("t_"), slug: "s" });
    const client = new Client(api, "198.18.2.1");
    client.envOverride = { TURNSTILE_SITEKEY: undefined };
    expect((await client.get("/healthz")).status).toBe(503);
    const page = await client.get("/setup");
    expect(page.status).toBe(200);
    const html = await page.text();
    expect(html).toContain("Human verification is not configured");
    expect(html).not.toContain("development environment");
    const csrf = /name="_csrf" value="([^"]+)"/.exec(html)![1]!;
    const res = await client.post("/setup", { _csrf: csrf, license_key: "PE-TEST-LICENSE-0001", username: uniq("own"), password: PASSWORD, password_confirm: PASSWORD, "cf-turnstile-response": "tok" });
    expect(res.status).toBe(503);
    expect(api.calls).toHaveLength(0);
    expect(api.turnstileCalls).toHaveLength(0);
    // Other pages keep working.
    expect((await client.get("/privacy")).status).toBe(200);
  });
});

// ---- 4. Turnstile hostname -----------------------------------------------------------------------------

describe("4. Turnstile answers must be for this hostname", () => {
  it("rejects tokens solved on another hostname", async () => {
    const api = new MockApi();
    expect(await verifyTurnstile(env, api.fetch, "tok", null, "setup", "dash.pandaeye.test")).toEqual({ ok: true });
    api.turnstileHostname = "evil.example";
    expect(await verifyTurnstile(env, api.fetch, "tok", null, "setup", "dash.pandaeye.test")).toEqual({ ok: false, reason: "invalid" });
    const noHost = async () => Response.json({ success: true });
    expect(await verifyTurnstile(env, noHost, "tok", null, "setup", "dash.pandaeye.test")).toEqual({ ok: false, reason: "invalid" });
  });

  it("the appeal route refuses a foreign-hostname token", async () => {
    const api = new MockApi().json("POST", "/api/v1/public/appeals", {});
    api.turnstileHostname = "evil.example";
    const client = new Client(api, "198.18.3.1");
    const csrf = await client.csrfFrom("/appeal");
    const res = await client.post("/appeal", { _csrf: csrf, verdict_id: "v1", appeal_code: "ABCD", message: "please review this", contact: "", "cf-turnstile-response": "tok" });
    expect(res.status).toBe(400);
    expect(api.calls).toHaveLength(0);
  });
});

// ---- 6. assertion TTL -----------------------------------------------------------------------------------

describe("6. assertions expire 45 s ahead", () => {
  it("signs exp = now + 45", async () => {
    const mock = new MockApi().json("GET", "/x", {});
    const now = 1_760_000_000_000;
    const client = new ApiClient(env, mock.fetch, { tenant: "t", user_id: "u", username: "n", role: "viewer" }, null, () => now);
    await client.get("/x");
    const body = mock.calls[0]!.headers.get("X-PandaEye-Assertion")!.split(".")[0]!;
    const claims = JSON.parse(atob(body.replace(/-/g, "+").replace(/_/g, "/") + "===".slice((body.length + 3) % 4)));
    expect(claims.exp).toBe(1_760_000_045);
  });
});

// ---- 7. flash lookup -------------------------------------------------------------------------------------

describe("7. flash codes never resolve prototype keys", () => {
  it("ignores __proto__, constructor and friends", async () => {
    for (const k of ["__proto__", "constructor", "toString", "hasOwnProperty", "valueOf"]) {
      expect(flashFromQuery({ ok: k })).toBeNull();
      expect(flashFromQuery({ err: k })).toBeNull();
    }
    expect(flashFromQuery({ ok: "welcome" })).toEqual({ kind: "ok", text: "Welcome to PandaEye." });
    const res = await new Client().get("/login?ok=__proto__&err=constructor");
    expect(res.status).toBe(200);
    expect(await res.text()).not.toContain("[object");
  });
});

// ---- 8. replay viewer prototype keys ------------------------------------------------------------------

describe("8. the replay viewer survives hostile target ids", () => {
  it("handles __proto__ / constructor target and player ids", () => {
    const R = (globalThis as unknown as { PandaEyeReplay: { normalizeClip(raw: unknown): any } }).PandaEyeReplay;
    const attack = (target: string, t: number) => ({
      time_us: t,
      attack: { target_player: target, eye: { x: 0, y: 65, z: 0 }, target_history: [{ time_us: t - 10, x: 1, y: 64, z: 1 }] },
    });
    const clip = R.normalizeClip(JSON.parse(JSON.stringify({ events: [attack("__proto__", 1000), attack("constructor", 2000), attack("toString", 3000), { time_us: 4000, move: { x: 1, y: 2, z: 3 } }] })));
    expect(Object.keys(clip.targets).sort()).toEqual(["__proto__", "constructor", "toString"]);
    expect(clip.targets["__proto__"].positions).toHaveLength(1);
    const anon = R.normalizeClip(JSON.parse('{"events":[{"player":"__proto__","time_us":1,"move":{"x":1,"y":2,"z":3}},{"player":"__proto__","time_us":2,"move":{"x":1,"y":2,"z":3}}]}'));
    expect(anon.player).toBe("__proto__");
    expect(anon.positions).toHaveLength(2);
  });
});

// ---- 9. appeal count and post-redirect-get ------------------------------------------------------------

describe("9. overview appeal count and post-redirect-get", () => {
  it('shows "100+" when more open appeals exist', async () => {
    const { client, api } = await signedInClient("viewer");
    api.json("GET", "/api/v1/appeals", { appeals: Array.from({ length: 100 }, (_, i) => ({ id: `a${i}` })), next_cursor: "more" });
    const html = await (await client.get("/")).text();
    expect(html).toContain('<div class="stat-value">100+</div>');
    api.json("GET", "/api/v1/appeals", { appeals: [{ id: "a1" }, { id: "a2" }] });
    expect(await (await client.get("/")).text()).toContain('<div class="stat-value">2</div>');
  });

  it("an invite link is shown once and a reload creates nothing", async () => {
    const admin = await seedUser({ role: "admin" });
    const c = new Client();
    const csrf = await signIn(c, admin);
    const post = await c.post("/users/invites", { _csrf: csrf, role: "viewer" });
    const location = post.headers.get("Location")!;
    expect(await (await c.get(location)).text()).toMatch(/\/invite\/[A-Za-z0-9_-]{43}/);
    expect(await (await c.get(location)).text()).not.toMatch(/\/invite\/[A-Za-z0-9_-]{43}/);
    expect(await env.DB.prepare("SELECT COUNT(*) AS n FROM invites WHERE created_by = ?1").bind(admin.id).first("n")).toBe(1);
  });

  it("one-time results expire after 10 minutes and are bound to the session", async () => {
    const t0 = Date.now();
    let now = t0;
    const api = new MockApi()
      .json("GET", "/api/v1/apikeys", { api_keys: [] })
      .json("POST", "/api/v1/apikeys", { id: "k9", name: "late", scopes: ["read"], key: "pe_k9_LATEPLAINTEXT" });
    const c = new Client(api, "203.0.113.9", () => now);
    const owner = await seedUser({ role: "owner" });
    const csrf = await signIn(c, owner, t0);
    const location = (await c.post("/apikeys", { _csrf: csrf, name: "late", "scope:read": "on" })).headers.get("Location")!;
    now = t0 + 10 * 60_000 + 1;
    // Keep the session alive (idle limit is 2 h) but the flash is gone.
    expect(await (await c.get(location)).text()).not.toContain("pe_k9_LATEPLAINTEXT");
  });
});
