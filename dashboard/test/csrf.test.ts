import { describe, expect, it } from "vitest";
import { Client, MockApi, PASSWORD, seedUser, signedInClient } from "./helpers";

describe("CSRF", () => {
  it("rejects signed-in POSTs without or with a wrong token and never calls the API", async () => {
    const { client, csrf, api } = await signedInClient("staff", new MockApi().json("POST", /confirm$/, {}));
    const missing = await client.post("/verdicts/v1/confirm", {});
    expect(missing.status).toBe(403);
    const wrong = await client.post("/verdicts/v1/confirm", { _csrf: csrf.slice(0, -2) + "xx" });
    expect(wrong.status).toBe(403);
    expect(api.calls).toHaveLength(0);
    const ok = await client.post("/verdicts/v1/confirm", { _csrf: csrf });
    expect(ok.status).toBe(303);
    expect(api.callsTo("POST", "/api/v1/verdicts/v1/confirm")).toHaveLength(1);
  });

  it("puts the session token into every POST form of a page", async () => {
    const { client, csrf } = await signedInClient("owner");
    const html = await (await client.get("/users")).text();
    const forms = html.match(/<form method="post"[\s\S]*?<\/form>/g) ?? [];
    expect(forms.length).toBeGreaterThan(1);
    for (const f of forms) expect(f).toContain(`name="_csrf" value="${csrf}"`);
  });

  it("protects pre-session forms with a double-submit cookie", async () => {
    const client = new Client();
    const u = await seedUser();
    // No cookie / token: rejected.
    const res = await client.post("/login", { username: u.username, password: PASSWORD });
    expect(res.status).toBe(403);
    // Token from another visitor: rejected.
    const other = new Client();
    const foreign = await other.csrfFrom("/login");
    await client.get("/login");
    expect((await client.post("/login", { _csrf: foreign, username: u.username, password: PASSWORD })).status).toBe(403);
    // Own token: accepted.
    const own = await client.csrfFrom("/login");
    expect((await client.post("/login", { _csrf: own, username: u.username, password: PASSWORD })).status).toBe(303);
  });

  it("blocks cross-site requests by Origin / Sec-Fetch-Site even with a valid token", async () => {
    const { client, csrf, api } = await signedInClient("staff", new MockApi().json("POST", /confirm$/, {}));
    const evil = await client.post("/verdicts/v1/confirm", { _csrf: csrf }, { Origin: "https://evil.example" });
    expect(evil.status).toBe(403);
    const site = await client.post("/verdicts/v1/confirm", { _csrf: csrf }, { "Sec-Fetch-Site": "cross-site" });
    expect(site.status).toBe(403);
    expect(api.calls).toHaveLength(0);
  });
});
