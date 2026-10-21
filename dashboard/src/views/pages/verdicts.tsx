import { categoryLabel, checkInfo, familyOf, FAMILY_CAPS } from "../../lib/checks";
import { ACTIONS, ACTION_LABEL, formatNumber, formatPercent, seg, shortId } from "../../lib/format";
import { VERDICT_STATUSES, type Evidence, type Verdict, type VerdictFilters } from "../../lib/pandaeye";
import { can } from "../../lib/roles";
import type { Session } from "../../lib/session";
import {
  AbsTime,
  ActionBadge,
  CategoryTag,
  CheckName,
  Csrf,
  EmptyRow,
  ExecState,
  Notice,
  PageHeader,
  Pager,
  PlayerLink,
  Pct,
  ShadowBadge,
  StatusBadge,
  TableWrap,
  Time,
  VerdictLink,
} from "../components";
import { Layout, type Flash } from "../layout";
import { VerdictRows, VerdictTableHead } from "./overview";

export interface VerdictFilterForm extends VerdictFilters {
  sinceDay?: string;
  untilDay?: string;
}

export function VerdictsPage(props: {
  nonce: string;
  session: Session;
  filters: VerdictFilterForm;
  verdicts: Verdict[];
  next: string | null;
  flash?: Flash | null;
}) {
  const f = props.filters;
  return (
    <Layout title="Verdicts" nonce={props.nonce} session={props.session} active="verdicts" flash={props.flash}>
      <PageHeader title="Verdicts" />
      <form method="get" action="/verdicts" class="filters" role="search" aria-label="Filter verdicts">
        <label>
          Action
          <select name="action">
            <option value="">Any</option>
            {ACTIONS.map((a) => (
              <option value={a} selected={f.action === a}>
                {ACTION_LABEL[a]}
              </option>
            ))}
          </select>
        </label>
        <label>
          Status
          <select name="status">
            <option value="">Any</option>
            {VERDICT_STATUSES.map((s) => (
              <option value={s} selected={f.status === s}>
                {s}
              </option>
            ))}
          </select>
        </label>
        <label>
          Shadow
          <select name="shadow">
            <option value="">All</option>
            <option value="false" selected={f.shadow === "false"}>
              Enforceable only
            </option>
            <option value="true" selected={f.shadow === "true"}>
              Shadow only
            </option>
          </select>
        </label>
        <label>
          Player
          <input name="player" value={f.player ?? ""} maxlength={64} placeholder="UUID or name" />
        </label>
        <label>
          Server id
          <input name="server" value={f.server ?? ""} maxlength={128} />
        </label>
        <label>
          From
          <input type="date" name="since" value={f.sinceDay ?? ""} />
        </label>
        <label>
          To
          <input type="date" name="until" value={f.untilDay ?? ""} />
        </label>
        <div class="filter-actions">
          <button type="submit" class="btn btn-primary">
            Apply
          </button>
          <a href="/verdicts" class="btn btn-ghost">
            Reset
          </a>
        </div>
      </form>
      <TableWrap label="Verdicts">
        <table class="table">
          <VerdictTableHead showServer />
          <tbody>
            <VerdictRows verdicts={props.verdicts} showServer />
          </tbody>
        </table>
      </TableWrap>
      <Pager next={props.next} />
    </Layout>
  );
}

function EvidenceSection(props: { evidence: Evidence; verdictId: string }) {
  const e = props.evidence;
  return (
    <section class="card" aria-labelledby="evidence-h">
      <h2 id="evidence-h">Evidence</h2>
      <dl class="kv kv-inline">
        <dt>Score</dt>
        <dd class="num">{e.score === null ? "—" : formatNumber(e.score, 1)}</dd>
        <dt>Confidence</dt>
        <dd class="num">{e.confidence === null ? "—" : formatPercent(e.confidence, 1)}</dd>
        <dt>Leniency</dt>
        <dd class="num">{e.leniency === null ? "—" : String(e.leniency)}</dd>
        {e.thresholds.map(([k, v]) => (
          <>
            <dt>{`${k} threshold`}</dt>
            <dd class="num">{formatNumber(v, 1)}</dd>
          </>
        ))}
      </dl>

      {e.checks.length ? (
        <>
          <h3>Check contributions</h3>
          <TableWrap label="Check contributions">
            <table class="table compact">
              <thead>
                <tr>
                  <th scope="col">Check</th>
                  <th scope="col">Category</th>
                  <th scope="col" class="num">
                    Points
                  </th>
                  <th scope="col" class="num">
                    Cap
                  </th>
                  <th scope="col" class="num">
                    Confidence
                  </th>
                  <th scope="col" class="num">
                    Signals
                  </th>
                </tr>
              </thead>
              <tbody>
                {e.checks.map((x) => {
                  const info = checkInfo(x.name);
                  return (
                    <tr>
                      <td>
                        <CheckName id={x.name} />
                      </td>
                      <td>{info ? categoryLabel(info.category) : <span class="muted">—</span>}</td>
                      <td class="num">{formatNumber(x.contribution, 1)}</td>
                      <td class="num">{x.cap !== null ? formatNumber(x.cap) : info ? formatNumber(info.cap) : "—"}</td>
                      <td class="num">{x.confidence === null ? "—" : formatPercent(x.confidence)}</td>
                      <td class="num">{x.count === null ? "—" : formatNumber(x.count)}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </TableWrap>
        </>
      ) : null}

      {e.families.length ? (
        <>
          <h3>Family contributions</h3>
          <TableWrap label="Family contributions">
            <table class="table compact">
              <thead>
                <tr>
                  <th scope="col">Family</th>
                  <th scope="col" class="num">
                    Points
                  </th>
                  <th scope="col" class="num">
                    Family cap
                  </th>
                </tr>
              </thead>
              <tbody>
                {e.families.map((x) => (
                  <tr>
                    <td>
                      <code>{x.name}</code>
                    </td>
                    <td class="num">{formatNumber(x.contribution, 1)}</td>
                    <td class="num">{x.cap !== null ? formatNumber(x.cap) : FAMILY_CAPS[x.name] !== undefined ? formatNumber(FAMILY_CAPS[x.name]) : "—"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </TableWrap>
        </>
      ) : null}

      <h3>Strongest signals</h3>
      <TableWrap label="Signals">
        <table class="table compact signals">
          <thead>
            <tr>
              <th scope="col">Time</th>
              <th scope="col">Check</th>
              <th scope="col">Category</th>
              <th scope="col" class="num">
                Points
              </th>
              <th scope="col" class="num">
                Confidence
              </th>
              <th scope="col">Info</th>
              <th scope="col">Data</th>
              <th scope="col">Clip</th>
            </tr>
          </thead>
          <tbody>
            {e.signals.length ? (
              e.signals.map((s) => (
                <tr>
                  <td>
                    <AbsTime value={s.time} />
                  </td>
                  <td>
                    <CheckName id={s.check} />
                    {s.local ? <span class="tag">plugin</span> : null}
                  </td>
                  <td>
                    <CategoryTag category={s.category ?? checkInfo(s.check)?.category ?? familyOf(s.check)} />
                  </td>
                  <td class="num">{formatNumber(s.points, 1)}</td>
                  <td class="num">
                    <Pct value={s.confidence} />
                  </td>
                  <td class="info">{s.info || <span class="muted">—</span>}</td>
                  <td class="data">
                    {s.data.length ? (
                      <ul class="data-list">
                        {s.data.map(([k, v]) => (
                          <li>
                            <span class="data-key">{k}</span> <span class="num">{v}</span>
                          </li>
                        ))}
                      </ul>
                    ) : (
                      <span class="muted">—</span>
                    )}
                  </td>
                  <td>
                    {s.clipId ? (
                      <a href={`/verdicts/${seg(props.verdictId)}?clip=${seg(s.clipId)}#replay`} class="mono">
                        {shortId(s.clipId)}
                      </a>
                    ) : (
                      <span class="muted">—</span>
                    )}
                  </td>
                </tr>
              ))
            ) : (
              <EmptyRow cols={8}>The verdict carries no signal list.</EmptyRow>
            )}
          </tbody>
        </table>
      </TableWrap>

      {e.session.length ? (
        <details class="details">
          <summary>Session</summary>
          <dl class="kv">
            {e.session.map(([k, v]) => (
              <>
                <dt>{k}</dt>
                <dd>{v}</dd>
              </>
            ))}
          </dl>
        </details>
      ) : null}
    </section>
  );
}

export function ReplayViewer(props: { verdictId: string; clipIds: string[]; clip: string }) {
  if (!props.clipIds.length) {
    return (
      <section class="card" id="replay" aria-labelledby="replay-h">
        <h2 id="replay-h">Replay</h2>
        <p class="muted">No replay clip was committed for this verdict.</p>
      </section>
    );
  }
  const url = `/verdicts/${seg(props.verdictId)}/clip/${seg(props.clip)}`;
  return (
    <section class="card" id="replay" aria-labelledby="replay-h">
      <div class="card-header">
        <h2 id="replay-h">Replay</h2>
        {props.clipIds.length > 1 ? (
          <nav aria-label="Clips" class="clip-tabs">
            {props.clipIds.map((id, i) => (
              <a href={`/verdicts/${seg(props.verdictId)}?clip=${seg(id)}#replay`} aria-current={id === props.clip ? "true" : undefined}>
                {`Clip ${i + 1}`}
              </a>
            ))}
          </nav>
        ) : null}
      </div>
      <div class="replay-viewer" data-clip-url={url}>
        <p class="replay-status" role="status">
          Loading clip…
        </p>
        <div class="replay-views">
          <figure class="replay-figure">
            <figcaption>Top-down view (x east →, z south ↓)</figcaption>
            <canvas class="replay-top" width="560" height="380" role="img" aria-label="Top-down view of the suspect's path, view direction and target positions"></canvas>
          </figure>
          <figure class="replay-figure">
            <figcaption class="replay-side-caption">Side view (height over the main horizontal axis)</figcaption>
            <canvas class="replay-side" width="560" height="380" role="img" aria-label="Side view of the suspect's path and target positions"></canvas>
          </figure>
        </div>
        <canvas class="replay-timeline" width="1140" height="64" aria-hidden="true"></canvas>
        <div class="replay-controls">
          <button type="button" class="btn replay-play" aria-pressed="false" disabled>
            Play
          </button>
          <button type="button" class="btn btn-small replay-prev" disabled aria-label="Previous event">
            ◀ Event
          </button>
          <button type="button" class="btn btn-small replay-next" disabled aria-label="Next event">
            Event ▶
          </button>
          <label class="replay-scrub-label">
            <span class="visually-hidden">Timeline position</span>
            <input type="range" class="replay-scrub" min="0" max="1000" step="1" value="0" disabled />
          </label>
          <output class="replay-time mono">0.000 s</output>
          <label>
            Speed{" "}
            <select class="replay-speed">
              <option value="0.1">0.1×</option>
              <option value="0.25">0.25×</option>
              <option value="0.5">0.5×</option>
              <option value="1" selected>
                1×
              </option>
              <option value="2">2×</option>
            </select>
          </label>
        </div>
        <dl class="kv replay-info">
          <dt>Position</dt>
          <dd data-field="pos">—</dd>
          <dt>Rotation (yaw / pitch)</dt>
          <dd data-field="rot">—</dd>
          <dt>Movement</dt>
          <dd data-field="move">—</dd>
          <dt>Environment</dt>
          <dd data-field="env">—</dd>
          <dt>Nearby events</dt>
          <dd data-field="events">—</dd>
        </dl>
        <ul class="replay-legend">
          <li>
            <span class="swatch swatch-suspect"></span>Suspect path and view ray
          </li>
          <li>
            <span class="swatch swatch-target"></span>Attack targets
          </li>
          <li>
            <span class="swatch swatch-attack"></span>Attacks
          </li>
          <li>
            <span class="swatch swatch-place"></span>Block placements
          </li>
          <li>
            <span class="swatch swatch-dig"></span>Digging
          </li>
          <li>
            <span class="swatch swatch-velocity"></span>Velocity (knockback)
          </li>
          <li>
            <span class="swatch swatch-teleport"></span>Teleports
          </li>
          <li>
            <span class="swatch swatch-flag"></span>Plugin flags
          </li>
        </ul>
      </div>
      <noscript>
        <p class="muted">The replay viewer needs JavaScript. The evidence table above lists every signal.</p>
      </noscript>
    </section>
  );
}

export function VerdictPage(props: { nonce: string; session: Session; verdict: Verdict; clip: string; flash?: Flash | null }) {
  const v = props.verdict;
  const staff = can(props.session.user.role, "verdicts:decide");
  const canConfirm = staff && v.status === "open" && v.action !== "unban" && v.action !== "unmitigate";
  const canOverturn = staff && (v.status === "open" || v.status === "confirmed") && v.action !== "unban" && v.action !== "unmitigate";
  return (
    <Layout title={`Verdict ${shortId(v.id)}`} nonce={props.nonce} session={props.session} active="verdicts" flash={props.flash} scripts={["/static/replay.js"]}>
      <div class="page-header">
        <h1>
          <ActionBadge action={v.action} /> <span>{v.playerName || shortId(v.playerUuid, 13) || "Unknown player"}</span>
        </h1>
        <div class="page-actions">
          <StatusBadge status={v.status} />
          {v.shadow ? <ShadowBadge /> : null}
        </div>
      </div>
      {v.shadow ? (
        <Notice kind="info" title="SHADOW – not executed">
          This verdict was recorded as what PandaEye would have done. It was not enforced, because enforcement is off, the action is
          disabled in Settings, or the server's plugin runs in shadow mode.
        </Notice>
      ) : null}

      <div class="grid-2">
        <section class="card">
          <h2>Summary</h2>
          <dl class="kv">
            <dt>Player</dt>
            <dd>
              <PlayerLink uuid={v.playerUuid} name={v.playerName} /> <span class="mono muted small">{v.playerUuid}</span>
            </dd>
            <dt>Created</dt>
            <dd>
              <AbsTime value={v.createdAt} /> (<Time value={v.createdAt} />)
            </dd>
            <dt>Score</dt>
            <dd class="num">{formatNumber(v.score, 1)}</dd>
            <dt>Confidence</dt>
            <dd>
              <meter min={0} max={1} value={Math.max(0, Math.min(1, v.confidence))} low={0.8} high={0.9} optimum={1}>
                {formatPercent(v.confidence)}
              </meter>{" "}
              <Pct value={v.confidence} digits={1} />
            </dd>
            <dt>Category</dt>
            <dd>
              <CategoryTag category={v.category} />
            </dd>
            <dt>Reason</dt>
            <dd>{v.reason || <span class="muted">—</span>}</dd>
            <dt>Checks</dt>
            <dd>
              {v.checks.length ? (
                <ul class="inline-list">
                  {v.checks.map((id) => (
                    <li>
                      <CheckName id={id} />
                    </li>
                  ))}
                </ul>
              ) : (
                <span class="muted">—</span>
              )}
            </dd>
            {v.mitigations.length ? (
              <>
                <dt>Mitigations</dt>
                <dd>
                  {v.mitigations.join(", ")}
                  {v.strength !== null ? ` (strength ${formatPercent(v.strength)})` : ""}
                </dd>
              </>
            ) : null}
            <dt>Server</dt>
            <dd>
              {v.serverName || <span class="mono">{shortId(v.serverId)}</span>}
              {v.networkId ? <span class="muted small"> network {v.networkId}</span> : null}
            </dd>
            <dt>Execution</dt>
            <dd>
              <ExecState v={v} />
              {v.ackedAt ? (
                <span class="muted small">
                  {" "}
                  acknowledged <Time value={v.ackedAt} />
                </span>
              ) : null}
            </dd>
            {v.supersedes ? (
              <>
                <dt>Reverts</dt>
                <dd>
                  <VerdictLink id={v.supersedes} />
                </dd>
              </>
            ) : null}
            <dt>Session</dt>
            <dd class="mono small">{v.sessionId || "—"}</dd>
          </dl>
        </section>

        <section class="card">
          <h2>Review</h2>
          {staff ? (
            <>
              <p class="muted small">
                Confirming labels the player as cheating (and, for executed bans of networks in the reputation network, shares the
                result). Overturning labels the player legit, removes mitigations and, if the ban was executed, sends an unban to the
                server that executed it.
              </p>
              <form method="post" action={`/verdicts/${seg(v.id)}/confirm`} class="stack">
                <Csrf token={props.session.csrf} />
                <label>
                  Note (optional)
                  <input name="note" maxlength={500} />
                </label>
                <button type="submit" class="btn btn-primary" disabled={!canConfirm}>
                  Confirm: cheating
                </button>
              </form>
              <form method="post" action={`/verdicts/${seg(v.id)}/overturn`} class="stack">
                <Csrf token={props.session.csrf} />
                <label>
                  Note (optional)
                  <input name="note" maxlength={500} />
                </label>
                <button type="submit" class="btn btn-danger" disabled={!canOverturn}>
                  {v.action === "ban" && v.executed ? "Overturn and unban" : "Overturn: legit"}
                </button>
              </form>
              {!canConfirm && !canOverturn ? <p class="muted small">{`This verdict is ${v.status}; no review action is available.`}</p> : null}
            </>
          ) : (
            <p class="muted">Staff, admins and owners can confirm or overturn verdicts.</p>
          )}
        </section>
      </div>

      {v.evidence ? <EvidenceSection evidence={v.evidence} verdictId={v.id} /> : <Notice kind="info">No evidence document is attached to this verdict.</Notice>}
      <ReplayViewer verdictId={v.id} clipIds={v.clipIds} clip={props.clip} />
    </Layout>
  );
}

export { categoryLabel };
