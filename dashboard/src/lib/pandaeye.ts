// Typed wrappers for the API endpoints the dashboard uses (ARCHITECTURE 5.8). The canonical wire shapes are
// snake_case JSON as documented next to each normaliser; a few alternatives (camelCase, enum names or
// numbers, unix numbers or RFC 3339 strings) are accepted so a small mismatch degrades gracefully.

import { ApiClient, type Query } from "./api";
import {
  arr,
  bool,
  boolOrNull,
  jsonish,
  listFrom,
  nextCursor,
  num,
  numOrNull,
  obj,
  pick,
  str,
  strOrNull,
  strings,
  type Json,
} from "./coerce";
import { normalizeCategory, type Category } from "./checks";
import { normalizeAction, type ActionName } from "./format";

export interface Page<T> {
  items: T[];
  nextCursor: string | null;
}

// ---- tenant ------------------------------------------------------------------------------------------

export interface Tenant {
  id: string;
  name: string;
  slug: string;
  plan: string;
  leniency: number;
  autoban: boolean;
  autokick: boolean;
  mitigations: boolean;
  enforcement: boolean;
  reputationOptIn: boolean;
  /** Masked webhook as returned by the API ("" when none is set). */
  discordWebhook: string;
  discordMinAction: ActionName;
  overLimitSince: string | null;
  maxServers: number | null;
}

/** Unwraps `{<key>: {...}, ...rest}` envelopes, keeping sibling fields. */
function unwrap(v: unknown, key: string): Json {
  const root = obj(v);
  const inner = root[key];
  return inner && typeof inner === "object" && !Array.isArray(inner) ? { ...root, ...obj(inner) } : root;
}

export function toTenant(v: unknown): Tenant {
  const o = unwrap(v, "tenant");
  const len = Math.round(num(o.leniency, 4));
  return {
    id: str(pick(o, "id", "tenant_id")),
    name: str(o.name, 200),
    slug: str(o.slug, 200),
    plan: str(o.plan, 64) || "free",
    leniency: len >= 1 && len <= 5 ? len : 4,
    autoban: bool(o.autoban),
    autokick: bool(o.autokick),
    mitigations: o.mitigations === undefined ? true : bool(o.mitigations),
    enforcement: bool(o.enforcement),
    reputationOptIn: bool(pick(o, "reputation_opt_in", "reputationOptIn")),
    discordWebhook: str(pick(o, "discord_webhook", "discord_webhook_masked", "discordWebhook"), 300),
    discordMinAction: normalizeAction(pick(o, "discord_min_action", "discordMinAction")) ?? "mitigate",
    overLimitSince: strOrNull(pick(o, "over_limit_since", "overLimitSince")),
    maxServers: numOrNull(pick(o, "max_servers", "maxServers")),
  };
}

/** PATCH /api/v1/tenant body: only the changed fields are sent. */
export interface TenantPatch {
  leniency?: number;
  autoban?: boolean;
  autokick?: boolean;
  mitigations?: boolean;
  enforcement?: boolean;
  reputation_opt_in?: boolean;
  /** "" removes the webhook. */
  discord_webhook?: string;
  discord_min_action?: ActionName;
}

// ---- servers -----------------------------------------------------------------------------------------

export interface Server {
  id: string;
  name: string;
  networkId: string;
  platform: string;
  platformVersion: string;
  pluginVersion: string;
  minecraftVersion: string;
  online: boolean;
  onlinePlayers: number;
  lastSeenAt: string | null;
  shadowMode: boolean | null;
}

export function toServer(v: unknown): Server {
  const o = obj(v);
  return {
    id: str(pick(o, "id", "server_id"), 128),
    name: str(o.name, 200),
    networkId: str(pick(o, "network_id", "networkId"), 128),
    platform: str(o.platform, 64).replace(/^PLATFORM_/, "").toLowerCase(),
    platformVersion: str(pick(o, "platform_version", "platformVersion"), 200),
    pluginVersion: str(pick(o, "plugin_version", "pluginVersion"), 64),
    minecraftVersion: str(pick(o, "minecraft_version", "minecraftVersion"), 64),
    online: bool(o.online),
    onlinePlayers: num(pick(o, "online_players", "onlinePlayers")),
    lastSeenAt: strOrNull(pick(o, "last_seen_at", "lastSeenAt")),
    shadowMode: boolOrNull(pick(o, "shadow_mode", "shadowMode")),
  };
}

// ---- verdicts ----------------------------------------------------------------------------------------

export type VerdictStatus = "open" | "confirmed" | "overturned" | "expired" | "superseded";
export const VERDICT_STATUSES: readonly VerdictStatus[] = ["open", "confirmed", "overturned", "expired", "superseded"];

export interface Verdict {
  id: string;
  serverId: string;
  serverName: string;
  networkId: string;
  playerUuid: string;
  playerName: string;
  sessionId: string;
  action: ActionName | null;
  category: Category | null;
  score: number;
  confidence: number;
  reason: string;
  checks: string[];
  shadow: boolean;
  status: string;
  supersedes: string;
  createdAt: string | null;
  deliveredAt: string | null;
  ackedAt: string | null;
  executed: boolean | null;
  ackNote: string;
  clipIds: string[];
  mitigations: string[];
  strength: number | null;
  /** Present on the detail endpoint. */
  evidence: Evidence | null;
}

export function toVerdict(v: unknown): Verdict {
  const o = unwrap(v, "verdict");
  const evidenceRaw = jsonish(o.evidence);
  return {
    id: str(pick(o, "id", "verdict_id"), 128),
    serverId: str(pick(o, "server_id", "serverId"), 128),
    serverName: str(pick(o, "server_name", "serverName"), 200),
    networkId: str(pick(o, "network_id", "networkId"), 128),
    playerUuid: str(pick(o, "player_uuid", "playerUuid", "player"), 64),
    playerName: str(pick(o, "player_name", "playerName"), 64),
    sessionId: str(pick(o, "session_id", "sessionId"), 128),
    action: normalizeAction(o.action),
    category: normalizeCategory(o.category),
    score: num(o.score),
    confidence: num(o.confidence),
    reason: str(o.reason, 1000),
    checks: strings(jsonish(o.checks)),
    shadow: bool(o.shadow),
    status: str(o.status, 32) || "open",
    supersedes: str(o.supersedes, 128),
    createdAt: strOrNull(pick(o, "created_at", "createdAt", "created_unix_ms")),
    deliveredAt: strOrNull(pick(o, "delivered_at", "deliveredAt")),
    ackedAt: strOrNull(pick(o, "acked_at", "ackedAt")),
    executed: boolOrNull(o.executed),
    ackNote: str(pick(o, "ack_note", "ackNote", "note"), 500),
    clipIds: strings(jsonish(pick(o, "clip_ids", "clipIds")), 128),
    mitigations: strings(jsonish(o.mitigations), 64).map((m) => m.replace(/^MITIGATION_/, "").toLowerCase()),
    strength: numOrNull(o.strength),
    evidence: evidenceRaw && typeof evidenceRaw === "object" ? toEvidence(evidenceRaw) : null,
  };
}

export interface EvidenceSignal {
  check: string;
  category: Category | null;
  points: number;
  confidence: number;
  info: string;
  data: [string, string][];
  time: string | null;
  clipId: string;
  local: boolean;
}

export interface EvidenceContribution {
  name: string;
  contribution: number;
  cap: number | null;
  confidence: number | null;
  count: number | null;
}

export interface Evidence {
  score: number | null;
  confidence: number | null;
  leniency: number | null;
  thresholds: [string, number][];
  families: EvidenceContribution[];
  checks: EvidenceContribution[];
  signals: EvidenceSignal[];
  session: [string, string][];
}

function contributions(v: unknown, nameKeys: string[]): EvidenceContribution[] {
  const list: EvidenceContribution[] = [];
  const toEntry = (name: string, x: unknown): EvidenceContribution => {
    if (typeof x === "number" || typeof x === "string") {
      return { name, contribution: num(x), cap: null, confidence: null, count: null };
    }
    const e = obj(x);
    return {
      name,
      contribution: num(pick(e, "contribution", "points", "score", "value", "acc")),
      cap: numOrNull(e.cap),
      confidence: numOrNull(pick(e, "confidence", "conf")),
      count: numOrNull(pick(e, "count", "signals", "n")),
    };
  };
  if (Array.isArray(v)) {
    for (const x of v.slice(0, 100)) {
      const e = obj(x);
      list.push(toEntry(str(pick(e, ...nameKeys), 128), x));
    }
  } else if (v && typeof v === "object") {
    for (const [k, x] of Object.entries(v).slice(0, 100)) list.push(toEntry(k.slice(0, 128), x));
  }
  return list.filter((e) => e.name).sort((a, b) => b.contribution - a.contribution);
}

function scalarEntries(v: unknown, limit = 32): [string, string][] {
  const out: [string, string][] = [];
  for (const [k, x] of Object.entries(obj(v)).slice(0, limit)) {
    if (x === null || x === undefined) continue;
    if (typeof x === "object") out.push([k.slice(0, 64), JSON.stringify(x).slice(0, 200)]);
    else out.push([k.slice(0, 64), String(x).slice(0, 200)]);
  }
  return out;
}

/**
 * Evidence JSON (ARCHITECTURE 5.6), canonical shape:
 * {score, confidence, leniency, thresholds: {alert, mitigate, kick, ban},
 *  families: [{family, contribution, cap}], checks: [{check, contribution, cap, confidence, count}],
 *  signals: [{check, category, points, confidence, info, data: {k: number}, time, clip_id, local}],
 *  session: {...}}
 */
export function toEvidence(v: unknown): Evidence {
  const o = obj(v);
  const thresholds = Object.entries(obj(o.thresholds))
    .map(([k, x]) => [k.slice(0, 32), num(x)] as [string, number])
    .slice(0, 8);
  const signals: EvidenceSignal[] = arr(pick(o, "signals", "top_signals"))
    .slice(0, 50)
    .map((x) => {
      const s = obj(x);
      const check = str(pick(s, "check", "check_id", "checkId"), 128);
      return {
        check,
        category: normalizeCategory(s.category),
        points: num(s.points),
        confidence: num(s.confidence),
        info: str(s.info, 512),
        data: scalarEntries(s.data),
        time: strOrNull(pick(s, "time", "event_time", "time_us", "event_time_us")),
        clipId: str(pick(s, "clip_id", "clipId"), 128),
        local: bool(s.local),
      };
    });
  return {
    score: numOrNull(o.score),
    confidence: numOrNull(o.confidence),
    leniency: numOrNull(o.leniency),
    thresholds,
    families: contributions(pick(o, "families", "family_contributions"), ["family", "name", "id"]),
    checks: contributions(pick(o, "checks", "check_contributions"), ["check", "check_id", "name", "id"]),
    signals,
    session: scalarEntries(o.session),
  };
}

export interface VerdictFilters {
  action?: string;
  status?: string;
  player?: string;
  server?: string;
  since?: string;
  until?: string;
  shadow?: string;
}

// ---- players -----------------------------------------------------------------------------------------

export interface PlayerSummary {
  uuid: string;
  lastName: string;
  firstSeenAt: string | null;
  lastSeenAt: string | null;
  lastServerId: string;
}

export function toPlayerSummary(v: unknown): PlayerSummary {
  const o = obj(v);
  return {
    uuid: str(pick(o, "uuid", "player_uuid"), 64),
    lastName: str(pick(o, "last_name", "lastName", "name"), 64),
    firstSeenAt: strOrNull(pick(o, "first_seen_at", "firstSeenAt")),
    lastSeenAt: strOrNull(pick(o, "last_seen_at", "lastSeenAt")),
    lastServerId: str(pick(o, "last_server_id", "lastServerId"), 128),
  };
}

export interface Reputation {
  risk: number;
  networksFlagged: number;
  confirmedBans: number;
}

export interface PlayerDetail extends PlayerSummary {
  names: { name: string; seenAt: string | null }[];
  brand: string;
  clientVersion: string;
  verdicts: Verdict[];
  bans: Ban[];
  reputation: Reputation | null;
}

export function toPlayerDetail(v: unknown): PlayerDetail {
  const root = obj(v);
  const o = obj(root.player ?? v);
  const base = toPlayerSummary(o);
  const rep = pick(o, "reputation") ?? pick(root, "reputation");
  const r = obj(rep);
  return {
    ...base,
    names: arr(pick(o, "names") ?? root.names)
      .slice(0, 100)
      .map((x) => (typeof x === "string" ? { name: x.slice(0, 64), seenAt: null } : { name: str(obj(x).name, 64), seenAt: strOrNull(pick(obj(x), "seen_at", "seenAt")) }))
      .filter((n) => n.name),
    brand: str(pick(o, "brand", "client_brand", "last_brand"), 128),
    clientVersion: str(pick(o, "client_version", "clientVersion"), 64),
    verdicts: arr(pick(o, "verdicts") ?? root.verdicts).slice(0, 200).map(toVerdict),
    bans: arr(pick(o, "bans") ?? root.bans).slice(0, 200).map(toBan),
    reputation: rep && typeof rep === "object"
      ? { risk: num(r.risk), networksFlagged: num(pick(r, "networks_flagged", "networksFlagged")), confirmedBans: num(pick(r, "confirmed_bans", "confirmedBans")) }
      : null,
  };
}

// ---- bans --------------------------------------------------------------------------------------------

export type UnbanState = "none" | "pending" | "done";

export interface Ban {
  verdictId: string;
  serverId: string;
  playerUuid: string;
  playerName: string;
  active: boolean;
  reason: string;
  createdAt: string | null;
  overturnedAt: string | null;
  overturnedBy: string;
  overturnNote: string;
  unbanVerdictId: string;
  unbanState: UnbanState;
}

export function toBan(v: unknown): Ban {
  const o = obj(v);
  const state = str(pick(o, "unban_state", "unbanState"), 16).toLowerCase();
  return {
    verdictId: str(pick(o, "verdict_id", "verdictId", "id"), 128),
    serverId: str(pick(o, "server_id", "serverId"), 128),
    playerUuid: str(pick(o, "player_uuid", "playerUuid"), 64),
    playerName: str(pick(o, "player_name", "playerName"), 64),
    active: bool(o.active),
    reason: str(o.reason, 1000),
    createdAt: strOrNull(pick(o, "created_at", "createdAt")),
    overturnedAt: strOrNull(pick(o, "overturned_at", "overturnedAt")),
    overturnedBy: str(pick(o, "overturned_by", "overturnedBy"), 128),
    overturnNote: str(pick(o, "overturn_note", "overturnNote"), 1000),
    unbanVerdictId: str(pick(o, "unban_verdict_id", "unbanVerdictId"), 128),
    unbanState: state === "pending" || state === "done" ? state : "none",
  };
}

// ---- appeals -----------------------------------------------------------------------------------------

export interface Appeal {
  id: string;
  verdictId: string;
  playerUuid: string;
  playerName: string;
  message: string;
  contact: string;
  status: string;
  createdAt: string | null;
  decidedAt: string | null;
  decidedBy: string;
  decisionNote: string;
}

export function toAppeal(v: unknown): Appeal {
  const o = obj(v);
  return {
    id: str(o.id, 128),
    verdictId: str(pick(o, "verdict_id", "verdictId"), 128),
    playerUuid: str(pick(o, "player_uuid", "playerUuid"), 64),
    playerName: str(pick(o, "player_name", "playerName"), 64),
    message: str(o.message, 2000),
    contact: str(o.contact, 200),
    status: str(o.status, 32) || "open",
    createdAt: strOrNull(pick(o, "created_at", "createdAt")),
    decidedAt: strOrNull(pick(o, "decided_at", "decidedAt")),
    decidedBy: str(pick(o, "decided_by", "decidedBy"), 128),
    decisionNote: str(pick(o, "decision_note", "decisionNote"), 1000),
  };
}

// ---- stats, usage, status ----------------------------------------------------------------------------

export interface CountWindow {
  label: string;
  byAction: Partial<Record<ActionName, number>>;
  byCategory: Partial<Record<Category, number>>;
  shadow: number | null;
}

function toCountWindow(label: string, v: unknown): CountWindow {
  const o = obj(v);
  const byAction: Partial<Record<ActionName, number>> = {};
  for (const [k, x] of Object.entries(obj(pick(o, "by_action", "byAction")))) {
    const a = normalizeAction(k);
    if (a) byAction[a] = (byAction[a] ?? 0) + num(x);
  }
  const byCategory: Partial<Record<Category, number>> = {};
  for (const [k, x] of Object.entries(obj(pick(o, "by_category", "byCategory")))) {
    const c = normalizeCategory(k);
    if (c) byCategory[c] = (byCategory[c] ?? 0) + num(x);
  }
  return { label, byAction, byCategory, shadow: numOrNull(o.shadow) };
}

/** GET /api/v1/stats/overview: {last_24h: {by_action, by_category, shadow}, last_7d: {...}} */
export function toOverview(v: unknown): CountWindow[] {
  const o = obj(v);
  return [
    toCountWindow("24 h", pick(o, "last_24h", "24h", "day")),
    toCountWindow("7 d", pick(o, "last_7d", "7d", "week")),
  ];
}

export interface Percentiles {
  label: string;
  p50Us: number | null;
  p95Us: number | null;
  p99Us: number | null;
  count: number | null;
}

function toPercentiles(label: string, v: unknown): Percentiles {
  const o = obj(v);
  const us = (k: string) => {
    const a = numOrNull(o[`${k}_us`]);
    if (a !== null) return a;
    const m = numOrNull(o[`${k}_ms`]);
    return m === null ? null : m * 1000;
  };
  return { label, p50Us: us("p50"), p95Us: us("p95"), p99Us: us("p99"), count: numOrNull(pick(o, "count", "samples")) };
}

export interface Latency {
  windows: Percentiles[];
  sloTargetMs: number;
  sloMet: boolean | null;
}

/** GET /api/v1/stats/latency: {last_1h: {p50_us, p95_us, p99_us, count}, last_24h: {...}, slo: {target_p95_ms, met}} */
export function toLatency(v: unknown): Latency {
  const o = obj(v);
  const slo = obj(o.slo);
  return {
    windows: [toPercentiles("1 h", pick(o, "last_1h", "1h", "hour")), toPercentiles("24 h", pick(o, "last_24h", "24h", "day"))],
    sloTargetMs: num(pick(slo, "target_p95_ms", "target_ms"), 100),
    sloMet: boolOrNull(slo.met),
  };
}

export interface Usage {
  plan: string;
  freeTierLimit: number;
  overLimitSince: string | null;
  days: { day: string; peakPlayers: number }[];
}

/** GET /api/v1/usage: {plan, free_tier_limit, over_limit_since, days: [{day: "YYYY-MM-DD", peak_players}]} */
export function toUsage(v: unknown): Usage {
  const o = obj(v);
  const days = arr(pick(o, "days", "usage", "daily"))
    .map((x) => {
      const d = obj(x);
      return { day: str(d.day, 10), peakPlayers: num(pick(d, "peak_players", "peakPlayers")) };
    })
    .filter((d) => /^\d{4}-\d{2}-\d{2}$/.test(d.day))
    .sort((a, b) => (a.day < b.day ? -1 : a.day > b.day ? 1 : 0))
    .slice(-366);
  return {
    plan: str(o.plan, 64) || "free",
    freeTierLimit: num(pick(o, "free_tier_limit", "freeTierLimit", "limit"), 30),
    overLimitSince: strOrNull(pick(o, "over_limit_since", "overLimitSince")),
    days,
  };
}

export interface PublicStatus {
  region: string;
  version: string;
  uptimeS: number | null;
  connectedServers: number | null;
  probe: Percentiles;
  sloTargetMs: number;
  sloMet: boolean | null;
}

/** GET /status: {region, version, uptime_s, connected_servers, probe_rtt: {p50_us, p95_us, p99_us, samples, window_s}, slo: {target_p95_ms, met, ...}} */
export function toPublicStatus(v: unknown): PublicStatus {
  const o = obj(v);
  const slo = obj(o.slo);
  return {
    region: str(o.region, 64),
    version: str(o.version, 64),
    uptimeS: numOrNull(pick(o, "uptime_s", "uptime_seconds", "uptime")),
    connectedServers: numOrNull(pick(o, "connected_servers", "connectedServers")),
    probe: toPercentiles("1 h", pick(o, "probe_rtt", "probe", "probe_rtt_1h")),
    sloTargetMs: num(pick(slo, "target_p95_ms", "target_ms"), 100),
    sloMet: boolOrNull(slo.met),
  };
}

// ---- API keys, checks, audit, misc -------------------------------------------------------------------

export const API_KEY_SCOPES = ["read", "verdicts:write", "settings:write", "erase"] as const;

export interface ApiKey {
  id: string;
  name: string;
  scopes: string[];
  createdAt: string | null;
  lastUsedAt: string | null;
  revokedAt: string | null;
}

export function toApiKey(v: unknown): ApiKey {
  const o = obj(v);
  return {
    id: str(o.id, 128),
    name: str(o.name, 100),
    scopes: strings(jsonish(o.scopes), 32),
    createdAt: strOrNull(pick(o, "created_at", "createdAt")),
    lastUsedAt: strOrNull(pick(o, "last_used_at", "lastUsedAt")),
    revokedAt: strOrNull(pick(o, "revoked_at", "revokedAt")),
  };
}

export interface CheckConfig {
  id: string;
  enabled: boolean;
  overrides: Record<string, number>;
}

/** GET /api/v1/checks: {checks: [{id, enabled, overrides: {name: number}}]} */
export function toCheckConfigs(v: unknown): CheckConfig[] {
  return listFrom(v, "checks")
    .slice(0, 500)
    .map((x) => {
      const o = obj(x);
      const overrides: Record<string, number> = {};
      for (const [k, val] of Object.entries(obj(jsonish(o.overrides))).slice(0, 32)) {
        const n = numOrNull(val);
        if (n !== null) overrides[k.slice(0, 64)] = n;
      }
      return { id: str(pick(o, "id", "check_id", "check"), 128), enabled: o.enabled === undefined ? true : bool(o.enabled), overrides };
    })
    .filter((c) => c.id);
}

export interface AuditEntry {
  id: string;
  principal: string;
  action: string;
  target: string;
  detail: string;
  createdAt: string | null;
}

export function toAuditEntry(v: unknown): AuditEntry {
  const o = obj(v);
  const detail = jsonish(o.detail);
  return {
    id: str(o.id, 64),
    principal: str(o.principal, 200),
    action: str(o.action, 100),
    target: str(o.target, 200),
    detail: typeof detail === "string" ? detail.slice(0, 300) : detail ? JSON.stringify(detail).slice(0, 300) : "",
    createdAt: strOrNull(pick(o, "created_at", "createdAt")),
  };
}

function page<T>(v: unknown, key: string, map: (x: unknown) => T, max = 200): Page<T> {
  return { items: listFrom(v, key).slice(0, max).map(map), nextCursor: nextCursor(v) };
}

/** Typed facade over ApiClient. Paths are relative to PANDAEYE_API_URL. */
export class PandaEyeApi {
  constructor(readonly client: ApiClient) {}

  // auth
  async verifyLicense(licenseKey: string): Promise<{ tenantId: string; slug: string; created: boolean }> {
    const r = obj(await this.client.post("/api/v1/auth/license", { license_key: licenseKey }, { anonymous: true }));
    return { tenantId: str(pick(r, "tenant_id", "tenantId"), 128), slug: str(r.slug, 200), created: bool(r.created) };
  }

  // tenant
  async tenant(): Promise<Tenant> {
    return toTenant(await this.client.get("/api/v1/tenant"));
  }
  async patchTenant(patch: TenantPatch): Promise<Tenant | null> {
    const r = await this.client.patch("/api/v1/tenant", patch);
    return r && typeof r === "object" && Object.keys(r).length ? toTenant(r) : null;
  }

  // servers
  async servers(): Promise<Server[]> {
    return listFrom(await this.client.get("/api/v1/servers"), "servers").slice(0, 1000).map(toServer);
  }

  // verdicts
  async verdicts(filters: VerdictFilters & { limit?: number; cursor?: string }): Promise<Page<Verdict>> {
    return page(await this.client.get("/api/v1/verdicts", filters as Query), "verdicts", toVerdict);
  }
  async verdict(id: string): Promise<Verdict> {
    return toVerdict(await this.client.get(`/api/v1/verdicts/${encodeURIComponent(id)}`));
  }
  async confirmVerdict(id: string, note: string): Promise<void> {
    await this.client.post(`/api/v1/verdicts/${encodeURIComponent(id)}/confirm`, note ? { note } : {});
  }
  async overturnVerdict(id: string, note: string): Promise<void> {
    await this.client.post(`/api/v1/verdicts/${encodeURIComponent(id)}/overturn`, note ? { note } : {});
  }
  async clip(id: string): Promise<unknown> {
    return this.client.get(`/api/v1/clips/${encodeURIComponent(id)}`);
  }

  // players
  async players(name: string, cursor?: string): Promise<Page<PlayerSummary>> {
    return page(await this.client.get("/api/v1/players", { name, cursor, limit: 50 }), "players", toPlayerSummary);
  }
  async player(uuid: string): Promise<PlayerDetail> {
    return toPlayerDetail(await this.client.get(`/api/v1/players/${encodeURIComponent(uuid)}`));
  }
  async erasePlayer(uuid: string): Promise<[string, string][]> {
    const r = obj(await this.client.delete(`/api/v1/players/${encodeURIComponent(uuid)}`));
    return Object.entries(obj(r.counts ?? r)).map(([k, v]) => [k.slice(0, 64), str(v, 32)] as [string, string]).slice(0, 32);
  }
  async watchPlayer(uuid: string, seconds: number): Promise<void> {
    await this.client.post(`/api/v1/players/${encodeURIComponent(uuid)}/watch`, { seconds });
  }

  // bans and appeals
  async bans(active: boolean, cursor?: string): Promise<Page<Ban>> {
    return page(await this.client.get("/api/v1/bans", { active: active ? "true" : undefined, cursor, limit: 100 }), "bans", toBan);
  }
  async appeals(status: string, cursor?: string): Promise<Page<Appeal>> {
    return page(await this.client.get("/api/v1/appeals", { status, cursor, limit: 100 }), "appeals", toAppeal);
  }
  async decideAppeal(id: string, decision: "accept" | "deny", note: string): Promise<void> {
    await this.client.post(`/api/v1/appeals/${encodeURIComponent(id)}/decide`, { decision, note });
  }
  async submitPublicAppeal(a: { verdict_id: string; appeal_code: string; message: string; contact: string }): Promise<void> {
    await this.client.post("/api/v1/public/appeals", a, { anonymous: true });
  }

  // stats
  async overview(): Promise<CountWindow[]> {
    return toOverview(await this.client.get("/api/v1/stats/overview"));
  }
  async latency(): Promise<Latency> {
    return toLatency(await this.client.get("/api/v1/stats/latency"));
  }
  async usage(): Promise<Usage> {
    return toUsage(await this.client.get("/api/v1/usage"));
  }
  async status(): Promise<PublicStatus> {
    return toPublicStatus(await this.client.request("GET", "/status", { anonymous: true }));
  }

  // API keys
  async apiKeys(): Promise<ApiKey[]> {
    return listFrom(await this.client.get("/api/v1/apikeys"), "api_keys").slice(0, 500).map(toApiKey);
  }
  async createApiKey(name: string, scopes: string[]): Promise<ApiKey & { key: string }> {
    const r = obj(await this.client.post("/api/v1/apikeys", { name, scopes }));
    const inner = obj(r.api_key ?? r);
    return { ...toApiKey(inner), key: str(pick(r, "key", "plaintext", "secret") ?? pick(inner, "key", "plaintext"), 512) };
  }
  async revokeApiKey(id: string): Promise<void> {
    await this.client.delete(`/api/v1/apikeys/${encodeURIComponent(id)}`);
  }

  // checks
  async checks(): Promise<CheckConfig[]> {
    return toCheckConfigs(await this.client.get("/api/v1/checks"));
  }
  async putChecks(checks: CheckConfig[]): Promise<void> {
    await this.client.put("/api/v1/checks", { checks: checks.map((c) => ({ id: c.id, enabled: c.enabled, overrides: c.overrides })) });
  }

  // audit
  async audit(cursor?: string): Promise<Page<AuditEntry>> {
    return page(await this.client.get("/api/v1/audit", { cursor, limit: 100 }), "entries", toAuditEntry);
  }
}

export type { Json };
