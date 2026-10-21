import { env } from "cloudflare:workers";
import { describe, expect, it } from "vitest";
import { sha256Hex } from "../src/lib/crypto";
import {
  createSession,
  loadSession,
  SESSION_ABSOLUTE_MS,
  SESSION_COOKIE,
  SESSION_IDLE_MS,
  SESSION_TOUCH_MS,
} from "../src/lib/session";
import { setUserDisabled, setUserPassword, setUserRole } from "../src/lib/users";
import { Client, MockApi, PASSWORD, seedUser, signIn, tenantFixture } from "./helpers";

describe("session lifecycle", () => {
  it("stores only sha256(id) and loads by the raw id", async () => {
    const u = await seedUser({ role: "staff" });
    const now = Date.now();
    const s = await createSession(env.DB, u.id, now);
    expect(s.id).toMatch(/^[A-Za-z0-9_-]{43}$/);
    const rows = await env.DB.prepare("SELECT id_hash FROM sessions WHERE user_id = ?1").bind(u.id).all<{ id_hash: string }>();
    expect(rows.results.map((r) => r.id_hash)).toEqual([await sha256Hex(s.id)]);
    expect(JSON.stringify(rows.results)).not.toContain(s.id);
    const loaded = await loadSession(env.DB, s.id, now + 1000);
    expect(loaded?.user).toMatchObject({ id: u.id, username: u.username, role: "staff", tenantId: u.tenantId });
    expect(loaded?.csrf).toBe(s.csrf);
    expect(await loadSession(env.DB, s.id.slice(1) + "A", now)).toBeNull();
    expect(await loadSession(env.DB, "not-a-session", now)).toBeNull();
  });

  it("expires after 2 h idle and refreshes last_seen_at on use", async () => {
    const u = await seedUser();
    const t0 = Date.now();
    const s = await createSession(env.DB, u.id, t0);
    const t1 = t0 + SESSION_IDLE_MS - 1000;
    expect(await loadSession(env.DB, s.id, t1)).not.toBeNull(); // touches
    const t2 = t1 + SESSION_IDLE_MS - 1000;
    expect(await loadSession(env.DB, s.id, t2)).not.toBeNull();
    expect(await loadSession(env.DB, s.id, t2 + SESSION_IDLE_MS + 1)).toBeNull();
    // Deleted on expiry.
    expect(await env.DB.prepare("SELECT COUNT(*) AS n FROM sessions WHERE user_id = ?1").bind(u.id).first("n")).toBe(0);
  });

  it("does not write on every request", async () => {
    const u = await seedUser();
    const t0 = Date.now();
    const s = await createSession(env.DB, u.id, t0);
    await loadSession(env.DB, s.id, t0 + SESSION_TOUCH_MS - 1);
    expect(await env.DB.prepare("SELECT last_seen_at FROM sessions WHERE user_id = ?1").bind(u.id).first("last_seen_at")).toBe(t0);
    await loadSession(env.DB, s.id, t0 + SESSION_TOUCH_MS);
    expect(await env.DB.prepare("SELECT last_seen_at FROM sessions WHERE user_id = ?1").bind(u.id).first("last_seen_at")).toBe(t0 + SESSION_TOUCH_MS);
  });

  it("expires 12 h after creation even when active", async () => {
    const u = await seedUser();
    const t0 = Date.now();
    const s = await createSession(env.DB, u.id, t0);
    let t = t0;
    while (t < t0 + SESSION_ABSOLUTE_MS - 3600_000) {
      t += 3600_000;
      expect(await loadSession(env.DB, s.id, t)).not.toBeNull();
    }
    expect(await loadSession(env.DB, s.id, t0 + SESSION_ABSOLUTE_MS)).toBeNull();
  });

  it("is deleted on role change, disable and password change", async () => {
    for (const change of ["role", "disable", "password"] as const) {
      const u = await seedUser({ role: "staff" });
      const s1 = await createSession(env.DB, u.id, Date.now());
      const s2 = await createSession(env.DB, u.id, Date.now());
      if (change === "role") await setUserRole(env.DB, u.tenantId, u.id, "viewer", "user:test", Date.now());
      if (change === "disable") await setUserDisabled(env.DB, u.tenantId, u.id, true, "user:test", Date.now());
      if (change === "password") await setUserPassword(env.DB, u, "pbkdf2-sha256$100000$AAAAAAAAAAAAAAAAAAAAAA==$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", Date.now());
      expect(await loadSession(env.DB, s1.id, Date.now())).toBeNull();
      expect(await loadSession(env.DB, s2.id, Date.now())).toBeNull();
    }
  });

  it("never loads a session of a disabled user", async () => {
    const u = await seedUser();
    const s = await createSession(env.DB, u.id, Date.now());
    await env.DB.prepare("UPDATE users SET disabled = 1 WHERE id = ?1").bind(u.id).run();
    expect(await loadSession(env.DB, s.id, Date.now())).toBeNull();
  });
});

describe("login flow", () => {
  it("signs in, sets a hardened cookie, rotates the session, and logs out", async () => {
    const api = new MockApi().json("GET", "/api/v1/tenant", tenantFixture());
    const client = new Client(api);
    const u = await seedUser({ role: "admin" });

    const csrf = await client.csrfFrom("/login?next=%2Fsettings");
    // A pre-existing session in the browser is rotated away on login.
    const old = await createSession(env.DB, u.id, Date.now());
    client.cookies.set(SESSION_COOKIE, old.id);

    const res = await client.post("/login", { _csrf: csrf, username: u.username.toUpperCase(), password: PASSWORD, next: "/settings" });
    expect(res.status).toBe(303);
    expect(res.headers.get("Location")).toBe("/settings");
    const cookie = res.headers.getSetCookie().find((c) => c.startsWith(`${SESSION_COOKIE}=`))!;
    expect(cookie).toMatch(/; HttpOnly/i);
    expect(cookie).toMatch(/; Secure/i);
    expect(cookie).toMatch(/; SameSite=Lax/i);
    expect(cookie).toMatch(/; Path=\//i);
    expect(cookie).not.toMatch(/Domain=/i);
    const newId = client.cookies.get(SESSION_COOKIE)!;
    expect(newId).not.toBe(old.id);
    expect(await loadSession(env.DB, old.id, Date.now())).toBeNull();
    expect(await loadSession(env.DB, newId, Date.now())).not.toBeNull();

    const page = await client.get("/settings");
    expect(page.status).toBe(200);

    const logoutCsrf = await client.csrfFrom("/settings");
    const out = await client.post("/logout", { _csrf: logoutCsrf });
    expect(out.status).toBe(303);
    expect(await loadSession(env.DB, newId, Date.now())).toBeNull();
    expect((await client.get("/settings")).status).toBe(303);
  });

  it("rejects wrong passwords, unknown users and disabled accounts with generic errors", async () => {
    const client = new Client();
    const u = await seedUser();
    const d = await seedUser({ disabled: true });
    const csrf = await client.csrfFrom("/login");
    const wrong = await client.post("/login", { _csrf: csrf, username: u.username, password: "wrong password!!" });
    expect(wrong.status).toBe(401);
    expect(await wrong.text()).toContain("Invalid username or password.");
    const unknown = await client.post("/login", { _csrf: csrf, username: "nobody-here", password: "wrong password!!" });
    expect(unknown.status).toBe(401);
    expect(await unknown.text()).toContain("Invalid username or password.");
    const disabled = await client.post("/login", { _csrf: csrf, username: d.username, password: PASSWORD });
    expect(disabled.status).toBe(403);
    expect(client.cookies.has(SESSION_COOKIE)).toBe(false);
  });

  it("redirects only to same-origin paths after login", async () => {
    const client = new Client();
    const u = await seedUser();
    const csrf = await client.csrfFrom("/login");
    const res = await client.post("/login", { _csrf: csrf, username: u.username, password: PASSWORD, next: "//evil.example/x" });
    expect(res.headers.get("Location")).toBe("/");
  });

  it("redirects anonymous visitors to /login with next", async () => {
    const res = await new Client().get("/verdicts?action=ban");
    expect(res.status).toBe(303);
    expect(res.headers.get("Location")).toBe("/login?next=%2Fverdicts%3Faction%3Dban");
  });

  it("changing the password signs out other sessions and keeps this one", async () => {
    const client = new Client();
    const u = await seedUser();
    const other = await createSession(env.DB, u.id, Date.now());
    const csrf = await signIn(client, u);
    const res = await client.post("/account/password", { _csrf: csrf, current: PASSWORD, password: "a brand new password", password_confirm: "a brand new password" });
    expect(res.status).toBe(303);
    expect(res.headers.get("Location")).toBe("/account?ok=password_changed");
    expect(await loadSession(env.DB, other.id, Date.now())).toBeNull();
    expect(await loadSession(env.DB, client.cookies.get(SESSION_COOKIE), Date.now())).not.toBeNull();
    const fresh = new Client();
    const c2 = await fresh.csrfFrom("/login");
    expect((await fresh.post("/login", { _csrf: c2, username: u.username, password: "a brand new password" })).status).toBe(303);
  });
});
