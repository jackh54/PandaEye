# PandaEye REST API (wire shapes)

Companion to [ARCHITECTURE.md](ARCHITECTURE.md) section 5.8, which defines authentication, roles, scopes
and semantics. This file pins the JSON shapes the dashboard consumes; the Go API and the dashboard are
tested against it.

## Conventions

- JSON with snake_case keys. Errors: `{"error": "<message>", "code": "<machine code>"}` with a matching
  HTTP status.
- Times are RFC 3339 strings in UTC (`created_at`, `last_seen_at`, ...). Microsecond event times inside
  clips (`time_us`, `start_us`, `end_us`) are integers (or decimal strings, as protojson emits 64-bit ints).
- `action` values are lowercase strings: `alert`, `mitigate`, `kick`, `ban`, `unmitigate`, `unban`.
  `category` values are the enum names without the prefix: `AUTO_CLICKER`, `HEURISTICS`, `INVALID_PROTOCOL`,
  `MOVEMENT`, `TICK_SPEED`, `INVENTORY`, `WORLD_INTERACTION`, `LATENCY_ABUSE`.
- Lists return `{<plural>: [...], "next_cursor": "<opaque or empty>"}`; request with `?limit=` (<= 200) and
  `?cursor=`.
- UUIDs are canonical lowercase strings with dashes.
- Dashboard requests carry `Authorization: Bearer <service token>`, `X-PandaEye-Client-IP`, and (when a user
  is signed in) `X-PandaEye-Assertion`. The assertion is `base64url(json) + "." + base64url(HMAC-SHA256(key,
  base64url(json)))`, base64url without padding, key = UTF-8 bytes of `PE_ASSERTION_KEY`, json =
  `{tenant, user_id, username, role, method, path, exp}` where `path` is the URL path without the query and
  `exp` is unix seconds (<= now + 60). `auth/license`, `public/appeals` and `/status` are called without an
  assertion; `public/appeals` still carries `X-PandaEye-Client-IP`, which the API uses for its rate limit
  when the service token is valid.

## Endpoints

| Endpoint | Request | Response |
|---|---|---|
| `POST /api/v1/auth/license` | `{license_key}` | `{tenant_id, slug, created}` |
| `POST /api/v1/auth/rebind` | `{license_key, revoke_others?}` (owner) | `{tenant_id, slug, revoked}`; an invalid licence answers 400 |
| `GET /api/v1/tenant` | | `{id, name, slug, plan, leniency, autoban, autokick, mitigations, enforcement, reputation_opt_in, discord_webhook (masked or ""), discord_min_action ("alert".."ban"), over_limit_since (time or null)}` |
| `PATCH /api/v1/tenant` | changed fields only; `discord_webhook: ""` removes it | the tenant object |
| `GET /api/v1/servers` | | `{servers: [{id, name, network_id, platform, platform_version, plugin_version, minecraft_version, online, online_players, last_seen_at, shadow_mode}], next_cursor}` |
| `GET /api/v1/verdicts` | `action, status, shadow (true/false), player, server, since, until (RFC 3339), limit, cursor` | `{verdicts: [verdict], next_cursor}` |
| `GET /api/v1/verdicts/{id}` | | `verdict` + `evidence` |
| `POST /api/v1/verdicts/{id}/confirm` | `{note?}` | the verdict |
| `POST /api/v1/verdicts/{id}/overturn` | `{note?}` | the verdict (status `overturned`) |
| `GET /api/v1/clips/{id}` | | ClipData as protojson: `{player, player_name?, start_us, end_us, events: [{player, time_us, tick, <oneof field name>: {...}}]}` |
| `GET /api/v1/players` | `name, limit, cursor` | `{players: [{uuid, last_name, first_seen_at, last_seen_at, last_server_id}], next_cursor}` |
| `GET /api/v1/players/{uuid}` | | `{uuid, last_name, first_seen_at, last_seen_at, last_server_id, names: [{name, seen_at}], brand?, client_version?, verdicts: [verdict], bans: [ban], reputation?: {risk, networks_flagged, confirmed_bans}}` |
| `DELETE /api/v1/players/{uuid}` | owner/admin assertion, or an API key with `erase` | `{counts: {<table>: n}}`; 503 if a step failed (retry finishes it) |
| `POST /api/v1/players/{uuid}/watch` | `{seconds}` (60..300) | `{ok: true}` |
| `GET /api/v1/bans` | `active=true` | `{bans: [ban]}` |
| `GET /api/v1/appeals` | `status=open` | `{appeals: [{id, verdict_id, player_uuid, player_name?, message, contact, status, created_at, decided_at, decided_by, decision_note}]}` |
| `POST /api/v1/appeals/{id}/decide` | `{decision: "accept"|"deny", note}` | the appeal |
| `POST /api/v1/public/appeals` | `{verdict_id, appeal_code, message, contact}` | 202 `{ok: true}` (same response for unknown ids or wrong codes) |
| `GET /api/v1/stats/overview` | | `{last_24h: {by_action: {alert: n, ...}, by_category: {HEURISTICS: n, ...}, shadow: n}, last_7d: {...}}` |
| `GET /api/v1/stats/latency` | | `{last_1h: {p50_us, p95_us, p99_us, count}, last_24h: {...}, slo: {target_p95_ms, met}}` |
| `GET /api/v1/usage` | | `{plan, free_tier_limit, over_limit_since, days: [{day: "YYYY-MM-DD", peak_players}]}` |
| `GET /api/v1/apikeys` | | `{api_keys: [{id, name, scopes, created_at, last_used_at, revoked_at}]}` |
| `POST /api/v1/apikeys` | `{name, scopes}` | 201 `{id, name, scopes, created_at, key: "pe_<id>_<secret>"}` (key shown once) |
| `DELETE /api/v1/apikeys/{id}` | | `{ok: true}` |
| `GET /api/v1/checks` | | `{checks: [{id, enabled, overrides: {<name>: number}}]}` |
| `PUT /api/v1/checks` | `{checks: [...]}` (full list; unknown ids are kept unchanged) | same shape |
| `GET /api/v1/audit` | `limit, cursor` | `{entries: [{id, principal, action, target, detail, created_at}], next_cursor}` |
| `GET /api/v1/export/labels` | `since, limit, cursor` | NDJSON, one label per line, oldest first, erased players excluded (`{action, category, checks, created_at, evidence, ...}`); a final `{"next_cursor": "..."}` line means more pages |
| `GET /status` | | `{region, version, uptime_s, connected_servers, generated_at, probe_rtt: {p50_us, p95_us, p99_us, samples, window_s}, slo: {target_p95_ms, target_p99_ms, p95_met, p99_met, met}}` (`met` = both; same document the gateway serves, cached 30 s) |
| `GET /healthz` | | 200 `{ok: true}`; 503 once shutdown began |

`verdict` = `{id, server_id, server_name?, network_id, player_uuid, player_name, session_id, action, category,
score, confidence, reason, checks: [string], shadow, status, supersedes, created_at, delivered_at, acked_at,
executed, ack_note, clip_ids: [string], mitigations: [string], strength}`.

`evidence` = `{score, confidence, leniency, thresholds: {alert, mitigate, kick, ban}, families: [{family,
contribution, cap}], checks: [{check, contribution, cap, confidence, count}], signals: [{check, category,
points, confidence, info, data, time, clip_id, local}], session: {session_id, server_id, started_at,
hot_loaded, exempt}}`.

`ban` = `{verdict_id, server_id, player_uuid, player_name?, active, reason, created_at, overturned_at,
overturned_by, overturn_note, unban_verdict_id, unban_state: "none"|"pending"|"done"}`.

Value conventions: `platform` and `mitigations` are lower case without the enum prefix (`paper`, `slow`);
an unspecified category is `""`; `latency.slo.met` is null without probe data; in clips, `time_us` values
are strings (protojson int64) while `start_us`/`end_us` are integers. API keys are accepted only where a
scope is listed (reads need `read`); audit, rebind, API key management, appeal decisions and watch are
dashboard-only.
