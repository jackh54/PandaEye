// Public pages: /status, /appeal, /privacy, /healthz

import { Hono } from "hono";
import { missingSecrets } from "../env";
import { ApiError } from "../lib/api";
import type { PublicStatus } from "../lib/pandaeye";
import { hitFixedWindow, ipKey, LIMITS } from "../lib/ratelimit";
import { turnstileMessage, turnstileMode, verifyTurnstile } from "../lib/turnstile";
import { APPEAL_CODE_RE, field, isId } from "../lib/validate";
import { preCsrfToken, verifyPreCsrf } from "../middleware";
import type { AppEnv } from "../types";
import { AppealPage, PrivacyPage, StatusPage, type AppealForm } from "../views/pages/public";

export function publicRoutes(): Hono<AppEnv> {
  const r = new Hono<AppEnv>();

  r.get("/healthz", (c) => (missingSecrets(c.env).length ? c.text("misconfigured", 503) : c.text("ok")));

  r.get("/status", async (c) => {
    let status: PublicStatus | null = null;
    try {
      status = await c.get("api").status();
    } catch {
      status = null;
    }
    const res = c.html(<StatusPage nonce={c.get("nonce")} status={status} checkedAt={c.get("deps").now()} />, status ? 200 : 503);
    return res;
  });

  r.get("/privacy", (c) => c.html(<PrivacyPage nonce={c.get("nonce")} />));

  r.get("/appeal", (c) => {
    c.set("turnstile", true);
    const form: Partial<AppealForm> = {};
    const v = c.req.query("verdict");
    const code = c.req.query("code");
    if (v && isId(v)) form.verdict_id = v;
    if (code && APPEAL_CODE_RE.test(code)) form.appeal_code = code;
    return c.html(<AppealPage nonce={c.get("nonce")} csrf={preCsrfToken(c)} sitekey={c.env.TURNSTILE_SITEKEY} turnstile={turnstileMode(c.env)} form={form} />);
  });

  r.post("/appeal", async (c) => {
    c.set("turnstile", true);
    const now = c.get("deps").now();
    const body = await c.req.parseBody();
    const form: AppealForm = {
      verdict_id: field(body, "verdict_id").trim(),
      appeal_code: field(body, "appeal_code").trim(),
      message: field(body, "message").trim(),
      contact: field(body, "contact").trim(),
    };
    const render = (error: string, status: 400 | 403 | 429 | 503) =>
      c.html(<AppealPage nonce={c.get("nonce")} csrf={preCsrfToken(c)} sitekey={c.env.TURNSTILE_SITEKEY} turnstile={turnstileMode(c.env)} form={form} error={error} />, status);

    if (!verifyPreCsrf(c, body)) return render("Your form expired. Please try again.", 403);
    const limit = await hitFixedWindow(c.env.DB, await ipKey("appeal", c.get("clientIp") ?? "0.0.0.0", c.env.PASSWORD_PEPPER), LIMITS.appealIp.limit, LIMITS.appealIp.windowMs, now);
    if (!limit.allowed) return render("Too many appeals from your network. Try again later.", 429);
    if (!isId(form.verdict_id) || !APPEAL_CODE_RE.test(form.appeal_code)) return render("Check the verdict id and the appeal code from the ban message.", 400);
    if (form.message.length < 10 || form.message.length > 2000) return render("Write a message of 10 to 2000 characters.", 400);
    if (form.contact.length > 200) return render("The contact field can hold at most 200 characters.", 400);

    const ts = await verifyTurnstile(c.env, c.get("deps").fetch, field(body, "cf-turnstile-response"), c.get("clientIp"), "appeal", new URL(c.req.url).hostname);
    if (!ts.ok) return render(turnstileMessage(ts.reason), ts.reason === "unavailable" || ts.reason === "not_configured" ? 503 : 400);

    try {
      await c.get("api").submitPublicAppeal(form);
    } catch (e) {
      if (e instanceof ApiError && e.status === 429) return render("Too many appeals. Try again later.", 429);
      if (e instanceof ApiError && e.kind !== "server" && e.kind !== "unreachable" && e.kind !== "timeout" && e.kind !== "bad_response" && e.kind !== "misconfigured" && e.kind !== "unauthorized") {
        // The API answers unknown verdicts and wrong codes identically; so do we.
        return render(
          "This appeal could not be accepted. Check the verdict id and the appeal code; appeals are possible only for active bans, and only one open appeal per ban.",
          400,
        );
      }
      return render("Appeals cannot be submitted right now. Please try again later.", 503);
    }
    return c.html(<AppealPage nonce={c.get("nonce")} csrf={preCsrfToken(c)} sitekey={c.env.TURNSTILE_SITEKEY} turnstile={turnstileMode(c.env)} done />);
  });

  return r;
}
