// Security headers (ARCHITECTURE section 7). Every response gets the CSP with a fresh per-request nonce,
// nosniff, Referrer-Policy same-origin and frame-ancestors 'none'.

import { b64encode, randomBytes } from "./crypto";
import { TURNSTILE_ORIGIN } from "./turnstile";

export function newNonce(): string {
  return b64encode(randomBytes(16));
}

/**
 * The CSP of ARCHITECTURE 7. Pages that embed the Turnstile widget (/setup, /appeal) additionally allow
 * Turnstile's script and iframe origin; without that the widget cannot load.
 */
export function buildCsp(nonce: string, opts: { turnstile?: boolean } = {}): string {
  const script = opts.turnstile ? `'self' 'nonce-${nonce}' ${TURNSTILE_ORIGIN}` : `'self' 'nonce-${nonce}'`;
  const parts = [
    "default-src 'self'",
    `script-src ${script}`,
    `style-src 'self' 'nonce-${nonce}'`,
    "img-src 'self' data:",
    "object-src 'none'",
    "base-uri 'none'",
    "frame-ancestors 'none'",
  ];
  if (opts.turnstile) parts.push(`frame-src ${TURNSTILE_ORIGIN}`);
  return parts.join("; ");
}

export function applySecurityHeaders(headers: Headers, nonce: string, opts: { turnstile?: boolean } = {}): void {
  headers.set("Content-Security-Policy", buildCsp(nonce, opts));
  headers.set("X-Content-Type-Options", "nosniff");
  headers.set("Referrer-Policy", "same-origin");
  headers.set("X-Frame-Options", "DENY");
  headers.set("Cross-Origin-Opener-Policy", "same-origin");
  headers.set("Cross-Origin-Resource-Policy", "same-origin");
  headers.set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()");
  headers.set("Strict-Transport-Security", "max-age=31536000; includeSubDomains");
  if (!headers.has("Cache-Control")) headers.set("Cache-Control", "no-store");
}
