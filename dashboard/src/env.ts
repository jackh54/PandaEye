// Worker bindings (ARCHITECTURE section 7).

export interface Env {
  DB: D1Database;
  ASSETS: Fetcher;
  /** Origin of the PandaEye Go API, e.g. https://api.example.com (no path); https outside development. */
  PANDAEYE_API_URL: string;
  PANDAEYE_SERVICE_TOKEN: string;
  PANDAEYE_ASSERTION_KEY: string;
  PASSWORD_PEPPER: string;
  TURNSTILE_SECRET?: string;
  TURNSTILE_SITEKEY?: string;
  /**
   * "production" (default) or "development". Development allows a missing Turnstile configuration and an
   * http:// API URL.
   */
  ENVIRONMENT?: string;
}

export function isDevelopment(env: Env): boolean {
  return env.ENVIRONMENT === "development";
}

/** True when PANDAEYE_API_URL is usable: https, or http only in development. */
export function apiUrlAllowed(env: Env): boolean {
  const url = env.PANDAEYE_API_URL || "";
  if (/^https:\/\/[^/]/i.test(url)) return true;
  return isDevelopment(env) && /^http:\/\/[^/]/i.test(url);
}

/** Configuration needed by the Turnstile pages (/setup, /appeal). */
export const TURNSTILE_CONFIG = ["TURNSTILE_SECRET", "TURNSTILE_SITEKEY"] as const;

/**
 * Names of required configuration values that are missing or invalid. Turnstile values are required unless
 * ENVIRONMENT = "development"; PANDAEYE_API_URL must be https unless ENVIRONMENT = "development".
 */
export function missingSecrets(env: Env): string[] {
  const missing: string[] = [];
  if (!env.PANDAEYE_API_URL) missing.push("PANDAEYE_API_URL");
  else if (!apiUrlAllowed(env)) missing.push("PANDAEYE_API_URL (must be https)");
  for (const name of ["PANDAEYE_SERVICE_TOKEN", "PANDAEYE_ASSERTION_KEY", "PASSWORD_PEPPER"] as const) {
    const v = env[name];
    if (typeof v !== "string" || v.length < 32) missing.push(name);
  }
  if (!isDevelopment(env)) {
    for (const name of TURNSTILE_CONFIG) if (!env[name]) missing.push(name);
  }
  return missing;
}
