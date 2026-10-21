import { env } from "cloudflare:workers";
import { describe, expect, it } from "vitest";
import { ApiClient, ApiError } from "../src/lib/api";
import { ASSERTION_TTL_S, buildClaims, signAssertion, type Principal } from "../src/lib/assertion";
import { MockApi } from "./helpers";

const KEY = "assertion-key-for-unit-tests-0123456789";
const principal: Principal = { tenant: "t_abc", user_id: "u_1", username: "alice", role: "staff" };

function b64urlToBytes(s: string): Uint8Array {
  const b = atob(s.replace(/-/g, "+").replace(/_/g, "/") + "===".slice((s.length + 3) % 4));
  return Uint8Array.from(b, (ch) => ch.charCodeAt(0));
}

/** Reference verifier written independently of src/lib (what the Go API does). */
async function referenceVerify(key: string, assertion: string): Promise<Record<string, unknown> | null> {
  const parts = assertion.split(".");
  if (parts.length !== 2) return null;
  const [body, mac] = parts as [string, string];
  if (/[=+/]/.test(body) || /[=+/]/.test(mac)) return null; // base64url without padding
  const k = await crypto.subtle.importKey("raw", new TextEncoder().encode(key), { name: "HMAC", hash: "SHA-256" }, false, ["verify"]);
  const ok = await crypto.subtle.verify("HMAC", k, b64urlToBytes(mac), new TextEncoder().encode(body));
  if (!ok) return null;
  return JSON.parse(new TextDecoder().decode(b64urlToBytes(body)));
}

describe("assertion signing", () => {
  it("signs base64url(json).base64url(hmac) that a reference HMAC verifies", async () => {
    const now = 1_760_000_000_000;
    const a = await signAssertion(KEY, buildClaims(principal, "get", "/api/v1/verdicts", now));
    const claims = await referenceVerify(KEY, a);
    expect(claims).toEqual({
      tenant: "t_abc",
      user_id: "u_1",
      username: "alice",
      role: "staff",
      method: "GET",
      path: "/api/v1/verdicts",
      exp: 1_760_000_045,
    });
  });

  it("is rejected with the wrong key or when tampered", async () => {
    const a = await signAssertion(KEY, buildClaims(principal, "POST", "/api/v1/verdicts/x/overturn", Date.now()));
    expect(await referenceVerify(KEY + "x", a)).toBeNull();
    const [body, mac] = a.split(".");
    const forged = btoa(JSON.stringify({ ...JSON.parse(atob(body!.replace(/-/g, "+").replace(/_/g, "/"))), role: "owner" }))
      .replace(/\+/g, "-")
      .replace(/\//g, "_")
      .replace(/=+$/, "");
    expect(await referenceVerify(KEY, `${forged}.${mac}`)).toBeNull();
  });

  it("expires 45 seconds ahead (the API allows <= 60 s plus 5 s leeway)", () => {
    const c = buildClaims(principal, "GET", "/x", 1_000_500);
    expect(c.exp).toBe(1000 + 45);
    expect(ASSERTION_TTL_S).toBe(45);
  });
});

describe("API client", () => {
  it("sends the service token, a fresh per-request assertion bound to method and path, and the client IP", async () => {
    const mock = new MockApi().json("GET", "/api/v1/verdicts", { verdicts: [] }).json("POST", "/api/v1/verdicts/v1/confirm", {});
    const client = new ApiClient(env, mock.fetch, principal, "198.51.100.4");
    await client.get("/api/v1/verdicts", { action: "ban", limit: 10 });
    await client.post("/api/v1/verdicts/v1/confirm", { note: "x" });
    expect(mock.calls).toHaveLength(2);
    for (const call of mock.calls) {
      expect(call.headers.get("Authorization")).toBe(`Bearer ${env.PANDAEYE_SERVICE_TOKEN}`);
      expect(call.headers.get("X-PandaEye-Client-IP")).toBe("198.51.100.4");
      const claims = await referenceVerify(env.PANDAEYE_ASSERTION_KEY, call.headers.get("X-PandaEye-Assertion")!);
      expect(claims).not.toBeNull();
      expect(claims!.method).toBe(call.method);
      expect(claims!.path).toBe(call.path);
      expect(claims!.tenant).toBe("t_abc");
      expect(claims!.role).toBe("staff");
    }
    expect(mock.calls[0]!.url.searchParams.get("action")).toBe("ban");
    expect(mock.calls[0]!.headers.get("X-PandaEye-Assertion")).not.toBe(mock.calls[1]!.headers.get("X-PandaEye-Assertion"));
    expect(mock.calls[1]!.body).toEqual({ note: "x" });
  });

  it("sends no assertion for anonymous requests", async () => {
    const mock = new MockApi().json("POST", "/api/v1/auth/license", { tenant_id: "t_1", slug: "s", created: true });
    const client = new ApiClient(env, mock.fetch, principal, null);
    await client.post("/api/v1/auth/license", { license_key: "k" }, { anonymous: true });
    expect(mock.calls[0]!.headers.get("X-PandaEye-Assertion")).toBeNull();
    expect(mock.calls[0]!.headers.get("Authorization")).toBe(`Bearer ${env.PANDAEYE_SERVICE_TOKEN}`);
  });

  it("maps failures to typed ApiErrors instead of throwing raw errors", async () => {
    const mock = new MockApi()
      .json("GET", "/a", { error: "nope", code: "forbidden" }, 403)
      .json("GET", "/b", { error: "boom" }, 500)
      .on("GET", "/c", () => new Response("not json", { status: 200 }));
    const client = new ApiClient(env, mock.fetch, principal, null);
    await expect(client.get("/a")).rejects.toMatchObject({ kind: "forbidden", status: 403, code: "forbidden", message: "nope" });
    await expect(client.get("/b")).rejects.toMatchObject({ kind: "server", status: 500 });
    await expect(client.get("/c")).rejects.toMatchObject({ kind: "bad_response" });
    await expect(client.get("/missing")).rejects.toMatchObject({ kind: "not_found" });
    mock.down = true;
    const err = (await client.get("/a").catch((e: unknown) => e)) as ApiError;
    expect(err).toBeInstanceOf(ApiError);
    expect(err.kind).toBe("unreachable");
  });
});
