import { categoryLabel, cloudChecks, localChecks, type CheckInfo } from "../../lib/checks";
import { ACTION_LABEL, type ActionName } from "../../lib/format";
import type { CheckConfig, Tenant } from "../../lib/pandaeye";
import type { Session } from "../../lib/session";
import { formatOverrides } from "../../lib/validate";
import { CheckName, Csrf, Notice, PageHeader, TableWrap } from "../components";
import { Layout, type Flash } from "../layout";

const LENIENCY: { value: number; label: string; hint: string }[] = [
  { value: 1, label: "1 – Strict", hint: "thresholds × 0.6" },
  { value: 2, label: "2", hint: "thresholds × 0.8" },
  { value: 3, label: "3 – Balanced", hint: "base thresholds (alert 15, mitigate 30, kick 70, ban 120)" },
  { value: 4, label: "4 – Lenient (default)", hint: "thresholds × 1.3" },
  { value: 5, label: "5 – Very lenient", hint: "thresholds × 1.7" },
];

const MIN_ACTIONS: ActionName[] = ["alert", "mitigate", "kick", "ban"];

export function SettingsPage(props: { nonce: string; session: Session; tenant: Tenant; error?: string | null; flash?: Flash | null }) {
  const t = props.tenant;
  return (
    <Layout title="Settings" nonce={props.nonce} session={props.session} active="settings" flash={props.flash}>
      <PageHeader title="Settings" />
      {props.error ? <Notice kind="error">{props.error}</Notice> : null}
      <form method="post" action="/settings" class="settings-form">
        <Csrf token={props.session.csrf} />

        <fieldset class="card">
          <legend>Detection</legend>
          <p class="muted small">Leniency scales every action threshold. Higher values mean fewer, more certain actions.</p>
          <div class="radio-list">
            {LENIENCY.map((l) => (
              <label class="radio">
                <input type="radio" name="leniency" value={String(l.value)} checked={t.leniency === l.value} />
                <span>
                  <strong>{l.label}</strong> <span class="muted small">{l.hint}</span>
                </span>
              </label>
            ))}
          </div>
        </fieldset>

        <fieldset class="card">
          <legend>Automatic actions</legend>
          <p class="muted small">
            With an action switched off, PandaEye still evaluates it and records a "SHADOW – not executed" verdict, so you can see
            what it would have done before turning it on.
          </p>
          <label class="check">
            <input type="checkbox" name="mitigations" checked={t.mitigations} />
            <span>
              <strong>Mitigations</strong> — quietly nerf suspicious players (cancel hits, reduce damage, set back movement) until they leave.
            </span>
          </label>
          <label class="check">
            <input type="checkbox" name="autokick" checked={t.autokick} />
            <span>
              <strong>Auto-kick</strong> — kick on high scores with confidence ≥ 80% from at least two check families.
            </span>
          </label>
          <label class="check">
            <input type="checkbox" name="autoban" checked={t.autoban} />
            <span>
              <strong>Auto-ban</strong> — ban on very high scores with confidence ≥ 90% and a replay clip as evidence. Players can
              appeal with the code in the ban message.
            </span>
          </label>
        </fieldset>

        <fieldset class={`card ${t.enforcement ? "" : "fieldset-warn"}`}>
          <legend>Enforcement</legend>
          <Notice kind="warn" title="Read before enabling">
            Enforcement is the master switch. While it is off, nothing is ever enforced on any server: every verdict is delivered as
            shadow. When it is on, servers whose plugin runs in <code>mode: live</code> apply the automatic actions enabled above
            (mitigations, kicks, bans) without asking. Run in shadow mode first and review the shadow verdicts.
          </Notice>
          <label class="check">
            <input type="checkbox" name="enforcement" checked={t.enforcement} />
            <span>
              <strong>Enforcement enabled</strong>
            </span>
          </label>
          {!t.enforcement ? (
            <label class="check">
              <input type="checkbox" name="confirm_enforcement" />
              <span>I reviewed the shadow verdicts and understand that enabling enforcement lets PandaEye act on live servers.</span>
            </label>
          ) : null}
        </fieldset>

        <fieldset class="card">
          <legend>Reputation network</legend>
          <label class="check">
            <input type="checkbox" name="reputation_opt_in" checked={t.reputationOptIn} />
            <span>
              <strong>Join the reputation network</strong> — share confirmed, executed bans with other trusted networks and see an
              advisory risk for joining players. Risk is shown only when at least two networks agree and never scores or acts on its
              own.
            </span>
          </label>
        </fieldset>

        <fieldset class="card">
          <legend>Discord alerts</legend>
          <p class="small">
            Current webhook: {t.discordWebhook ? <code>{t.discordWebhook}</code> : <span class="muted">none</span>}
          </p>
          <label>
            New webhook URL
            <input
              name="discord_webhook"
              type="url"
              autocomplete="off"
              spellcheck={false}
              maxlength={300}
              placeholder="https://discord.com/api/webhooks/…"
            />
            <span class="hint">Leave empty to keep the current webhook. A test message is sent when you save.</span>
          </label>
          {t.discordWebhook ? (
            <label class="check">
              <input type="checkbox" name="discord_webhook_remove" />
              <span>Remove the webhook</span>
            </label>
          ) : null}
          <label>
            Post verdicts from
            <select name="discord_min_action">
              {MIN_ACTIONS.map((a) => (
                <option value={a} selected={t.discordMinAction === a}>
                  {`${ACTION_LABEL[a]} and above`}
                </option>
              ))}
            </select>
          </label>
        </fieldset>

        <div class="form-footer">
          <button type="submit" class="btn btn-primary">
            Save settings
          </button>
        </div>
      </form>
    </Layout>
  );
}

function CloudCheckRow(props: { info: CheckInfo; config: CheckConfig | undefined }) {
  const id = props.info.id;
  const enabled = props.config ? props.config.enabled : !props.info.defaultOff;
  return (
    <tr>
      <td>
        <CheckName id={id} />
      </td>
      <td>{categoryLabel(props.info.category)}</td>
      <td class="num">{props.info.points}</td>
      <td class="num">{String(props.info.cap)}</td>
      <td>
        <label class="check compact">
          <input type="checkbox" name={`enabled:${id}`} checked={enabled} />
          <span class="visually-hidden">{`Enable ${id}`}</span>
        </label>
      </td>
      <td>
        <label>
          <span class="visually-hidden">{`Threshold overrides for ${id}`}</span>
          <input name={`overrides:${id}`} value={props.config ? formatOverrides(props.config.overrides) : ""} maxlength={500} placeholder="key=value, …" class="mono" />
        </label>
      </td>
    </tr>
  );
}

export function ChecksPage(props: { nonce: string; session: Session; configs: CheckConfig[]; error?: string | null; flash?: Flash | null }) {
  const byId = new Map(props.configs.map((c) => [c.id, c]));
  const known = new Set(cloudChecks().map((c) => c.id));
  const unknown = props.configs.filter((c) => !known.has(c.id) && !localChecks().some((l) => l.id === c.id));
  return (
    <Layout title="Checks" nonce={props.nonce} session={props.session} active="checks" flash={props.flash}>
      <PageHeader title="Checks" />
      {props.error ? <Notice kind="error">{props.error}</Notice> : null}
      <p class="muted">
        Cloud checks run on PandaEye's workers; disabled checks emit no signals. Threshold overrides replace the shipped defaults (see
        CHECKS.md) as <code>name=number</code> pairs. Local checks run inside the plugin and are configured in its{" "}
        <code>config.yml</code>.
      </p>
      <form method="post" action="/checks">
        <Csrf token={props.session.csrf} />
        <section class="card">
          <h2>Cloud checks</h2>
          <TableWrap label="Cloud checks">
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
                  <th scope="col">Enabled</th>
                  <th scope="col">Threshold overrides</th>
                </tr>
              </thead>
              <tbody>
                {cloudChecks().map((info) => (
                  <CloudCheckRow info={info} config={byId.get(info.id)} />
                ))}
              </tbody>
            </table>
          </TableWrap>
          {unknown.length ? (
            <p class="muted small">{`The API also reports ${unknown.length} check(s) this dashboard does not know: ${unknown.map((u) => u.id).join(", ")}. They are kept unchanged.`}</p>
          ) : null}
          <div class="form-footer">
            <button type="submit" class="btn btn-primary">
              Save checks
            </button>
          </div>
        </section>
      </form>
      <section class="card">
        <h2>Local checks (plugin config.yml)</h2>
        <TableWrap label="Local checks">
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
              </tr>
            </thead>
            <tbody>
              {localChecks().map((info) => (
                <tr>
                  <td>
                    <CheckName id={info.id} />
                  </td>
                  <td>{categoryLabel(info.category)}</td>
                  <td class="num">{info.points}</td>
                  <td class="num">{String(info.cap)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      </section>
    </Layout>
  );
}
