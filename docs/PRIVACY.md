# PandaEye privacy notice and data processing description

This document has four parts: **A**, a privacy notice for server owners and players; **B**, a description
of the processing for data processing agreements; **C**, a short notice server owners can add to their
rules; **D**, notes for maintainers, which are not part of the published notice. Text in `[brackets]` is
a placeholder the operator of the PandaEye service fills in before publishing. Everything else describes
what PandaEye v1 does as specified in [ARCHITECTURE.md](ARCHITECTURE.md).

---

## Part A: Privacy notice

*Version [version], valid from [date].*

### A1. Who is responsible

PandaEye is an anti-cheat service for Minecraft Java Edition servers, operated by [operator legal name,
address] ("PandaEye", "we"). Server owners ("customers") install the PandaEye plugin on their servers and
choose its settings.

| Processing | Controller | PandaEye's role |
|---|---|---|
| Detecting cheating on a server, evidence, kicks, bans, mitigations, appeals for that server | the server owner | **processor**, acting on the server owner's instructions and settings |
| The reputation network (only servers that opt in) | PandaEye | **controller** |
| Training detection models on data from several customers | PandaEye | **controller** |
| Operating and securing the service (abuse prevention, capacity, licence checks, aggregate usage metering) | PandaEye | **controller** |

Players: questions about a decision on a particular server go to that server's owner first; you can
always contact us as well ([A14](#a14-contact)).

### A2. What data is processed

**About players**

| Category | Data |
|---|---|
| Identifiers | Minecraft UUID, player name and the names seen with that UUID on a server, random session identifiers generated per join |
| Client information | Minecraft version and protocol number, client brand string (first value, at most 64 characters), whether the player joined through Geyser/Floodgate (Bedrock), whether the server grants the player an anti-cheat bypass |
| Gameplay telemetry | movement (position, rotation, on-ground state, sprinting, sneaking, flying, gliding and similar states), attacks (target, positions of the target over the last moments, distance), arm swings, block placements and digs (coordinates, face, click position), inventory clicks (window, slot, click type, container type), item use, knockback received, teleports, game mode, potion effects and movement attributes, held hotbar slot, the environment around the player (for example water, ice, cobweb), connection round-trip times, results of the plugin's local checks |
| Derived data | detection signals, suspicion scores, verdicts (alert, mitigate, kick, ban and their reversals) with evidence (contributing checks, confidence, top signals), replay clips (the player's last 25 seconds of telemetry; a clip also contains the positions of other players the player attacked), 30-second statistical feature windows used for model training |
| Enforcement and appeals | bans executed on a server, appeals (the verdict, the appeal code, your message of up to 2 000 characters, a contact detail of up to 200 characters you choose to give), decisions and decision notes, labels ("cheat" or "legit") set by staff |
| Reputation network | only on servers that opted in: records that a player received a staff-confirmed ban there (see A6) |

How much telemetry is sent depends on what the player does: every movement packet is sent while the
player is in combat, takes knockback, builds or digs, uses an inventory, was recently flagged, or is
being watched by staff; otherwise movement is sampled twice per second.

**About customers and their staff**: server names, network identifiers, server software versions, online
player counts (also aggregated per day for billing), licence keys (stored only as a keyed hash), dashboard
accounts (username, password hash, role), dashboard sessions, audit logs of actions taken, API keys
(stored hashed), Discord webhook addresses (stored encrypted).

**Technical data**: the IP address of game servers connecting to the service (for connection rate limits
and abuse prevention). When someone uses the dashboard or the public appeal form, their IP address is
used for rate limiting, and Cloudflare Turnstile checks the setup and appeal forms for bots.

**What PandaEye does not collect**

- Players' IP addresses: the plugin does not send them and the PandaEye data model has no field holding a
  player's IP address.
- Chat messages, private messages, commands, or the text of signs and books.
- Account email addresses, real names, payment details of players, skins, mod lists (only the client brand
  string), device or hardware identifiers, or location other than in-game coordinates.
- Special categories of data (for example health or political opinions).

### A3. Purposes

1. Detecting cheating and enforcing the server's rules: alerts to staff, mitigations that reduce a
   suspected cheater's advantage during a session, kicks and bans, all according to the server owner's
   settings.
2. Evidence, human review and appeals, so that every kick or ban can be checked and reversed.
3. The reputation network, for servers that opt in (A6).
4. Improving detection: measuring false positives and training models (A7).
5. Operating the service: security, abuse prevention, licence checks, capacity, latency measurements
   (these use server-level test messages without player data) and billing by peak player count.

PandaEye does not use the data for advertising or marketing profiles and does not sell it.

### A4. Legal bases

| Purpose | Controller | Legal basis (GDPR) |
|---|---|---|
| Detection and enforcement on a server; appeals | server owner | legitimate interests (Art. 6(1)(f)); a server owner may instead rely on its terms of service with the player (Art. 6(1)(b)) |
| Reputation network | PandaEye | legitimate interests (Art. 6(1)(f)) |
| Model training across customers | PandaEye | legitimate interests (Art. 6(1)(f)) |
| Operating and securing the service | PandaEye | legitimate interests (Art. 6(1)(f)) |

**Legitimate interest assessment (summary)**

- *Interest*: cheating ruins the game for other players, distorts competition and server economies, and
  costs server owners staff time. Players and server owners have a shared interest in fair play.
- *Necessity*: cheats are recognisable only from how a player moves and interacts with the game, so the
  analysis needs gameplay telemetry. PandaEye limits it: no IP addresses, no chat, reduced sampling when
  nothing relevant happens, raw telemetry kept for 24 hours, every other category for fixed periods (A8).
- *Balancing*: players expect anti-cheat protection on multiplayer servers (server owners tell them, see
  Part C). The data describes in-game behaviour, not private life. Safeguards: new servers start in an
  observe-only mode; automatic kicks and bans are off until the server owner enables them; every decision
  carries evidence and can be reviewed and reversed by staff; banned players get an appeal code; a player
  marked legitimate by staff cannot be kicked or banned again for the same period; the reputation network
  is opt-in, needs staff confirmation and agreement of at least two networks, and never acts on its own;
  children's interests are given extra weight (A11).
- *Result*: legitimate interests are an appropriate basis with these safeguards. You can object (A10).

### A5. Automated decisions and your safeguards

PandaEye calculates suspicion scores automatically. Whether a score leads to an automatic kick or ban is
the server owner's decision: cloud enforcement, automatic kicks and automatic bans are all off by default,
and the plugin starts in an observe-only (shadow) mode. Where a server owner enables automatic kicks or
bans, the decision is made without human involvement at the time it is made. Whether or not such a
decision has legal or similarly significant effects for you (GDPR Art. 22), PandaEye provides these
safeguards:

- **Appeal route and code**: the default ban message contains the server's appeal link and a personal
  appeal code; the appeal form accepts appeals for executed bans.
- **Human review**: server staff can confirm or overturn every verdict in the dashboard and decide every
  appeal. Overturning a ban sends an unban to the server that executed it and removes any reputation
  record created from it.
- **Evidence**: every kick and ban records which checks fired, how confident they were and, for bans, a
  replay clip.
- **Strict conditions**: an automatic ban requires a high score, high confidence, a saved replay clip and
  either several independent detection families or one check that is certain on its own. Gameplay data
  that is more than 10 minutes old when it is judged (for example after a long outage) can raise an alert
  but never cause a mitigation, kick or ban.
- **Limits**: by default each server stops executing automatic punishments after 3 bans per hour or 5
  kicks per 10 minutes until staff reset it.

Mitigations (for example cancelling some of a suspected cheater's hits) are automated, apply only during
the current session and end when the player leaves.

### A6. Reputation network (opt-in)

- Only servers whose owners opt in contribute to and receive from the network.
- A contribution is created only when a ban was executed on a participating server, was not issued in
  observe-only mode, was confirmed as cheating by that server's staff, includes at least one check computed
  by the PandaEye cloud, and the participating customer is established (a paid plan, or older than 30 days
  with at least 50 distinct players).
- A player is shown as a risk only if at least two participating networks have such contributions; each
  network's weight is capped.
- What a participating server sees: when a player joins it, its staff may see a risk value, the number of
  other networks with confirmed cheating verdicts and the number of confirmed bans. Not shown: which
  servers, the evidence or the reasons. The information is only shown to staff; it never changes the
  player's score and never triggers an action.
- Lookups happen only for players who actually joined that server in the last 24 hours, within a budget.
- Overturning the ban, or erasing the player's data on the contributing server, deletes the contribution.
  You can object to or request erasure of reputation data directly with us ([A14](#a14-contact)).

### A7. Model training

PandaEye trains detection models on 30-second feature windows (numerical summaries of gameplay, keyed by
UUID; no names) together with labels from staff decisions (confirmed cheating, overturned verdicts,
manual labels). Only data from established customers is used, and erased players are excluded. A new
model is used only after it passed a test showing it does not produce more false positives than the
existing checks on players it was not trained on, and it starts with no effect on scores.
[Customer opt-out from model training: describe your policy here.]

### A8. Retention

**In the PandaEye service**

| Data | Kept for |
|---|---|
| Raw gameplay telemetry (event stream) | 24 hours [default; state the operated value if changed] |
| Detection signals (internal stream) | 7 days |
| Verdict records (internal stream) | 30 days |
| Verdicts and evidence | 180 days; for an active ban, as long as the ban is active |
| Replay clips | 90 days, longer while needed for an active ban, an open appeal or a label |
| Ban records | while active; afterwards [period] |
| Appeals | 1 year; the contact detail is deleted 30 days after the decision |
| Labels | 2 years |
| Analytics copies (if enabled): signals / verdicts / sessions / feature windows | 30 days / 90 days / 90 days / 180 days |
| Player records (UUID, latest name, first and last seen, last server) and name history | [period] |
| Reputation contributions | until the ban is overturned or the data erased [maximum period] |
| Records of completed erasures (UUID, dates, counts) | [period], to discard data still in transit and document the erasure |
| Daily peak player counts (aggregate, no player data) | 2 years |
| Latency statistics (no player data) | 30 days |
| Audit log | 1 year |
| Backups | [backup retention period] |

**On the game server** (stored by the plugin on the server owner's machine; the server owner is
responsible): detection logs and tuning statistics 14 days, the list of verdicts executed there 7 days,
unsent data buffered across a plugin restart at most 1 hour.

**In the dashboard** (Cloudflare D1): staff accounts until deleted, login sessions at most 12 hours (2 hours
idle), invitations valid for 48 hours, login rate-limit counters for short windows, the dashboard audit log
[period].

**Discord**: if a server owner connects a Discord webhook, alerts (player name, server, action, score,
confidence, top checks, evidence link; no IP addresses) are posted to the server owner's Discord channel
and kept there under the server owner's control.

### A9. Erasure

- **How**: ask the server owner, who starts the erasure in the dashboard or through the API (owner or admin
  role, or an API key with the `erase` permission). You can also ask us and we will forward or assist.
- **What is deleted**, for that server owner's account: the player record and name history, verdicts,
  bans, appeals, labels, replay clips (including clips in which the player appears as another player's
  target), reputation contributions and the analytics copies. An erasure marker makes the service discard
  any of the player's data still in transit.
- **Exception**: while a ban is active, a minimal record (verdict identifier, UUID, check identifiers, score,
  time) is kept so the ban can be enforced and later lifted. It is deleted when the ban is lifted.
- **Residual copies** expire automatically: raw telemetry within 24 hours, signals within 7 days, verdict
  records within 30 days, backups within [backup retention period]. Logs on the game server are removed by
  the server owner with `/pandaeye forget <uuid>` on each server, or expire within 14 days.
- **Timing**: the erasure starts when it is requested and reports what it deleted; analytics deletion
  completes in the background. All copies in the PandaEye service outside backups are gone within 30 days
  of the request. Requests
  are answered within one month (GDPR Art. 12(3)).
- Each server owner's data is separate: erasure on one server owner's account does not erase data held for
  another server owner.

### A10. Your rights

You have the right to access (Art. 15), rectification (Art. 16), erasure (Art. 17), restriction (Art. 18)
and to object to processing based on legitimate interests (Art. 21), and to lodge a complaint with a
supervisory authority. Data portability (Art. 20) does not apply to processing based on legitimate
interests. For data a server holds about you, contact the server owner, who can view and export your
player summary, verdicts, evidence and clips from the dashboard; PandaEye assists server owners with
requests [within N days]. For the reputation network and model training, contact us.

### A11. Children

Many Minecraft players are under 18. PandaEye does not ask for or infer age and processes the same
gameplay data for every player; it does not rely on consent. The balancing in A4 gives children's
interests extra weight, the safeguards of A5 apply to everyone, and parents or guardians can exercise
rights on behalf of their child. Server owners whose servers are directed at children should check the
rules that apply to them (for example age-appropriate design codes or, in the United States, COPPA).

### A12. Sub-processors and regions

| Sub-processor | Purpose | Data | Location |
|---|---|---|---|
| [hosting provider] | runs the PandaEye cloud (gateway, processing, databases, event stream) | all service data | [US-Central region, e.g. Chicago or Iowa] and [EU region, e.g. Frankfurt or Amsterdam] |
| Cloudflare, Inc. | dashboard hosting (Workers), dashboard database (D1: staff accounts, sessions, dashboard audit log), bot protection (Turnstile) on the setup and appeal forms[, network proxy in front of the gateway] | dashboard data; pages shown to staff, including evidence, pass through Cloudflare's network | global network; D1 primary location [location] |
| [licence verification provider] | checks licence keys | licence keys only, no player data | [location] |

Each game server sends its data to the regional deployment it is configured for. [State whether customers
in the EU are served only from the EU region and how international transfers are safeguarded, e.g.
Standard Contractual Clauses.] Discord is not a sub-processor: webhook posts are sent only to webhooks a
server owner configures.

### A13. Security

Encrypted connections between game servers and the service (the plugin refuses unencrypted connections
except to the same machine) and on the internal message bus; signed internal messages; separate
credentials per internal component; every database query restricted to one customer; licence keys,
server secrets, API keys and passwords stored only as hashes (passwords with PBKDF2 and a secret pepper),
webhooks encrypted; secrets removed from logs; dashboard roles (viewer, staff, admin, owner), invite-only
accounts, rate-limited logins, short sessions and CSRF protection; an audit log of every change; strict
limits on incoming data; bounded retention and erasure markers.

### A14. Contact

[operator legal name], [address], [privacy@example.com]. [Data protection officer, if appointed.] [EU or UK
representative, if required.] Changes to this notice are published here with a new version and date.

---

## Part B: Data processing description (for data processing agreements)

| Item | Description |
|---|---|
| Subject matter | anti-cheat detection and enforcement for the customer's Minecraft servers |
| Duration | the term of the agreement, then the retention periods of A8 |
| Nature | collection by the plugin, encrypted transmission, storage, automated analysis and scoring, evidence and replay, delivery of verdicts to the customer's servers, human review tools, deletion |
| Purposes | A3 items 1 and 2 (processor); items 3 to 5 are PandaEye's own purposes as controller |
| Data subjects | players on the customer's servers (may include children), the customer's staff (dashboard users), people who submit appeals |
| Data categories | A2 |
| Special categories | none |
| Customer instructions | given through settings: enforcement on/off, automatic bans, kicks and mitigations, leniency, per-check configuration, reputation network opt-in, Discord webhook, erasure requests, verdict confirmation and overturning |
| Retention | A8 |
| Sub-processors | A12 |
| Assistance | erasure via dashboard/API (A9); access via dashboard and API exports; [breach notification within N hours] |
| Separation | every stored record carries the customer identifier and every query filters by it; identifiers of another customer are answered as "not found" |

Technical and organisational measures:

- **Transport**: TLS for plugin connections (`wss://`), the dashboard and the API; TLS and per-component
  credentials with least-privilege permissions on the internal message bus; mutual TLS between bus nodes;
  HMAC signatures on messages that reach game servers.
- **Access control**: role-based dashboard (viewer, staff, admin, owner); owner/admin required for
  enforcement settings, API keys and erasure; single-use, expiring, hashed invitations; session cookies
  `HttpOnly`, `Secure`, `SameSite=Lax`, 12 h absolute and 2 h idle lifetime, rotated on login; CSRF tokens;
  login rate limits per IP and per username; API keys with scopes, stored hashed.
- **Secrets**: licence keys stored as HMAC, server secrets and API keys as hashes, passwords as
  PBKDF2-SHA256 (100 000 iterations) over a peppered HMAC, Discord webhooks encrypted with AES-GCM; secrets
  are redacted from logs at every log level (tested).
- **Application security**: hostile-input limits on every message (sizes, decompression, field limits),
  output escaping and a strict Content Security Policy in the dashboard, validation of webhook addresses
  and refusal of internal network targets, an audit log of every change.
- **Minimisation and retention**: no player IP addresses, no chat, reduced sampling, fixed retention
  (A8), erasure with tombstones (A9).
- **Safety of enforcement**: observe-only default, per-server limits on automatic punishments, evidence on
  every kick and ban, reversible decisions.
- **Availability**: replicated event stream, backups [backup policy], graceful degradation (game servers
  keep their local checks and buffer data while the service is unreachable).

---

## Part C: Template notice for server rules

Server owners can add this text to their rules or website and adapt the bracketed parts.

> **Anti-cheat.** This server uses PandaEye, an anti-cheat service. While you play, PandaEye analyses how
> you move and interact with the game (movement, combat, blocks, inventory and connection latency)
> together with your Minecraft UUID, name and client version, to detect cheating. It does not collect
> your IP address or your chat messages. [Server name] decides how detections are handled; [our staff
> review detections / kicks and bans may be applied automatically]. If you are banned, the ban message
> contains an appeal link and a code: [appeal URL]. Raw gameplay data is kept for 24 hours; decisions
> and evidence for up to 180 days (longer while a ban is active); appeals for 1 year. [Server name] is
> responsible for this processing and PandaEye processes the data on our behalf.
> [We take part in the PandaEye reputation network: if two or more other participating networks have
> confirmed cheating bans for your account, our staff may see a warning when you join. It is never used
> to punish you automatically.] For access, deletion or objection, contact [contact]. More details:
> [link to the PandaEye privacy notice].

---

## Part D: Notes for maintainers (not for publication)

Points where the spec does not support a statement the notice needs. Resolve them before publishing.

1. **Retention not defined** in ARCHITECTURE 5.7 for: `players`, `player_names`, `servers`, inactive
   `bans` rows, `reputation_contrib` beyond overturn/erasure, the `erasures` table, the dashboard D1
   `audit_log`, `sessions` and `login_attempts` clean-up, the "bans executed here" part of the plugin's
   `executed.json`, and backups. A8 uses placeholders for them; storage limitation (Art. 5(1)(e)) needs
   actual periods.
2. **IP hash**: `PlayerJoin.ip_hash` exists in `wire.proto` ("optional, off by default", HMAC of the IP)
   but no plugin setting enables it, while ARCHITECTURE 9 says no IPs are collected. A2 states that no IP
   field exists in the data model; remove the protocol field or document when it would be used.
3. **Model-training opt-out**: 5.10 uses labels "only from trusted tenants" but defines no opt-in or
   opt-out for customers; A7 has a placeholder.
4. **Reputation erasure as controller**: erasure (5.13) is per tenant. There is no operator procedure for a
   player who asks PandaEye directly to erase all reputation data across tenants.
5. **Controllership in the reputation network**: the spec names PandaEye the controller; whether
   participating customers are independent or joint controllers should be settled in the customer terms.
6. **Appeal rate limiting**: the API limits appeals per client IP (5/h); whether and how long that
   counter is stored is not specified. A2 states only that the IP is used for rate limiting.
7. **Configurable retention**: `PE_EVENTS_MAX_AGE` can change the 24-hour raw-telemetry period; the
   notice must state the operated value.
8. **Regions**: OPERATIONS 2.3 records that tenant-to-region binding and EU data residency are not
   specified; A12 has placeholders.
9. **Evidence through Cloudflare**: dashboard pages, including evidence and replay data, pass through
   Cloudflare's network; the transfer assessment must cover it.
10. **Exempt (bypass) status and Bedrock flag** are processed for all players; both are covered in A2.
