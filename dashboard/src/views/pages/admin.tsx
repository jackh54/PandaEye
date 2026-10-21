import type { DashboardAuditRow } from "../../lib/audit";
import { API_KEY_SCOPES, type ApiKey, type AuditEntry } from "../../lib/pandaeye";
import { assignableRoles, can, invitableRoles, ROLE_LABEL, type Role } from "../../lib/roles";
import type { Session } from "../../lib/session";
import type { InviteRow, UserRow } from "../../lib/users";
import { AbsTime, Csrf, EmptyRow, Notice, PageHeader, Pager, TableWrap, Time } from "../components";
import { Layout, type Flash } from "../layout";

export function UsersPage(props: {
  nonce: string;
  session: Session;
  users: UserRow[];
  invites: InviteRow[];
  newInvite?: { url: string; role: Role; expiresAt: number } | null;
  error?: string | null;
  flash?: Flash | null;
}) {
  const me = props.session.user;
  const manage = can(me.role, "users:manage");
  const roles = invitableRoles(me.role);
  return (
    <Layout title="Users" nonce={props.nonce} session={props.session} active="users" flash={props.flash}>
      <PageHeader title="Users and invites" />
      {props.error ? <Notice kind="error">{props.error}</Notice> : null}
      {props.newInvite ? (
        <Notice kind="ok" title="Invite created">
          <p>
            {`Send this link to the new ${ROLE_LABEL[props.newInvite.role].toLowerCase()}. It works once and expires `}
            <AbsTime value={props.newInvite.expiresAt} />. It is shown only now.
          </p>
          <input class="mono copy-field" readonly value={props.newInvite.url} aria-label="Invite link" />
        </Notice>
      ) : null}

      <section class="card">
        <h2>Users</h2>
        <TableWrap label="Users">
          <table class="table">
            <thead>
              <tr>
                <th scope="col">Username</th>
                <th scope="col">Role</th>
                <th scope="col">State</th>
                <th scope="col">Created</th>
                <th scope="col">Password changed</th>
                {manage ? <th scope="col">Manage</th> : null}
              </tr>
            </thead>
            <tbody>
              {props.users.map((u) => {
                const self = u.id === me.id;
                return (
                  <tr>
                    <td>
                      {u.username}
                      {self ? <span class="tag">you</span> : null}
                    </td>
                    <td>{ROLE_LABEL[u.role]}</td>
                    <td>{u.disabled ? <span class="badge badge-warn">Disabled</span> : <span class="badge badge-ok">Active</span>}</td>
                    <td>
                      <Time value={u.created_at} />
                    </td>
                    <td>
                      <Time value={u.password_changed_at} />
                    </td>
                    {manage ? (
                      <td>
                        {self ? (
                          <span class="muted small">—</span>
                        ) : (
                          <div class="row-actions">
                            <form method="post" action={`/users/${encodeURIComponent(u.id)}/role`} class="inline-form">
                              <Csrf token={props.session.csrf} />
                              <label>
                                <span class="visually-hidden">{`Role of ${u.username}`}</span>
                                <select name="role">
                                  {assignableRoles(me.role).map((r) => (
                                    <option value={r} selected={u.role === r}>
                                      {ROLE_LABEL[r]}
                                    </option>
                                  ))}
                                </select>
                              </label>
                              <button type="submit" class="btn btn-small">
                                Set role
                              </button>
                            </form>
                            <form method="post" action={`/users/${encodeURIComponent(u.id)}/${u.disabled ? "enable" : "disable"}`} class="inline-form">
                              <Csrf token={props.session.csrf} />
                              <button type="submit" class={`btn btn-small ${u.disabled ? "" : "btn-danger"}`}>
                                {u.disabled ? "Enable" : "Disable"}
                              </button>
                            </form>
                          </div>
                        )}
                      </td>
                    ) : null}
                  </tr>
                );
              })}
            </tbody>
          </table>
        </TableWrap>
        {!manage ? <p class="muted small">Only owners can change roles or disable users.</p> : null}
      </section>

      <section class="card">
        <h2>Invites</h2>
        <form method="post" action="/users/invites" class="inline-form">
          <Csrf token={props.session.csrf} />
          <label>
            Role
            <select name="role">
              {roles.map((r) => (
                <option value={r} selected={r === "viewer"}>
                  {ROLE_LABEL[r]}
                </option>
              ))}
            </select>
          </label>
          <button type="submit" class="btn btn-primary">
            Create invite link
          </button>
        </form>
        <p class="hint">Invites are single-use and expire after 48 hours. Only a hash of the link is stored.</p>
        <TableWrap label="Pending invites">
          <table class="table compact">
            <thead>
              <tr>
                <th scope="col">Role</th>
                <th scope="col">Created</th>
                <th scope="col">Expires</th>
                <th scope="col">
                  <span class="visually-hidden">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {props.invites.length ? (
                props.invites.map((i) => (
                  <tr>
                    <td>{ROLE_LABEL[i.role]}</td>
                    <td>
                      <Time value={i.created_at} />
                    </td>
                    <td>
                      <Time value={i.expires_at} />
                    </td>
                    <td>
                      <form method="post" action={`/users/invites/${encodeURIComponent(i.token_hash)}/revoke`} class="inline-form">
                        <Csrf token={props.session.csrf} />
                        <button type="submit" class="btn btn-small btn-ghost">
                          Revoke
                        </button>
                      </form>
                    </td>
                  </tr>
                ))
              ) : (
                <EmptyRow cols={4}>No pending invites.</EmptyRow>
              )}
            </tbody>
          </table>
        </TableWrap>
      </section>
    </Layout>
  );
}

export function ApiKeysPage(props: {
  nonce: string;
  session: Session;
  keys: ApiKey[];
  created?: (ApiKey & { key: string }) | null;
  error?: string | null;
  flash?: Flash | null;
}) {
  return (
    <Layout title="API keys" nonce={props.nonce} session={props.session} active="apikeys" flash={props.flash}>
      <PageHeader title="API keys" />
      {props.error ? <Notice kind="error">{props.error}</Notice> : null}
      {props.created ? (
        <Notice kind="ok" title="API key created">
          <p>Copy the key now. It is shown only once; PandaEye stores only its hash.</p>
          <input class="mono copy-field" readonly value={props.created.key} aria-label="New API key" />
        </Notice>
      ) : null}
      <section class="card">
        <h2>Create a key</h2>
        <form method="post" action="/apikeys" class="stack narrow">
          <Csrf token={props.session.csrf} />
          <label>
            Name
            <input name="name" required maxlength={64} placeholder="e.g. moderation bot" />
          </label>
          <fieldset class="plain">
            <legend>Scopes</legend>
            {API_KEY_SCOPES.map((s) => (
              <label class="check">
                <input type="checkbox" name={`scope:${s}`} checked={s === "read"} />
                <span>
                  <code>{s}</code>
                  {s === "read" ? " — read verdicts, players, bans, stats" : s === "verdicts:write" ? " — confirm/overturn verdicts" : s === "settings:write" ? " — change settings (not enforcement)" : " — erase player data"}
                </span>
              </label>
            ))}
          </fieldset>
          <button type="submit" class="btn btn-primary">
            Create key
          </button>
        </form>
      </section>
      <section class="card">
        <h2>Keys</h2>
        <TableWrap label="API keys">
          <table class="table">
            <thead>
              <tr>
                <th scope="col">Name</th>
                <th scope="col">Id</th>
                <th scope="col">Scopes</th>
                <th scope="col">Created</th>
                <th scope="col">Last used</th>
                <th scope="col">State</th>
                <th scope="col">
                  <span class="visually-hidden">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {props.keys.length ? (
                props.keys.map((k) => (
                  <tr>
                    <td>{k.name || <span class="muted">unnamed</span>}</td>
                    <td class="mono small">{k.id}</td>
                    <td>{k.scopes.join(", ") || "read"}</td>
                    <td>
                      <Time value={k.createdAt} />
                    </td>
                    <td>
                      <Time value={k.lastUsedAt} />
                    </td>
                    <td>{k.revokedAt ? <span class="badge">Revoked</span> : <span class="badge badge-ok">Active</span>}</td>
                    <td>
                      {!k.revokedAt ? (
                        <form method="post" action={`/apikeys/${encodeURIComponent(k.id)}/revoke`} class="inline-form">
                          <Csrf token={props.session.csrf} />
                          <button type="submit" class="btn btn-small btn-danger">
                            Revoke
                          </button>
                        </form>
                      ) : null}
                    </td>
                  </tr>
                ))
              ) : (
                <EmptyRow cols={7}>No API keys.</EmptyRow>
              )}
            </tbody>
          </table>
        </TableWrap>
      </section>
    </Layout>
  );
}

export function AuditPage(props: { nonce: string; session: Session; api: AuditEntry[] | null; next: string | null; local: DashboardAuditRow[] }) {
  return (
    <Layout title="Audit" nonce={props.nonce} session={props.session} active="audit">
      <PageHeader title="Audit log" />
      <section class="card">
        <h2>API actions</h2>
        {props.api === null ? (
          <Notice kind="warn">The API audit log could not be loaded right now.</Notice>
        ) : (
          <>
            <TableWrap label="API audit log">
              <table class="table compact">
                <thead>
                  <tr>
                    <th scope="col">When</th>
                    <th scope="col">Principal</th>
                    <th scope="col">Action</th>
                    <th scope="col">Target</th>
                    <th scope="col">Detail</th>
                  </tr>
                </thead>
                <tbody>
                  {props.api.length ? (
                    props.api.map((e) => (
                      <tr>
                        <td>
                          <AbsTime value={e.createdAt} />
                        </td>
                        <td>{e.principal}</td>
                        <td>
                          <code>{e.action}</code>
                        </td>
                        <td class="mono small">{e.target}</td>
                        <td class="info small">{e.detail}</td>
                      </tr>
                    ))
                  ) : (
                    <EmptyRow cols={5}>No entries.</EmptyRow>
                  )}
                </tbody>
              </table>
            </TableWrap>
            <Pager next={props.next} />
          </>
        )}
      </section>
      <section class="card">
        <h2>Dashboard sign-ins and account changes</h2>
        <TableWrap label="Dashboard audit log">
          <table class="table compact">
            <thead>
              <tr>
                <th scope="col">When</th>
                <th scope="col">Principal</th>
                <th scope="col">Action</th>
                <th scope="col">Target</th>
                <th scope="col">Detail</th>
              </tr>
            </thead>
            <tbody>
              {props.local.length ? (
                props.local.map((e) => (
                  <tr>
                    <td>
                      <AbsTime value={e.created_at} />
                    </td>
                    <td>{e.principal}</td>
                    <td>
                      <code>{e.action}</code>
                    </td>
                    <td class="mono small">{e.target ?? ""}</td>
                    <td class="info small">{e.detail ?? ""}</td>
                  </tr>
                ))
              ) : (
                <EmptyRow cols={5}>No entries.</EmptyRow>
              )}
            </tbody>
          </table>
        </TableWrap>
      </section>
    </Layout>
  );
}
