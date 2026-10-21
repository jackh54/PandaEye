// Dashboard user assertion (ARCHITECTURE 5.8):
//   X-PandaEye-Assertion: base64url(json) "." base64url(HMAC-SHA256(PANDAEYE_ASSERTION_KEY, base64url(json)))
// json = {tenant, user_id, username, role, method, path, exp}; exp = unix seconds, now + 45 (the API accepts
// exp <= now + 60 and allows 5 s of clock leeway).
// base64url is RFC 4648 section 5 without padding; the HMAC key is the UTF-8 bytes of the secret and the
// MAC covers the ASCII bytes of the first (encoded) part.

import { b64url, hmacSha256, utf8 } from "./crypto";
import type { Role } from "./roles";

export const ASSERTION_TTL_S = 45;

export interface Principal {
  tenant: string;
  user_id: string;
  username: string;
  role: Role;
}

export interface AssertionClaims extends Principal {
  method: string;
  path: string;
  exp: number;
}

export function buildClaims(principal: Principal, method: string, path: string, nowMs: number): AssertionClaims {
  return {
    tenant: principal.tenant,
    user_id: principal.user_id,
    username: principal.username,
    role: principal.role,
    method: method.toUpperCase(),
    path,
    exp: Math.floor(nowMs / 1000) + ASSERTION_TTL_S,
  };
}

export async function signAssertion(key: string, claims: AssertionClaims): Promise<string> {
  if (!key) throw new Error("PANDAEYE_ASSERTION_KEY is not configured");
  const body = b64url(utf8(JSON.stringify(claims)));
  const mac = await hmacSha256(key, body);
  return `${body}.${b64url(mac)}`;
}
