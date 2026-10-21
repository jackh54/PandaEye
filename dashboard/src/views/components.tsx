import type { Child } from "hono/jsx";
import { categoryLabel, checkInfo, type Category } from "../lib/checks";
import {
  ACTION_LABEL,
  formatNumber,
  formatPercent,
  formatRelative,
  formatUtc,
  isoString,
  seg,
  shortId,
  type ActionName,
} from "../lib/format";
import type { Ban, Verdict } from "../lib/pandaeye";

export function Csrf(props: { token: string }) {
  return <input type="hidden" name="_csrf" value={props.token} />;
}

export function ActionBadge(props: { action: ActionName | null }) {
  if (!props.action) return <span class="badge">Unknown</span>;
  return <span class={`badge action-${props.action}`}>{ACTION_LABEL[props.action]}</span>;
}

export const SHADOW_LABEL = "SHADOW – not executed";

export function ShadowBadge() {
  return (
    <span class="badge badge-shadow" title="Produced in shadow mode or forbidden by the tenant's settings: recorded as 'would have', never enforced.">
      {SHADOW_LABEL}
    </span>
  );
}

export function StatusBadge(props: { status: string }) {
  const s = props.status || "open";
  return <span class={`badge status-${/^[a-z]+$/.test(s) ? s : "other"}`}>{s}</span>;
}

/** Delivery / execution state of a verdict. */
export function ExecState(props: { v: Verdict }) {
  const v = props.v;
  if (v.shadow) return <ShadowBadge />;
  if (v.action === "alert") return <span class="muted">alert only</span>;
  if (v.executed === true) return <span class="badge badge-ok">Executed</span>;
  if (v.executed === false)
    return (
      <span class="badge badge-warn" title={v.ackNote || undefined}>
        Not executed{v.ackNote ? `: ${v.ackNote}` : ""}
      </span>
    );
  if (v.deliveredAt) return <span class="badge">Delivered, awaiting ack</span>;
  return <span class="badge">Pending delivery</span>;
}

export function UnbanBadge(props: { ban: Ban }) {
  const b = props.ban;
  if (b.unbanState === "pending") return <span class="badge badge-warn">Unban pending</span>;
  if (b.unbanState === "done") return <span class="badge badge-ok">Unbanned</span>;
  return b.active ? <span class="badge action-ban">Active</span> : <span class="badge">Inactive</span>;
}

export function Time(props: { value: unknown; seconds?: boolean }) {
  const iso = isoString(props.value);
  if (!iso) return <span class="muted">—</span>;
  return (
    <time datetime={iso} title={formatUtc(props.value, true)}>
      {formatRelative(props.value)}
    </time>
  );
}

export function AbsTime(props: { value: unknown }) {
  const iso = isoString(props.value);
  if (!iso) return <span class="muted">—</span>;
  return <time datetime={iso}>{formatUtc(props.value, true)}</time>;
}

export function Pct(props: { value: unknown; digits?: number }) {
  return <span class="num">{formatPercent(props.value, props.digits ?? 0)}</span>;
}

export function Num(props: { value: unknown; digits?: number }) {
  return <span class="num">{formatNumber(props.value, props.digits ?? 0)}</span>;
}

export function CategoryTag(props: { category: Category | null | string }) {
  return <span class="tag">{categoryLabel(props.category)}</span>;
}

export function CheckName(props: { id: string }) {
  const info = checkInfo(props.id);
  return (
    <span class="check-id">
      <code>{props.id}</code>
      {info?.certain ? (
        <span class="tag tag-certain" title="This check alone may justify a ban">
          certain
        </span>
      ) : null}
      {info ? <span class="tag tag-where">{info.where}</span> : null}
    </span>
  );
}

export function PlayerLink(props: { uuid: string; name?: string }) {
  const label = props.name || shortId(props.uuid, 13) || "unknown";
  if (!props.uuid) return <span>{label}</span>;
  return <a href={`/players/${seg(props.uuid)}`}>{label}</a>;
}

export function VerdictLink(props: { id: string; children?: Child }) {
  if (!props.id) return <span class="muted">—</span>;
  return (
    <a href={`/verdicts/${seg(props.id)}`} class="mono">
      {props.children ?? shortId(props.id)}
    </a>
  );
}

export function Notice(props: { kind: "ok" | "error" | "warn" | "info"; title?: string; children?: Child }) {
  return (
    <div class={`notice notice-${props.kind}`} role={props.kind === "error" ? "alert" : undefined}>
      {props.title ? <strong class="notice-title">{props.title}</strong> : null}
      {props.children}
    </div>
  );
}

export function Pager(props: { next: string | null }) {
  if (!props.next) return null;
  return (
    <nav class="pager" aria-label="Pagination">
      <a class="btn btn-small" href={props.next}>
        Next page →
      </a>
    </nav>
  );
}

export function EmptyRow(props: { cols: number; children?: Child }) {
  return (
    <tr>
      <td colspan={props.cols} class="empty">
        {props.children ?? "Nothing here yet."}
      </td>
    </tr>
  );
}

export function TableWrap(props: { children?: Child; label?: string }) {
  return (
    <div class="table-wrap" role="region" aria-label={props.label} tabindex={0}>
      {props.children}
    </div>
  );
}

export function PageHeader(props: { title: string; children?: Child }) {
  return (
    <div class="page-header">
      <h1>{props.title}</h1>
      {props.children ? <div class="page-actions">{props.children}</div> : null}
    </div>
  );
}

export function Stat(props: { label: string; value: Child; hint?: Child; tone?: "ok" | "warn" | "bad" }) {
  return (
    <div class={`stat${props.tone ? ` stat-${props.tone}` : ""}`}>
      <div class="stat-label">{props.label}</div>
      <div class="stat-value">{props.value}</div>
      {props.hint ? <div class="stat-hint">{props.hint}</div> : null}
    </div>
  );
}

/** Banner for the free-tier cap (ARCHITECTURE 5.8): over the limit for 3 days caps verdicts at ALERT. */
export function OverLimitBanner(props: { since: string | null; limit?: number; now?: number }) {
  if (!props.since) return null;
  const d = isoString(props.since);
  if (!d) return null;
  const days = ((props.now ?? Date.now()) - new Date(d).getTime()) / 86_400_000;
  const capped = days >= 3;
  return (
    <div class={`notice ${capped ? "notice-error" : "notice-warn"}`} role="status">
      <strong class="notice-title">{capped ? "Verdicts are capped at ALERT" : "Free tier limit exceeded"}</strong>
      Your servers have been over the free tier ({props.limit ?? 30} peak players) since <AbsTime value={props.since} />.{" "}
      {capped
        ? "After 3 days over the limit PandaEye only raises alerts: no mitigations, kicks or bans are issued until usage drops or the plan is upgraded. Players are never affected by billing state."
        : "If usage stays over the limit for 3 days, verdicts will be capped at ALERT (no mitigations, kicks or bans). Players are never affected by billing state."}{" "}
      <a href="/usage">View usage</a>
    </div>
  );
}
