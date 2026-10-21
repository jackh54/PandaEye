// Cloudflare Turnstile server-side verification. Fails closed: without TURNSTILE_SECRET and TURNSTILE_SITEKEY
// verification only passes when ENVIRONMENT = "development". A passing answer must be for this hostname
// (and, when Turnstile reports one, for the expected widget action).

import { isDevelopment, type Env } from "../env";
import type { FetchFn } from "./api";

export const TURNSTILE_VERIFY_URL = "https://challenges.cloudflare.com/turnstile/v0/siteverify";
export const TURNSTILE_SCRIPT_URL = "https://challenges.cloudflare.com/turnstile/v0/api.js";
export const TURNSTILE_ORIGIN = "https://challenges.cloudflare.com";

export type TurnstileResult = { ok: true } | { ok: false; reason: "missing" | "invalid" | "unavailable" | "not_configured" };

/** How a Turnstile page renders: the widget, a development notice, or a "not configured" error. */
export type TurnstileMode = "widget" | "dev" | "unconfigured";

export function turnstileMode(env: Env): TurnstileMode {
  if (env.TURNSTILE_SITEKEY && env.TURNSTILE_SECRET) return "widget";
  return isDevelopment(env) && !env.TURNSTILE_SECRET ? "dev" : "unconfigured";
}

export async function verifyTurnstile(
  env: Env,
  fetchFn: FetchFn,
  token: string,
  ip: string | null,
  expectedAction: string,
  expectedHostname: string,
): Promise<TurnstileResult> {
  const mode = turnstileMode(env);
  if (mode === "dev") return { ok: true };
  if (mode === "unconfigured") return { ok: false, reason: "not_configured" };
  if (!token || token.length > 2048) return { ok: false, reason: "missing" };
  const form = new URLSearchParams({ secret: env.TURNSTILE_SECRET!, response: token });
  if (ip) form.set("remoteip", ip);
  try {
    const res = await fetchFn(TURNSTILE_VERIFY_URL, {
      method: "POST",
      body: form,
      signal: AbortSignal.timeout(5_000),
    });
    if (!res.ok) return { ok: false, reason: "unavailable" };
    const data = (await res.json()) as { success?: unknown; action?: unknown; hostname?: unknown };
    if (data.success !== true) return { ok: false, reason: "invalid" };
    if (typeof data.hostname !== "string" || data.hostname.toLowerCase() !== expectedHostname.toLowerCase()) {
      return { ok: false, reason: "invalid" };
    }
    if (typeof data.action === "string" && data.action && data.action !== expectedAction) return { ok: false, reason: "invalid" };
    return { ok: true };
  } catch {
    return { ok: false, reason: "unavailable" };
  }
}

export function turnstileMessage(reason: string): string {
  switch (reason) {
    case "missing":
      return "Please complete the human verification.";
    case "unavailable":
      return "Human verification is temporarily unavailable. Try again in a minute.";
    case "not_configured":
      return "Human verification is not configured on this dashboard, so this form cannot be submitted. Please tell the network's staff.";
    default:
      return "Human verification failed. Please try again.";
  }
}
