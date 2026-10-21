import { describe, expect, it } from "vitest";
import { b64decode, timingSafeEqual, timingSafeEqualString } from "../src/lib/crypto";
import {
  DUMMY_PASSWORD_HASH,
  hashPassword,
  needsRehash,
  parsePasswordHash,
  passwordProblem,
  PBKDF2_ITERATIONS,
  verifyPassword,
} from "../src/lib/password";

const PEPPER = "unit-test-pepper-0123456789abcdef0123456789";

describe("password hashing", () => {
  it("produces the versioned pbkdf2-sha256$100000$<salt>$<hash> format", async () => {
    const h = await hashPassword("correct horse battery staple", PEPPER);
    const parts = h.split("$");
    expect(parts).toHaveLength(4);
    expect(parts[0]).toBe("pbkdf2-sha256");
    expect(parts[1]).toBe("100000");
    expect(b64decode(parts[2]!)).toHaveLength(16);
    expect(b64decode(parts[3]!)).toHaveLength(32);
    expect(PBKDF2_ITERATIONS).toBe(100_000);
  });

  it("uses a fresh salt every time", async () => {
    const a = await hashPassword("same password here", PEPPER);
    const b = await hashPassword("same password here", PEPPER);
    expect(a).not.toBe(b);
  });

  it("verifies the right password and rejects wrong ones", async () => {
    const h = await hashPassword("correct horse battery staple", PEPPER);
    expect(await verifyPassword("correct horse battery staple", h, PEPPER)).toBe(true);
    expect(await verifyPassword("correct horse battery stapl", h, PEPPER)).toBe(false);
    expect(await verifyPassword("", h, PEPPER)).toBe(false);
  });

  it("hashes HMAC(pepper, password): a different pepper does not verify", async () => {
    const h = await hashPassword("correct horse battery staple", PEPPER);
    expect(await verifyPassword("correct horse battery staple", h, PEPPER + "x")).toBe(false);
  });

  it("matches an independent PBKDF2(HMAC(pepper, pw)) computation", async () => {
    const h = await hashPassword("reference check pw", PEPPER);
    const [, , saltB64, hashB64] = h.split("$");
    const enc = new TextEncoder();
    const hk = await crypto.subtle.importKey("raw", enc.encode(PEPPER), { name: "HMAC", hash: "SHA-256" }, false, ["sign"]);
    const pre = new Uint8Array(await crypto.subtle.sign("HMAC", hk, enc.encode("reference check pw")));
    const pk = await crypto.subtle.importKey("raw", pre, "PBKDF2", false, ["deriveBits"]);
    const bits = new Uint8Array(
      await crypto.subtle.deriveBits({ name: "PBKDF2", hash: "SHA-256", salt: b64decode(saltB64!), iterations: 100000 }, pk, 256),
    );
    expect(bits).toEqual(b64decode(hashB64!));
  });

  it("never verifies malformed or out-of-range hashes", async () => {
    const good = await hashPassword("x".repeat(12) + "y", PEPPER);
    const [, , salt, hash] = good.split("$");
    for (const bad of [
      "",
      "plain",
      `bcrypt$100000$${salt}$${hash}`,
      `pbkdf2-sha256$200000$${salt}$${hash}`,
      `pbkdf2-sha256$0$${salt}$${hash}`,
      `pbkdf2-sha256$abc$${salt}$${hash}`,
      `pbkdf2-sha256$100000$!!!$${hash}`,
      `pbkdf2-sha256$100000$${salt}`,
    ]) {
      expect(parsePasswordHash(bad)).toBeNull();
      expect(await verifyPassword("x".repeat(12) + "y", bad, PEPPER)).toBe(false);
    }
  });

  it("the dummy hash never verifies", async () => {
    expect(parsePasswordHash(DUMMY_PASSWORD_HASH)).not.toBeNull();
    expect(await verifyPassword("", DUMMY_PASSWORD_HASH, PEPPER)).toBe(false);
    expect(await verifyPassword("anything at all", DUMMY_PASSWORD_HASH, PEPPER)).toBe(false);
  });

  it("flags hashes with old parameters for rehash", async () => {
    const h = await hashPassword("some long password", PEPPER);
    expect(needsRehash(h)).toBe(false);
    expect(needsRehash(h.replace("$100000$", "$50000$"))).toBe(true);
    expect(needsRehash("garbage")).toBe(true);
  });

  it("enforces a minimum password policy", () => {
    expect(passwordProblem("short")).toMatch(/at least/);
    expect(passwordProblem("aaaaaaaaaaaaaaaa")).toMatch(/repeat/);
    expect(passwordProblem("a fine long password")).toBeNull();
    expect(passwordProblem("x".repeat(2000))).toMatch(/at most/);
  });
});

describe("constant-time comparison", () => {
  it("compares equal and unequal buffers", () => {
    expect(timingSafeEqual(new Uint8Array([1, 2, 3]), new Uint8Array([1, 2, 3]))).toBe(true);
    expect(timingSafeEqual(new Uint8Array([1, 2, 3]), new Uint8Array([1, 2, 4]))).toBe(false);
    expect(timingSafeEqual(new Uint8Array([1, 2]), new Uint8Array([1, 2, 3]))).toBe(false);
    expect(timingSafeEqual(new Uint8Array([]), new Uint8Array([1]))).toBe(false);
    expect(timingSafeEqual(new Uint8Array([]), new Uint8Array([]))).toBe(true);
    expect(timingSafeEqualString("token", "token")).toBe(true);
    expect(timingSafeEqualString("token", "tokem")).toBe(false);
  });
});
