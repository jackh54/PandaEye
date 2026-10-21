import { env } from "cloudflare:workers";
import { describe, expect, it } from "vitest";
import { sha256Hex } from "../src/lib/crypto";
import { SESSION_COOKIE } from "../src/lib/session";
import { Client, MockApi, PASSWORD, seedUser, signIn, uniq } from "./helpers";

function licenseApi(tenantId: string) {
  return new MockApi().json("POST", "/api/v1/auth/license", { tenant_id: tenantId, slug: "net", created: true });
}

async function setupForm(client: Client, extra: Record<string, string> = {}) {
  const csrf = await client.csrfFrom("/setup");
  return {
    _csrf: csrf,
    license_key: "PE-TEST-LICENSE-0001",
    username: uniq("owner"),
    password: PASSWORD,
    password_confirm: PASSWORD,
    "cf-turnstile-response": "turnstile-token",
    ...extra,
  };
}

describe("/setup", () => {
  it("verifies Turnstile and the license, creates the owner and signs in", async () => {
    const tenant = uniq("t_");
    const api = licenseApi(tenant);
    const client = new Client(api, "198.51.100.10");
    const form = await setupForm(client);
    const res = await client.post("/setup", form);
    expect(res.status).toBe(303);
    expect(res.headers.get("Location")).toBe("/?ok=welcome");
    expect(client.cookies.has(SESSION_COOKIE)).toBe(true);
    expect(api.turnstileCalls).toHaveLength(1);
    expect(api.turnstileCalls[0]!.get("secret")).toBe(env.TURNSTILE_SECRET);
    expect(api.turnstileCalls[0]!.get("remoteip")).toBe("198.51.100.10");
    const call = api.callsTo("POST", "/api/v1/auth/license")[0]!;
    expect(call.body).toEqual({ license_key: "PE-TEST-LICENSE-0001" });
    expect(call.headers.get("Authorization")).toBe(`Bearer ${env.PANDAEYE_SERVICE_TOKEN}`);
    expect(call.headers.get("X-PandaEye-Assertion")).toBeNull();
    const row = await env.DB.prepare("SELECT role, tenant_id, password_hash FROM users WHERE username = ?1").bind(form.username).first<{ role: string; tenant_id: string; password_hash: string }>();
    expect(row).toMatchObject({ role: "owner", tenant_id: tenant });
    expect(row!.password_hash).toMatch(/^pbkdf2-sha256\$100000\$/);
  });

  it("works only while the tenant has zero users", async () => {
    const tenant = uniq("t_");
    await seedUser({ tenantId: tenant, role: "staff" });
    const client = new Client(licenseApi(tenant), "198.51.100.11");
    const res = await client.post("/setup", await setupForm(client));
    expect(res.status).toBe(409);
    expect(await res.text()).toContain("already has an owner");
    expect(await env.DB.prepare("SELECT COUNT(*) AS n FROM users WHERE tenant_id = ?1").bind(tenant).first("n")).toBe(1);
  });

  it("lets exactly one of two concurrent setups win", async () => {
    const tenant = uniq("t_");
    const a = new Client(licenseApi(tenant), "198.51.100.12");
    const b = new Client(licenseApi(tenant), "198.51.100.13");
    const [fa, fb] = [await setupForm(a), await setupForm(b)];
    const [ra, rb] = await Promise.all([a.post("/setup", fa), b.post("/setup", fb)]);
    expect([ra.status, rb.status].sort()).toEqual([303, 409]);
    expect(await env.DB.prepare("SELECT COUNT(*) AS n FROM users WHERE tenant_id = ?1").bind(tenant).first("n")).toBe(1);
  });

  it("rejects failed Turnstile and invalid licenses, and survives the API being down", async () => {
    const api = new MockApi().json("POST", "/api/v1/auth/license", { error: "invalid license", code: "unauthenticated" }, 401);
    const client = new Client(api, "198.51.100.14");
    api.turnstileOk = false;
    const ts = await client.post("/setup", await setupForm(client));
    expect(ts.status).toBe(400);
    expect(api.calls).toHaveLength(0);
    api.turnstileOk = true;
    const bad = await client.post("/setup", await setupForm(client));
    expect(bad.status).toBe(400);
    expect(await bad.text()).toContain("license key was not accepted");
    api.down = true;
    const down = await client.post("/setup", await setupForm(client));
    expect(down.status).toBe(503);
    expect(await down.text()).toContain("PandaEye API is unavailable");
  });

  it("is rate limited to 5 attempts per hour per IP", async () => {
    const api = new MockApi().json("POST", "/api/v1/auth/license", { error: "invalid" }, 401);
    const client = new Client(api, "198.51.100.15");
    for (let i = 0; i < 5; i++) expect((await client.post("/setup", await setupForm(client))).status).toBe(400);
    const sixth = await client.post("/setup", await setupForm(client));
    expect(sixth.status).toBe(429);
    expect(api.calls).toHaveLength(5);
    // Another IP is unaffected.
    const other = new Client(api, "198.51.100.16");
    expect((await other.post("/setup", await setupForm(other))).status).toBe(400);
  });

  it("validates the form before calling anything", async () => {
    const api = licenseApi(uniq("t_"));
    const client = new Client(api, "198.51.100.17");
    expect((await client.post("/setup", await setupForm(client, { password_confirm: "different password" }))).status).toBe(400);
    expect((await client.post("/setup", await setupForm(client, { username: "Bad Name!" }))).status).toBe(400);
    expect((await client.post("/setup", await setupForm(client, { password: "short", password_confirm: "short" }))).status).toBe(400);
    expect(api.calls).toHaveLength(0);
    expect(api.turnstileCalls).toHaveLength(0);
  });
});

describe("invites", () => {
  it("admin creates a single-use hashed invite that creates a user with the role", async () => {
    const admin = await seedUser({ role: "admin" });
    const adminClient = new Client();
    const csrf = await signIn(adminClient, admin);
    const page = await adminClient.post("/users/invites", { _csrf: csrf, role: "staff" });
    expect(page.status).toBe(200);
    const html = await page.text();
    const link = /value="(https:\/\/dash\.pandaeye\.test\/invite\/([A-Za-z0-9_-]{43}))"/.exec(html);
    expect(link).not.toBeNull();
    const token = link![2]!;
    const stored = await env.DB.prepare("SELECT token_hash, role, tenant_id, expires_at, created_at FROM invites WHERE tenant_id = ?1").bind(admin.tenantId).first<{ token_hash: string; role: string; tenant_id: string; expires_at: number; created_at: number }>();
    expect(stored!.token_hash).toBe(await sha256Hex(token));
    expect(stored!.role).toBe("staff");
    expect(stored!.expires_at - stored!.created_at).toBe(48 * 3600_000);

    const invitee = new Client();
    const icsrf = await invitee.csrfFrom(`/invite/${token}`);
    const username = uniq("new");
    const res = await invitee.post(`/invite/${token}`, { _csrf: icsrf, username, password: PASSWORD, password_confirm: PASSWORD });
    expect(res.status).toBe(303);
    const row = await env.DB.prepare("SELECT role, tenant_id FROM users WHERE username = ?1").bind(username).first();
    expect(row).toEqual({ role: "staff", tenant_id: admin.tenantId });

    const again = new Client();
    expect((await again.get(`/invite/${token}`)).status).toBe(404);
  });

  it("admins cannot invite admins or owners; staff cannot invite", async () => {
    const admin = await seedUser({ role: "admin" });
    const c = new Client();
    const csrf = await signIn(c, admin);
    expect((await c.post("/users/invites", { _csrf: csrf, role: "admin" })).status).toBe(400);
    expect((await c.post("/users/invites", { _csrf: csrf, role: "owner" })).status).toBe(400);
    const staff = await seedUser({ role: "staff" });
    const s = new Client();
    const scsrf = await signIn(s, staff);
    expect((await s.post("/users/invites", { _csrf: scsrf, role: "viewer" })).status).toBe(403);
  });

  it("rejects expired invites", async () => {
    const admin = await seedUser({ role: "admin" });
    const token = "A".repeat(43);
    await env.DB.prepare("INSERT INTO invites (token_hash, tenant_id, role, expires_at, created_by, created_at) VALUES (?1, ?2, 'viewer', ?3, ?4, ?3)")
      .bind(await sha256Hex(token), admin.tenantId, Date.now() - 1, admin.id)
      .run();
    const c = new Client();
    expect((await c.get(`/invite/${token}`)).status).toBe(404);
  });

  it("owner manages roles and disables users of the own tenant only", async () => {
    const owner = await seedUser({ role: "owner" });
    const member = await seedUser({ tenantId: owner.tenantId, role: "viewer" });
    const foreign = await seedUser({ role: "viewer" });
    const c = new Client();
    const csrf = await signIn(c, owner);
    expect((await c.post(`/users/${member.id}/role`, { _csrf: csrf, role: "staff" })).status).toBe(303);
    expect(await env.DB.prepare("SELECT role FROM users WHERE id = ?1").bind(member.id).first("role")).toBe("staff");
    expect((await c.post(`/users/${foreign.id}/role`, { _csrf: csrf, role: "admin" })).status).toBe(404);
    expect(await env.DB.prepare("SELECT role FROM users WHERE id = ?1").bind(foreign.id).first("role")).toBe("viewer");
    expect((await c.post(`/users/${owner.id}/role`, { _csrf: csrf, role: "viewer" })).status).toBe(400);
    expect((await c.post(`/users/${member.id}/disable`, { _csrf: csrf })).status).toBe(303);
    expect(await env.DB.prepare("SELECT disabled FROM users WHERE id = ?1").bind(member.id).first("disabled")).toBe(1);
    // Admins cannot manage users.
    const admin = await seedUser({ tenantId: owner.tenantId, role: "admin" });
    const a = new Client();
    const acsrf = await signIn(a, admin);
    expect((await a.post(`/users/${member.id}/enable`, { _csrf: acsrf })).status).toBe(403);
  });
});

describe("login rate limiting through the route", () => {
  it("allows 10 attempts per 15 minutes per IP", async () => {
    const client = new Client(new MockApi(), "192.0.2.50");
    const csrf = await client.csrfFrom("/login");
    for (let i = 0; i < 10; i++) {
      const r = await client.post("/login", { _csrf: csrf, username: uniq("x"), password: "wrong password" });
      expect(r.status).toBe(401);
    }
    const blocked = await client.post("/login", { _csrf: csrf, username: "whoever", password: "wrong password" });
    expect(blocked.status).toBe(429);
    expect(blocked.headers.get("Retry-After")).toMatch(/^\d+$/);
  });

  it("backs off per username across IPs after 3 failures", async () => {
    const u = await seedUser();
    for (let i = 0; i < 3; i++) {
      const c = new Client(new MockApi(), `192.0.2.${100 + i}`);
      const csrf = await c.csrfFrom("/login");
      expect((await c.post("/login", { _csrf: csrf, username: u.username, password: "wrong password" })).status).toBe(401);
    }
    const c = new Client(new MockApi(), "192.0.2.200");
    const csrf = await c.csrfFrom("/login");
    const locked = await c.post("/login", { _csrf: csrf, username: u.username, password: PASSWORD });
    expect(locked.status).toBe(429);
    expect(await locked.text()).toContain("Too many failed attempts for this account");
  });
});
