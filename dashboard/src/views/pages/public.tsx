import { formatDuration, formatMsFromUs, formatNumber } from "../../lib/format";
import type { PublicStatus } from "../../lib/pandaeye";
import { Csrf, Notice, Stat } from "../components";
import { Layout } from "../layout";

export function StatusPage(props: { nonce: string; status: PublicStatus | null; checkedAt: number }) {
  const s = props.status;
  const operational = !!s;
  const sloOk = s?.sloMet;
  return (
    <Layout title="Status" nonce={props.nonce}>
      <section>
        <h1>PandaEye status</h1>
        {!operational ? (
          <Notice kind="error" title="Status unavailable">
            The PandaEye cloud did not answer the status check. Plugins keep running their local checks and buffer events until the
            cloud is reachable again.
          </Notice>
        ) : sloOk === false ? (
          <Notice kind="warn" title="Degraded performance">
            The cloud is up, but verdict latency is above the published objective.
          </Notice>
        ) : (
          <Notice kind="ok" title="All systems operational">
            The cloud is accepting connections and processing events.
          </Notice>
        )}
        {s ? (
          <>
            <div class="stats">
              <Stat label="Region" value={s.region || "—"} />
              <Stat label="Connected servers" value={formatNumber(s.connectedServers)} />
              <Stat
                label="Probe RTT p95 (1 h)"
                value={formatMsFromUs(s.probe.p95Us)}
                hint={`objective: p95 < ${s.sloTargetMs} ms in-region`}
                tone={sloOk === false ? "bad" : sloOk === true ? "ok" : undefined}
              />
              <Stat label="Uptime" value={s.uptimeS === null ? "—" : formatDuration(s.uptimeS)} />
            </div>
            <h2>End-to-end verdict latency (last hour)</h2>
            <p class="muted small">
              Measured by the plugins: a probe event travels the full pipeline (gateway, event stream, worker, verdict service) and
              back.
            </p>
            <table class="table compact">
              <thead>
                <tr>
                  <th scope="col">Percentile</th>
                  <th scope="col">Round trip</th>
                </tr>
              </thead>
              <tbody>
                <tr>
                  <td>p50</td>
                  <td class="num">{formatMsFromUs(s.probe.p50Us)}</td>
                </tr>
                <tr>
                  <td>p95</td>
                  <td class="num">{formatMsFromUs(s.probe.p95Us)}</td>
                </tr>
                <tr>
                  <td>p99</td>
                  <td class="num">{formatMsFromUs(s.probe.p99Us)}</td>
                </tr>
              </tbody>
            </table>
            <p class="muted small">Version {s.version || "unknown"}.</p>
          </>
        ) : null}
        <p class="muted small">
          Checked <time datetime={new Date(props.checkedAt).toISOString()}>{new Date(props.checkedAt).toISOString().slice(0, 19).replace("T", " ")} UTC</time>.
        </p>
      </section>
    </Layout>
  );
}

export interface AppealForm {
  verdict_id: string;
  appeal_code: string;
  message: string;
  contact: string;
}

export function AppealPage(props: {
  nonce: string;
  csrf: string;
  sitekey: string | undefined;
  form?: Partial<AppealForm>;
  error?: string | null;
  done?: boolean;
}) {
  const f = props.form ?? {};
  if (props.done) {
    return (
      <Layout title="Appeal received" nonce={props.nonce}>
        <section class="auth-card">
          <h1>Appeal received</h1>
          <p>
            Thank you. The staff of the network that banned you will review the evidence and your message. A human makes the
            decision; if the ban is overturned, it is lifted automatically on the server.
          </p>
          <p class="muted small">
            You can see how PandaEye handles your data on the <a href="/privacy">privacy page</a>.
          </p>
        </section>
      </Layout>
    );
  }
  return (
    <Layout title="Appeal a ban" nonce={props.nonce} turnstile={!!props.sitekey}>
      <section class="auth-card wide">
        <h1>Appeal a ban</h1>
        <p>
          The ban message on the server shows a verdict id and an appeal code. Enter both and explain what happened. Staff of the
          network review every appeal by hand.
        </p>
        {props.error ? <Notice kind="error">{props.error}</Notice> : null}
        <form method="post" action="/appeal" class="stack">
          <Csrf token={props.csrf} />
          <label>
            Verdict id
            <input name="verdict_id" required maxlength={64} spellcheck={false} autocomplete="off" value={f.verdict_id ?? ""} placeholder="e.g. 3f0c2c9e-..." />
          </label>
          <label>
            Appeal code
            <input name="appeal_code" required maxlength={32} spellcheck={false} autocomplete="off" value={f.appeal_code ?? ""} />
          </label>
          <label>
            Your message
            <textarea name="message" required minlength={10} maxlength={2000} rows={6}>
              {f.message ?? ""}
            </textarea>
            <span class="hint">10–2000 characters. Do not include passwords or payment details.</span>
          </label>
          <label>
            How staff can reach you (optional)
            <input name="contact" maxlength={200} autocomplete="off" value={f.contact ?? ""} placeholder="Discord name, e-mail, forum name..." />
            <span class="hint">Deleted 30 days after the appeal is decided.</span>
          </label>
          <TurnstileBox sitekey={props.sitekey} />
          <button type="submit" class="btn btn-primary">
            Submit appeal
          </button>
        </form>
      </section>
    </Layout>
  );
}

function TurnstileBox(props: { sitekey: string | undefined }) {
  if (!props.sitekey) return <p class="muted small">Human verification is disabled in this development environment.</p>;
  return <div class="cf-turnstile" data-sitekey={props.sitekey} data-action="appeal"></div>;
}

export function PrivacyPage(props: { nonce: string }) {
  return (
    <Layout title="Privacy" nonce={props.nonce}>
      <article class="prose">
        <h1>Privacy at PandaEye</h1>
        <p>
          PandaEye is an anticheat service for Minecraft servers. This page summarises how player and staff data is handled; the full
          policy is in the project's PRIVACY document.
        </p>

        <h2>Roles</h2>
        <ul>
          <li>
            For gameplay data of a network's players, PandaEye acts as a <strong>processor</strong> on behalf of the server operator
            (the controller). Operators receive a template notice to inform their players.
          </li>
          <li>
            For the opt-in reputation network and any cross-network model training, PandaEye is a <strong>controller</strong>, based
            on a documented legitimate-interest assessment.
          </li>
        </ul>

        <h2>What is collected</h2>
        <ul>
          <li>Player UUID and name, client version and client brand.</li>
          <li>Gameplay telemetry needed for detection: movement, rotations, combat, block and inventory interactions, latency.</li>
          <li>
            <strong>No IP addresses</strong> of players are collected or stored.
          </li>
          <li>For appeals: the message and an optional contact handle provided by the player.</li>
          <li>
            For dashboard staff accounts: username, a salted and peppered password hash, session records and short-lived rate-limit
            counters keyed by a pseudonymous hash of the network address (deleted after a day).
          </li>
        </ul>

        <h2>Purposes</h2>
        <p>
          Detecting cheating, producing evidence (checks, confidence and short replay clips) for each action, handling appeals, and,
          only for networks that opt in, sharing confirmed cheat verdicts with other trusted networks.
        </p>

        <h2>Retention</h2>
        <ul>
          <li>Raw event stream: 24 hours. Detection signals: 7 days. Verdict delivery records: 30 days.</li>
          <li>Replay clips: 90 days, unless referenced by an active ban, an open appeal or a label.</li>
          <li>Verdicts and evidence: 180 days (kept while a ban is active).</li>
          <li>Appeals: 1 year; the contact handle is deleted 30 days after the decision.</li>
          <li>Labels: 2 years. Usage statistics: 2 years. Latency statistics: 30 days. Audit logs: 1 year.</li>
        </ul>

        <h2>Automated decisions and your rights</h2>
        <ul>
          <li>
            Every kick and ban carries evidence. Ban messages include an appeal code and a link to the <a href="/appeal">appeal form</a>
            .
          </li>
          <li>Appeals are reviewed by people. An accepted appeal overturns the verdict and lifts the ban automatically.</li>
          <li>Players can ask the server operator to erase their data; operators can erase a player from the dashboard.</li>
          <li>
            After an erasure, an active ban keeps only a minimal record (verdict id, UUID, check ids, score, time) under the
            legitimate-interest exception until it is lifted. Residual data in the event stream expires within the retention times
            above.
          </li>
        </ul>

        <h2>Minors</h2>
        <p>
          Many players are minors. PandaEye collects no contact data during play and no IP addresses; appeal contact fields are
          optional and short-lived.
        </p>

        <h2>Regions and sub-processors</h2>
        <p>
          The dashboard runs on Cloudflare Workers and D1. The cloud service runs in the region shown on the <a href="/status">status page</a>.
          The full list of sub-processors is in the PRIVACY document.
        </p>
      </article>
    </Layout>
  );
}
