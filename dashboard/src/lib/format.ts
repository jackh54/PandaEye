// Display helpers. Everything returns plain strings; escaping is done by the JSX renderer.

export const ACTIONS = ["alert", "mitigate", "kick", "ban", "unmitigate", "unban"] as const;
export type ActionName = (typeof ACTIONS)[number];

/** Accepts "BAN", "ACTION_BAN", "ban" or the proto enum number (1..6). */
export function normalizeAction(v: unknown): ActionName | null {
  if (typeof v === "number") return ACTIONS[v - 1] ?? null;
  if (typeof v !== "string") return null;
  const s = v.trim().toLowerCase().replace(/^action_/, "");
  return (ACTIONS as readonly string[]).includes(s) ? (s as ActionName) : null;
}

export const ACTION_LABEL: Record<ActionName, string> = {
  alert: "Alert",
  mitigate: "Mitigate",
  kick: "Kick",
  ban: "Ban",
  unmitigate: "Unmitigate",
  unban: "Unban",
};

/**
 * Parses API timestamps: RFC 3339 strings, or unix numbers in seconds, milliseconds or microseconds
 * (numeric strings too, as protojson encodes 64-bit integers as strings).
 */
export function toDate(v: unknown): Date | null {
  if (v === null || v === undefined || v === "" || v === 0 || v === "0") return null;
  let n: number | null = null;
  if (typeof v === "number") n = v;
  else if (typeof v === "string" && /^\d+(\.\d+)?$/.test(v)) n = Number(v);
  if (n !== null) {
    if (!Number.isFinite(n) || n <= 0) return null;
    if (n < 1e11) n *= 1000; // seconds
    else if (n > 1e14) n /= 1000; // microseconds
    const d = new Date(n);
    return Number.isNaN(d.getTime()) ? null : d;
  }
  if (typeof v === "string") {
    const d = new Date(v);
    return Number.isNaN(d.getTime()) ? null : d;
  }
  return null;
}

export function isoString(v: unknown): string | null {
  const d = toDate(v);
  return d ? d.toISOString() : null;
}

/** "2026-10-21 18:32 UTC" */
export function formatUtc(v: unknown, withSeconds = false): string {
  const d = toDate(v);
  if (!d) return "—";
  const iso = d.toISOString();
  return `${iso.slice(0, 10)} ${iso.slice(11, withSeconds ? 19 : 16)} UTC`;
}

export function formatRelative(v: unknown, now = Date.now()): string {
  const d = toDate(v);
  if (!d) return "—";
  const s = Math.round((now - d.getTime()) / 1000);
  const future = s < 0;
  const a = Math.abs(s);
  let text: string;
  if (a < 45) text = `${a} s`;
  else if (a < 45 * 60) text = `${Math.round(a / 60)} min`;
  else if (a < 36 * 3600) text = `${Math.round(a / 3600)} h`;
  else text = `${Math.round(a / 86400)} d`;
  return future ? `in ${text}` : `${text} ago`;
}

export function formatNumber(v: unknown, digits = 0): string {
  const n = typeof v === "number" ? v : typeof v === "string" ? Number(v) : NaN;
  if (!Number.isFinite(n)) return "—";
  return n.toLocaleString("en-US", { minimumFractionDigits: digits, maximumFractionDigits: digits });
}

export function formatPercent(v: unknown, digits = 0): string {
  const n = typeof v === "number" ? v : typeof v === "string" ? Number(v) : NaN;
  if (!Number.isFinite(n)) return "—";
  return `${(n * 100).toFixed(digits)}%`;
}

export function formatMsFromUs(us: unknown): string {
  const n = typeof us === "number" ? us : typeof us === "string" ? Number(us) : NaN;
  if (!Number.isFinite(n)) return "—";
  const ms = n / 1000;
  return ms < 10 ? `${ms.toFixed(1)} ms` : `${Math.round(ms)} ms`;
}

export function formatDuration(seconds: unknown): string {
  const n = typeof seconds === "number" ? seconds : Number(seconds);
  if (!Number.isFinite(n) || n < 0) return "—";
  const d = Math.floor(n / 86400);
  const h = Math.floor((n % 86400) / 3600);
  const m = Math.floor((n % 3600) / 60);
  if (d > 0) return `${d} d ${h} h`;
  if (h > 0) return `${h} h ${m} min`;
  return `${m} min`;
}

export function shortId(id: string, len = 8): string {
  return id.length > len ? id.slice(0, len) : id;
}

/** Path segment, percent-encoded. */
export function seg(s: string): string {
  return encodeURIComponent(s);
}

export function withQuery(path: string, params: Record<string, string | number | undefined | null>): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== null && v !== "") q.set(k, String(v));
  }
  const s = q.toString();
  return s ? `${path}?${s}` : path;
}
