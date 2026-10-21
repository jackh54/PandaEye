// Worker bindings (ARCHITECTURE section 7).

export interface Env {
  DB: D1Database;
  ASSETS: Fetcher;
  /** Origin of the PandaEye Go API, e.g. https://api.example.com (no path). */
  PANDAEYE_API_URL: string;
  PANDAEYE_SERVICE_TOKEN: string;
  PANDAEYE_ASSERTION_KEY: string;
  PASSWORD_PEPPER: string;
  TURNSTILE_SECRET?: string;
  TURNSTILE_SITEKEY?: string;
  /** "production" (default) or "development"; development allows a missing TURNSTILE_SECRET. */
  ENVIRONMENT?: string;
}

export function isDevelopment(env: Env): boolean {
  return env.ENVIRONMENT === "development";
}

/** Returns the names of required secrets that are missing or too short. */
export function missingSecrets(env: Env): string[] {
  const missing: string[] = [];
  if (!env.PANDAEYE_API_URL) missing.push("PANDAEYE_API_URL");
  for (const name of ["PANDAEYE_SERVICE_TOKEN", "PANDAEYE_ASSERTION_KEY", "PASSWORD_PEPPER"] as const) {
    const v = env[name];
    if (typeof v !== "string" || v.length < 32) missing.push(name);
  }
  if (!isDevelopment(env) && !env.TURNSTILE_SECRET) missing.push("TURNSTILE_SECRET");
  return missing;
}
