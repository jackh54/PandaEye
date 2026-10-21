// Signed-in pages. Every route here runs behind requireSession + csrfGuard; mutating routes also check the
// role permission (the API enforces roles again from the assertion).

import { Hono } from "hono";
import { ApiError } from "../lib/api";
import { recentAudit } from "../lib/audit";
import { cloudChecks } from "../lib/checks";
import { normalizeAction, withQuery, type ActionName } from "../lib/format";
import { flashFromQuery } from "../lib/messages";
import { API_KEY_SCOPES, VERDICT_STATUSES, type CheckConfig, type TenantPatch, type VerdictStatus } from "../lib/pandaeye";
import { invitableRoles, isRole, type Role } from "../lib/roles";
import {
  createInvite,
  findUser,
  listPendingInvites,
  listUsers,
  revokeInvite,
  setUserDisabled,
  setUserRole,
} from "../lib/users";
import { checkbox, field, isDiscordWebhook, isId, isUuid, parseOverrides } from "../lib/validate";
import { csrfGuard, requirePerm, requireSession } from "../middleware";
import type { AppEnv, Ctx } from "../types";
import { ErrorPage } from "../views/errors";
import { ApiKeysPage, AuditPage, UsersPage } from "../views/pages/admin";
import { AppealsPage, BansPage } from "../views/pages/moderation";
import { OverviewPage, ServersPage, UsagePage, type Section } from "../views/pages/overview";
import { PlayerPage, PlayersPage } from "../views/pages/players";
import { ChecksPage, SettingsPage } from "../views/pages/settings";
import { VerdictPage, VerdictsPage, type VerdictFilterForm } from "../views/pages/verdicts";

async function settle<T>(p: Promise<T>): Promise<Section<T>> {
  try {
    return { ok: true, value: await p };
  } catch (e) {
    if (!(e instanceof ApiError)) throw e;
    return { ok: false };
  }
}

function flash(c: Ctx) {
  return flashFromQuery({ ok: c.req.query("ok"), err: c.req.query("err") });
}

const DAY_RE = /^\d{4}-\d{2}-\d{2}$/;
const PLAYER_FILTER_RE = /^[A-Za-z0-9_-]{1,64}$/;
const MIN_ACTIONS: ActionName[] = ["alert", "mitigate", "kick", "ban"];

function dayToIso(day: string, plusDays = 0): string | undefined {
  if (!DAY_RE.test(day)) return undefined;
  const d = new Date(`${day}T00:00:00Z`);
  if (Number.isNaN(d.getTime())) return undefined;
  d.setUTCDate(d.getUTCDate() + plusDays);
  return d.toISOString().replace(".000Z", "Z");
}

/** "Request rejected" page for API 4xx answers to a form submission, linking back. */
function rejected(c: Ctx, e: ApiError) {
  return c.html(
    <ErrorPage
      nonce={c.get("nonce")}
      session={c.get("session")}
      info={{ status: e.status === 403 ? 403 : 400, title: e.status === 403 ? "Not allowed" : "Request rejected", message: e.message }}
    />,
    e.status === 403 ? 403 : e.status === 404 ? 404 : 400,
  );
}

function isClientError(e: unknown): e is ApiError {
  return e instanceof ApiError && (e.kind === "client" || e.kind === "forbidden" || e.kind === "not_found");
}

export function dashboardRoutes(): Hono<AppEnv> {
  const r = new Hono<AppEnv>();
  r.use("*", requireSession(), csrfGuard());

  // ---- overview, servers, usage -----------------------------------------------------------------

  r.get("/", async (c) => {
    const api = c.get("api");
    const [tenant, stats, latency, servers, recent, appeals] = await Promise.all([
      settle(api.tenant()),
      settle(api.overview()),
      settle(api.latency()),
      settle(api.servers()),
      settle(api.verdicts({ limit: 10 }).then((p) => p.items)),
      settle(api.appeals("open").then((p) => p.items.length)),
    ]);
    return c.html(
      <OverviewPage
        nonce={c.get("nonce")}
        session={c.get("session")!}
        flash={flash(c)}
        tenant={tenant}
        stats={stats}
        latency={latency}
        servers={servers}
        recent={recent}
        openAppeals={appeals}
      />,
    );
  });

  r.get("/servers", async (c) => {
    const servers = await c.get("api").servers();
    return c.html(<ServersPage nonce={c.get("nonce")} session={c.get("session")!} servers={servers} />);
  });

  r.get("/usage", async (c) => {
    const usage = await c.get("api").usage();
    return c.html(<UsagePage nonce={c.get("nonce")} session={c.get("session")!} usage={usage} />);
  });

  // ---- verdicts ---------------------------------------------------------------------------------

  r.get("/verdicts", async (c) => {
    const q = c.req.query();
    const form: VerdictFilterForm = {};
    const action = normalizeAction(q.action);
    if (action) form.action = action;
    if (q.status && (VERDICT_STATUSES as readonly string[]).includes(q.status)) form.status = q.status as VerdictStatus;
    if (q.shadow === "true" || q.shadow === "false") form.shadow = q.shadow;
    const player = (q.player ?? "").trim();
    if (PLAYER_FILTER_RE.test(player)) form.player = player;
    if (q.server && isId(q.server)) form.server = q.server;
    if (q.since && DAY_RE.test(q.since)) form.sinceDay = q.since;
    if (q.until && DAY_RE.test(q.until)) form.untilDay = q.until;
    const cursor = q.cursor && q.cursor.length <= 512 ? q.cursor : undefined;

    const page = await c.get("api").verdicts({
      action: form.action,
      status: form.status,
      shadow: form.shadow,
      player: form.player,
      server: form.server,
      since: form.sinceDay ? dayToIso(form.sinceDay) : undefined,
      until: form.untilDay ? dayToIso(form.untilDay, 1) : undefined,
      cursor,
      limit: 50,
    });
    const next = page.nextCursor
      ? withQuery("/verdicts", {
          action: form.action,
          status: form.status,
          shadow: form.shadow,
          player: form.player,
          server: form.server,
          since: form.sinceDay,
          until: form.untilDay,
          cursor: page.nextCursor,
        })
      : null;
    return c.html(<VerdictsPage nonce={c.get("nonce")} session={c.get("session")!} filters={form} verdicts={page.items} next={next} flash={flash(c)} />);
  });

  r.get("/verdicts/:id", async (c) => {
    const id = c.req.param("id");
    if (!isId(id)) return c.notFound();
    const verdict = await c.get("api").verdict(id);
    const requested = c.req.query("clip");
    const clip = requested && verdict.clipIds.includes(requested) ? requested : (verdict.clipIds[0] ?? "");
    return c.html(<VerdictPage nonce={c.get("nonce")} session={c.get("session")!} verdict={verdict} clip={clip} flash={flash(c)} />);
  });

  /** JSON proxy for the replay viewer: API GET /api/v1/clips/{clipId}. */
  r.get("/verdicts/:id/clip/:clipId", async (c) => {
    const id = c.req.param("id");
    const clipId = c.req.param("clipId");
    c.header("Cache-Control", "private, no-store");
    if (!isId(id) || !isId(clipId)) return c.json({ error: "invalid id" }, 400);
    try {
      const data = await c.get("api").clip(clipId);
      if (!data || typeof data !== "object") return c.json({ error: "empty clip" }, 502);
      return c.json(data);
    } catch (e) {
      if (e instanceof ApiError) {
        const status = e.kind === "not_found" ? 404 : e.kind === "forbidden" ? 403 : 502;
        return c.json({ error: status === 404 ? "clip not found" : status === 403 ? "not allowed" : "the PandaEye API is unavailable" }, status);
      }
      throw e;
    }
  });

  for (const action of ["confirm", "overturn"] as const) {
    r.post(`/verdicts/:id/${action}`, requirePerm("verdicts:decide"), async (c) => {
      const id = c.req.param("id");
      if (!isId(id)) return c.notFound();
      const body = await c.req.parseBody();
      const note = field(body, "note").trim().slice(0, 500);
      try {
        if (action === "confirm") await c.get("api").confirmVerdict(id, note);
        else await c.get("api").overturnVerdict(id, note);
      } catch (e) {
        if (isClientError(e)) return rejected(c, e);
        throw e;
      }
      const ok = action === "confirm" ? "confirmed" : "overturned";
      if (field(body, "return") === "/bans") return c.redirect(`/bans?ok=${ok}`, 303);
      return c.redirect(`/verdicts/${encodeURIComponent(id)}?ok=${ok}`, 303);
    });
  }

  // ---- players ----------------------------------------------------------------------------------

  r.get("/players", async (c) => {
    const q = (c.req.query("q") ?? "").trim().slice(0, 64);
    if (isUuid(q)) return c.redirect(`/players/${q.toLowerCase()}`, 303);
    if (!q) return c.html(<PlayersPage nonce={c.get("nonce")} session={c.get("session")!} query="" players={null} next={null} flash={flash(c)} />);
    const cursor = c.req.query("cursor");
    const page = await c.get("api").players(q, cursor && cursor.length <= 512 ? cursor : undefined);
    const next = page.nextCursor ? withQuery("/players", { q, cursor: page.nextCursor }) : null;
    return c.html(<PlayersPage nonce={c.get("nonce")} session={c.get("session")!} query={q} players={page.items} next={next} />);
  });

  r.get("/players/:uuid", async (c) => {
    const uuid = c.req.param("uuid");
    if (!isUuid(uuid)) return c.notFound();
    const player = await c.get("api").player(uuid);
    return c.html(<PlayerPage nonce={c.get("nonce")} session={c.get("session")!} player={player} flash={flash(c)} />);
  });

  r.post("/players/:uuid/watch", requirePerm("players:watch"), async (c) => {
    const uuid = c.req.param("uuid");
    if (!isUuid(uuid)) return c.notFound();
    const body = await c.req.parseBody();
    const seconds = Math.round(Number(field(body, "seconds")));
    if (!Number.isFinite(seconds) || seconds < 60 || seconds > 300) return c.redirect(`/players/${uuid}?err=invalid`, 303);
    try {
      await c.get("api").watchPlayer(uuid, seconds);
    } catch (e) {
      if (isClientError(e)) return rejected(c, e);
      throw e;
    }
    return c.redirect(`/players/${uuid}?ok=watch_requested`, 303);
  });

  r.post("/players/:uuid/erase", requirePerm("players:erase"), async (c) => {
    const uuid = c.req.param("uuid");
    if (!isUuid(uuid)) return c.notFound();
    const body = await c.req.parseBody();
    if (field(body, "confirm").trim().toLowerCase() !== uuid.toLowerCase()) return c.redirect(`/players/${uuid}?err=invalid`, 303);
    let counts: [string, string][];
    try {
      counts = await c.get("api").erasePlayer(uuid);
    } catch (e) {
      if (isClientError(e)) return rejected(c, e);
      throw e;
    }
    const text = `Erasure finished for ${uuid}${counts.length ? `: ${counts.map(([k, v]) => `${k} ${v}`).join(", ")}` : ""}.`;
    return c.html(<PlayersPage nonce={c.get("nonce")} session={c.get("session")!} query="" players={null} next={null} flash={{ kind: "ok", text }} />);
  });

  // ---- bans and appeals ---------------------------------------------------------------------------

  r.get("/bans", async (c) => {
    const all = c.req.query("all") === "1";
    const cursor = c.req.query("cursor");
    const page = await c.get("api").bans(!all, cursor && cursor.length <= 512 ? cursor : undefined);
    const next = page.nextCursor ? withQuery("/bans", { all: all ? "1" : undefined, cursor: page.nextCursor }) : null;
    return c.html(<BansPage nonce={c.get("nonce")} session={c.get("session")!} bans={page.items} activeOnly={!all} next={next} flash={flash(c)} />);
  });

  r.get("/appeals", async (c) => {
    const status = c.req.query("status") === "all" ? "all" : "open";
    const cursor = c.req.query("cursor");
    const page = await c.get("api").appeals(status === "all" ? "" : "open", cursor && cursor.length <= 512 ? cursor : undefined);
    const next = page.nextCursor ? withQuery("/appeals", { status: status === "all" ? "all" : undefined, cursor: page.nextCursor }) : null;
    return c.html(<AppealsPage nonce={c.get("nonce")} session={c.get("session")!} appeals={page.items} status={status} next={next} flash={flash(c)} />);
  });

  r.post("/appeals/:id/decide", requirePerm("appeals:decide"), async (c) => {
    const id = c.req.param("id");
    if (!isId(id)) return c.notFound();
    const body = await c.req.parseBody();
    const decision = field(body, "decision");
    if (decision !== "accept" && decision !== "deny") return c.redirect("/appeals?err=invalid", 303);
    try {
      await c.get("api").decideAppeal(id, decision, field(body, "note").trim().slice(0, 500));
    } catch (e) {
      if (isClientError(e)) return rejected(c, e);
      throw e;
    }
    return c.redirect(`/appeals?ok=${decision === "accept" ? "appeal_accepted" : "appeal_denied"}`, 303);
  });

  // ---- settings and checks ------------------------------------------------------------------------

  r.get("/settings", requirePerm("settings:write"), async (c) => {
    const tenant = await c.get("api").tenant();
    return c.html(<SettingsPage nonce={c.get("nonce")} session={c.get("session")!} tenant={tenant} flash={flash(c)} />);
  });

  r.post("/settings", requirePerm("settings:write"), async (c) => {
    const api = c.get("api");
    const current = await api.tenant();
    const body = await c.req.parseBody();
    const render = (error: string, status: 400 | 403 = 400) =>
      c.html(<SettingsPage nonce={c.get("nonce")} session={c.get("session")!} tenant={current} error={error} />, status);

    const leniency = Number(field(body, "leniency"));
    if (!Number.isInteger(leniency) || leniency < 1 || leniency > 5) return render("Choose a leniency between 1 and 5.");
    const patch: TenantPatch = {};
    if (leniency !== current.leniency) patch.leniency = leniency;
    const flags = [
      ["autoban", "autoban"],
      ["autokick", "autokick"],
      ["mitigations", "mitigations"],
      ["enforcement", "enforcement"],
      ["reputation_opt_in", "reputationOptIn"],
    ] as const;
    for (const [name, key] of flags) {
      const v = checkbox(body, name);
      if (v !== current[key]) patch[name] = v;
    }
    if (patch.enforcement === true && !checkbox(body, "confirm_enforcement")) {
      return render("To enable enforcement, tick the confirmation box below the switch.");
    }
    const webhook = field(body, "discord_webhook").trim();
    if (checkbox(body, "discord_webhook_remove")) patch.discord_webhook = "";
    else if (webhook) {
      if (!isDiscordWebhook(webhook)) return render("The webhook must be a Discord webhook URL (https://discord.com/api/webhooks/<id>/<token>).");
      patch.discord_webhook = webhook;
    }
    const minAction = normalizeAction(field(body, "discord_min_action"));
    if (!minAction || !MIN_ACTIONS.includes(minAction)) return render("Choose the minimum action for Discord alerts.");
    if (minAction !== current.discordMinAction) patch.discord_min_action = minAction;

    if (Object.keys(patch).length) {
      try {
        await api.patchTenant(patch);
      } catch (e) {
        if (isClientError(e)) return render(e.kind === "forbidden" ? "Your role does not allow this change." : `The PandaEye API rejected the settings: ${e.message}`, e.kind === "forbidden" ? 403 : 400);
        throw e;
      }
    }
    return c.redirect("/settings?ok=settings_saved", 303);
  });

  r.get("/checks", requirePerm("checks:write"), async (c) => {
    const configs = await c.get("api").checks();
    return c.html(<ChecksPage nonce={c.get("nonce")} session={c.get("session")!} configs={configs} flash={flash(c)} />);
  });

  r.post("/checks", requirePerm("checks:write"), async (c) => {
    const api = c.get("api");
    const current = await api.checks();
    const body = await c.req.parseBody();
    const cloudIds = new Set(cloudChecks().map((x) => x.id));
    const next: CheckConfig[] = [];
    for (const info of cloudChecks()) {
      const parsed = parseOverrides(field(body, `overrides:${info.id}`));
      if (!parsed.ok) {
        return c.html(<ChecksPage nonce={c.get("nonce")} session={c.get("session")!} configs={current} error={`${info.id}: ${parsed.error}`} />, 400);
      }
      next.push({ id: info.id, enabled: checkbox(body, `enabled:${info.id}`), overrides: parsed.value });
    }
    // Checks the API knows but this dashboard does not (e.g. ml.<model>) are passed through unchanged.
    for (const cfg of current) if (!cloudIds.has(cfg.id)) next.push(cfg);
    try {
      await api.putChecks(next);
    } catch (e) {
      if (isClientError(e)) {
        return c.html(<ChecksPage nonce={c.get("nonce")} session={c.get("session")!} configs={current} error={`The PandaEye API rejected the configuration: ${e.message}`} />, 400);
      }
      throw e;
    }
    return c.redirect("/checks?ok=checks_saved", 303);
  });

  // ---- users and invites (D1) -------------------------------------------------------------------

  async function renderUsers(c: Ctx, extra: { newInvite?: { url: string; role: Role; expiresAt: number }; error?: string }, status: 200 | 400 = 200) {
    const s = c.get("session")!;
    const now = c.get("deps").now();
    const [users, invites] = await Promise.all([listUsers(c.env.DB, s.user.tenantId), listPendingInvites(c.env.DB, s.user.tenantId, now)]);
    return c.html(<UsersPage nonce={c.get("nonce")} session={s} users={users} invites={invites} flash={flash(c)} {...extra} />, status);
  }

  r.get("/users", requirePerm("users:view"), (c) => renderUsers(c, {}));

  r.post("/users/invites", requirePerm("invites:manage"), async (c) => {
    const s = c.get("session")!;
    const body = await c.req.parseBody();
    const role = field(body, "role");
    if (!isRole(role) || !invitableRoles(s.user.role).includes(role)) return renderUsers(c, { error: "You cannot invite users with this role." }, 400);
    const inv = await createInvite(c.env.DB, { tenantId: s.user.tenantId, role, createdBy: s.user.id, principal: `user:${s.user.username}`, now: c.get("deps").now() });
    const url = `${new URL(c.req.url).origin}/invite/${inv.token}`;
    return renderUsers(c, { newInvite: { url, role, expiresAt: inv.expiresAt } });
  });

  r.post("/users/invites/:hash/revoke", requirePerm("invites:manage"), async (c) => {
    const s = c.get("session")!;
    const hash = c.req.param("hash");
    if (!/^[0-9a-f]{64}$/.test(hash)) return c.notFound();
    await revokeInvite(c.env.DB, s.user.tenantId, hash, `user:${s.user.username}`, c.get("deps").now());
    return c.redirect("/users?ok=invite_revoked", 303);
  });

  r.post("/users/:id/role", requirePerm("users:manage"), async (c) => {
    const s = c.get("session")!;
    const id = c.req.param("id");
    const body = await c.req.parseBody();
    const role = field(body, "role");
    if (id === s.user.id) return renderUsers(c, { error: "You cannot change your own role." }, 400);
    if (!isRole(role)) return renderUsers(c, { error: "Unknown role." }, 400);
    const target = await findUser(c.env.DB, s.user.tenantId, id);
    if (!target) return c.notFound();
    await setUserRole(c.env.DB, s.user.tenantId, id, role, `user:${s.user.username}`, c.get("deps").now());
    return c.redirect("/users?ok=role_changed", 303);
  });

  for (const op of ["disable", "enable"] as const) {
    r.post(`/users/:id/${op}`, requirePerm("users:manage"), async (c) => {
      const s = c.get("session")!;
      const id = c.req.param("id");
      if (id === s.user.id) return renderUsers(c, { error: "You cannot disable your own account." }, 400);
      const target = await findUser(c.env.DB, s.user.tenantId, id);
      if (!target) return c.notFound();
      await setUserDisabled(c.env.DB, s.user.tenantId, id, op === "disable", `user:${s.user.username}`, c.get("deps").now());
      return c.redirect(`/users?ok=${op === "disable" ? "user_disabled" : "user_enabled"}`, 303);
    });
  }

  // ---- API keys ---------------------------------------------------------------------------------

  r.get("/apikeys", requirePerm("apikeys:manage"), async (c) => {
    const keys = await c.get("api").apiKeys();
    return c.html(<ApiKeysPage nonce={c.get("nonce")} session={c.get("session")!} keys={keys} flash={flash(c)} />);
  });

  r.post("/apikeys", requirePerm("apikeys:manage"), async (c) => {
    const api = c.get("api");
    const body = await c.req.parseBody();
    const name = field(body, "name").trim();
    const scopes = API_KEY_SCOPES.filter((s) => checkbox(body, `scope:${s}`));
    if (!name || name.length > 64) {
      return c.html(<ApiKeysPage nonce={c.get("nonce")} session={c.get("session")!} keys={await api.apiKeys()} error="Give the key a name of at most 64 characters." />, 400);
    }
    let created;
    try {
      created = await api.createApiKey(name, scopes.length ? scopes : ["read"]);
    } catch (e) {
      if (isClientError(e)) return rejected(c, e);
      throw e;
    }
    const keys = await api.apiKeys().catch(() => []);
    return c.html(<ApiKeysPage nonce={c.get("nonce")} session={c.get("session")!} keys={keys} created={created} />);
  });

  r.post("/apikeys/:id/revoke", requirePerm("apikeys:manage"), async (c) => {
    const id = c.req.param("id");
    if (!isId(id)) return c.notFound();
    try {
      await c.get("api").revokeApiKey(id);
    } catch (e) {
      if (isClientError(e)) return rejected(c, e);
      throw e;
    }
    return c.redirect("/apikeys?ok=apikey_revoked", 303);
  });

  // ---- audit ------------------------------------------------------------------------------------

  r.get("/audit", requirePerm("audit:read"), async (c) => {
    const s = c.get("session")!;
    const cursor = c.req.query("cursor");
    const [apiAudit, local] = await Promise.all([settle(c.get("api").audit(cursor && cursor.length <= 512 ? cursor : undefined)), recentAudit(c.env.DB, s.user.tenantId)]);
    const next = apiAudit.ok && apiAudit.value.nextCursor ? withQuery("/audit", { cursor: apiAudit.value.nextCursor }) : null;
    return c.html(<AuditPage nonce={c.get("nonce")} session={s} api={apiAudit.ok ? apiAudit.value.items : null} next={next} local={local} />);
  });

  return r;
}

