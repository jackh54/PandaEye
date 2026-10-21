import { env } from "cloudflare:test";
import { it, expect } from "vitest";
import worker from "../src/index";
it("d1 works", async () => {
  const e = env as any;
  await e.DB.prepare("INSERT INTO t (v) VALUES (?)").bind("a").run();
  const r = await e.DB.prepare("INSERT INTO t (v) VALUES (?) RETURNING id").bind("b").first();
  expect(r.id).toBe(2);
  const res = await worker.fetch(new Request("http://x/"), e);
  expect(await res.text()).toBe('{"x":1}');
  const a = await e.ASSETS.fetch("http://x/a.txt");
  expect((await a.text()).trim()).toBe("hi");
  const k = await crypto.subtle.importKey("raw", new TextEncoder().encode("pw"), "PBKDF2", false, ["deriveBits"]);
  const t0 = Date.now();
  await crypto.subtle.deriveBits({ name: "PBKDF2", hash: "SHA-256", salt: new Uint8Array(16), iterations: 100000 }, k, 256);
  console.log("pbkdf2 ms", Date.now() - t0);
});
