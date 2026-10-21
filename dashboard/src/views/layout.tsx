import type { Child } from "hono/jsx";
import { can, ROLE_LABEL, type Permission } from "../lib/roles";
import type { Session } from "../lib/session";
import { TURNSTILE_SCRIPT_URL } from "../lib/turnstile";

export const ASSET_VERSION = "1";

export interface Flash {
  kind: "ok" | "error" | "warn" | "info";
  text: string;
}

export interface LayoutProps {
  title: string;
  nonce: string;
  session?: Session | null;
  active?: string;
  flash?: Flash | null;
  turnstile?: boolean;
  scripts?: string[];
  children?: Child;
}

interface NavItem {
  href: string;
  key: string;
  label: string;
  perm?: Permission;
}

const NAV: NavItem[] = [
  { href: "/", key: "overview", label: "Overview" },
  { href: "/servers", key: "servers", label: "Servers" },
  { href: "/verdicts", key: "verdicts", label: "Verdicts" },
  { href: "/players", key: "players", label: "Players" },
  { href: "/bans", key: "bans", label: "Bans" },
  { href: "/appeals", key: "appeals", label: "Appeals" },
  { href: "/usage", key: "usage", label: "Usage" },
  { href: "/settings", key: "settings", label: "Settings", perm: "settings:write" },
  { href: "/checks", key: "checks", label: "Checks", perm: "checks:write" },
  { href: "/users", key: "users", label: "Users", perm: "users:view" },
  { href: "/apikeys", key: "apikeys", label: "API keys", perm: "apikeys:manage" },
  { href: "/audit", key: "audit", label: "Audit", perm: "audit:read" },
];

export function Layout(props: LayoutProps) {
  const { session } = props;
  return (
    <html lang="en">
      <head>
        <meta charset="utf-8" />
        <meta name="viewport" content="width=device-width, initial-scale=1" />
        <meta name="color-scheme" content="light dark" />
        <meta name="referrer" content="same-origin" />
        <title>{`${props.title} · PandaEye`}</title>
        <link rel="stylesheet" href={`/static/app.css?v=${ASSET_VERSION}`} nonce={props.nonce} />
        <link rel="icon" href={`/static/favicon.svg?v=${ASSET_VERSION}`} type="image/svg+xml" />
        {props.turnstile ? <script src={TURNSTILE_SCRIPT_URL} nonce={props.nonce} async defer></script> : null}
        {(props.scripts ?? []).map((src) => (
          <script src={`${src}?v=${ASSET_VERSION}`} nonce={props.nonce} defer></script>
        ))}
      </head>
      <body>
        <a class="skip-link" href="#main">
          Skip to content
        </a>
        <header class="site-header">
          <div class="header-inner">
            <a class="brand" href={session ? "/" : "/status"}>
              <span class="brand-mark" aria-hidden="true">
                ◉
              </span>
              PandaEye
            </a>
            {session ? (
              <div class="account">
                <a href="/account" class="account-link">
                  {session.user.username}
                  <span class="role-tag">{ROLE_LABEL[session.user.role]}</span>
                </a>
                <form method="post" action="/logout" class="inline-form">
                  <input type="hidden" name="_csrf" value={session.csrf} />
                  <button type="submit" class="btn btn-small btn-ghost">
                    Log out
                  </button>
                </form>
              </div>
            ) : null}
          </div>
          {session ? (
            <nav class="main-nav" aria-label="Main">
              <ul>
                {NAV.filter((n) => !n.perm || can(session.user.role, n.perm)).map((n) => (
                  <li>
                    <a href={n.href} aria-current={props.active === n.key ? "page" : undefined}>
                      {n.label}
                    </a>
                  </li>
                ))}
              </ul>
            </nav>
          ) : null}
        </header>
        <main id="main" class="container">
          {props.flash ? (
            <div class={`notice notice-${props.flash.kind}`} role={props.flash.kind === "error" ? "alert" : "status"}>
              {props.flash.text}
            </div>
          ) : null}
          {props.children}
        </main>
        <footer class="site-footer">
          <div class="container">
            <a href="/status">Status</a>
            <a href="/privacy">Privacy</a>
            <a href="/appeal">Appeal a ban</a>
          </div>
        </footer>
      </body>
    </html>
  );
}
