import { PASSWORD_MIN_LENGTH } from "../../lib/password";
import { ROLE_LABEL, type Role } from "../../lib/roles";
import type { Session } from "../../lib/session";
import { AbsTime, Csrf, Notice, PageHeader } from "../components";
import { Layout, type Flash } from "../layout";

export function LoginPage(props: { nonce: string; csrf: string; next: string; username?: string; error?: string | null; flash?: Flash | null }) {
  return (
    <Layout title="Sign in" nonce={props.nonce} flash={props.flash}>
      <section class="auth-card">
        <h1>Sign in to PandaEye</h1>
        {props.error ? <Notice kind="error">{props.error}</Notice> : null}
        <form method="post" action="/login" class="stack">
          <Csrf token={props.csrf} />
          <input type="hidden" name="next" value={props.next} />
          <label>
            Username
            <input name="username" autocomplete="username" required maxlength={32} value={props.username ?? ""} autofocus />
          </label>
          <label>
            Password
            <input name="password" type="password" autocomplete="current-password" required maxlength={1024} />
          </label>
          <button type="submit" class="btn btn-primary">
            Sign in
          </button>
        </form>
        <p class="muted small">
          New network? <a href="/setup">Set up PandaEye with your license key</a>. Joining a team? Ask an admin for an invite link.
        </p>
      </section>
    </Layout>
  );
}

function TurnstileWidget(props: { sitekey: string | undefined; action: string }) {
  if (!props.sitekey) return <p class="muted small">Human verification is disabled in this development environment.</p>;
  return <div class="cf-turnstile" data-sitekey={props.sitekey} data-action={props.action}></div>;
}

export function SetupPage(props: {
  nonce: string;
  csrf: string;
  sitekey: string | undefined;
  username?: string;
  error?: string | null;
}) {
  return (
    <Layout title="Set up" nonce={props.nonce} turnstile={!!props.sitekey}>
      <section class="auth-card">
        <h1>Set up PandaEye</h1>
        <p>
          Enter the license key of your network to create its owner account. This works only once per license: later users join
          through invites.
        </p>
        {props.error ? <Notice kind="error">{props.error}</Notice> : null}
        <form method="post" action="/setup" class="stack">
          <Csrf token={props.csrf} />
          <label>
            License key
            <input name="license_key" type="password" autocomplete="off" required maxlength={256} spellcheck={false} />
          </label>
          <label>
            Owner username
            <input name="username" autocomplete="username" required minlength={3} maxlength={32} pattern="[a-z0-9][a-z0-9_.\-]{2,31}" value={props.username ?? ""} />
            <span class="hint">3–32 characters: lowercase letters, digits, dot, dash, underscore.</span>
          </label>
          <label>
            Password
            <input name="password" type="password" autocomplete="new-password" required minlength={PASSWORD_MIN_LENGTH} maxlength={1024} />
            <span class="hint">At least {PASSWORD_MIN_LENGTH} characters.</span>
          </label>
          <label>
            Repeat password
            <input name="password_confirm" type="password" autocomplete="new-password" required minlength={PASSWORD_MIN_LENGTH} maxlength={1024} />
          </label>
          <TurnstileWidget sitekey={props.sitekey} action="setup" />
          <button type="submit" class="btn btn-primary">
            Create owner account
          </button>
        </form>
        <p class="muted small">
          Already set up? <a href="/login">Sign in</a>.
        </p>
      </section>
    </Layout>
  );
}

export function InvitePage(props: { nonce: string; csrf: string; token: string; role: Role | null; username?: string; error?: string | null }) {
  if (!props.role) {
    return (
      <Layout title="Invite" nonce={props.nonce}>
        <section class="auth-card">
          <h1>Invite not valid</h1>
          <p>This invite link is invalid, was already used, or has expired. Invites are valid for 48 hours and can be used once.</p>
          <p>
            <a href="/login">Go to sign in</a>
          </p>
        </section>
      </Layout>
    );
  }
  return (
    <Layout title="Accept invite" nonce={props.nonce}>
      <section class="auth-card">
        <h1>Join PandaEye</h1>
        <p>
          You were invited as <strong>{ROLE_LABEL[props.role]}</strong>. Choose a username and password.
        </p>
        {props.error ? <Notice kind="error">{props.error}</Notice> : null}
        <form method="post" action={`/invite/${encodeURIComponent(props.token)}`} class="stack">
          <Csrf token={props.csrf} />
          <label>
            Username
            <input name="username" autocomplete="username" required minlength={3} maxlength={32} pattern="[a-z0-9][a-z0-9_.\-]{2,31}" value={props.username ?? ""} />
            <span class="hint">3–32 characters: lowercase letters, digits, dot, dash, underscore.</span>
          </label>
          <label>
            Password
            <input name="password" type="password" autocomplete="new-password" required minlength={PASSWORD_MIN_LENGTH} maxlength={1024} />
          </label>
          <label>
            Repeat password
            <input name="password_confirm" type="password" autocomplete="new-password" required minlength={PASSWORD_MIN_LENGTH} maxlength={1024} />
          </label>
          <button type="submit" class="btn btn-primary">
            Create account
          </button>
        </form>
      </section>
    </Layout>
  );
}

export function AccountPage(props: { nonce: string; session: Session; error?: string | null; flash?: Flash | null }) {
  const s = props.session;
  return (
    <Layout title="Account" nonce={props.nonce} session={s} flash={props.flash}>
      <PageHeader title="Your account" />
      <dl class="kv">
        <dt>Username</dt>
        <dd>{s.user.username}</dd>
        <dt>Role</dt>
        <dd>{ROLE_LABEL[s.user.role]}</dd>
        <dt>Tenant</dt>
        <dd class="mono">{s.user.tenantId}</dd>
        <dt>Session started</dt>
        <dd>
          <AbsTime value={s.createdAt} />
        </dd>
        <dt>Session expires</dt>
        <dd>
          <AbsTime value={s.expiresAt} /> (or after 2 hours without activity)
        </dd>
      </dl>
      <section class="card">
        <h2>Change password</h2>
        {props.error ? <Notice kind="error">{props.error}</Notice> : null}
        <form method="post" action="/account/password" class="stack narrow">
          <Csrf token={s.csrf} />
          <label>
            Current password
            <input name="current" type="password" autocomplete="current-password" required maxlength={1024} />
          </label>
          <label>
            New password
            <input name="password" type="password" autocomplete="new-password" required minlength={PASSWORD_MIN_LENGTH} maxlength={1024} />
          </label>
          <label>
            Repeat new password
            <input name="password_confirm" type="password" autocomplete="new-password" required minlength={PASSWORD_MIN_LENGTH} maxlength={1024} />
          </label>
          <button type="submit" class="btn btn-primary">
            Change password
          </button>
          <p class="hint">Changing your password signs out all your other sessions.</p>
        </form>
      </section>
    </Layout>
  );
}
