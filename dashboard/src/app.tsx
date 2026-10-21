import { Hono } from "hono";
import { bodyLimit } from "hono/body-limit";
import { missingSecrets, TURNSTILE_CONFIG } from "./env";
import { ApiError } from "./lib/api";
import { originGuard, requestContext, securityHeaders, sessionLoader } from "./middleware";
import { authRoutes } from "./routes/auth";
import { dashboardRoutes } from "./routes/dashboard";
import { publicRoutes } from "./routes/public";
import type { AppEnv, Deps } from "./types";
import { apiErrorInfo, ErrorPage } from "./views/errors";

export const MAX_FORM_BYTES = 64 * 1024;

export function createApp(overrides: Partial<Deps> = {}): Hono<AppEnv> {
  const deps: Deps = {
    fetch: overrides.fetch ?? ((input, init) => fetch(input, init)),
    now: overrides.now ?? Date.now,
  };
  const app = new Hono<AppEnv>();

  app.use("*", securityHeaders());
  app.use("*", requestContext(deps));
  app.use("*", bodyLimit({ maxSize: MAX_FORM_BYTES, onError: (c) => c.text("Request body too large", 413) }));
  app.use("*", originGuard());

  // Static files (stylesheet, replay viewer) from the assets binding, with the security headers above.
  app.get("/static/*", async (c) => {
    const url = new URL(c.req.url);
    if (!/^\/static\/[A-Za-z0-9_.-]+$/.test(url.pathname)) return c.text("Not found", 404);
    const res = await c.env.ASSETS.fetch(new Request(`${url.origin}${url.pathname}`));
    if (res.status !== 200) return c.text("Not found", 404);
    const out = new Response(res.body, res);
    out.headers.set("Cache-Control", "public, max-age=3600");
    return out;
  });

  app.use("*", async (c, next) => {
    const all = missingSecrets(c.env);
    if (all.length) console.error(`pandaeye-dashboard: missing or invalid configuration: ${all.join(", ")}`);
    // Missing Turnstile configuration disables only /setup and /appeal (they fail closed); anything else
    // makes the whole dashboard unavailable. /healthz reports both (see routes/public.tsx).
    const missing = all.filter((n) => !(TURNSTILE_CONFIG as readonly string[]).includes(n));
    if (missing.length && c.req.path !== "/healthz") {
      return c.html(
        <ErrorPage
          nonce={c.get("nonce")}
          session={null}
          info={{ status: 503, title: "Dashboard not configured", message: "This dashboard is missing required secrets. See wrangler.toml for the list." }}
        />,
        503,
      );
    }
    await next();
  });

  app.use("*", sessionLoader());
  app.route("/", publicRoutes());
  app.route("/", authRoutes());
  app.route("/", dashboardRoutes());

  app.notFound((c) =>
    c.html(
      <ErrorPage nonce={c.get("nonce")} session={c.get("session")} info={{ status: 404, title: "Not found", message: "There is no page at this address." }} />,
      404,
    ),
  );

  app.onError((err, c) => {
    const session = c.get("session") ?? null;
    if (err instanceof ApiError) {
      const info = apiErrorInfo(err);
      if (info.status >= 500) console.error(`pandaeye-dashboard: API ${err.kind} ${err.status} ${err.code}`);
      if (err.retryAfterS) c.header("Retry-After", String(err.retryAfterS));
      return c.html(<ErrorPage nonce={c.get("nonce")} session={session} info={info} />, info.status as 400);
    }
    console.error("pandaeye-dashboard: unhandled error", err instanceof Error ? err.message : String(err));
    return c.html(
      <ErrorPage nonce={c.get("nonce")} session={session} info={{ status: 500, title: "Something went wrong", message: "The dashboard hit an unexpected error. Please try again." }} />,
      500,
    );
  });

  return app;
}


