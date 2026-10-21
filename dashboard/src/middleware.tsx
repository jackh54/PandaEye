import { getCookie, setCookie } from "hono/cookie";
import { createMiddleware } from "hono/factory";
import { ApiClient } from "./lib/api";
import { randomToken, timingSafeEqualString } from "./lib/crypto";
import { PandaEyeApi } from "./lib/pandaeye";
import { can, type Permission } from "./lib/roles";
import { applySecurityHeaders, newNonce } from "./lib/security";
import { loadSession, SESSION_COOKIE } from "./lib/session";
import type { AppEnv, Ctx, Deps } from "./types";
import { ErrorPage } from "./views/errors";

/** Per-request nonce + security headers on every response (including errors and assets). */
export function securityHeaders() {
  return createMiddleware<AppEnv>(async (c, next) => {
    const nonce = newNonce();
    c.set("nonce", nonce);
    c.set("turnstile", false);
    await next();
    // Responses from fetch()/assets can have immutable headers; re-wrap them.
    let res = c.res;
    try {
      applySecurityHeaders(res.headers, nonce, { turnstile: c.get("turnstile") });
    } catch {
      res = new Response(res.body, res);
      applySecurityHeaders(res.headers, nonce, { turnstile: c.get("turnstile") });
      c.res = res;
    }
  });
}

/** Request-scoped dependencies: client IP, anonymous API client. */
export function requestContext(deps: Deps) {
  return createMiddleware<AppEnv>(async (c, next) => {
    const ip = c.req.header("CF-Connecting-IP") ?? null;
    c.set("clientIp", ip);
    c.set("deps", deps);
    c.set("session", null);
    c.set("api", new PandaEyeApi(new ApiClient(c.env, deps.fetch, null, ip, deps.now)));
    await next();
  });
}

/** Loads the session cookie (if any) and binds the API client to the signed-in principal. */
export function sessionLoader() {
  return createMiddleware<AppEnv>(async (c, next) => {
    const raw = getCookie(c, SESSION_COOKIE);
    if (raw) {
      const session = await loadSession(c.env.DB, raw, c.get("deps").now());
      if (session) {
        c.set("session", session);
        const u = session.user;
        c.set("api", new PandaEyeApi(c.get("api").client.forPrincipal({ tenant: u.tenantId, user_id: u.id, username: u.username, role: u.role })));
      }
    }
    await next();
  });
}

/**
 * Cross-site request guard for every state-changing request: a present Origin must be ours and
 * Sec-Fetch-Site must not be cross-site. (CSRF tokens are checked separately.)
 */
export function originGuard() {
  return createMiddleware<AppEnv>(async (c, next) => {
    if (c.req.method !== "GET" && c.req.method !== "HEAD") {
      const origin = c.req.header("Origin");
      const site = c.req.header("Sec-Fetch-Site");
      const ours = new URL(c.req.url).origin;
      if ((origin && origin !== ours) || (site && site !== "same-origin" && site !== "none")) {
        return forbidden(c, "Cross-site request blocked.");
      }
    }
    await next();
  });
}

export function forbidden(c: Ctx, message = "Your role does not allow this action.") {
  return c.html(
    <ErrorPage nonce={c.get("nonce")} session={c.get("session")} info={{ status: 403, title: "Not allowed", message }} />,
    403,
  );
}

/** Pages behind sign-in. GET requests redirect to /login with a `next` parameter. */
export function requireSession() {
  return createMiddleware<AppEnv>(async (c, next) => {
    if (!c.get("session")) {
      if (c.req.method === "GET" || c.req.method === "HEAD") {
        const u = new URL(c.req.url);
        const nextPath = u.pathname + u.search;
        return c.redirect(nextPath === "/" ? "/login" : `/login?next=${encodeURIComponent(nextPath)}`, 303);
      }
      return c.redirect("/login?err=session_expired", 303);
    }
    await next();
  });
}

/** CSRF: every POST from a signed-in user must carry the session's token in `_csrf`. */
export function csrfGuard() {
  return createMiddleware<AppEnv>(async (c, next) => {
    if (c.req.method === "POST") {
      const session = c.get("session");
      const body = await c.req.parseBody();
      const token = typeof body._csrf === "string" ? body._csrf : "";
      if (!session || !token || !timingSafeEqualString(token, session.csrf)) {
        return c.html(
          <ErrorPage
            nonce={c.get("nonce")}
            session={session}
            info={{ status: 403, title: "Form expired", message: "The form's security token did not match. Reload the page and try again." }}
          />,
          403,
        );
      }
    }
    await next();
  });
}

export function requirePerm(permission: Permission) {
  return createMiddleware<AppEnv>(async (c, next) => {
    const session = c.get("session");
    if (!session || !can(session.user.role, permission)) return forbidden(c);
    await next();
  });
}

// ---- pre-session CSRF (login, setup, invite, appeal): double-submit cookie ----------------------------

export const PRE_CSRF_COOKIE = "__Host-pe_csrf";

/** Returns the visitor's pre-session CSRF token, setting the cookie if needed. */
export function preCsrfToken(c: Ctx): string {
  const existing = getCookie(c, PRE_CSRF_COOKIE);
  if (existing && /^[A-Za-z0-9_-]{43}$/.test(existing)) return existing;
  const token = randomToken(32);
  setCookie(c, PRE_CSRF_COOKIE, token, { path: "/", secure: true, httpOnly: true, sameSite: "Strict", maxAge: 2 * 60 * 60 });
  return token;
}

export function verifyPreCsrf(c: Ctx, body: Record<string, unknown>): boolean {
  const cookie = getCookie(c, PRE_CSRF_COOKIE);
  const field = typeof body._csrf === "string" ? body._csrf : "";
  return !!cookie && !!field && timingSafeEqualString(field, cookie);
}
