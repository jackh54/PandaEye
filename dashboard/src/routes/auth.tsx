// /login, /logout, /setup, /invite/:token, /account

import { Hono } from "hono";
import { deleteCookie, getCookie, setCookie } from "hono/cookie";
import { ApiError } from "../lib/api";
import { audit } from "../lib/audit";
import { inBackground } from "../lib/background";
import { flashFromQuery } from "../lib/messages";
import { DUMMY_PASSWORD_HASH, hashPassword, needsRehash, passwordProblem, verifyPassword } from "../lib/password";
import {
  claimBackoff,
  clearBackoff,
  hitFixedWindow,
  ipKey,
  LIMITS,
  recordBackoffFailure,
  userKey,
} from "../lib/ratelimit";
import { createSession, destroySession, SESSION_ABSOLUTE_MS, SESSION_COOKIE } from "../lib/session";
import { turnstileMessage, turnstileMode, verifyTurnstile } from "../lib/turnstile";
import {
  acceptInvite,
  countUsers,
  createOwnerIfNone,
  findUserByUsername,
  findValidInvite,
  newUserId,
  setUserPassword,
} from "../lib/users";
import { field, isUsername, LICENSE_KEY_RE, normalizeUsername, safeNext } from "../lib/validate";
import { csrfGuard, preCsrfToken, requireSession, verifyPreCsrf } from "../middleware";
import type { AppEnv, Ctx } from "../types";
import { AccountPage, InvitePage, LoginPage, SetupPage } from "../views/pages/auth";

function retryText(seconds: number): string {
  return seconds >= 90 ? `${Math.ceil(seconds / 60)} minutes` : `${seconds} seconds`;
}

async function startSession(c: Ctx, userId: string): Promise<void> {
  const db = c.env.DB;
  // Rotation: whatever session the browser presented is destroyed before a new one is issued.
  await destroySession(db, getCookie(c, SESSION_COOKIE));
  const s = await createSession(db, userId, c.get("deps").now());
  setCookie(c, SESSION_COOKIE, s.id, {
    path: "/",
    secure: true,
    httpOnly: true,
    sameSite: "Lax",
    maxAge: Math.floor(SESSION_ABSOLUTE_MS / 1000),
  });
}

function clientIp(c: Ctx): string {
  return c.get("clientIp") ?? "0.0.0.0";
}

export function authRoutes(): Hono<AppEnv> {
  const r = new Hono<AppEnv>();

  // ---- login --------------------------------------------------------------------------------------

  r.get("/login", (c) => {
    if (c.get("session")) return c.redirect(safeNext(c.req.query("next")), 303);
    return c.html(
      <LoginPage
        nonce={c.get("nonce")}
        csrf={preCsrfToken(c)}
        next={safeNext(c.req.query("next"))}
        flash={flashFromQuery({ ok: c.req.query("ok"), err: c.req.query("err") })}
      />,
    );
  });

  r.post("/login", async (c) => {
    const db = c.env.DB;
    const now = c.get("deps").now();
    const body = await c.req.parseBody();
    const next = safeNext(field(body, "next"));
    const username = normalizeUsername(field(body, "username"));
    const password = field(body, "password");
    const render = (error: string, status: 400 | 401 | 403 | 429) =>
      c.html(<LoginPage nonce={c.get("nonce")} csrf={preCsrfToken(c)} next={next} username={username} error={error} />, status);

    if (!verifyPreCsrf(c, body)) return render("Your form expired. Please try again.", 403);

    const ipLimit = await hitFixedWindow(db, await ipKey("login", clientIp(c), c.env.PASSWORD_PEPPER), LIMITS.loginIp.limit, LIMITS.loginIp.windowMs, now);
    if (!ipLimit.allowed) {
      c.header("Retry-After", String(ipLimit.retryAfterS));
      return render(`Too many sign-in attempts from your network. Try again in ${retryText(ipLimit.retryAfterS)}.`, 429);
    }
    if (!isUsername(username) || !password || password.length > 1024) return render("Invalid username or password.", 401);

    const key = userKey("login", username);
    const claim = await claimBackoff(db, key, now);
    if (!claim.allowed) {
      c.header("Retry-After", String(claim.retryAfterS));
      return render(`Too many failed attempts for this account. Try again in ${retryText(claim.retryAfterS)}.`, 429);
    }

    // Unknown users and wrong passwords do the same work before the response: one user lookup, one PBKDF2
    // verification, one backoff update. The audit row for a known user is written after the response.
    const user = await findUserByUsername(db, username);
    const ok = await verifyPassword(password, user ? user.password_hash : DUMMY_PASSWORD_HASH, c.env.PASSWORD_PEPPER);
    if (!user || !ok) {
      await recordBackoffFailure(db, key, now);
      if (user) inBackground(c, audit(db, { tenantId: user.tenant_id, principal: `user:${user.username}`, action: "login.failed", target: user.id, now }));
      return render("Invalid username or password.", 401);
    }
    await clearBackoff(db, key);
    if (user.disabled) return render("This account is disabled. Ask an owner of your network to re-enable it.", 403);

    if (needsRehash(user.password_hash)) {
      await db.prepare("UPDATE users SET password_hash = ?2 WHERE id = ?1").bind(user.id, await hashPassword(password, c.env.PASSWORD_PEPPER)).run();
    }
    await startSession(c, user.id);
    inBackground(c, audit(db, { tenantId: user.tenant_id, principal: `user:${user.username}`, action: "login", target: user.id, now }));
    return c.redirect(next, 303);
  });

  r.post("/logout", requireSession(), csrfGuard(), async (c) => {
    await destroySession(c.env.DB, getCookie(c, SESSION_COOKIE));
    deleteCookie(c, SESSION_COOKIE, { path: "/", secure: true, httpOnly: true, sameSite: "Lax" });
    return c.redirect("/login?ok=logged_out", 303);
  });

  // ---- setup --------------------------------------------------------------------------------------

  r.get("/setup", (c) => {
    c.set("turnstile", true);
    return c.html(<SetupPage nonce={c.get("nonce")} csrf={preCsrfToken(c)} sitekey={c.env.TURNSTILE_SITEKEY} turnstile={turnstileMode(c.env)} />);
  });

  r.post("/setup", async (c) => {
    c.set("turnstile", true);
    const db = c.env.DB;
    const now = c.get("deps").now();
    const body = await c.req.parseBody();
    const username = normalizeUsername(field(body, "username"));
    const render = (error: string, status: 400 | 403 | 409 | 429 | 503) =>
      c.html(<SetupPage nonce={c.get("nonce")} csrf={preCsrfToken(c)} sitekey={c.env.TURNSTILE_SITEKEY} turnstile={turnstileMode(c.env)} username={username} error={error} />, status);

    if (!verifyPreCsrf(c, body)) return render("Your form expired. Please try again.", 403);
    const limit = await hitFixedWindow(db, await ipKey("setup", clientIp(c), c.env.PASSWORD_PEPPER), LIMITS.setupIp.limit, LIMITS.setupIp.windowMs, now);
    if (!limit.allowed) {
      c.header("Retry-After", String(limit.retryAfterS));
      return render(`Too many setup attempts from your network. Try again in ${retryText(limit.retryAfterS)}.`, 429);
    }

    const licenseKey = field(body, "license_key").trim();
    const password = field(body, "password");
    if (!LICENSE_KEY_RE.test(licenseKey)) return render("Enter a valid license key.", 400);
    if (!isUsername(username)) return render("Choose a username of 3–32 lowercase letters, digits, dots, dashes or underscores.", 400);
    const problem = passwordProblem(password);
    if (problem) return render(problem, 400);
    if (password !== field(body, "password_confirm")) return render("The passwords do not match.", 400);

    const ts = await verifyTurnstile(c.env, c.get("deps").fetch, field(body, "cf-turnstile-response"), c.get("clientIp"), "setup", new URL(c.req.url).hostname);
    if (!ts.ok) return render(turnstileMessage(ts.reason), ts.reason === "unavailable" || ts.reason === "not_configured" ? 503 : 400);

    let license: { tenantId: string; slug: string };
    try {
      license = await c.get("api").verifyLicense(licenseKey);
    } catch (e) {
      if (e instanceof ApiError && (e.kind === "unauthorized" || e.kind === "client" || e.kind === "not_found" || e.kind === "forbidden")) {
        if (e.status === 429) return render("Too many license checks. Try again later.", 429);
        return render("This license key was not accepted.", 400);
      }
      return render("The license could not be verified right now because the PandaEye API is unavailable. Try again in a minute.", 503);
    }
    if (!license.tenantId) return render("The license could not be verified right now. Try again in a minute.", 503);

    // Cheap early exit; the authoritative check is the conditional insert below.
    if ((await countUsers(db, license.tenantId)) > 0) {
      return render("This license already has an owner account. Ask an owner or admin of your network for an invite.", 409);
    }
    const id = newUserId();
    const result = await createOwnerIfNone(db, {
      id,
      tenantId: license.tenantId,
      username,
      passwordHash: await hashPassword(password, c.env.PASSWORD_PEPPER),
      now,
      slug: license.slug,
    });
    if (!result.ok) {
      if (result.reason === "username_taken") return render("This username is taken. Choose another one.", 409);
      return render("This license already has an owner account. Ask an owner or admin of your network for an invite.", 409);
    }
    await startSession(c, id);
    return c.redirect("/?ok=welcome", 303);
  });

  // ---- invites ------------------------------------------------------------------------------------

  r.get("/invite/:token", async (c) => {
    const invite = await findValidInvite(c.env.DB, c.req.param("token"), c.get("deps").now());
    return c.html(
      <InvitePage nonce={c.get("nonce")} csrf={preCsrfToken(c)} token={c.req.param("token")} role={invite?.role ?? null} />,
      invite ? 200 : 404,
    );
  });

  r.post("/invite/:token", async (c) => {
    const db = c.env.DB;
    const now = c.get("deps").now();
    const token = c.req.param("token");
    const body = await c.req.parseBody();
    const username = normalizeUsername(field(body, "username"));
    const invite = await findValidInvite(db, token, now);
    const render = (error: string, status: 400 | 403 | 404 | 409 | 429) =>
      c.html(<InvitePage nonce={c.get("nonce")} csrf={preCsrfToken(c)} token={token} role={invite?.role ?? null} username={username} error={error} />, status);

    if (!verifyPreCsrf(c, body)) return render("Your form expired. Please try again.", 403);
    if (!invite) return render("This invite is no longer valid.", 404);
    const limit = await hitFixedWindow(db, await ipKey("invite", clientIp(c), c.env.PASSWORD_PEPPER), LIMITS.inviteIp.limit, LIMITS.inviteIp.windowMs, now);
    if (!limit.allowed) return render(`Too many attempts. Try again in ${retryText(limit.retryAfterS)}.`, 429);
    if (!isUsername(username)) return render("Choose a username of 3–32 lowercase letters, digits, dots, dashes or underscores.", 400);
    const password = field(body, "password");
    const problem = passwordProblem(password);
    if (problem) return render(problem, 400);
    if (password !== field(body, "password_confirm")) return render("The passwords do not match.", 400);

    const id = newUserId();
    const result = await acceptInvite(db, { token, userId: id, username, passwordHash: await hashPassword(password, c.env.PASSWORD_PEPPER), now });
    if (!result.ok) {
      if (result.reason === "username_taken") return render("This username is taken. Choose another one.", 409);
      return render("This invite is no longer valid.", 404);
    }
    await startSession(c, id);
    return c.redirect("/?ok=welcome", 303);
  });

  // ---- account ------------------------------------------------------------------------------------

  r.get("/account", requireSession(), (c) =>
    c.html(<AccountPage nonce={c.get("nonce")} session={c.get("session")!} flash={flashFromQuery({ ok: c.req.query("ok") })} />),
  );

  r.post("/account/password", requireSession(), csrfGuard(), async (c) => {
    const db = c.env.DB;
    const now = c.get("deps").now();
    const session = c.get("session")!;
    const body = await c.req.parseBody();
    const render = (error: string, status: 400 | 401 | 429) => c.html(<AccountPage nonce={c.get("nonce")} session={session} error={error} />, status);

    const limit = await hitFixedWindow(db, userKey("pwchange", session.user.id), LIMITS.passwordChangeUser.limit, LIMITS.passwordChangeUser.windowMs, now);
    if (!limit.allowed) return render(`Too many attempts. Try again in ${retryText(limit.retryAfterS)}.`, 429);
    const row = await findUserByUsername(db, session.user.username);
    if (!row || !(await verifyPassword(field(body, "current"), row.password_hash, c.env.PASSWORD_PEPPER))) {
      return render("The current password is not correct.", 401);
    }
    const password = field(body, "password");
    const problem = passwordProblem(password);
    if (problem) return render(problem, 400);
    if (password !== field(body, "password_confirm")) return render("The new passwords do not match.", 400);
    if (password === field(body, "current")) return render("Choose a password different from the current one.", 400);

    await setUserPassword(db, { id: row.id, tenantId: row.tenant_id, username: row.username }, await hashPassword(password, c.env.PASSWORD_PEPPER), now);
    await startSession(c, row.id);
    return c.redirect("/account?ok=password_changed", 303);
  });

  return r;
}
