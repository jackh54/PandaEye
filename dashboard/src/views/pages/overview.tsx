import { CATEGORIES, CATEGORY_LABEL } from "../../lib/checks";
import { ACTION_LABEL, formatMsFromUs, formatNumber, seg, shortId, type ActionName } from "../../lib/format";
import type { CountWindow, Latency, Server, Tenant, Usage, Verdict } from "../../lib/pandaeye";
import type { Session } from "../../lib/session";
import { PeakPlayersChart } from "../chart";
import {
  ActionBadge,
  CategoryTag,
  EmptyRow,
  ExecState,
  Notice,
  OverLimitBanner,
  PageHeader,
  PlayerLink,
  Pct,
  ShadowBadge,
  Stat,
  TableWrap,
  Time,
  VerdictLink,
} from "../components";
import { Layout, type Flash } from "../layout";

/** Result of one API call on a page that degrades per section. */
export type Section<T> = { ok: true; value: T } | { ok: false };

function SectionError(props: { what: string }) {
  return <Notice kind="warn">{`${props.what} could not be loaded from the PandaEye API right now.`}</Notice>;
}

export function EnforcementBanner(props: { tenant: Tenant }) {
  const t = props.tenant;
  if (!t.enforcement) {
    return (
      <Notice kind="info" title="Shadow mode">
        Enforcement is off: PandaEye records what it would do (marked "SHADOW – not executed") but never mitigates, kicks or bans.
        Admins can change this in <a href="/settings">Settings</a>.
      </Notice>
    );
  }
  return (
    <Notice kind="ok" title="Enforcement on">
      {`Auto-ban ${t.autoban ? "on" : "off"}, auto-kick ${t.autokick ? "on" : "off"}, mitigations ${t.mitigations ? "on" : "off"}. Plugins enforce only on servers whose local mode is "live".`}
    </Notice>
  );
}

export function VerdictRows(props: { verdicts: Verdict[]; showServer?: boolean }) {
  const cols = props.showServer ? 9 : 8;
  if (!props.verdicts.length) return <EmptyRow cols={cols}>No verdicts match.</EmptyRow>;
  return (
    <>
      {props.verdicts.map((v) => (
        <tr class={v.shadow ? "row-shadow" : undefined}>
          <td>
            <VerdictLink id={v.id} />
          </td>
          <td>
            <Time value={v.createdAt} />
          </td>
          <td>
            <PlayerLink uuid={v.playerUuid} name={v.playerName} />
          </td>
          <td>
            <ActionBadge action={v.action} /> {v.shadow ? <ShadowBadge /> : null}
          </td>
          <td>
            <CategoryTag category={v.category} />
          </td>
          <td class="num">{formatNumber(v.score, 1)}</td>
          <td class="num">
            <Pct value={v.confidence} />
          </td>
          {props.showServer ? <td>{v.serverName || shortId(v.serverId)}</td> : null}
          <td>{v.shadow ? <span class="muted">—</span> : <ExecState v={v} />}</td>
        </tr>
      ))}
    </>
  );
}

export function VerdictTableHead(props: { showServer?: boolean }) {
  return (
    <thead>
      <tr>
        <th scope="col">Verdict</th>
        <th scope="col">When</th>
        <th scope="col">Player</th>
        <th scope="col">Action</th>
        <th scope="col">Category</th>
        <th scope="col" class="num">
          Score
        </th>
        <th scope="col" class="num">
          Confidence
        </th>
        {props.showServer ? <th scope="col">Server</th> : null}
        <th scope="col">Execution</th>
      </tr>
    </thead>
  );
}

const MAIN_ACTIONS: ActionName[] = ["alert", "mitigate", "kick", "ban"];

export function OverviewPage(props: {
  nonce: string;
  session: Session;
  flash: Flash | null;
  tenant: Section<Tenant>;
  stats: Section<CountWindow[]>;
  latency: Section<Latency>;
  servers: Section<Server[]>;
  recent: Section<Verdict[]>;
  openAppeals: Section<{ count: number; more: boolean }>;
}) {
  const servers = props.servers.ok ? props.servers.value : [];
  const online = servers.filter((s) => s.online);
  const players = online.reduce((a, s) => a + s.onlinePlayers, 0);
  const lat = props.latency.ok ? props.latency.value.windows[0] : undefined;
  const slo = props.latency.ok ? props.latency.value : null;
  const windows = props.stats.ok ? props.stats.value : [];
  return (
    <Layout title="Overview" nonce={props.nonce} session={props.session} active="overview" flash={props.flash}>
      <PageHeader title="Overview" />
      {props.tenant.ok ? <OverLimitBanner since={props.tenant.value.overLimitSince} /> : null}
      {props.tenant.ok ? <EnforcementBanner tenant={props.tenant.value} /> : <SectionError what="Tenant settings" />}

      <div class="stats">
        <Stat
          label="Servers online"
          value={props.servers.ok ? `${online.length} / ${servers.length}` : "—"}
          hint={props.servers.ok ? `${formatNumber(players)} players online` : "unavailable"}
        />
        <Stat
          label="Verdict latency p95 (1 h)"
          value={lat ? formatMsFromUs(lat.p95Us) : "—"}
          hint={slo ? `objective < ${slo.sloTargetMs} ms${slo.sloMet === false ? " (missed)" : slo.sloMet ? " (met)" : ""}` : "unavailable"}
          tone={slo?.sloMet === false ? "bad" : slo?.sloMet ? "ok" : undefined}
        />
        <Stat
          label="Open appeals"
          value={props.openAppeals.ok ? `${formatNumber(props.openAppeals.value.count)}${props.openAppeals.value.more ? "+" : ""}` : "—"}
          hint={<a href="/appeals">Review appeals</a>}
          tone={props.openAppeals.ok && props.openAppeals.value.count > 0 ? "warn" : undefined}
        />
      </div>

      <section class="card">
        <h2>Verdicts</h2>
        {props.stats.ok ? (
          <TableWrap label="Verdict counts">
            <table class="table compact">
              <thead>
                <tr>
                  <th scope="col">Window</th>
                  {MAIN_ACTIONS.map((a) => (
                    <th scope="col" class="num">
                      {ACTION_LABEL[a]}
                    </th>
                  ))}
                  <th scope="col" class="num">
                    Shadow
                  </th>
                </tr>
              </thead>
              <tbody>
                {props.stats.value.map((w) => (
                  <tr>
                    <th scope="row">{w.label}</th>
                    {MAIN_ACTIONS.map((a) => (
                      <td class="num">{formatNumber(w.byAction[a] ?? 0)}</td>
                    ))}
                    <td class="num">{w.shadow === null ? "—" : formatNumber(w.shadow)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </TableWrap>
        ) : (
          <SectionError what="Verdict statistics" />
        )}
        {props.stats.ok ? (
          <details class="details">
            <summary>By category</summary>
            <TableWrap label="Verdicts by category">
              <table class="table compact">
                <thead>
                  <tr>
                    <th scope="col">Category</th>
                    {props.stats.value.map((w) => (
                      <th scope="col" class="num">
                        {w.label}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {CATEGORIES.map((cat) => (
                    <tr>
                      <th scope="row">{CATEGORY_LABEL[cat]}</th>
                      {windows.map((w) => (
                        <td class="num">{formatNumber(w.byCategory[cat] ?? 0)}</td>
                      ))}
                    </tr>
                  ))}
                </tbody>
              </table>
            </TableWrap>
          </details>
        ) : null}
      </section>

      <section class="card">
        <div class="card-header">
          <h2>Recent verdicts</h2>
          <a href="/verdicts">All verdicts →</a>
        </div>
        {props.recent.ok ? (
          <TableWrap label="Recent verdicts">
            <table class="table">
              <VerdictTableHead />
              <tbody>
                <VerdictRows verdicts={props.recent.value} />
              </tbody>
            </table>
          </TableWrap>
        ) : (
          <SectionError what="Recent verdicts" />
        )}
      </section>
    </Layout>
  );
}

export function ServersPage(props: { nonce: string; session: Session; servers: Server[] }) {
  const sorted = [...props.servers].sort((a, b) => Number(b.online) - Number(a.online) || a.name.localeCompare(b.name));
  return (
    <Layout title="Servers" nonce={props.nonce} session={props.session} active="servers">
      <PageHeader title="Servers" />
      <p class="muted">
        Servers appear after their plugin connects with your license key. "Shadow" means the plugin's local mode never enforces.
      </p>
      <TableWrap label="Servers">
        <table class="table">
          <thead>
            <tr>
              <th scope="col">Name</th>
              <th scope="col">State</th>
              <th scope="col" class="num">
                Players
              </th>
              <th scope="col">Network</th>
              <th scope="col">Platform</th>
              <th scope="col">Minecraft</th>
              <th scope="col">Plugin</th>
              <th scope="col">Last seen</th>
              <th scope="col">Server id</th>
            </tr>
          </thead>
          <tbody>
            {sorted.length ? (
              sorted.map((s) => (
                <tr>
                  <td>{s.name || <span class="muted">unnamed</span>}</td>
                  <td>
                    {s.online ? <span class="badge badge-ok">Online</span> : <span class="badge">Offline</span>}{" "}
                    {s.shadowMode === true ? <span class="badge badge-shadow">Shadow</span> : s.shadowMode === false ? <span class="badge badge-warn">Live</span> : null}
                  </td>
                  <td class="num">{s.online ? formatNumber(s.onlinePlayers) : "—"}</td>
                  <td>{s.networkId || <span class="muted">—</span>}</td>
                  <td>
                    {s.platform || "—"} <span class="muted small">{s.platformVersion}</span>
                  </td>
                  <td>{s.minecraftVersion || "—"}</td>
                  <td>{s.pluginVersion || "—"}</td>
                  <td>
                    <Time value={s.lastSeenAt} />
                  </td>
                  <td>
                    <a class="mono" href={`/verdicts?server=${seg(s.id)}`} title="Verdicts from this server">
                      {shortId(s.id)}
                    </a>
                  </td>
                </tr>
              ))
            ) : (
              <EmptyRow cols={9}>No server has connected yet. Install the plugin and set your license key in its config.yml.</EmptyRow>
            )}
          </tbody>
        </table>
      </TableWrap>
    </Layout>
  );
}

export function UsagePage(props: { nonce: string; session: Session; usage: Usage }) {
  const u = props.usage;
  const last30 = u.days.slice(-30);
  const peak30 = last30.reduce((a, d) => Math.max(a, d.peakPlayers), 0);
  const isFree = u.plan.toLowerCase() === "free";
  return (
    <Layout title="Usage" nonce={props.nonce} session={props.session} active="usage">
      <PageHeader title="Usage" />
      <OverLimitBanner since={u.overLimitSince} limit={u.freeTierLimit} />
      <div class="stats">
        <Stat label="Plan" value={u.plan} />
        <Stat label="Highest peak (30 d)" value={formatNumber(peak30)} hint={isFree ? `free tier: ${u.freeTierLimit} peak players` : undefined} tone={isFree && peak30 > u.freeTierLimit ? "warn" : undefined} />
        <Stat label="Days recorded" value={formatNumber(u.days.length)} />
      </div>
      <section class="card">
        <h2>Peak concurrent players per day</h2>
        <PeakPlayersChart days={u.days.slice(-90)} limit={isFree ? u.freeTierLimit : null} title="Peak concurrent players per day, last 90 days" />
        <p class="muted small">
          Metered every minute from connected servers (per network: the proxy's count when a proxy is connected, else the sum of
          backends). Billing state never affects players.
        </p>
        <details class="details">
          <summary>Show as table</summary>
          <TableWrap label="Peak players per day">
            <table class="table compact">
              <thead>
                <tr>
                  <th scope="col">Day</th>
                  <th scope="col" class="num">
                    Peak players
                  </th>
                </tr>
              </thead>
              <tbody>
                {u.days.length ? (
                  [...u.days].reverse().map((d) => (
                    <tr>
                      <td>{d.day}</td>
                      <td class="num">{formatNumber(d.peakPlayers)}</td>
                    </tr>
                  ))
                ) : (
                  <EmptyRow cols={2}>No usage recorded yet.</EmptyRow>
                )}
              </tbody>
            </table>
          </TableWrap>
        </details>
      </section>
    </Layout>
  );
}

