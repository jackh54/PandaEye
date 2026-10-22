# PandaEye operations

How to deploy, configure, run, scale, recover and monitor PandaEye, and how to roll the plugin out on a
production game server. Companion documents: [ARCHITECTURE](ARCHITECTURE.md) (the contract),
[PROTOCOL](PROTOCOL.md), [SLO](SLO.md), [PRIVACY](PRIVACY.md). Deployment files are in [`../deploy`](../deploy).
Behaviour the spec leaves open is marked **Open** and collected in section 11.

## 1. Components

| Role | Command | Listens | State | Instances |
|---|---|---|---|---|
| gateway | `pandaeye gateway` | `PE_GATEWAY_LISTEN` (`/v1/connect`, `/healthz`, `/status`) | none (connections; KV) | >= 2 behind a load balancer |
| worker | `pandaeye worker` | metrics only | partition leases `events.<NN>`, per-player state, clips | any number; partitions are shared by leases |
| verdict | `pandaeye verdict` | metrics only | leases `signals.<NN>` and `writer` (store writer) | >= 2 (standby takes over leases) |
| api | `pandaeye api` | `PE_API_LISTEN` (REST, `/status`) | leased loops (metering, janitor) | >= 2 |
| migrate | `pandaeye migrate` | - | database schema | once per release |
| replay | `pandaeye replay --tenant --player --since --until` | - | read-only | on demand |
| all | `pandaeye all` | gateway + API ports | embedded NATS (`PE_NATS_DIR`), sqlite | exactly 1 |

Every role serves `/metrics` and pprof on `PE_METRICS_LISTEN` (default `127.0.0.1:9090`). Never expose that
port publicly. The dashboard is a Cloudflare Worker that calls the API over HTTPS (`PANDAEYE_API_URL`).

## 2. Deployment topologies

### 2.1 Single node: `pandaeye all`

One process with an in-process NATS server (`DontListen`: no NATS port), sqlite (WAL, one writer),
and every role. `PE_OUTBOUND_KEY` and `PE_LICENSE_PEPPER` may be left empty: they are generated and
persisted in the database (which makes the database backup also a secrets backup). Use it for
development, evaluation and small installations (one region, a few servers). Files:
[`deploy/systemd/pandaeye.service`](../deploy/systemd/pandaeye.service),
[`deploy/pterodactyl.md`](../deploy/pterodactyl.md), or the image with `CMD ["all"]`.

Limits: sqlite and the single process cap throughput; there is no redundancy; `PE_NATS_SYNC=default`
fsyncs JetStream every 2 min, so a host crash (not a clean stop) can lose up to 2 min of events that
plugins already saw acknowledged. Move to the split topology when the probe p95 on `/status` approaches the
SLO or when you need zero-downtime deploys.

### 2.2 Production: split services

```mermaid
flowchart LR
    subgraph Game servers
        P1[Paper/Folia plugin]
        P2[Velocity plugin]
    end
    LB[TLS terminator / L4 or L7 load balancer]
    subgraph Region
        G1[gateway]
        G2[gateway]
        subgraph NATS cluster R3
            N1[(nats-1)]
            N2[(nats-2)]
            N3[(nats-3)]
        end
        W[workers x N]
        V[verdict x 2]
        A[api x 2]
        PG[(Postgres)]
        CH[(ClickHouse, optional)]
    end
    D[Dashboard Worker + D1]
    P1 & P2 -- wss /v1/connect --> LB --> G1 & G2
    G1 & G2 <--> N1
    W <--> N2
    V <--> N3
    A <--> N1
    G1 & G2 & W & V & A --> PG
    W & V & A --> CH
    D -- HTTPS service token + assertion --> A
```

- **NATS**: 3-node cluster, JetStream, `PE_NATS_REPLICAS=3`, TLS, per-role creds (section 5);
  [`deploy/nats/nats-cluster.conf`](../deploy/nats/nats-cluster.conf).
- **Postgres** (`PE_DB_DRIVER=postgres`): the source of truth (tenants, servers, verdicts, bans, appeals,
  labels, clips). Use a managed or replicated instance with point-in-time recovery.
- **ClickHouse** (`PE_CLICKHOUSE_DSN`, optional): telemetry and ML feature windows;
  [`deploy/clickhouse/init.sql`](../deploy/clickhouse/init.sql). Empty DSN disables telemetry.
- **Gateways** are stateless apart from their connections; run at least two so a deploy only moves
  connections. **Workers** share partitions through leases; with `PE_LEASES=auto` (the default outside
  `all`) each takes `ceil(partitions / live members)` and releases extras when another member joins.
  **Verdict** instances share `signals.<NN>` leases the same way; one holds the `writer` lease.
- The compose file [`deploy/docker-compose.yml`](../deploy/docker-compose.yml) is this topology on one
  host (single NATS node, two workers) for staging and testing.

### 2.3 Regions

Place one deployment per region close to the game servers it serves, because the SLO is defined
in-region ([SLO.md](SLO.md)): for example US-Central (Chicago metro or Iowa) for North America and EU
(Frankfurt or Amsterdam) for Europe. Each regional deployment is a complete, independent stack (its own
NATS, Postgres, ClickHouse and API) with its own `PE_REGION`, which `/status` reports. Game servers select
the region with `cloud.endpoint`.

**Open:** the spec defines no cross-region model: how a tenant is bound to a region, whether a tenant may
have servers in two regions, and how one dashboard (`PANDAEYE_API_URL` is a single URL) serves several
regional APIs. Until that exists, treat a tenant as belonging to exactly one region and run one dashboard
deployment per region. This also keeps EU tenants' data in the EU (see [PRIVACY.md](PRIVACY.md)).

## 3. Configuration reference

Configuration is read from `PE_*` environment variables and optionally a YAML file (`--config`). File
keys are the variable names in lower case with dots, nested maps flattened (`PE_NATS_URL` = `nats.url`,
`PE_SERVICE_TOKEN_NEXT` = `service.token.next`). Precedence: environment, then file, then defaults.
Unknown `PE_` variables are ignored; unknown file keys are errors. Every process validates its
configuration for its role at start and refuses to run on an error. The "Used by" column is derived
from the data flows in ARCHITECTURE 5 and the role checks of `cloud/internal/config`; a role ignores keys
it does not use.

| Key | Default | Used by | Guidance |
|---|---|---|---|
| `PE_GATEWAY_LISTEN` | `:8080` | gateway, all | `/v1/connect`, `/healthz`, `/status`. Bind to the private interface behind a TLS terminator |
| `PE_API_LISTEN` | `:8081` | api, all | REST API. Must be reachable over HTTPS by the dashboard Worker |
| `PE_METRICS_LISTEN` | `127.0.0.1:9090` | all roles | `/metrics` and pprof. Keep it on loopback or a private network; empty disables it |
| `PE_TLS_CERT`, `PE_TLS_KEY` | empty | gateway, api, all | set both to serve TLS directly; otherwise terminate TLS in front (section 7) |
| `PE_TRUSTED_PROXIES` | empty | gateway, api | CIDRs (or IPs) of your TLS terminators. `X-Forwarded-For` / `CF-Connecting-IP` are honoured only from them; the client IP is the right-most untrusted address. Wrong values break per-IP rate limits |
| `PE_TRUSTED_PROXIES_PRESET` | empty | gateway, api | `cloudflare` adds Cloudflare's published IPv4/IPv6 ranges (embedded list, refreshed from cloudflare.com at start, falling back to the embedded list) |
| `PE_REQUIRE_TRUSTED_PROXY` | `false` | gateway, api | `true` refuses (close / HTTP 403) every TCP peer outside the trusted proxies, so clients cannot bypass the proxy by connecting to the origin directly. Requires trusted proxies to be set |
| `PE_PUBLIC_URL` | empty | verdict, api | dashboard base URL for evidence links, Discord embeds and appeal links |
| `PE_NATS_URL` | empty | all roles but migrate | external NATS, comma-separated `tls://` URLs. Empty is allowed only in `all` (embedded). Must not contain credentials |
| `PE_NATS_CREDS` | empty | all roles but migrate | creds file of the role's NATS user (section 5). Required for non-loopback NATS unless `PE_NATS_TOKEN` is set |
| `PE_NATS_TOKEN` | empty | all roles but migrate | alternative to creds: a single server-wide token, no per-role isolation. Not for production |
| `PE_NATS_TLS_CA` | empty | all roles but migrate | CA bundle of the NATS server certificate; required for non-loopback NATS unless the URL is `tls://` with a publicly trusted certificate |
| `PE_NATS_DIR` | `./data/nats` | all | embedded JetStream storage |
| `PE_NATS_REPLICAS` | `1` | all roles but migrate | replicas of every stream and KV bucket (1..5); `3` on an R3 cluster, `1` embedded. Every process provisions the streams with its own value, so set it identically everywhere |
| `PE_NATS_SYNC` | `default` | all | embedded file-store sync: `default` (every 2 min) or `always` (every write, more IOPS) |
| `PE_OUTBOUND_KEY` | generated in `all` | gateway, verdict, api (configuration also requires it for worker) | HMAC key of SignedOutbound, >= 32 bytes, identical on every instance. Webhook encryption (and, in the current implementation, appeal codes) is derived from it: see 8.6 before changing it |
| `PE_PARTITIONS` | `16` | gateway, worker, verdict | partition count (1..1024), fixed at first start (KV `PE_META`); a mismatch refuses to start. Choose it for the largest worker count you expect (section 8.2) |
| `PE_DB_DRIVER` | `sqlite` | all roles | `postgres` outside `all` (sqlite is accepted only for `all`, `migrate` and `replay`) |
| `PE_DB_DSN` | `./data/pandaeye.db` | all roles | sqlite path or `postgres://...`; treated as a secret (may carry a password) |
| `PE_DB_BACKUP_INTERVAL`, `PE_DB_BACKUP_KEEP`, `PE_DB_BACKUP_DIR` | `6h`, `8`, `backups/` next to the database | all (sqlite only) | online sqlite backups (`VACUUM INTO`, a consistent copy while running) as `pandaeye-<UTC>.db`, the newest `KEEP` kept; interval `0` disables. Ignored for postgres |
| `PE_CLICKHOUSE_DSN` | empty | worker, verdict, api | `clickhouse://user:pass@host:9000/pandaeye`; empty disables telemetry. The user needs SELECT, INSERT and ALTER DELETE (erasure) |
| `PE_LICENSE_MODE` | `static` | gateway, api | `static` or `pandascript` |
| `PE_LICENSE_URL`, `PE_LICENSE_PRODUCT` | empty, `pandaeye` | gateway, api | pandascript licence worker, plain `https://` URL only |
| `PE_LICENSE_PEPPER` | generated in `all` | gateway, api | HMAC key of licence lookups, >= 32 bytes, required for gateway and api outside `all`, identical everywhere. Never rotate it (8.6) |
| `PE_STATIC_LICENSES` | empty | gateway, api | `key[:maxServers[:plan]],...` (maxServers 0 = unlimited, plan default `free`); also honoured in `pandascript` mode, checked before the licence worker (own servers, partners) |
| `PE_SERVICE_TOKEN`, `PE_SERVICE_TOKEN_NEXT` | empty | api | dashboard transport token(s), >= 32 bytes; empty disables dashboard access. NEXT is accepted too, for rotation |
| `PE_ASSERTION_KEY` | empty | api | HMAC key of dashboard user assertions, >= 32 bytes; required when `PE_SERVICE_TOKEN` is set |
| `PE_EVENTS_MAX_AGE`, `PE_EVENTS_MAX_BYTES` | `24h`, `10GiB` | all roles but migrate (each provisions `PE_EVENTS`) | `PE_EVENTS` retention; bounds residual data after erasure and how far back `replay` reaches. Set identically everywhere, or every restart rewrites the stream's limits |
| `PE_WORKER_MAX_MEMORY` | `2GiB` | worker, all | byte budget of worker state and clip rings (LRU, per tenant first); keep it well below the container limit |
| `PE_REGION` | `local` | gateway, api | shown on `/status` |
| `PE_LOG_LEVEL` | `info` | all roles | `debug`, `info`, `warn`, `error`; secrets are redacted at every level |
| `PE_ML_MODEL`, `PE_ML_POINTS` | empty, `0` | worker (`-tags onnx` builds) | ONNX model path; 0 points = shadow scoring. The static image cannot load models |
| `PE_OWNER_ID` | `<role>-<hostname>` | worker, verdict, api, all | stable lease owner id, `[A-Za-z0-9_-]{1,64}`. Not in ARCHITECTURE 5.1 (implementation) |
| `PE_LEASES` | `auto` (`all` in `all`) | worker, verdict | `auto` (fair share), `all`, or a list like `0-3,7`. Not in ARCHITECTURE 5.1 (implementation) |

Secrets summary (generate with `openssl rand -hex 32`; `deploy/scripts/gen-env.sh` does it for compose):

| Secret | Holders | Same value everywhere | Rotation |
|---|---|---|---|
| `PE_OUTBOUND_KEY` | gateway, verdict, api, (worker) | yes | disruptive (8.6) |
| `PE_LICENSE_PEPPER` | gateway, api | yes | never |
| `PE_SERVICE_TOKEN` (+ `_NEXT`) | api, dashboard `PANDAEYE_SERVICE_TOKEN` | yes | zero downtime (8.6) |
| `PE_ASSERTION_KEY` | api, dashboard `PANDAEYE_ASSERTION_KEY` | yes | brief dashboard outage (8.6) |
| NATS creds | one file per role | no | zero downtime (8.6) |
| `PE_DB_DSN`, `PE_CLICKHOUSE_DSN` passwords | roles using them | yes | standard credential rotation |

## 4. Datastores

- **Postgres**: run `pandaeye migrate` (with the same `PE_DB_*`) before starting a new release. The spec
  does not say whether migrations are backward compatible with the previous release (**Open**): until it
  does, assume a rolling deploy must not depend on it and test migrations on a staging copy first.
- **sqlite** (`all` only): WAL, `busy_timeout` 5000, one writer. The process backs it up online every 6 h (`PE_DB_BACKUP_*`, section 8.3).
- **ClickHouse**: apply `deploy/clickhouse/init.sql` once (the image runs it from
  `/docker-entrypoint-initdb.d`). Tables `signals` (TTL 30 d), `verdicts` (90 d), `feature_windows`
  (180 d, schema owned by [`ml/FEATURES.md`](../ml/FEATURES.md)) and `sessions` (90 d), ordered by
  `(tenant_id, time)`. Inserts are batched (1 s / 10 000 rows) and dropped on overflow, so ClickHouse
  downtime loses telemetry but never affects detection.

## 5. NATS

### 5.1 Requirements

- JetStream enabled; `max_payload >= 8 MiB` (every service checks it at start); credentials and TLS for
  every non-loopback client. The shipped configs set `max_payload: 8388608`.
- Streams and buckets (ARCHITECTURE 5.4): `PE_EVENTS` (`pe.events.*`, file, limits, `PE_EVENTS_MAX_AGE` /
  `PE_EVENTS_MAX_BYTES`, discard old, duplicates 10 min), `PE_SIGNALS` (`pe.signals.*`, 7 d, duplicates
  10 min), `PE_VERDICTS` (`pe.verdicts.*`, `pe.acks.*`, 30 d, duplicates 1 h), KV `PE_LEASES` (TTL 15 s),
  `PE_CONNECTIONS` (TTL 30 s), `PE_SERVERS`, `PE_TENANTS`, `PE_META` (no TTL), plus `PE_ERASURES`
  (erasure tombstones, TTL 31 d; the spec does not name this bucket, the name comes from
  `cloud/internal/stream`). Core subject `pe.out.<tenant>.<server>`. All with `PE_NATS_REPLICAS` replicas.
- Provisioning: the spec does not say which process creates streams and buckets on an external cluster.
  In the implementation every process that connects (all roles except `migrate`) runs create-or-update on
  all three streams and all six buckets and create-or-get on `PE_META`, so every service user may create
  and update them (never delete or purge). Settings changed by hand are overwritten at the next start.
- Disk: size JetStream storage for `PE_EVENTS_MAX_BYTES` + 7 days of signals + 30 days of verdict records,
  times the replica count across the cluster.

### 5.2 Authentication

PandaEye services authenticate with `PE_NATS_CREDS` or `PE_NATS_TOKEN`; the configuration rejects
credentials embedded in `PE_NATS_URL`. A NATS token is one identity for the whole server (nats-server
refuses a token together with users or accounts), so per-role permissions need decentralised (operator /
JWT) authentication with one creds file per role:

```sh
make -C deploy nats-auth          # = cd deploy/nats/gen-auth && go run . -out ../auth
#   nats/auth/server/resolver.conf  operator JWT, SYS + PANDAEYE account JWTs (MEMORY resolver)
#   nats/auth/creds/pe-<role>.creds one per role: pe-gateway, pe-worker, pe-verdict, pe-api, pe-replay,
#                                   pe-admin (operators only), sys (SYS account, monitoring)
#   nats/auth/keys/                 operator/account seeds and issued user keys (keep offline, back up)
make -C deploy nats-permissions   # print every role's exact subject list
```

[`deploy/nats/nats-server.conf`](../deploy/nats/nats-server.conf) (single node) and
[`nats-cluster.conf`](../deploy/nats/nats-cluster.conf) (R3, mutual TLS between routes) include
`auth/server/resolver.conf` and serve client TLS. Mount the `server/` directory, not the file, into
containers so that a regenerated file is seen by `nats-server --signal reload`. A user JWT carries its
permissions and travels in the client's CONNECT line, so the configs raise `max_control_line` to 8192
(the generated JWTs are about 2-3 KB; the 4 KiB default is too tight with longer lists). If you already
run `nsc`, the same permissions can be put into user JWTs issued by your own operator.

### 5.3 Permissions per role

ARCHITECTURE 5.4 summarises the data plane as gateway pub `pe.events.>` / sub `pe.out.>`, worker pub
`pe.signals.>`, verdict and api pub `pe.out.>` and `pe.verdicts.>`. The full set below adds what the data
flows of 5.3-5.13 and the implementation also need: the gateway publishes `AckRecord` on
`pe.acks.<tenant>` (5.3); every process provisions the streams and buckets and creates or reads
`PE_META` when it connects; the api writes erasure tombstones that workers and the verdict service watch
(5.13); and every user receives replies on `_INBOX.>`. Data-plane tokens are narrowed to the subject
shapes actually used (`pe.events.*` rather than `pe.events.>`). The authoritative list is the table in
[`deploy/nats/gen-auth/main.go`](../deploy/nats/gen-auth/main.go); `make -C deploy nats-permissions`
prints it.

| User | Publish (data plane) | Subscribe | Consumes | KV read + write | KV read |
|---|---|---|---|---|---|
| `pe-gateway` | `pe.events.*`, `pe.acks.*` | `_INBOX.>`, `pe.out.>` | - | `PE_CONNECTIONS`, `PE_SERVERS`, `PE_META` | `PE_TENANTS` |
| `pe-worker` | `pe.signals.*` | `_INBOX.>` | `PE_EVENTS` | `PE_LEASES`, `PE_META` | `PE_TENANTS`, `PE_ERASURES` |
| `pe-verdict` | `pe.verdicts.*`, `pe.out.*.*` | `_INBOX.>` | `PE_SIGNALS`, `PE_VERDICTS` | `PE_LEASES`, `PE_META` | `PE_TENANTS`, `PE_CONNECTIONS`, `PE_ERASURES` |
| `pe-api` | `pe.out.*.*`, `pe.verdicts.*` | `_INBOX.>` | - | `PE_TENANTS`, `PE_LEASES`, `PE_ERASURES`, `PE_META` | `PE_CONNECTIONS`, `PE_SERVERS` |
| `pe-replay` | - | `_INBOX.>` | `PE_EVENTS` (plus `STREAM.MSG.GET`) | `PE_META` | - |
| `pe-admin` | `>` | `>` | all | all | all |

Every user except `pe-admin` also gets the service base set, and the columns expand to these exact
subjects (publish permissions; `S` is a stream, `B` a bucket):

| Part | Subjects |
|---|---|
| Service base (every service user) | `$JS.API.INFO`, `$JS.API.STREAM.INFO.*`, `$JS.FC.>` (flow-control replies of its own push consumers), and `$JS.API.STREAM.CREATE.<name>` + `$JS.API.STREAM.UPDATE.<name>` for `PE_EVENTS`, `PE_SIGNALS`, `PE_VERDICTS` and `KV_` + each of the six buckets |
| Consumes `S` | `$JS.API.CONSUMER.*.S`, `$JS.API.CONSUMER.*.S.>` (create incl. ephemeral, info, delete, pause, unpin, reset, list, names), `$JS.API.CONSUMER.DURABLE.CREATE.S.>`, `$JS.API.CONSUMER.MSG.NEXT.S.>`, `$JS.ACK.S.>` and `$JS.ACK.*.*.S.>` (ack, nak, in-progress; v1 and v2 ack formats) |
| KV read `B` | `$JS.API.DIRECT.GET.KV_B`, `$JS.API.DIRECT.GET.KV_B.>` (get; buckets allow direct get), `$JS.API.CONSUMER.*.KV_B.>` (watch, key listing) |
| KV read + write `B` | KV read plus `$KV.B.>` (put, create, update with revision, delete, purge) |

Create and update are granted per stream name, never with a wildcard: a role able to create arbitrary
streams could capture `pe.out.>` and read every outbound message. No service user can delete, purge,
snapshot or restore a stream or delete individual messages. Consumer rights on a stream include deleting
its consumers (lease handover deletes and recreates them), so `pe-replay` could disrupt a worker's
consumer: run replays as an operator, not from automation. Because `PE_META` must be create-or-get for
every process and NATS cannot separate "create" from "put" on a key, every service user can overwrite
`PE_META`; a changed partition count or key makes every process refuse to start, which is noisy but not
silent.

These permissions were checked against nats-server 2.15 with nats.go 1.54 by acting out each role: the
implementation's provisioning of all streams and buckets with its settings, `PE_META` create-or-get,
publish with PubAck, pinned durable pull consumers with ack and in-progress, ordered consumers, KV
create/update/get/list/watch on the buckets each role uses, erasure tombstone put and watch, and core
`pe.out` delivery from verdict and api to the gateway; and by checking that cross-role publishes, KV
writes to buckets a role only reads, unrelated stream creation and stream deletion/purge are denied.
Creds rotation with revocation (8.6) was checked the same way.

**Open:** add `pe.out.*.*` to `pe-gateway` if the gateway sends `close_connection_id` (the spec does not
name its publisher).

### 5.4 TLS for NATS

Client TLS on 4222 with a certificate whose SAN covers the names in `PE_NATS_URL`; services trust it via
`PE_NATS_TLS_CA`. Cluster routes use mutual TLS (`verify: true`) with their own certificate. For
development, `make -C deploy certs` creates a CA and both certificates. The monitoring port 8222 is
unauthenticated HTTP: keep it on the private network.

## 6. Running with docker compose

```sh
make -C deploy env certs nats-auth image   # .env with random secrets, dev certs, NATS creds, pandaeye:dev
$EDITOR deploy/.env                         # PE_STATIC_LICENSES or the pandascript licence settings
make -C deploy up                           # preflight checks, then docker compose up -d
docker compose -f deploy/docker-compose.yml ps
```

The stack runs `migrate` once, then gateway, two workers, verdict and api, each with only its own NATS
creds and only the secrets it needs. Published ports bind to `127.0.0.1` (`GATEWAY_BIND`, `API_BIND`).
Images: `pandaeye` from [`deploy/Dockerfile`](../deploy/Dockerfile) (static, distroless, nonroot,
`linux/amd64` and `linux/arm64`: `make -C deploy image-multiarch IMAGE=<registry>/pandaeye TAG=<version>`);
NATS, Postgres and ClickHouse from mirror.gcr.io.

## 7. TLS termination

Plugins require `wss://`. They accept `ws://` only to a loopback address, or to a same-host or
private-network address when `cloud.allow-insecure: true` is set (the plugin warns at enable); use that
only where the path between game server and gateway never leaves a network you control. The options:

- **Directly on the gateway**: `PE_TLS_CERT` / `PE_TLS_KEY` (with systemd, pass them through
  `LoadCredential=` as shown in the unit files). Simplest path, lowest latency.
- **Reverse proxy or load balancer** (nginx, HAProxy, Caddy, a cloud L7 LB): forward WebSocket upgrades on
  `/v1/connect` to the gateways, allow messages of at least 1 MiB + 1 byte, set read/idle timeouts well
  above the 15 s heartbeat timeout (60 s or more; heartbeats flow every 5 s), disable response buffering,
  and set `PE_TRUSTED_PROXIES` to the proxy addresses so per-IP limits see the real client. Example
  (nginx):

```nginx
location /v1/connect {
    proxy_pass http://pandaeye_gateways;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_read_timeout 120s;
    proxy_send_timeout 120s;
    proxy_buffering off;
}
```

- **Cloudflare in front of the gateway**: use it only as a plain proxied DNS record (WebSockets enabled),
  never by terminating plugin connections in a Cloudflare Worker: the gateway is a long-lived stateful
  server and the Workers runtime adds its own per-message, CPU and duration limits and an extra hop.
  Cloudflare closes WebSocket connections that are idle for about 100 s; PandaEye heartbeats every 5 s, so
  this never triggers. Cloudflare also drops connections when it restarts edge servers; plugins reconnect
  and replay, which is harmless but shows up as reconnects. Set `PE_TRUSTED_PROXIES_PRESET=cloudflare`
  (the gateway honours `CF-Connecting-IP` only from trusted proxies) and `PE_REQUIRE_TRUSTED_PROXY=true`
  so nobody can reach the origin around Cloudflare. Every extra hop adds latency to the probe RTT; measure
  the SLO with the topology you actually use.
- **API**: the dashboard Worker calls `PE_API_LISTEN` over public HTTPS. Its source addresses are not
  fixed, so access control is the service token plus the signed user assertion (ARCHITECTURE 5.8); API
  keys use the same endpoint.

## 8. Runbooks

### 8.1 Rolling deploys

Order (readers before writers, so a new message field is understood before it is sent):

1. Read the release notes for protocol, bus or schema changes.
2. Run `pandaeye migrate` once (compose: the `migrate` service; systemd: the `systemd-run` line in
   `pandaeye@.service`).
3. Restart api instances one at a time.
4. Restart verdict instances one at a time.
5. Restart workers one at a time.
6. Restart gateways one at a time.

Between instances, wait until the replaced one is healthy, every partition is leased again
(`sum(pandaeye_leases_held)` per group equals `PE_PARTITIONS`), consumer lag is back to its baseline and
the probe p95 has recovered.

What happens on a graceful stop (SIGTERM, completes within 10 s; give the runtime >= 20 s before SIGKILL):

- gateway: every connection gets `Error{CODE_SHUTTING_DOWN, retry_after_ms = jittered}` and close 1001.
  Plugins reconnect (through the load balancer, to another gateway) after the jitter and replay their
  pending batches. Take the instance out of the load balancer first. **Open:** whether `/healthz` fails
  during shutdown is not specified; use the load balancer's draining.
- worker / verdict: consumers are drained, leases are deleted (revision-checked). Another instance takes
  each free partition: it waits 5 s after taking the lease, recreates the durable consumer at the old ack
  floor + 1, and warms up (worker: replays the last 60 s of events without emitting signals; verdict:
  rebuilds scores from the last 2 h of signals without acting) before resuming. Expect a probe RTT spike of
  several seconds on the moved partitions; no event is lost (it waits in the stream).
- api: leased loops (metering every minute, the daily janitor) move to another instance.
- After a crash instead of a stop, leases expire after their 15 s TTL; an old owner that lost contact
  self-fences after 10 s without a successful renewal, and the pinned consumer allows one puller only.

### 8.2 Scaling

- **Gateways**: add instances behind the load balancer. Connections are long-lived, so new instances only
  receive reconnecting and new servers; rebalance by restarting old instances one at a time.
- **Workers / verdict**: add instances; with `PE_LEASES=auto` each takes a fair share of free partitions
  and existing members release extras. Parallelism is capped at `PE_PARTITIONS` per role.
- **Partitions** are fixed for the lifetime of the deployment (`PE_META`), because a player's partition
  decides which worker holds his state and which subject his history is in. Choose a count with headroom
  (for example 32 or 64 if you expect more than 16 workers; the maximum is 1024). There is no online
  re-partitioning. Changing the count is a planned migration that resets in-flight state:

  1. Plan a window shorter than 10 minutes (the plugins' pending buffer age; buffers are also capped at
     64 MiB per server). Plugins keep detecting locally and buffer meanwhile.
  2. Stop all gateways. Wait until every `events.*` and `signals.*` consumer and the store writer have
     zero pending messages (`nats consumer report PE_EVENTS`, `PE_SIGNALS`, `PE_VERDICTS`).
  3. Stop all workers and verdict instances.
  4. As `pe-admin`: delete the streams `PE_EVENTS` and `PE_SIGNALS` (`nats stream rm`), the keys
     `partitions` and `partition_key` of `PE_META`, and the `events.*` / `signals.*` keys of `PE_LEASES`
     (or wait 15 s for them to expire). Keep `PE_VERDICTS` and the other buckets.
  5. Set the new `PE_PARTITIONS` on every gateway, worker and verdict instance; start verdict, then
     workers, then gateways.
  6. Plugins reconnect and replay their buffers.

  Consequences: accumulated suspicion scores, worker windows and the replay history reset to empty (stored
  verdicts, bans, appeals and clips are unaffected). A batch in flight at the cut-over may be processed
  twice or skipped. Rehearse on staging. **Open:** the spec provides no re-partitioning tool.

### 8.3 Backups and disaster recovery

| Data | Where | Back up how | If lost |
|---|---|---|---|
| SQL store | Postgres (split) / sqlite (`all`) | Postgres PITR (WAL archiving) plus regular base backups; sqlite: built-in online backups every 6 h to `data/backups/` (`PE_DB_BACKUP_*`); copy that directory off the host, since a copy on the same disk does not survive losing it. Never copy the live `pandaeye.db` file itself | Tenants, servers (secret hashes), verdicts, bans, appeals, labels, clips, licence mappings, API keys, audit log. This is the source of truth |
| Secrets | env files / secret manager | escrow outside the cluster | `PE_LICENSE_PEPPER`: every licence-to-tenant mapping is orphaned (tenants must be rebuilt by hand). `PE_OUTBOUND_KEY`: stored Discord webhooks become unreadable. In `all` both live in the database |
| `PE_META` | NATS KV | record `partitions` and `partition_key` (`nats kv get PE_META partition_key`) after the first start | a new key re-partitions every player: same effects as 8.2 |
| `PE_VERDICTS` | NATS | R3 replication; optionally `nats stream backup` | records not yet consumed by the store writer never reach the database |
| `PE_EVENTS`, `PE_SIGNALS` | NATS | R3 replication only | transient (24 h / 7 d): scores and windows rebuild from new data |
| `PE_SERVERS`, `PE_TENANTS` | NATS KV | rebuilt from the database (see restore) | see restore step 4 |
| ClickHouse | optional | `BACKUP DATABASE pandaeye TO ...` | analytics and ML feature windows only |
| Dashboard D1 | Cloudflare | D1 Time Travel / `wrangler d1 export` | dashboard users, invites, sessions |
| NATS auth | `deploy/nats/auth/keys` | offline copy | re-issue every creds file (8.6) |

Backups contain personal data and keep erased players' data until they expire: set a backup retention
and state it in your privacy notice ([PRIVACY.md](PRIVACY.md) has a placeholder for it).

Restore after losing the region (or the NATS cluster):

1. Restore Postgres to the latest consistent point; restore the secrets (same pepper, outbound key,
   service token, assertion key).
2. Build a new NATS cluster with the same `resolver.conf` (or re-issue creds).
3. Before starting any PandaEye service, create `PE_META` as `pe-admin` and put the recorded
   `partitions` and `partition_key`, so players keep their partitions.
4. Start api, verdict, workers, then gateways. **Open:** the spec does not say whether the api
   repopulates `PE_TENANTS` from the database at start. If it does not, re-save every tenant's settings
   (`PATCH /api/v1/tenant`), and until then treat enforcement as off for tenants without a KV entry.
5. Plugins reconnect. If `PE_SERVERS` was lost, `Welcome.last_acked_seq` is 0, plugins resend their whole
   pending buffer and a few batches can be processed twice (the msg-id window is gone).
6. Verify: `/status`, probe RTT, consumer lag, one test server connecting and receiving an ALERT.

### 8.4 Emergency kill switch

From the smallest to the largest blast radius:

- **One tenant, everything**: dashboard setting "enforcement" off (`PATCH /api/v1/tenant
  {"enforcement": false}`, owner/admin). The API writes the database and KV `PE_TENANTS`; gateways then
  force `shadow = true` on every verdict they deliver, the verdict service marks new verdicts shadow, and
  the new `RemoteConfig` reaches connected plugins as `config_update` and at the latest with the next Pong
  (<= 5 s). Plugins persist the last-known value and stop all enforcement (setbacks, mitigations, kicks,
  bans). Disconnected plugins keep their last-known value until they reconnect; they receive no verdicts
  meanwhile, but their local setbacks continue, so use a local switch for them. Bans already executed are
  undone only by overturning (8.5). When enforcement flips off at runtime the plugin removes its
  mitigation listeners and clears active mitigations; the listener also re-checks the gate on every event.
- **One tenant, one action type**: autoban / autokick / mitigations off. Verdicts are still produced as
  shadow ("would have banned"), which keeps measuring.
- **One server**: set `mode: shadow` in `plugins/PandaEye/config.yml` and restart or
  `plugman unload PandaEye` + `plugman load PandaEye` (`/pandaeye reload` reloads thresholds and alert
  settings only). Per-feature switches: `setbacks.enabled`, `mitigations.enabled`,
  `checks.<id>.enabled`; **Open:** whether `/pandaeye reload` applies them is not specified, so reload the
  plugin.
- **Plugin circuit breaker** (automatic): more than `punishments.max-bans-per-hour` (3) bans or
  `max-kicks-per-10min` (5) kicks makes the plugin stop executing, ack `executed = false, note =
  "circuit-open"` and alert console and staff until `/pandaeye breaker reset`. Do not reset it during a
  suspected false-positive wave.
- **Whole platform**: there is no global switch in v1 (**Open**). Stopping every verdict instance stops
  new verdicts while signals wait in `PE_SIGNALS` (7 d); after a restart that backlog can produce ALERT at
  most (signals older than 2 min are never acted on). Verdicts already created stay pending and are still
  delivered by the gateway sweep (KICK for 5 min, BAN for 24 h) unless their tenants' enforcement is off.
  Probes stop too, so `/status` reports the SLO as unmet. The alternative is rolling back the release.

### 8.5 False-positive wave

1. **Detect**: a jump in non-shadow KICK/BAN verdicts (`pandaeye_verdicts_total`), `circuit-open` acks,
   an appeal surge, staff reports; usually one check or one client version dominates the evidence.
2. **Contain** with the smallest switch that stops harm (8.4): affected tenants' autoban/autokick or
   enforcement off; a cloud check off per tenant (`PUT /api/v1/checks`, disabled checks emit nothing); a
   local check off in `config.yml`; for a platform-wide bug, stop the verdict service or roll back.
3. **Diagnose**: verdict evidence (score, top signals, clips, replay viewer); re-run the checks over stored
   data with `pandaeye replay --tenant <t> --player <uuid> --since <t0> --until <t1>` (events are kept for
   `PE_EVENTS_MAX_AGE`); compare client versions and brands in the sessions telemetry.
4. **Remediate**: overturn every wrongful verdict (`POST /api/v1/verdicts/{id}/overturn`, staff or an API
   key with `verdicts:write`). Overturn labels the player `legit` for the window (which also blocks
   KICK/BAN from that window), removes reputation contributions, sends UNMITIGATE to the open session and,
   for executed bans, an UNBAN to the server that executed it (pending until that server acks; the
   dashboard shows "unban pending"). **Open:** there is no bulk-overturn endpoint; script it.
5. **Fix**: add the case as a legit fixture (ARCHITECTURE 8), ship the fix, run it in shadow (tenant
   enforcement or autoban off) and compare the would-be verdicts before re-enabling.
6. **Communicate**: status page note, affected tenants, appeal handling.

### 8.6 Rotating keys

- **Licence key (rebind)**: the tenant owner binds the new key with `POST /api/v1/auth/rebind
  {license_key}` (dashboard), optionally revoking old keys; history stays with the tenant. Then set
  `cloud.license-key` on every server and restart or reload the plugin (`/pandaeye reload` does not
  reload it). Server identities are unaffected. With `PE_LICENSE_MODE=static`, add the new key to
  `PE_STATIC_LICENSES` on gateway and api first (rolling restart) and remove the old one last.
- **Service token** (zero downtime): set `PE_SERVICE_TOKEN_NEXT=<new>` on every api instance (rolling);
  set the dashboard secret `PANDAEYE_SERVICE_TOKEN=<new>` and deploy the Worker; then set
  `PE_SERVICE_TOKEN=<new>` and remove `PE_SERVICE_TOKEN_NEXT` (rolling).
- **Assertion key**: there is no NEXT key (**Open**), so the API and the Worker must switch together and
  dashboard calls fail with 401 in between. In a quiet period, set `PANDAEYE_ASSERTION_KEY` on the Worker
  and `PE_ASSERTION_KEY` on the api instances as close together as possible.
- **Outbound key**: there is no NEXT key either (**Open**). Gateways drop every SignedOutbound whose MAC
  does not verify, so while instances disagree, verdicts, config updates, watch requests and probe results
  sent through `pe.out` are lost; verdicts still arrive through the gateway sweep from the database,
  config is healed by Pong, probes resume. Webhook encryption is derived from this key, so every stored
  Discord webhook must be re-saved by its tenant afterwards, and in the current implementation appeal
  codes are derived from it as well (stored codes stay valid; re-derived ones change). Rotate only after a
  compromise: stop the verdict and api instances, set the new key on every gateway, worker, verdict and api
  instance, start gateways, then verdict and api.
- **Licence pepper**: do not rotate. Licence rows are looked up by `HMAC(pepper, key)` and raw keys are
  never stored, so a new pepper orphans every tenant's licence mapping.
- **NATS creds** (zero downtime), from `deploy/nats/gen-auth`:
  `go run . -out ../auth -reissue pe-worker` (or `all`) issues new creds while the old ones stay valid;
  deploy the new file and restart that role's instances one at a time; then
  `go run . -out ../auth -revoke-old` and reload every nats-server (`nats-server --signal reload`). The
  revoked creds are refused from then on.
- **Server secret**: a server whose `server-secret` leaked or was lost gets a new identity: delete
  `plugins/PandaEye/server-id` and `server-secret` and restart; the dashboard shows a new server.
  **Open:** there is no API to reset a server's secret or remove the old server entry.
- **Proxy session secret**: change Velocity's `session-secret` and every backend's `proxy.secret`
  together. While they differ, backends reject the session message, sessions lose their
  `network_session`, detection is unaffected.

### 8.7 Plugin rollout on a production server (Paper / Folia)

**Prerequisites.** Paper, Folia or a fork, 1.21.4+, on Java 21+ (Spigot/CraftBukkit and older versions are
refused with a log message); the PacketEvents plugin 2.13.x installed and
enabled (PandaEye declares `depend: [packetevents]` and never shades it). If PacketEvents is not installed
yet, add it during a regular restart; PandaEye's hot-load safety covers PandaEye, not PacketEvents. A
licence key and the regional endpoint.

**1. Install in shadow mode.** Put the PandaEye jar in `plugins/` and prepare
`plugins/PandaEye/config.yml` before the first enable (defaults: ARCHITECTURE 4.11): keep `mode: shadow`,
set `cloud.license-key`, `cloud.endpoint` (`wss://<regional host>/v1/connect`), `server-name` and, on a
network, `network-id`. Shadow mode guarantees (ARCHITECTURE 1.1): no packet is sent to any client, no
packet or event is cancelled or modified, no teleports or velocity changes, no punishment or mitigation
(except UNBAN of a ban this server executed), bounded queues, files and logs, every callback catches
every Throwable.

**2. Restart or hot load.**

- Restart: the cleanest start; every session is seeded at join.
- Hot load with PlugManX: `plugman load PandaEye` (the jar file name without `.jar`). Online players get
  sessions marked `hot_loaded`; until each player's snapshot is taken on his own scheduler the session is
  unseeded and local checks are off for him. Hot-loaded sessions count half in scoring until seeded and
  for 60 s after.

**3. Verify zero impact** (right after enabling, again after an hour and after a day):

- Console: the enable messages and no stack traces. Warnings are rate-limited (1 per 10 s per site);
  repeated ones are bugs to report. Check the warning about `ban-command` / `unban-command` labels that
  do not resolve to the same plugin.
- `/pandaeye status`: mode `shadow`, enforcement `false`, cloud connected, connection RTT and probe RTT
  p50/p95 present, pending batches near 0, dropped events and dropped log lines 0, tracked players equal to
  online Java players, unseeded sessions back to 0 within seconds, packet-handler cost (average and p99
  microseconds per packet) and dropped confirmations. The Enforcer's attempted-call counter shows what live
  mode would have done.
- Kicks and disconnects: compare the disconnect reasons in the server log with the period before
  installation; PandaEye in shadow mode never kicks, so any new pattern (timeouts, invalid packets) is a
  bug.
- TPS / MSPT and CPU: compare with the baseline (for example with spark: `/spark tps`, a profile filtered
  on `dev.pandascript.pandaeye`). Budget: under 2% of one core at 200 players.
- Dashboard: the server is online; probe RTT is reported.

**4. Unload / roll back.** `plugman unload PandaEye` disables within 300 ms: the listener is removed,
in-flight callbacks are awaited (100 ms), unacknowledged batches are spooled and resent if PandaEye is
loaded again within 1 h, and no `PandaEye-*` thread remains (check a thread dump). Remove the jar to
uninstall; keep `server-id` and `server-secret` if you may reinstall, so the server keeps its identity.
Use `/pandaeye forget <uuid>` to remove a player's lines from local logs.

**5. Observe and tune** (shadow, at least 7 days). Local files:

- `plugins/PandaEye/logs/flags-YYYY-MM-DD.jsonl`: confirmed local flags aggregated per player and check per
  5 s (count, max severity, first and last info). Bounded: 50 lines/s, 256 MiB per day, kept 14 days,
  writing stops below 1 GiB free disk.
- `plugins/PandaEye/logs/peaks-YYYY-MM-DD.jsonl`: per player, per check, per 5 minutes: the maximum test
  statistic, evaluated, candidates, discarded by the confirmation step, confirmed. The same numbers go to
  the cloud as `CheckStats`.

The JSON field names of these logs are defined by the plugin, not the spec (**Open**). The examples
assume the `CheckStats` names (`check_id`, `max_stat`, `evaluated`, `candidates`, `discarded`,
`confirmed`) plus `player`; adjust them to the shipped format.

```sh
# confirmed flags per check over the last 7 days
cat logs/peaks-*.jsonl | jq -s 'group_by(.check_id) | map({check: .[0].check_id, confirmed: (map(.confirmed) | add)})'
# each player's peak statistic for one check, highest first
cat logs/peaks-*.jsonl | jq -s '[.[] | select(.check_id == "movement.speed")] | group_by(.player)
  | map({player: .[0].player, peak: (map(.max_stat) | max)}) | sort_by(-.peak) | .[:20]'
```

Peak-per-player method (CHECKS.md section 0): thresholds are tightened from measured peaks, never by
guessing.

1. For each check, take every player's peak statistic over the observation period (the maximum over his
   5-minute windows).
2. Split players into staff-verified legitimate players, confirmed cheaters (dashboard labels) and the
   rest.
3. A threshold must stay above the highest peak of any verified legitimate player, with a safety margin
   you choose and record. Tighten the lenient default towards that value only; if a verified legitimate
   player's peak is above the default, the check needs a fix, not a threshold change.
4. Apply the threshold in `checks.<id>` (**Open:** the per-check threshold key names are not specified),
   reload, and observe for another full gate-2 period.

**6. Promote to live** when gate 2 passes ([SLO.md](SLO.md): >= 7 days and >= 1000 player-hours, <= 1
confirmed flag per 50 player-hours per check, no staff-verified legitimate player at a setback VL) and,
in addition: no PandaEye-caused disconnects, plugin cost within budget, the shadow verdicts shown as
"would have" KICK/BAN reviewed and justified, ban and unban commands verified, and
`punishments.appeal-url` set (the ban message must carry the appeal route and code). Then, in stages,
each followed by an observation period:

1. `mode: live` in `config.yml` and restart or reload the plugin. Nothing is enforced yet while the
   dashboard's enforcement is off.
2. Dashboard: enforcement on, mitigations on, autokick and autoban off (setbacks and mitigations only).
3. Autokick on.
4. Autoban on.

Keep the circuit-breaker defaults.

### 8.8 Velocity proxy and session secret

1. Install the `pandaeye` plugin on Velocity; configure `plugins/pandaeye/config.yml`: licence,
   endpoint, the network's `network-id`, `mode` (shadow until promoted) and `session-secret`
   (`openssl rand -hex 32`).
2. On every backend: the same `network-id`, `proxy.session-channel: true`, and `proxy.secret` equal to
   the proxy's `session-secret`; restart or reload the plugin.
3. Effect: the proxy reports joins (with brand and Bedrock detection), server switches and quits, and
   after each backend connect sends the HMAC-signed `pandaeye:session` message, so backend sessions carry
   the network session (PROTOCOL section 11). The proxy drops client-sent messages on that channel; the
   backend reads it at packet level without registering a channel.
4. Verdicts on the proxy: in live mode with enforcement the proxy can run its configured commands for
   KICK/BAN; the default is shadow / log only. The same circuit breaker and placeholder rules apply.
5. Metering: per network the proxy's player count is used while a proxy is connected, else the sum of the
   backends. **Open:** whether the proxy's connection counts towards the licence's `maxServers`.

## 9. Monitoring

### 9.1 Metrics

PandaEye metrics (`/metrics` on `PE_METRICS_LISTEN`; no tenant or server labels), from
`cloud/internal/metrics`:

| Metric | Labels | Meaning |
|---|---|---|
| `pandaeye_probe_rtt_seconds` | `stage`: `plugin` (the SLO: plugin-measured end to end), `cloud` (gateway receive to gateway send), `gateway_worker`, `worker_verdict`, `verdict_gateway` | probe latency histogram |
| `pandaeye_gateway_batches_total` | `result`: `stored`, `duplicate`, `too_large`, `invalid` | batches handled |
| `pandaeye_gateway_events_total`, `pandaeye_gateway_batch_bytes_total` | - | events stored, wire bytes received |
| `pandaeye_gateway_auth_total` | `result`: `ok`, `invalid`, `limit`, `duplicate`, `rate_limited`, `unavailable` | Hello outcomes |
| `pandaeye_gateway_connections` | - | open plugin connections |
| `pandaeye_gateway_connections_closed_total` | `reason` | closed connections by close reason |
| `pandaeye_consumer_lag_messages` | `consumer` | messages pending for a stream consumer |
| `pandaeye_leases_held`, `pandaeye_lease_changes_total` | `group`; `group`, `event` (`acquired`, `released`, `lost`) | partition and singleton leases |
| `pandaeye_signals_total` | `check` | signals emitted |
| `pandaeye_verdicts_total` | `action`, `shadow` | verdicts produced |
| `pandaeye_dropped_total` | `reason` (overflow, stale, erased, ...) | anything dropped |
| `pandaeye_stream_publish_errors_total` | `stream` | failed publishes |
| `go_*`, `process_*` | - | runtime and process |

Also scrape NATS (monitoring port 8222 through a Prometheus NATS exporter: JetStream storage, consumer
`num_pending` and redeliveries, slow consumers, route health), Postgres (connections, replication lag,
disk) and ClickHouse (insert errors). Plugin-side health is visible in `/pandaeye status` and in the
dashboard (server online, probe RTT).

### 9.2 Suggested alerts

| Alert | Condition (PromQL sketch) | Why |
|---|---|---|
| Probe p95 over SLO | `histogram_quantile(0.95, sum by (le) (rate(pandaeye_probe_rtt_seconds_bucket{stage="plugin"}[10m]))) > 0.1` for 15 min | SLO burn ([SLO.md](SLO.md)); use the `stage` histograms to find the slow hop |
| Probe p99 over target | same with 0.99 and `> 0.25` for 15 min | tail latency |
| Probes missing | `sum(rate(pandaeye_probe_rtt_seconds_count{stage="cloud"}[5m])) == 0` while connections > 0 | the pipeline does not complete; the plugin-reported value can stay stale |
| Consumer lag | `max by (consumer) (pandaeye_consumer_lag_messages) > 10000` for 5 min (tune to your rate) | a worker or verdict partition is falling behind |
| Unowned partitions | `sum by (group) (pandaeye_leases_held)` below `PE_PARTITIONS` for 1 min | nobody processes some partitions |
| Lease losses | `increase(pandaeye_lease_changes_total{event="lost"}[10m]) > 0` | instability, NATS trouble or stalls |
| Auth failures | `sum(rate(pandaeye_gateway_auth_total{result!="ok"}[5m])) / sum(rate(pandaeye_gateway_auth_total[5m])) > 0.2` | licence verifier outage (`unavailable`), misconfigured servers (`duplicate`), attacks (`invalid`, `rate_limited`) |
| Drops | `sum by (reason) (rate(pandaeye_dropped_total[5m])) > 0` | data loss somewhere; check the reason |
| Poison batches | `increase(pandaeye_gateway_batches_total{result=~"too_large\|invalid"}[15m]) > 0` | a plugin version sends invalid data |
| Publish errors | `increase(pandaeye_stream_publish_errors_total[5m]) > 0` | JetStream unavailable or full |
| Enforcement spike | non-shadow KICK/BAN rate of `pandaeye_verdicts_total` above 3x its 7-day baseline | possible false-positive wave (8.5) |
| Connection drop | `pandaeye_gateway_connections` falls by > 20% in 5 min | gateway, proxy or network incident |
| NATS storage | JetStream storage > 80% of its limit | `PE_EVENTS` discards old data at its byte limit; other streams fail to accept |

## 10. Security checklist

- TLS everywhere on public paths; `PE_METRICS_LISTEN` and NATS monitoring private.
- One NATS creds file per role; `pe-admin` creds never on service hosts.
- `PE_TRUSTED_PROXIES` set exactly to your terminators.
- Secrets only in root-readable env files, a secret manager, or systemd credentials; never in images or
  version control (`deploy/.gitignore` excludes `.env`, `certs/`, `nats/auth/`).
- Logs at any level never contain secrets (redaction is tested), but still treat logs as personal data.

## 11. Open points

1. Who provisions streams and KV buckets on an external NATS cluster, and the name of the erasure
   tombstone bucket, are not in the spec; the implementation provisions from every process and uses
   `PE_ERASURES` (5.1).
2. Per-role NATS permissions in ARCHITECTURE 5.4 are a summary: the gateway also publishes `pe.acks.*`,
   and every role needs JetStream API and KV access (5.3).
3. NATS authentication: per-role isolation needs creds (operator mode); `PE_NATS_TOKEN` cannot provide it.
4. `PE_OWNER_ID` and `PE_LEASES` exist in the implementation but not in ARCHITECTURE 5.1; the spec says
   nothing about how leases are balanced.
5. `PE_OUTBOUND_KEY` is "random in `all`" in 5.1 but must persist (webhook encryption is derived from it);
   the implementation persists it in the database. The configuration also requires it for workers, which
   never sign or verify outbound messages.
6. No dual-key rotation for `PE_ASSERTION_KEY` and `PE_OUTBOUND_KEY`.
7. Re-partitioning, a platform-wide kill switch, bulk overturn, server-secret reset and server removal have
   no tooling.
8. Whether `PE_TENANTS` is repopulated from the database after a NATS loss; whether `/healthz` fails during
   graceful shutdown; whether migrations are backward compatible.
9. ARCHITECTURE 5.4 lists only `events.<NN>`, `signals.<NN>` and `writer` as `PE_LEASES` keys; the
   implementation also uses `metering`, `janitor` and `members.*` (lease balancing).
10. Whether `/pandaeye reload` applies `mode`, `checks.<id>.enabled`, `setbacks.enabled` and
    `mitigations.enabled`; the per-check threshold key names; the JSON schema of the flags and peaks logs.
11. Decided: when `enforcement` turns off at runtime the plugin clears active mitigations and removes the
    mitigation listeners (8.4).
12. Multi-region tenancy and how one dashboard serves several regional APIs.
13. Whether a Velocity proxy connection counts towards `maxServers`.
14. KnockbackSync shades its own PacketEvents and its decoder runs first: it drops the PONGs answering its
    own PINGs, so PandaEye (on the PacketEvents plugin) never sees them. Passive acknowledgement then
    depends on other PING senders; without any, pending values are promoted only at JOIN/RESPAWN, so
    movement checks stay exempt after a player's first change (fewer detections, never more flags).
15. `plugman unload` of an instance that was loaded at server start leaves its class loader reachable
    through `CraftServer`'s help map (the plugin.yml command's help topic) until the next restart, a few MB.
    Instances loaded with `plugman load` are collected. Registering `/pandaeye` at runtime would avoid it.
16. Seen in the first live shadow run: `protocol.cursor` was stricter than vanilla (fixed: [-0.5, 1.5]);
    `dig.speed` flagged one player hundreds of times ("0-1 ticks, expected 3") and needs a look at custom
    tools/effects before it is trusted; local checks produced about 2,500 dropped confirmations in 25 minutes
    at 100 players (bounded, but thresholds need tuning).
