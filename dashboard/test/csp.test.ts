import { describe, expect, it } from "vitest";
import { buildCsp } from "../src/lib/security";
import { Client, MockApi, signedInClient, tenantFixture } from "./helpers";

function cspNonce(res: Response): string {
  const m = /'nonce-([A-Za-z0-9+/=]+)'/.exec(res.headers.get("Content-Security-Policy") ?? "");
  if (!m) throw new Error("no nonce");
  return m[1]!;
}

function expectSecurityHeaders(res: Response) {
  const nonce = cspNonce(res);
  expect(res.headers.get("Content-Security-Policy")).toBe(
    `default-src 'self'; script-src 'self' 'nonce-${nonce}'; style-src 'self' 'nonce-${nonce}'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'`,
  );
  expect(res.headers.get("X-Content-Type-Options")).toBe("nosniff");
  expect(res.headers.get("Referrer-Policy")).toBe("same-origin");
  expect(res.headers.get("X-Frame-Options")).toBe("DENY");
}

describe("security headers", () => {
  it("sends the ARCHITECTURE 7 CSP with a fresh nonce used by the page's tags", async () => {
    const client = new Client();
    const a = await client.get("/login");
    const b = await client.get("/login");
    expectSecurityHeaders(a);
    expect(cspNonce(a)).not.toBe(cspNonce(b));
    const html = await a.text();
    expect(html).toContain(`<link rel="stylesheet" href="/static/app.css?v=1" nonce="${cspNonce(a)}"`);
    expect(html).not.toMatch(/<script(?![^>]*nonce=)/);
    expect(html).not.toMatch(/\sstyle="/);
    expect(html).not.toMatch(/\son[a-z]+="/);
  });

  it("nonces the replay viewer script on the verdict page", async () => {
    const { client, api } = await signedInClient("viewer");
    api.json("GET", "/api/v1/verdicts/v1", { id: "v1", action: "kick", clip_ids: ["c1"] });
    const res = await client.get("/verdicts/v1");
    expect(res.status).toBe(200);
    expectSecurityHeaders(res);
    expect(await res.text()).toContain(`<script src="/static/replay.js?v=1" nonce="${cspNonce(res)}" defer=""></script>`);
  });

  it("widens the CSP only on Turnstile pages", async () => {
    const res = await new Client().get("/setup");
    const nonce = cspNonce(res);
    expect(res.headers.get("Content-Security-Policy")).toBe(buildCsp(nonce, { turnstile: true }));
    expect(res.headers.get("Content-Security-Policy")).toContain("frame-src https://challenges.cloudflare.com");
    expect(res.headers.get("Content-Security-Policy")).toContain("frame-ancestors 'none'");
    expect(await res.text()).toContain(`<script src="https://challenges.cloudflare.com/turnstile/v0/api.js" nonce="${nonce}"`);
  });

  it("applies to static assets, 404s, error pages and JSON", async () => {
    const { client, api } = await signedInClient("viewer");
    const css = await client.get("/static/app.css");
    expect(css.status).toBe(200);
    expect(css.headers.get("Content-Type")).toMatch(/text\/css/);
    expectSecurityHeaders(css);
    const js = await client.get("/static/replay.js");
    expect(js.status).toBe(200);
    expect(js.headers.get("Content-Type")).toMatch(/javascript/);
    expectSecurityHeaders(js);
    expectSecurityHeaders(await client.get("/static/nope.js"));
    expectSecurityHeaders(await client.get("/definitely/not/here"));
    api.down = true;
    const down = await client.get("/servers");
    expect(down.status).toBe(503);
    expectSecurityHeaders(down);
    api.down = false;
    api.json("GET", "/api/v1/clips/c1", { events: [] });
    const clip = await client.get("/verdicts/v1/clip/c1");
    expect(clip.headers.get("Content-Type")).toMatch(/application\/json/);
    expect(clip.headers.get("Cache-Control")).toBe("private, no-store");
    expectSecurityHeaders(clip);
  });

  it("marks dynamic pages no-store", async () => {
    const { client, api } = await signedInClient("admin");
    api.json("GET", "/api/v1/tenant", tenantFixture());
    expect((await client.get("/settings")).headers.get("Cache-Control")).toBe("no-store");
    void MockApi;
  });
});
