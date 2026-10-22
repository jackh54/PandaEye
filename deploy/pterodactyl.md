# Running `pandaeye all` on Pterodactyl

`pandaeye all` is one static binary with embedded NATS and sqlite, so it runs in any generic Linux egg
(for example a Debian "yolks" image). This suits small installations: one region, a handful of game
servers. For larger installations use the split deployment in [../docs/OPERATIONS.md](../docs/OPERATIONS.md).

## Server setup

| Item | Value |
|---|---|
| Image | any glibc or musl Linux image; the binary is static (`CGO_ENABLED=0`). Pick the build matching the node: `linux/amd64` or `linux/arm64` |
| Files | upload `pandaeye` to `/home/container/` (SFTP) and make it executable (`chmod 755 pandaeye` over SFTP, or in the egg's install script) |
| Startup command | `./pandaeye all` |
| Stop command | `^C` (SIGINT). PandaEye shuts down gracefully within 10 s: connections receive `CODE_SHUTTING_DOWN`, leases are released, stores are flushed. Do not use a kill-style stop |
| Startup "done" text | a substring of the line the binary logs once the gateway listens (copy it from the console of the first start; logs are JSON) |
| Primary allocation | the gateway port: `PE_GATEWAY_LISTEN=0.0.0.0:{{SERVER_PORT}}` |
| Second allocation | the REST API port, needed only if a dashboard or API clients connect: set `PE_API_LISTEN=0.0.0.0:<second port>` (Pterodactyl does not expose additional allocations as variables) |
| Data directory | `/home/container/data` (created by the defaults `PE_NATS_DIR=./data/nats` and `PE_DB_DSN=./data/pandaeye.db`) |

## Variables

Set these as egg variables (environment). Everything not listed keeps the defaults of
[OPERATIONS.md "Configuration reference"](../docs/OPERATIONS.md#3-configuration-reference).

| Variable | Value |
|---|---|
| `PE_GATEWAY_LISTEN` | `0.0.0.0:{{SERVER_PORT}}` |
| `PE_API_LISTEN` | `0.0.0.0:<second allocation>`, or `127.0.0.1:8081` when unused |
| `PE_METRICS_LISTEN` | keep `127.0.0.1:9090` (only reachable inside the container); never bind it to an allocation (it serves pprof) |
| `PE_STATIC_LICENSES` or `PE_LICENSE_MODE`/`PE_LICENSE_URL` | your licences |
| `PE_REGION` | e.g. `eu-central`, shown on `/status` |
| `PE_PUBLIC_URL` | dashboard base URL, if you run one |
| `PE_TLS_CERT`, `PE_TLS_KEY` | paths of a certificate and key you upload (e.g. `tls/fullchain.pem`, `tls/privkey.pem`) when the gateway terminates TLS itself |
| `PE_NATS_SYNC` | `default` (fsync every 2 min) or `always` (see below) |
| `PE_WORKER_MAX_MEMORY` | below the container memory limit, e.g. 50% of it |
| `GOMEMLIMIT` | Go runtime soft memory limit, e.g. 80% of the container limit (`1600MiB` for 2 GiB) |

In `all` mode `PE_OUTBOUND_KEY` and `PE_LICENSE_PEPPER` may stay empty: the binary generates them and
persists them in the database. They are part of the data you must back up (losing the pepper orphans
every licence-to-tenant mapping; losing the outbound key makes stored Discord webhooks unreadable).

## TLS

Plugins refuse `ws://` to anything but loopback, and a game server in another Pterodactyl container
reaches PandaEye through the node's address, not loopback. Use TLS:

- **On the gateway**: upload a certificate valid for the hostname the plugins use and set `PE_TLS_CERT`
  / `PE_TLS_KEY`. Renewals are manual (upload, restart).
- **On the node**: a reverse proxy on the host (nginx, Caddy, HAProxy) terminating TLS and forwarding
  WebSocket upgrades to the allocation; set `PE_TRUSTED_PROXIES` to the proxy's address as seen from the
  container (often the `pterodactyl0` bridge gateway, e.g. `172.18.0.1/32`). Proxy settings: see
  [OPERATIONS.md "TLS termination"](../docs/OPERATIONS.md#7-tls-termination).

Plugins then use `cloud.endpoint: "wss://<hostname>:<port>/v1/connect"`.

Only when every game server runs on the same node, or on a private network you control, may plugins
use `ws://<node address>:<port>/v1/connect` with `cloud.allow-insecure: true` in their `config.yml`
(they log a warning at enable). Never use it across the internet: licence keys and server secrets travel
in the Hello.

## Durability and resources

- `PE_NATS_SYNC=default` lets the embedded JetStream fsync every 2 minutes: a node crash (not a clean
  stop) can lose up to 2 minutes of events that were already acknowledged to plugins. `always` fsyncs
  every write and costs IOPS; prefer it on SSD nodes.
- Disk: embedded JetStream holds up to `PE_EVENTS_MAX_BYTES` (10 GiB default) of events plus 7 days of
  signals and 30 days of verdict records; size the server's disk limit accordingly or lower
  `PE_EVENTS_MAX_BYTES`.
- Building on the server (an egg that runs `go build` at start): Pterodactyl's `/tmp` is a small tmpfs and
  the build fails with "no space left on device". Set `GOTMPDIR` (and `TMPDIR`) to a folder under
  `/home/container`. Set `GOMEMLIMIT` a little below the container's memory limit.
- sqlite allows one writer; `all` is for small installations. Move to the split deployment when the
  `/status` probe p95 approaches the SLO ([../docs/SLO.md](../docs/SLO.md)).

## Backups

The cloud writes an online copy of the database every 6 h to `data/backups/pandaeye-<UTC>.db` and
keeps the newest 8 (`PE_DB_BACKUP_INTERVAL`, `PE_DB_BACKUP_KEEP`). Those copies live on the same disk:
schedule a Pterodactyl backup (it may run while the server is up; exclude `data/pandaeye.db*` and
`data/nats/` from it and rely on `data/backups/`) or copy `data/backups/` off the node. To restore, stop
the server, replace `data/pandaeye.db` with a backup copy (delete `pandaeye.db-wal` and `-shm`) and start. Details: [OPERATIONS.md "Backups and disaster recovery"](../docs/OPERATIONS.md#83-backups-and-disaster-recovery).

## Production checklist (single node behind Cloudflare)

This is the setup of the hosted service (`eye.pandascript.dev` for plugins, `eye-api.pandascript.dev`
for the dashboard). Each item says how to check it.

| Item | How | Check |
|---|---|---|
| Origin only reachable through Cloudflare | `PE_TRUSTED_PROXIES_PRESET=cloudflare`, `PE_REQUIRE_TRUSTED_PROXY=true` | a direct request to the node's port gets 403 `untrusted_peer` |
| TLS end to end | Cloudflare SSL mode **Full (strict)** with a Cloudflare Origin CA certificate (valid 15 years, covers both hostnames) uploaded to `tls/`, `PE_TLS_CERT=tls/origin.pem`, `PE_TLS_KEY=tls/origin.key`; point Cloudflare's origin port (Origin Rule) at the TLS listeners. Flexible mode leaves the Cloudflare-to-node leg in plain text, and licence keys and server secrets travel in the Hello | `/pandaeye status` connected; `curl -vk https://<node>:<port>/status` completes the TLS handshake (the 403 is expected) |
| Secrets generated once, kept off the repo | the start script generates `PE_OUTBOUND_KEY`, `PE_LICENSE_PEPPER`, `PE_SERVICE_TOKEN`, `PE_ASSERTION_KEY` into `pandaeye.env` (mode 600) when missing | `pandaeye.env` exists, never committed; values only ever pasted into the dashboard's Worker secrets |
| Rotation | service token without downtime through `PE_SERVICE_TOKEN_NEXT`; assertion and outbound keys in a quiet period; **never** the pepper ([OPERATIONS 8.6](../docs/OPERATIONS.md#86-rotating-keys)). Rotate any value that was ever shown in a chat, log or ticket | dashboard still signs in after the switch |
| Licensing | `PE_LICENSE_MODE=pandascript`, `PE_LICENSE_URL=<licence worker>`, `PE_LICENSE_PRODUCT=pandaeye` (the product must exist in the licence system). Keys in `PE_STATIC_LICENSES` stay valid and are checked first | a new key connects; a revoked one is refused within 10 minutes (valid answers are cached that long) |
| Backups | built in: `data/backups/` every 6 h, 8 kept (see Backups above); a Pterodactyl backup schedule copies them off the node | `online backup written` in the console every 6 h |
| Monitoring | an external uptime check on `https://<api host>/status` every 1-5 min: alert on non-200, `connected_servers` dropping to 0, or `slo.met` false | the check is green |
| Memory and disk | `GOMEMLIMIT` and `PE_WORKER_MAX_MEMORY` below the container limit; disk for `PE_EVENTS_MAX_BYTES` plus backups | container stays below its limits for a week |
| Capacity | measured: one Folia server with 100-250 players used about 25% of 2 CPUs. Extrapolated, not measured: about 3 servers of that size per 2-CPU node with headroom; beyond that add CPU or move to the split deployment | `/status` `probe_rtt.p95_us` far below the SLO |
| Updates | push to the deploy branch, restart the server (it rebuilds and restarts in about 10 s; plugins reconnect and replay their spool) | `/status` `version` shows the new revision |
