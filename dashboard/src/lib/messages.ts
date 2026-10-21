// Fixed flash messages selected by `?ok=` / `?err=` codes after a redirect. User-controlled text is never
// reflected through the query string.

import type { Flash } from "../views/layout";

const OK: Record<string, string> = {
  confirmed: "Verdict confirmed and labelled as cheating.",
  overturned: "Verdict overturned. If the ban was executed, an unban is pending until the server acknowledges it.",
  appeal_accepted: "Appeal accepted: the verdict was overturned.",
  appeal_denied: "Appeal denied.",
  settings_saved: "Settings saved and pushed to connected servers.",
  checks_saved: "Check configuration saved.",
  invite_revoked: "Invite revoked.",
  role_changed: "Role changed. The user's sessions were signed out.",
  user_disabled: "User disabled and signed out.",
  user_enabled: "User enabled.",
  password_changed: "Password changed. Other sessions were signed out.",
  apikey_revoked: "API key revoked.",
  watch_requested: "Full-rate telemetry requested for this player.",
  welcome: "Welcome to PandaEye.",
  logged_out: "You have been signed out.",
};

const ERR: Record<string, string> = {
  csrf: "Your form expired. Please try again.",
  forbidden: "Your role does not allow this action.",
  invalid: "Some fields were invalid.",
  api: "The PandaEye API rejected the request.",
  session_expired: "Your session expired. Please sign in again.",
};

export function flashFromQuery(q: { ok?: string; err?: string }): Flash | null {
  if (q.ok && OK[q.ok]) return { kind: "ok", text: OK[q.ok]! };
  if (q.err && ERR[q.err]) return { kind: "error", text: ERR[q.err]! };
  return null;
}
