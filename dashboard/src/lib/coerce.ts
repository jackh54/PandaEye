// Defensive coercion of untrusted JSON into typed values. The dashboard never trusts the shape of API
// responses: a missing or odd field degrades to a placeholder instead of an exception.

export type Json = Record<string, unknown>;

export function obj(v: unknown): Json {
  return v && typeof v === "object" && !Array.isArray(v) ? (v as Json) : {};
}

export function arr(v: unknown): unknown[] {
  return Array.isArray(v) ? v : [];
}

/** First present key (snake_case first, then camelCase alternatives). */
export function pick(o: Json, ...keys: string[]): unknown {
  for (const k of keys) {
    if (o[k] !== undefined && o[k] !== null) return o[k];
  }
  return undefined;
}

export function str(v: unknown, max = 4096): string {
  if (typeof v === "string") return v.length > max ? v.slice(0, max) : v;
  if (typeof v === "number" && Number.isFinite(v)) return String(v);
  return "";
}

export function strOrNull(v: unknown, max = 4096): string | null {
  const s = str(v, max);
  return s ? s : null;
}

export function num(v: unknown, fallback = 0): number {
  const n = typeof v === "number" ? v : typeof v === "string" && v.trim() !== "" ? Number(v) : NaN;
  return Number.isFinite(n) ? n : fallback;
}

export function numOrNull(v: unknown): number | null {
  const n = typeof v === "number" ? v : typeof v === "string" && v.trim() !== "" ? Number(v) : NaN;
  return Number.isFinite(n) ? n : null;
}

export function bool(v: unknown): boolean {
  return v === true || v === 1 || v === "1" || v === "true";
}

export function boolOrNull(v: unknown): boolean | null {
  if (v === undefined || v === null) return null;
  return bool(v);
}

export function strings(v: unknown, max = 256): string[] {
  return arr(v)
    .map((x) => str(x, max))
    .filter(Boolean);
}

/** Parses JSON-in-a-string fields (sqlite json columns) or passes objects through. */
export function jsonish(v: unknown): unknown {
  if (typeof v === "string") {
    const t = v.trim();
    if (t.startsWith("{") || t.startsWith("[")) {
      try {
        return JSON.parse(t);
      } catch {
        return v;
      }
    }
  }
  return v;
}

/** A list from `{<key>: [...]}`, `{items: [...]}`, `{data: [...]}` or a bare array. */
export function listFrom(v: unknown, key: string): unknown[] {
  if (Array.isArray(v)) return v;
  const o = obj(v);
  return arr(o[key] ?? o.items ?? o.data);
}

export function nextCursor(v: unknown): string | null {
  const o = obj(v);
  const c = pick(o, "next_cursor", "nextCursor", "cursor");
  return typeof c === "string" && c ? c.slice(0, 512) : null;
}
