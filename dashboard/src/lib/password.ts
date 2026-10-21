// Password hashing: PBKDF2-SHA256 (100 000 iterations, the Workers maximum) over
// HMAC-SHA256(PASSWORD_PEPPER, password). Stored as `pbkdf2-sha256$<iterations>$<salt b64>$<hash b64>`.

import { b64decode, b64encode, hmacSha256, randomBytes, timingSafeEqual } from "./crypto";

export const PBKDF2_ITERATIONS = 100_000;
/** Workers' WebCrypto refuses more than 100 000 PBKDF2 iterations. */
export const PBKDF2_MAX_ITERATIONS = 100_000;
const SALT_BYTES = 16;
const HASH_BYTES = 32;
const SCHEME = "pbkdf2-sha256";

export const PASSWORD_MIN_LENGTH = 12;
export const PASSWORD_MAX_LENGTH = 1024;

/**
 * A syntactically valid hash that matches no password. Verifying against it costs the same as a real
 * verification, which keeps "unknown user" and "wrong password" indistinguishable by timing.
 */
export const DUMMY_PASSWORD_HASH = `${SCHEME}$${PBKDF2_ITERATIONS}$${b64encode(new Uint8Array(SALT_BYTES))}$${b64encode(
  new Uint8Array(HASH_BYTES),
)}`;

async function derive(
  password: string,
  pepper: string,
  salt: Uint8Array<ArrayBuffer>,
  iterations: number,
  bytes: number,
): Promise<Uint8Array<ArrayBuffer>> {
  const prehash = await hmacSha256(pepper, password);
  const key = await crypto.subtle.importKey("raw", prehash, "PBKDF2", false, ["deriveBits"]);
  const bits = await crypto.subtle.deriveBits({ name: "PBKDF2", hash: "SHA-256", salt, iterations }, key, bytes * 8);
  return new Uint8Array(bits);
}

export async function hashPassword(password: string, pepper: string): Promise<string> {
  if (!pepper) throw new Error("PASSWORD_PEPPER is not configured");
  const salt = randomBytes(SALT_BYTES);
  const hash = await derive(password, pepper, salt, PBKDF2_ITERATIONS, HASH_BYTES);
  return `${SCHEME}$${PBKDF2_ITERATIONS}$${b64encode(salt)}$${b64encode(hash)}`;
}

interface ParsedHash {
  iterations: number;
  salt: Uint8Array<ArrayBuffer>;
  hash: Uint8Array<ArrayBuffer>;
}

export function parsePasswordHash(stored: string): ParsedHash | null {
  const parts = stored.split("$");
  if (parts.length !== 4 || parts[0] !== SCHEME) return null;
  const iterations = Number(parts[1]);
  if (!Number.isInteger(iterations) || iterations < 1 || iterations > PBKDF2_MAX_ITERATIONS) return null;
  try {
    const salt = b64decode(parts[2]!);
    const hash = b64decode(parts[3]!);
    if (salt.length < 8 || hash.length < 16 || hash.length > 64) return null;
    return { iterations, salt, hash };
  } catch {
    return null;
  }
}

/** Verifies a password in constant time with respect to the stored hash. Malformed hashes never match. */
export async function verifyPassword(password: string, stored: string, pepper: string): Promise<boolean> {
  const parsed = parsePasswordHash(stored);
  if (!parsed || !pepper) {
    // Still spend the time of a real verification.
    await derive(password, pepper || "x", new Uint8Array(SALT_BYTES), PBKDF2_ITERATIONS, HASH_BYTES);
    return false;
  }
  const candidate = await derive(password, pepper, parsed.salt, parsed.iterations, parsed.hash.length);
  return timingSafeEqual(candidate, parsed.hash);
}

/** True when the stored hash uses parameters other than the current ones. */
export function needsRehash(stored: string): boolean {
  const parsed = parsePasswordHash(stored);
  return !parsed || parsed.iterations !== PBKDF2_ITERATIONS || parsed.hash.length !== HASH_BYTES;
}

export function passwordProblem(password: string): string | null {
  if (password.length < PASSWORD_MIN_LENGTH) return `Use at least ${PASSWORD_MIN_LENGTH} characters.`;
  if (password.length > PASSWORD_MAX_LENGTH) return `Use at most ${PASSWORD_MAX_LENGTH} characters.`;
  if (/^(.)\1+$/.test(password)) return "Do not repeat a single character.";
  return null;
}
