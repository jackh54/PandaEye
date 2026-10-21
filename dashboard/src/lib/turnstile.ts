// Cloudflare Turnstile server-side verification. Fails closed: without TURNSTILE_SECRET verification only
// passes when ENVIRONMENT = "development".

import { isDevelopment, type Env } from "../env";
import type { FetchFn } from "./api";

export const TURNSTILE_VERIFY_URL = "https://challenges.cloudflare.com/turnstile/v0/siteverify";
export const TURNSTILE_SCRIPT_URL = "https://challenges.cloudflare.com/turnstile/v0/api.js";
export const TURNSTILE_ORIGIN = "https://challenges.cloudflare.com";

export type TurnstileResult = { ok: true } | { ok: false; reason: "missing" | "invalid" | "unavailable" | "not_configured" };

export async function verifyTurnstile(
  env: Env,
  fetchFn: FetchFn,
  token: string,
  ip: string | null,
  expectedAction: string,
): Promise<TurnstileResult> {
  if (!env.TURNSTILE_SECRET) return isDevelopment(env) ? { ok: true } : { ok: false, reason: "not_configured" };
  if (!token || token.length > 2048) return { ok: false, reason: "missing" };
  const form = new URLSearchParams({ secret: env.TURNSTILE_SECRET, response: token });
  if (ip) form.set("remoteip", ip);
  try {
    const res = await fetchFn(TURNSTILE_VERIFY_URL, {
      method: "POST",
      body: form,
      signal: AbortSignal.timeout(5_000),
    });
    if (!res.ok) return { ok: false, reason: "unavailable" };
    const data = (await res.json()) as { success?: unknown; action?: unknown };
    if (data.success !== true) return { ok: false, reason: "invalid" };
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
      return "Human verification is not configured on this dashboard.";
    default:
      return "Human verification failed. Please try again.";
  }
}
