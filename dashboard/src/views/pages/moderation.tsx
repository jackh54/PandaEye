import { seg, shortId } from "../../lib/format";
import type { Appeal, Ban } from "../../lib/pandaeye";
import { can } from "../../lib/roles";
import type { Session } from "../../lib/session";
import { Csrf, EmptyRow, PageHeader, Pager, StatusBadge, TableWrap, Time, VerdictLink } from "../components";
import { Layout, type Flash } from "../layout";
import { BanRows, BanTableHead } from "./players";

export function BansPage(props: { nonce: string; session: Session; bans: Ban[]; activeOnly: boolean; next: string | null; flash?: Flash | null }) {
  const pending = props.bans.filter((b) => b.unbanState === "pending").length;
  return (
    <Layout title="Bans" nonce={props.nonce} session={props.session} active="bans" flash={props.flash}>
      <PageHeader title="Bans">
        <nav class="segmented" aria-label="Ban filter">
          <a href="/bans" aria-current={props.activeOnly ? "page" : undefined}>
            Active
          </a>
          <a href="/bans?all=1" aria-current={!props.activeOnly ? "page" : undefined}>
            All
          </a>
        </nav>
      </PageHeader>
      <p class="muted">
        Only bans a server actually executed appear here. Overturning a ban sends an unban to the server that executed it; it shows
        "Unban pending" until that server acknowledges it.
        {pending ? ` ${pending} unban${pending === 1 ? "" : "s"} pending on this page.` : ""}
      </p>
      <TableWrap label="Bans">
        <table class="table">
          <BanTableHead showPlayer />
          <tbody>
            <BanRows bans={props.bans} session={props.session} showPlayer />
          </tbody>
        </table>
      </TableWrap>
      <Pager next={props.next} />
    </Layout>
  );
}

export function AppealsPage(props: { nonce: string; session: Session; appeals: Appeal[]; status: string; next: string | null; flash?: Flash | null }) {
  const staff = can(props.session.user.role, "appeals:decide");
  return (
    <Layout title="Appeals" nonce={props.nonce} session={props.session} active="appeals" flash={props.flash}>
      <PageHeader title="Appeals">
        <nav class="segmented" aria-label="Appeal filter">
          <a href="/appeals" aria-current={props.status === "open" ? "page" : undefined}>
            Open
          </a>
          <a href="/appeals?status=all" aria-current={props.status === "all" ? "page" : undefined}>
            All
          </a>
        </nav>
      </PageHeader>
      <p class="muted">Accepting an appeal overturns the verdict (label "legit") and unbans the player. Denying keeps the ban.</p>
      {props.appeals.length ? (
        <ul class="appeal-list">
          {props.appeals.map((a) => (
            <li class="card appeal">
              <div class="appeal-head">
                <div>
                  <strong>
                    <a href={`/players/${seg(a.playerUuid)}`}>{a.playerName || shortId(a.playerUuid, 13) || "unknown player"}</a>
                  </strong>{" "}
                  <span class="muted small">
                    appealed <Time value={a.createdAt} /> · verdict <VerdictLink id={a.verdictId} />
                  </span>
                </div>
                <StatusBadge status={a.status} />
              </div>
              <blockquote class="appeal-message">{a.message}</blockquote>
              {a.contact ? (
                <p class="small">
                  <span class="muted">Contact:</span> {a.contact}
                </p>
              ) : null}
              {a.status !== "open" ? (
                <p class="muted small">
                  Decided <Time value={a.decidedAt} />
                  {a.decidedBy ? ` by ${a.decidedBy}` : ""}
                  {a.decisionNote ? `: ${a.decisionNote}` : ""}
                </p>
              ) : staff ? (
                <form method="post" action={`/appeals/${seg(a.id)}/decide`} class="appeal-actions">
                  <Csrf token={props.session.csrf} />
                  <label class="grow">
                    <span class="visually-hidden">Decision note</span>
                    <input name="note" maxlength={500} placeholder="Note (optional, visible to staff)" />
                  </label>
                  <button type="submit" name="decision" value="accept" class="btn btn-primary">
                    Accept and overturn
                  </button>
                  <button type="submit" name="decision" value="deny" class="btn">
                    Deny
                  </button>
                  <a class="btn btn-ghost" href={`/verdicts/${seg(a.verdictId)}`}>
                    Evidence
                  </a>
                </form>
              ) : null}
            </li>
          ))}
        </ul>
      ) : (
        <table class="table">
          <tbody>
            <EmptyRow cols={1}>{props.status === "open" ? "No open appeals." : "No appeals yet."}</EmptyRow>
          </tbody>
        </table>
      )}
      <Pager next={props.next} />
    </Layout>
  );
}
