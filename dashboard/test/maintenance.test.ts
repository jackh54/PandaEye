import { env } from "cloudflare:workers";
import { describe, expect, it } from "vitest";
import { createSession, loadSession } from "../src/lib/session";
import { verifyTurnstile } from "../src/lib/turnstile";
import { cleanup } from "../src/lib/users";
import { MockApi, seedUser, uniq } from "./helpers";

describe("hourly cleanup", () => {
  it("removes expired sessions, stale invites and old rate-limit rows", async () => {
    const u = await seedUser();
    const now = Date.now();
    const old = await createSession(env.DB, u.id, now - 13 * 3600_000);
    const live = await createSession(env.DB, u.id, now);
    const key = uniq("cleanup:");
    await env.DB.prepare("INSERT INTO login_attempts (key, count, window_start, locked_until) VALUES (?1, 3, ?2, 0)").bind(key, now - 2 * 86_400_000).run();
    await cleanup(env.DB, now);
    expect(await env.DB.prepare("SELECT COUNT(*) AS n FROM sessions WHERE user_id = ?1").bind(u.id).first("n")).toBe(1);
    expect(await loadSession(env.DB, live.id, now)).not.toBeNull();
    expect(await loadSession(env.DB, old.id, now)).toBeNull();
    expect(await env.DB.prepare("SELECT COUNT(*) AS n FROM login_attempts WHERE key = ?1").bind(key).first("n")).toBe(0);
  });
});

describe("Turnstile", () => {
  const api = new MockApi();
  it("fails closed without a secret outside development", async () => {
    expect(await verifyTurnstile({ ...env, TURNSTILE_SECRET: undefined, ENVIRONMENT: "production" }, api.fetch, "tok", null, "setup", "dash.pandaeye.test")).toEqual({ ok: false, reason: "not_configured" });
    expect(await verifyTurnstile({ ...env, TURNSTILE_SECRET: undefined, ENVIRONMENT: "development" }, api.fetch, "tok", null, "setup", "dash.pandaeye.test")).toEqual({ ok: true });
  });

  it("requires a token and a successful siteverify answer", async () => {
    expect(await verifyTurnstile(env, api.fetch, "", null, "setup", "dash.pandaeye.test")).toEqual({ ok: false, reason: "missing" });
    expect(await verifyTurnstile(env, api.fetch, "tok", "192.0.2.1", "setup", "dash.pandaeye.test")).toEqual({ ok: true });
    api.turnstileOk = false;
    expect(await verifyTurnstile(env, api.fetch, "tok", null, "setup", "dash.pandaeye.test")).toEqual({ ok: false, reason: "invalid" });
    const failing = async () => {
      throw new TypeError("network");
    };
    expect(await verifyTurnstile(env, failing, "tok", null, "setup", "dash.pandaeye.test")).toEqual({ ok: false, reason: "unavailable" });
  });
});
