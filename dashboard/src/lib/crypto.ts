// Small WebCrypto helpers shared by passwords, sessions, assertions and rate limiting.

const encoder = new TextEncoder();

export function utf8(s: string): Uint8Array<ArrayBuffer> {
  return encoder.encode(s) as Uint8Array<ArrayBuffer>;
}

export function randomBytes(n: number): Uint8Array<ArrayBuffer> {
  const b = new Uint8Array(n);
  crypto.getRandomValues(b);
  return b;
}

export function b64encode(bytes: Uint8Array): string {
  let s = "";
  for (let i = 0; i < bytes.length; i++) s += String.fromCharCode(bytes[i]!);
  return btoa(s);
}

/** Decodes standard or URL-safe base64 (padding optional). Throws on invalid input. */
export function b64decode(s: string): Uint8Array<ArrayBuffer> {
  let t = s.replace(/-/g, "+").replace(/_/g, "/");
  if (!/^[A-Za-z0-9+/]*={0,2}$/.test(t)) throw new Error("invalid base64");
  t = t.replace(/=+$/, "");
  while (t.length % 4 !== 0) t += "=";
  const bin = atob(t);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

/** RFC 4648 base64url without padding. */
export function b64url(bytes: Uint8Array): string {
  return b64encode(bytes).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

export function hex(bytes: Uint8Array): string {
  let s = "";
  for (let i = 0; i < bytes.length; i++) s += bytes[i]!.toString(16).padStart(2, "0");
  return s;
}

/** Random URL-safe token with `nBytes` bytes of entropy. */
export function randomToken(nBytes = 32): string {
  return b64url(randomBytes(nBytes));
}

export async function sha256(data: string | Uint8Array<ArrayBuffer>): Promise<Uint8Array<ArrayBuffer>> {
  const bytes = typeof data === "string" ? utf8(data) : data;
  return new Uint8Array(await crypto.subtle.digest("SHA-256", bytes));
}

export async function sha256Hex(data: string | Uint8Array<ArrayBuffer>): Promise<string> {
  return hex(await sha256(data));
}

export async function hmacSha256(
  key: string | Uint8Array<ArrayBuffer>,
  data: string | Uint8Array<ArrayBuffer>,
): Promise<Uint8Array<ArrayBuffer>> {
  const k = await crypto.subtle.importKey(
    "raw",
    typeof key === "string" ? utf8(key) : key,
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"],
  );
  const sig = await crypto.subtle.sign("HMAC", k, typeof data === "string" ? utf8(data) : data);
  return new Uint8Array(sig);
}

/**
 * Constant-time comparison. The running time depends only on the length of `b` (the value supplied by
 * the caller is passed as `a`), and a length mismatch still walks the full buffer.
 */
export function timingSafeEqual(a: Uint8Array, b: Uint8Array): boolean {
  let diff = a.length ^ b.length;
  for (let i = 0; i < b.length; i++) diff |= (a[i % Math.max(a.length, 1)] ?? 0) ^ b[i]!;
  return diff === 0;
}

export function timingSafeEqualString(a: string, b: string): boolean {
  return timingSafeEqual(utf8(a), utf8(b));
}
