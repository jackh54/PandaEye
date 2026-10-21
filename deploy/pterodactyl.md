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
[OPERATIONS.md "Configuration reference"](../docs/OPERATIONS.md#configuration-reference).

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
reaches PandaEye through the node's address, not loopback. So the gateway needs TLS either way:

- **On the gateway**: upload a certificate valid for the hostname the plugins use and set `PE_TLS_CERT`
  / `PE_TLS_KEY`. Renewals are manual (upload, restart).
- **On the node**: a reverse proxy on the host (nginx, Caddy, HAProxy) terminating TLS and forwarding
  WebSocket upgrades to the allocation; set `PE_TRUSTED_PROXIES` to the proxy's address as seen from the
  container (often the `pterodactyl0` bridge gateway, e.g. `172.18.0.1/32`). Proxy settings: see
  [OPERATIONS.md "TLS termination"](../docs/OPERATIONS.md#tls-termination).

Plugins then use `cloud.endpoint: "wss://<hostname>:<port>/v1/connect"`.

## Durability and resources

- `PE_NATS_SYNC=default` lets the embedded JetStream fsync every 2 minutes: a node crash (not a clean
  stop) can lose up to 2 minutes of events that were already acknowledged to plugins. `always` fsyncs
  every write and costs IOPS; prefer it on SSD nodes.
- Disk: embedded JetStream holds up to `PE_EVENTS_MAX_BYTES` (10 GiB default) of events plus 7 days of
  signals and 30 days of verdict records; size the server's disk limit accordingly or lower
  `PE_EVENTS_MAX_BYTES`.
- sqlite allows one writer; `all` is for small installations. Move to the split deployment when the
  `/status` probe p95 approaches the SLO ([../docs/SLO.md](../docs/SLO.md)).

## Backups

Stop the server (clean shutdown), then copy `/home/container/data/` (Pterodactyl backups do this). A
live copy of `pandaeye.db` must use sqlite's backup API (`sqlite3 data/pandaeye.db ".backup
backup.db"`), not a file copy. Details: [OPERATIONS.md "Backups and disaster recovery"](../docs/OPERATIONS.md#backups-and-disaster-recovery).
