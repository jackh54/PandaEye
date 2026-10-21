import { formatNumber, formatPercent, seg, shortId } from "../../lib/format";
import type { Ban, PlayerDetail, PlayerSummary } from "../../lib/pandaeye";
import { can } from "../../lib/roles";
import type { Session } from "../../lib/session";
import { AbsTime, Csrf, EmptyRow, Notice, PageHeader, Pager, TableWrap, Time, UnbanBadge, VerdictLink } from "../components";
import { Layout, type Flash } from "../layout";
import { VerdictRows, VerdictTableHead } from "./overview";

export function PlayersPage(props: { nonce: string; session: Session; query: string; players: PlayerSummary[] | null; next: string | null; flash?: Flash | null }) {
  return (
    <Layout title="Players" nonce={props.nonce} session={props.session} active="players" flash={props.flash}>
      <PageHeader title="Players" />
      <form method="get" action="/players" class="filters" role="search" aria-label="Search players">
        <label class="grow">
          Name or UUID
          <input name="q" value={props.query} maxlength={64} placeholder="Player name or UUID" autofocus />
        </label>
        <div class="filter-actions">
          <button type="submit" class="btn btn-primary">
            Search
          </button>
        </div>
      </form>
      {props.players === null ? (
        <p class="muted">Search by current or previous name, or paste a UUID to open a player directly.</p>
      ) : (
        <>
          <TableWrap label="Players">
            <table class="table">
              <thead>
                <tr>
                  <th scope="col">Name</th>
                  <th scope="col">UUID</th>
                  <th scope="col">First seen</th>
                  <th scope="col">Last seen</th>
                  <th scope="col">Last server</th>
                </tr>
              </thead>
              <tbody>
                {props.players.length ? (
                  props.players.map((p) => (
                    <tr>
                      <td>
                        <a href={`/players/${seg(p.uuid)}`}>{p.lastName || "unknown"}</a>
                      </td>
                      <td class="mono small">{p.uuid}</td>
                      <td>
                        <Time value={p.firstSeenAt} />
                      </td>
                      <td>
                        <Time value={p.lastSeenAt} />
                      </td>
                      <td class="mono">{shortId(p.lastServerId) || "—"}</td>
                    </tr>
                  ))
                ) : (
                  <EmptyRow cols={5}>No player matches.</EmptyRow>
                )}
              </tbody>
            </table>
          </TableWrap>
          <Pager next={props.next} />
        </>
      )}
    </Layout>
  );
}

export function BanRows(props: { bans: Ban[]; session: Session; showPlayer?: boolean }) {
  const staff = can(props.session.user.role, "verdicts:decide");
  const cols = props.showPlayer ? 7 : 6;
  if (!props.bans.length) return <EmptyRow cols={cols}>No bans.</EmptyRow>;
  return (
    <>
      {props.bans.map((b) => (
        <tr>
          <td>
            <VerdictLink id={b.verdictId} />
          </td>
          {props.showPlayer ? (
            <td>
              <a href={`/players/${seg(b.playerUuid)}`}>{b.playerName || shortId(b.playerUuid, 13)}</a>
            </td>
          ) : null}
          <td>
            <Time value={b.createdAt} />
          </td>
          <td class="info">{b.reason || <span class="muted">—</span>}</td>
          <td>
            <UnbanBadge ban={b} />
            {b.overturnedAt ? (
              <div class="muted small">
                overturned <Time value={b.overturnedAt} />
                {b.overturnedBy ? ` by ${b.overturnedBy}` : ""}
              </div>
            ) : null}
          </td>
          <td class="mono">{shortId(b.serverId) || "—"}</td>
          <td>
            {staff && b.active && b.unbanState === "none" ? (
              <form method="post" action={`/verdicts/${seg(b.verdictId)}/overturn`} class="inline-form">
                <Csrf token={props.session.csrf} />
                <input type="hidden" name="return" value="/bans" />
                <button type="submit" class="btn btn-small btn-danger">
                  Overturn and unban
                </button>
              </form>
            ) : null}
          </td>
        </tr>
      ))}
    </>
  );
}

export function BanTableHead(props: { showPlayer?: boolean }) {
  return (
    <thead>
      <tr>
        <th scope="col">Verdict</th>
        {props.showPlayer ? <th scope="col">Player</th> : null}
        <th scope="col">Banned</th>
        <th scope="col">Reason</th>
        <th scope="col">State</th>
        <th scope="col">Executed on</th>
        <th scope="col">
          <span class="visually-hidden">Actions</span>
        </th>
      </tr>
    </thead>
  );
}

export function PlayerPage(props: { nonce: string; session: Session; player: PlayerDetail; flash?: Flash | null; erased?: [string, string][] | null }) {
  const p = props.player;
  const role = props.session.user.role;
  return (
    <Layout title={p.lastName || "Player"} nonce={props.nonce} session={props.session} active="players" flash={props.flash}>
      <PageHeader title={p.lastName || "Unknown player"} />
      {props.erased ? (
        <Notice kind="ok" title="Erasure started">
          <ul class="inline-list">
            {props.erased.map(([k, v]) => (
              <li>{`${k}: ${v}`}</li>
            ))}
          </ul>
        </Notice>
      ) : null}
      <div class="grid-2">
        <section class="card">
          <h2>Profile</h2>
          <dl class="kv">
            <dt>UUID</dt>
            <dd class="mono small">{p.uuid}</dd>
            <dt>First seen</dt>
            <dd>
              <AbsTime value={p.firstSeenAt} />
            </dd>
            <dt>Last seen</dt>
            <dd>
              <AbsTime value={p.lastSeenAt} /> {p.lastServerId ? <span class="muted small">on {shortId(p.lastServerId)}</span> : null}
            </dd>
            {p.clientVersion ? (
              <>
                <dt>Client version</dt>
                <dd>{p.clientVersion}</dd>
              </>
            ) : null}
            {p.brand ? (
              <>
                <dt>Client brand</dt>
                <dd>
                  <code>{p.brand}</code>
                </dd>
              </>
            ) : null}
            <dt>Known names</dt>
            <dd>
              {p.names.length ? (
                <ul class="inline-list">
                  {p.names.map((n) => (
                    <li>{n.name}</li>
                  ))}
                </ul>
              ) : (
                <span class="muted">—</span>
              )}
            </dd>
          </dl>
          {p.reputation ? (
            <Notice kind={p.reputation.risk > 0 ? "warn" : "info"} title="Reputation network">
              {`Risk ${formatPercent(p.reputation.risk)}; flagged on ${formatNumber(p.reputation.networksFlagged)} other networks; ${formatNumber(p.reputation.confirmedBans)} confirmed bans. Advisory only: it never scores or acts.`}
            </Notice>
          ) : null}
        </section>
        <section class="card">
          <h2>Actions</h2>
          {can(role, "players:watch") ? (
            <form method="post" action={`/players/${seg(p.uuid)}/watch`} class="stack">
              <Csrf token={props.session.csrf} />
              <label>
                Full-rate telemetry for
                <select name="seconds">
                  <option value="60">1 minute</option>
                  <option value="120">2 minutes</option>
                  <option value="300" selected>
                    5 minutes
                  </option>
                </select>
              </label>
              <button type="submit" class="btn">
                Watch player
              </button>
              <p class="hint">Asks the player's server to stream every movement packet while you investigate. No effect on the player.</p>
            </form>
          ) : (
            <p class="muted">Staff can request full-rate telemetry for a player.</p>
          )}
          {can(role, "players:erase") ? (
            <details class="details danger-zone">
              <summary>Erase this player's data</summary>
              <form method="post" action={`/players/${seg(p.uuid)}/erase`} class="stack">
                <Csrf token={props.session.csrf} />
                <p class="small">
                  Deletes verdicts, evidence, clips, appeals and labels of this player for your network (GDPR erasure). Active bans keep a
                  minimal record until they are lifted. This cannot be undone.
                </p>
                <label>
                  Type the player's UUID to confirm
                  <input name="confirm" required maxlength={36} autocomplete="off" spellcheck={false} />
                </label>
                <button type="submit" class="btn btn-danger">
                  Erase player data
                </button>
              </form>
            </details>
          ) : null}
        </section>
      </div>
      <section class="card">
        <h2>Verdicts</h2>
        <TableWrap label="Verdicts of this player">
          <table class="table">
            <VerdictTableHead showServer />
            <tbody>
              <VerdictRows verdicts={p.verdicts} showServer />
            </tbody>
          </table>
        </TableWrap>
      </section>
      <section class="card">
        <h2>Bans</h2>
        <TableWrap label="Bans of this player">
          <table class="table">
            <BanTableHead />
            <tbody>
              <BanRows bans={p.bans} session={props.session} />
            </tbody>
          </table>
        </TableWrap>
      </section>
    </Layout>
  );
}
