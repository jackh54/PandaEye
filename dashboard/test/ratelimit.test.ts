import { env } from "cloudflare:workers";
import { describe, expect, it } from "vitest";
import {
  backoffDelayMs,
  claimBackoff,
  clearBackoff,
  hitFixedWindow,
  ipKey,
  ipPrefix,
  recordBackoffFailure,
} from "../src/lib/ratelimit";
import { uniq } from "./helpers";

describe("fixed window", () => {
  it("allows `limit` hits per window, then blocks until the window resets", async () => {
    const key = uniq("fw:");
    const t0 = 1_000_000;
    for (let i = 1; i <= 10; i++) {
      const r = await hitFixedWindow(env.DB, key, 10, 15 * 60_000, t0 + i);
      expect(r.allowed).toBe(true);
      expect(r.count).toBe(i);
    }
    const blocked = await hitFixedWindow(env.DB, key, 10, 15 * 60_000, t0 + 60_000);
    expect(blocked.allowed).toBe(false);
    expect(blocked.retryAfterS).toBeGreaterThan(0);
    expect(blocked.retryAfterS).toBeLessThanOrEqual(15 * 60);
    const reset = await hitFixedWindow(env.DB, key, 10, 15 * 60_000, t0 + 15 * 60_000 + 1);
    expect(reset.allowed).toBe(true);
    expect(reset.count).toBe(1);
  });
});

describe("per-username exponential backoff", () => {
  it("computes 0,0,0,5s,10s,20s,... capped at 30 min", () => {
    expect([0, 1, 2].map(backoffDelayMs)).toEqual([0, 0, 0]);
    expect(backoffDelayMs(3)).toBe(5_000);
    expect(backoffDelayMs(4)).toBe(10_000);
    expect(backoffDelayMs(5)).toBe(20_000);
    expect(backoffDelayMs(12)).toBe(30 * 60_000);
    expect(backoffDelayMs(1000)).toBe(30 * 60_000);
  });

  it("locks after repeated failures and clears on success", async () => {
    const key = uniq("bo:");
    let t = 5_000_000;
    for (let i = 0; i < 3; i++) {
      t += 2_000;
      expect((await claimBackoff(env.DB, key, t)).allowed).toBe(true);
      await recordBackoffFailure(env.DB, key, t);
    }
    // 3 failures: locked for 5 s.
    const locked = await claimBackoff(env.DB, key, t + 1_000);
    expect(locked.allowed).toBe(false);
    expect(locked.retryAfterS).toBeGreaterThanOrEqual(3);
    expect((await claimBackoff(env.DB, key, t + 5_001)).allowed).toBe(true);
    await recordBackoffFailure(env.DB, key, t + 5_001);
    // 4 failures: 10 s.
    expect((await claimBackoff(env.DB, key, t + 5_001 + 9_000)).allowed).toBe(false);
    expect((await claimBackoff(env.DB, key, t + 5_001 + 10_001)).allowed).toBe(true);
    await clearBackoff(env.DB, key);
    expect((await claimBackoff(env.DB, key, t + 5_001 + 10_002)).allowed).toBe(true);
  });

  it("gives a burst of parallel attempts exactly one claim", async () => {
    const key = uniq("par:");
    const t = 9_000_000;
    const results = await Promise.all(Array.from({ length: 8 }, () => claimBackoff(env.DB, key, t)));
    expect(results.filter((r) => r.allowed)).toHaveLength(1);
  });

  it("forgets failures after a day", async () => {
    const key = uniq("day:");
    let t = 20_000_000;
    for (let i = 0; i < 6; i++) {
      t += 60 * 60_000;
      await claimBackoff(env.DB, key, t);
      await recordBackoffFailure(env.DB, key, t);
    }
    const next = await claimBackoff(env.DB, key, t + 24 * 60 * 60_000 + 1);
    expect(next.allowed).toBe(true);
    expect(next.failures).toBe(0);
  });
});

describe("IP keys", () => {
  it("keeps IPv4 whole and IPv6 at /64", () => {
    expect(ipPrefix("203.0.113.7")).toBe("203.0.113.7");
    expect(ipPrefix("2001:db8:1:2:3:4:5:6")).toBe("2001:db8:1:2::/64");
    expect(ipPrefix("2001:db8::1")).toBe("2001:db8:0:0::/64");
    expect(ipPrefix("2001:0DB8:0001:0002::")).toBe("2001:db8:1:2::/64");
  });

  it("never stores the raw IP", async () => {
    const k = await ipKey("login", "203.0.113.7", "pepper");
    expect(k).not.toContain("203.0.113.7");
    expect(k).toMatch(/^login:ip:[0-9a-f]{32}$/);
    expect(await ipKey("login", "2001:db8:1:2::1", "pepper")).toBe(await ipKey("login", "2001:db8:1:2:ffff::9", "pepper"));
  });
});
