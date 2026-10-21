// Client for the PandaEye Go API (ARCHITECTURE 5.8). Every request carries
//   Authorization: Bearer PANDAEYE_SERVICE_TOKEN
//   X-PandaEye-Assertion: fresh per request (when acting for a signed-in user)
//   X-PandaEye-Client-IP: the visitor's CF-Connecting-IP (when known)
// Failures surface as ApiError; callers render a friendly page instead of crashing.

import type { Env } from "../env";
import { buildClaims, signAssertion, type Principal } from "./assertion";

export type FetchFn = (input: Request | string | URL, init?: RequestInit) => Promise<Response>;

export const API_TIMEOUT_MS = 10_000;
/** Upper bound for any API response body (clips are the largest). */
export const API_MAX_BODY_BYTES = 16 * 1024 * 1024;

export type ApiErrorKind = "unreachable" | "timeout" | "unauthorized" | "forbidden" | "not_found" | "client" | "server" | "bad_response" | "misconfigured";

export class ApiError extends Error {
  constructor(
    readonly kind: ApiErrorKind,
    readonly status: number,
    readonly code: string,
    message: string,
    readonly retryAfterS = 0,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

function kindForStatus(status: number): ApiErrorKind {
  if (status === 401) return "unauthorized";
  if (status === 403) return "forbidden";
  if (status === 404) return "not_found";
  if (status >= 500) return "server";
  return "client";
}

const CLIENT_IP_RE = /^[0-9A-Fa-f.:]{2,45}$/;

export type Query = Record<string, string | number | boolean | undefined | null>;

export interface ApiRequestOptions {
  query?: Query;
  body?: unknown;
  /** Send no assertion (public and setup endpoints). */
  anonymous?: boolean;
}

export class ApiClient {
  constructor(
    private readonly env: Env,
    private readonly fetchFn: FetchFn,
    private readonly principal: Principal | null,
    private readonly clientIp: string | null,
    private readonly now: () => number = Date.now,
  ) {}

  forPrincipal(principal: Principal | null): ApiClient {
    return new ApiClient(this.env, this.fetchFn, principal, this.clientIp, this.now);
  }

  url(path: string, query?: Query): URL {
    const base = (this.env.PANDAEYE_API_URL || "").replace(/\/+$/, "");
    if (!/^https?:\/\//.test(base)) throw new ApiError("misconfigured", 0, "misconfigured", "PANDAEYE_API_URL is not configured");
    const u = new URL(base + path);
    if (query) {
      for (const [k, v] of Object.entries(query)) {
        if (v !== undefined && v !== null && v !== "") u.searchParams.set(k, String(v));
      }
    }
    return u;
  }

  async request<T = unknown>(method: string, path: string, opts: ApiRequestOptions = {}): Promise<T> {
    const url = this.url(path, opts.query);
    if (!this.env.PANDAEYE_SERVICE_TOKEN) {
      throw new ApiError("misconfigured", 0, "misconfigured", "PANDAEYE_SERVICE_TOKEN is not configured");
    }
    const headers = new Headers({
      Accept: "application/json",
      Authorization: `Bearer ${this.env.PANDAEYE_SERVICE_TOKEN}`,
      "User-Agent": "pandaeye-dashboard",
    });
    if (!opts.anonymous && this.principal) {
      const claims = buildClaims(this.principal, method, url.pathname, this.now());
      headers.set("X-PandaEye-Assertion", await signAssertion(this.env.PANDAEYE_ASSERTION_KEY, claims));
    }
    if (this.clientIp && CLIENT_IP_RE.test(this.clientIp)) headers.set("X-PandaEye-Client-IP", this.clientIp);
    let body: string | undefined;
    if (opts.body !== undefined) {
      headers.set("Content-Type", "application/json");
      body = JSON.stringify(opts.body);
    }

    let res: Response;
    try {
      res = await this.fetchFn(url.toString(), {
        method,
        headers,
        body,
        redirect: "manual",
        signal: AbortSignal.timeout(API_TIMEOUT_MS),
      });
    } catch (e) {
      const timeout = e instanceof Error && (e.name === "TimeoutError" || e.name === "AbortError");
      throw new ApiError(timeout ? "timeout" : "unreachable", 0, timeout ? "timeout" : "unreachable", "The PandaEye API could not be reached.");
    }

    const text = await readCapped(res);
    let data: unknown = null;
    if (text) {
      try {
        data = JSON.parse(text);
      } catch {
        if (res.ok) throw new ApiError("bad_response", res.status, "bad_response", "The PandaEye API returned an invalid response.");
      }
    }
    if (!res.ok) {
      const err = (data && typeof data === "object" ? data : {}) as { error?: unknown; code?: unknown };
      const message = typeof err.error === "string" && err.error ? err.error.slice(0, 300) : `HTTP ${res.status}`;
      const code = typeof err.code === "string" ? err.code.slice(0, 64) : String(res.status);
      const retry = Number(res.headers.get("Retry-After"));
      throw new ApiError(
        res.status >= 300 && res.status < 400 ? "bad_response" : kindForStatus(res.status),
        res.status,
        code,
        message,
        Number.isFinite(retry) && retry > 0 ? retry : 0,
      );
    }
    return data as T;
  }

  get<T = unknown>(path: string, query?: Query): Promise<T> {
    return this.request<T>("GET", path, { query });
  }
  post<T = unknown>(path: string, body?: unknown, opts: Omit<ApiRequestOptions, "body"> = {}): Promise<T> {
    return this.request<T>("POST", path, { ...opts, body: body ?? {} });
  }
  patch<T = unknown>(path: string, body: unknown): Promise<T> {
    return this.request<T>("PATCH", path, { body });
  }
  put<T = unknown>(path: string, body: unknown): Promise<T> {
    return this.request<T>("PUT", path, { body });
  }
  delete<T = unknown>(path: string): Promise<T> {
    return this.request<T>("DELETE", path);
  }
}

async function readCapped(res: Response): Promise<string> {
  const len = Number(res.headers.get("Content-Length"));
  if (Number.isFinite(len) && len > API_MAX_BODY_BYTES) {
    await res.body?.cancel();
    throw new ApiError("bad_response", res.status, "too_large", "The PandaEye API response was too large.");
  }
  if (!res.body) return "";
  const reader = res.body.getReader();
  const chunks: Uint8Array[] = [];
  let total = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    total += value.byteLength;
    if (total > API_MAX_BODY_BYTES) {
      await reader.cancel();
      throw new ApiError("bad_response", res.status, "too_large", "The PandaEye API response was too large.");
    }
    chunks.push(value);
  }
  const all = new Uint8Array(total);
  let off = 0;
  for (const c of chunks) {
    all.set(c, off);
    off += c.byteLength;
  }
  return new TextDecoder().decode(all);
}
