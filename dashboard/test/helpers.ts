import { createExecutionContext, waitOnExecutionContext } from "cloudflare:test";
import { env } from "cloudflare:workers";
import { createApp } from "../src/app";
import { randomToken } from "../src/lib/crypto";
import { hashPassword } from "../src/lib/password";
import type { Role } from "../src/lib/roles";
import { createSession, SESSION_COOKIE } from "../src/lib/session";
import { TURNSTILE_VERIFY_URL } from "../src/lib/turnstile";
import { newUserId } from "../src/lib/users";

export const ORIGIN = "https://dash.pandaeye.test";
export const API = "https://api.pandaeye.test";
export const XSS = "<img src=x onerror=alert(1)>";
export const XSS_ESCAPED = "&lt;img src=x onerror=alert(1)&gt;";
export const PASSWORD = "correct horse battery staple";

export interface ApiCall {
  method: string;
  url: URL;
  path: string;
  headers: Headers;
  body: unknown;
}

type Handler = (call: ApiCall) => Response | unknown | Promise<Response | unknown>;

/** Mock of the Go API (and Turnstile) behind the injected fetch. */
export class MockApi {
  calls: ApiCall[] = [];
  turnstileCalls: URLSearchParams[] = [];
  turnstileOk = true;
  down = false;
  private routes: { method: string; match: string | RegExp; handler: Handler }[] = [];

  on(method: string, match: string | RegExp, handler: Handler): this {
    this.routes.unshift({ method, match, handler });
    return this;
  }

  json(method: string, match: string | RegExp, data: unknown, status = 200): this {
    return this.on(method, match, () => Response.json(data, { status }));
  }

  callsTo(method: string, path: string | RegExp): ApiCall[] {
    return this.calls.filter((c) => c.method === method && (typeof path === "string" ? c.path === path : path.test(c.path)));
  }

  fetch = async (input: Request | string | URL, init?: RequestInit): Promise<Response> => {
    const req = input instanceof Request ? input : new Request(String(input), init);
    const url = new URL(req.url);
    if (url.href === TURNSTILE_VERIFY_URL) {
      this.turnstileCalls.push(new URLSearchParams(new TextDecoder().decode(await req.arrayBuffer())));
      return Response.json({ success: this.turnstileOk });
    }
    if (this.down) throw new TypeError("fetch failed: connection refused");
    const text = await req.text();
    let body: unknown = undefined;
    if (text) {
      try {
        body = JSON.parse(text);
      } catch {
        body = text;
      }
    }
    const call: ApiCall = { method: req.method, url, path: url.pathname, headers: req.headers, body };
    this.calls.push(call);
    for (const r of this.routes) {
      if (r.method !== req.method) continue;
      if (typeof r.match === "string" ? r.match === url.pathname : r.match.test(url.pathname)) {
        const out = await r.handler(call);
        return out instanceof Response ? out : Response.json(out ?? {});
      }
    }
    return Response.json({ error: "not found", code: "not_found" }, { status: 404 });
  };
}

/** A browser-like client with a cookie jar, talking to a fresh app instance. */
export class Client {
  cookies = new Map<string, string>();
  readonly app: ReturnType<typeof createApp>;

  constructor(
    readonly api: MockApi = new MockApi(),
    readonly ip = "203.0.113.7",
    now?: () => number,
  ) {
    this.app = createApp({ fetch: api.fetch, now });
  }

  async request(path: string, opts: { method?: string; form?: Record<string, string>; headers?: Record<string, string>; noOrigin?: boolean } = {}): Promise<Response> {
    const method = opts.method ?? (opts.form ? "POST" : "GET");
    const headers = new Headers(opts.headers);
    if (!headers.has("CF-Connecting-IP")) headers.set("CF-Connecting-IP", this.ip);
    if (this.cookies.size) headers.set("Cookie", [...this.cookies].map(([k, v]) => `${k}=${v}`).join("; "));
    let body: string | undefined;
    if (opts.form) {
      body = new URLSearchParams(opts.form).toString();
      headers.set("Content-Type", "application/x-www-form-urlencoded");
    }
    if (method !== "GET" && !opts.noOrigin && !headers.has("Origin")) headers.set("Origin", ORIGIN);
    const ctx = createExecutionContext();
    const res = await this.app.fetch(new Request(ORIGIN + path, { method, headers, body, redirect: "manual" }), env, ctx);
    await waitOnExecutionContext(ctx);
    for (const sc of res.headers.getSetCookie()) {
      const [pair] = sc.split(";");
      const i = pair!.indexOf("=");
      const name = pair!.slice(0, i).trim();
      const value = pair!.slice(i + 1).trim();
      if (/max-age=0/i.test(sc) || value === "") this.cookies.delete(name);
      else this.cookies.set(name, value);
    }
    return res;
  }

  get(path: string, headers?: Record<string, string>) {
    return this.request(path, { headers });
  }

  post(path: string, form: Record<string, string>, headers?: Record<string, string>) {
    return this.request(path, { form, headers });
  }

  /** GETs a page and returns the value of its first `_csrf` hidden field. */
  async csrfFrom(path: string): Promise<string> {
    const html = await (await this.get(path)).text();
    const m = /name="_csrf" value="([^"]+)"/.exec(html);
    if (!m) throw new Error(`no csrf field on ${path}`);
    return m[1]!;
  }
}

let hashCache: Promise<string> | null = null;
export function passwordHash(): Promise<string> {
  hashCache ??= hashPassword(PASSWORD, env.PASSWORD_PEPPER);
  return hashCache;
}

export function uniq(prefix: string): string {
  return `${prefix}${randomToken(6).toLowerCase().replace(/[^a-z0-9]/g, "x")}`;
}

export interface SeededUser {
  id: string;
  tenantId: string;
  username: string;
  role: Role;
}

export async function seedUser(opts: { tenantId?: string; username?: string; role?: Role; disabled?: boolean } = {}): Promise<SeededUser> {
  const user: SeededUser = {
    id: newUserId(),
    tenantId: opts.tenantId ?? uniq("t_"),
    username: opts.username ?? uniq("user"),
    role: opts.role ?? "owner",
  };
  const now = Date.now();
  await env.DB.prepare(
    "INSERT INTO users (id, tenant_id, username, password_hash, role, created_at, disabled, password_changed_at) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?6)",
  )
    .bind(user.id, user.tenantId, user.username, await passwordHash(), user.role, now, opts.disabled ? 1 : 0)
    .run();
  return user;
}

/** Signs the client in as `user` (direct session creation) and returns the session's CSRF token. */
export async function signIn(client: Client, user: SeededUser, now = Date.now()): Promise<string> {
  const s = await createSession(env.DB, user.id, now);
  client.cookies.set(SESSION_COOKIE, s.id);
  return s.csrf;
}

export async function signedInClient(role: Role = "owner", api = new MockApi()): Promise<{ client: Client; user: SeededUser; csrf: string; api: MockApi }> {
  const client = new Client(api);
  const user = await seedUser({ role });
  const csrf = await signIn(client, user);
  return { client, user, csrf, api };
}

export function tenantFixture(overrides: Record<string, unknown> = {}) {
  return {
    id: "t_abc",
    name: "Example network",
    slug: "example",
    plan: "free",
    leniency: 4,
    autoban: false,
    autokick: false,
    mitigations: true,
    enforcement: false,
    reputation_opt_in: false,
    discord_webhook: "",
    discord_min_action: "mitigate",
    over_limit_since: null,
    ...overrides,
  };
}

export function verdictFixture(overrides: Record<string, unknown> = {}) {
  return {
    id: "3f0c2c9e-1111-4a2b-9c3d-0123456789ab",
    server_id: "0d5e7a8b-2222-4c3d-8e9f-0123456789ab",
    server_name: "SMP",
    network_id: "",
    player_uuid: "8667ba71-b85a-4004-af54-457a9734eed7",
    player_name: "Suspect",
    session_id: "session-1",
    action: "ban",
    category: "HEURISTICS",
    score: 131.5,
    confidence: 0.97,
    reason: "Combat heuristics (killaura.hitbox x7)",
    checks: ["killaura.hitbox", "reach.distance"],
    shadow: false,
    status: "open",
    supersedes: "",
    created_at: "2026-10-21T12:00:00Z",
    delivered_at: "2026-10-21T12:00:01Z",
    acked_at: "2026-10-21T12:00:02Z",
    executed: true,
    ack_note: "",
    clip_ids: ["clip-1"],
    evidence: {
      score: 131.5,
      confidence: 0.97,
      leniency: 4,
      thresholds: { alert: 19.5, mitigate: 39, kick: 91, ban: 156 },
      families: [{ family: "killaura", contribution: 70, cap: 120 }],
      checks: [{ check: "killaura.hitbox", contribution: 40, cap: 80, confidence: 0.85, count: 8 }],
      signals: [
        { check: "killaura.hitbox", category: "HEURISTICS", points: 5, confidence: 0.85, info: "hit outside hitbox by 0.21", data: { offset: 0.21 }, time: "2026-10-21T11:59:58Z", clip_id: "clip-1" },
      ],
      session: { server_id: "0d5e7a8b", hot_loaded: false, brand: "vanilla" },
    },
    ...overrides,
  };
}
