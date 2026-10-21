# PandaEye architecture (implementation spec)

This document is the contract every PandaEye component is built against. If code and this document
disagree, one of them is a bug. Companion documents: [`CHECKS.md`](CHECKS.md) (every detection check,
its algorithm and thresholds), [`../proto/pandaeye/v1/wire.proto`](../proto/pandaeye/v1/wire.proto)
(plugin <-> cloud protocol) and [`../proto/pandaeye/v1/stream.proto`](../proto/pandaeye/v1/stream.proto)
(internal stream payloads).

## 1. What PandaEye is

A cloud anticheat for Minecraft Java servers, split like Polar:

| Runs where | What | Why |
|---|---|---|
| Plugin (Paper/Folia, Velocity) | Packet capture, same-tick checks (movement, timer, invalid protocol), setbacks, applying mitigations and punishments | Must act within the tick |
| Cloud (Go) | Combat heuristics, click analysis, aim, reach, scaffold, inventory, latency abuse, verdicts, evidence, reputation | Detection delay is fine; heavy math leaves the game server; detection logic never ships in the jar |
| Dashboard (Cloudflare Worker + D1) | Accounts, licensing UI, evidence, replays, appeals, settings, public status page | Reads stored results only, never on the packet hot path |

Loop: plugin streams events -> gateway -> event stream -> workers (score) -> verdict service (decide) ->
gateway -> plugin (apply). Suspicion produces MITIGATE, certainty produces BAN.

### 1.1 Differences from Polar we build in from day one

- Evidence on every verdict: triggering checks, confidence, and a replay clip.
- Degraded mode: if the cloud is unreachable, local checks keep running and buffered events are sent
  (and judged) when the connection returns.
- Pure JVM plugin, no native libraries (zstd is pure Java via aircompressor) -> works on ARM hosts.
- Opt-in reputation network, appeal -> label loop, published latency SLO + public status endpoint.
- Free tier + per-peak-concurrent-player metering (usage is recorded; billing itself is out of scope).

### 1.2 Hard safety invariants (shadow mode)

The plugin has a local `mode`: `shadow` (default) or `live`. In `shadow` mode the plugin MUST:

1. never send a packet to any client (no transactions/pings/keep-alives of its own, no resyncs);
2. never cancel or modify any packet (PacketEvents listener at `MONITOR` priority, read-only);
3. never cancel or modify any Bukkit event, never teleport/set back, never change velocity;
4. never run a punishment command (kick/ban/unban), never apply a mitigation;
5. never touch the world off the owning thread (Folia rules), never block a netty or region thread;
6. catch every exception in its listeners (one bad packet must never disconnect a player);
7. cost < 2% of one CPU core at 200 players.

Grim's shadow mode on a production network caused mass "out of order confirmation" kicks because it
still sent confirmation packets. PandaEye's shadow mode sends nothing. Enforcement requires BOTH local
`mode: live` AND the cloud's `RemoteConfig.enforcement = true`; verdicts with `shadow = true` are never
enforced.

## 2. Repository layout

```
proto/pandaeye/v1/        wire.proto, stream.proto (single source of truth for both languages)
plugin/                   Gradle multi-project (Kotlin DSL), Java 21
  protocol/               generated protobuf-lite classes (from ../proto) - package dev.pandascript.pandaeye.protocol.v1
  common/                 platform-independent: transport, codec, batching, buffers, clocks, util
  paper/                  Paper + Folia plugin "PandaEye"
  velocity/               Velocity plugin "pandaeye"
cloud/                    Go module github.com/jackh54/PandaEye/cloud (Go 1.24)
  cmd/pandaeye/           single binary, subcommands (see 5.1)
  internal/...            see 5.2
dashboard/                Cloudflare Worker (TypeScript, Hono) + D1 migrations
ml/                       Python training pipeline (features, training, ONNX export, gate evaluation)
deploy/                   Dockerfile, docker-compose, systemd unit, Pterodactyl notes
test/e2e/                 local end-to-end harness: real Paper/Folia servers + bots + cheat clients
docs/                     this file, CHECKS.md, PROTOCOL.md, OPERATIONS.md, PRIVACY.md, SLO.md
```

Naming: Java base package `dev.pandascript.pandaeye`; shaded libraries relocate to
`dev.pandascript.pandaeye.libs.<lib>`. The product never names a specific customer network in code,
configs or docs.

Toolchains (pinned): Java 21 (`--release 21`), Gradle 8.14, shadow plugin `com.gradleup.shadow` 9.x,
protobuf gradle plugin 0.9.5, protoc + protobuf-javalite 4.36.2, aircompressor 0.27 (pure Java zstd),
paper-api `1.21.4-R0.1-SNAPSHOT` (compileOnly), packetevents-spigot `2.13.0` (compileOnly; runtime is
whatever PacketEvents plugin the server has, 2.13.x), velocity-api `3.4.0` (compileOnly). Go 1.24
(`GOTOOLCHAIN=local`), google.golang.org/protobuf, github.com/nats-io/nats-server/v2 (embedded),
github.com/nats-io/nats.go (JetStream API `jetstream` package), github.com/coder/websocket,
github.com/klauspost/compress/zstd, github.com/jackc/pgx/v5, modernc.org/sqlite (pure Go),
github.com/ClickHouse/clickhouse-go/v2, github.com/prometheus/client_golang. Pick the newest versions of
the Go modules that build with Go 1.24.

## 3. Protocol summary

See wire.proto for the authoritative field list. Key rules:

- One WebSocket per server at `wss://<host>/v1/connect`. Binary messages only. Byte 0 = codec
  (`0x00` raw, `0x01` zstd frame with content size). Compressed <= 1 MiB, decompressed <= 8 MiB.
  The plugin compresses a frame when its raw size is > 256 bytes. The cloud does not compress.
- First frame: `Hello` within 10 s. Response: `Welcome` or fatal `Error` + close (code 4001 auth,
  4002 limit, 4003 protocol, 4008 rate limit, 1001 shutting down).
- `Batch.seq` increases by 1 per batch. The gateway publishes the batch to the stream, waits for the
  JetStream ack, then sends `BatchAck{seq}`. The plugin keeps unacked batches (bounded, see 4.6) and
  resends them after reconnect. The gateway treats a batch as replayed when `replayed` is set or when
  `now - sent_unix_ms > 30 s`.
- De-duplication: JetStream message id `<server_id>:<seq>:<partition>` (duplicate window 10 min), plus
  `Welcome.last_acked_seq` (the plugin drops pending batches with seq <= it, and if it has no pending
  batches and its next seq <= last_acked_seq it continues at last_acked_seq + 1).
- Heartbeat: plugin sends `Ping` every `heartbeat_interval_ms` (default 5000); gateway answers `Pong`
  immediately. Either side closes the connection after 3 intervals without any frame.
- Verdicts arrive as `ServerFrame.verdict`; the plugin answers each with `VerdictAck`.
- `Probe` events travel the full pipeline (gateway -> stream -> worker -> verdict service -> gateway)
  and return as `ProbeResult`: this is the phase-1 dummy check and the source of the latency SLO.
  The plugin sends one probe per 10 s (player bytes empty).
- Event clock: `Event.time_us` (see wire.proto header). The cloud never uses its own arrival time for
  detection math, only plugin time and client tick numbers, so replays and outage catch-up give the
  same results as live processing.

## 4. Paper / Folia plugin (`plugin/paper`)

### 4.1 Platform and lifecycle

- `plugin.yml`: `name: PandaEye`, `main: dev.pandascript.pandaeye.paper.PandaEyePlugin`,
  `api-version: '1.21'`, `folia-supported: true`, `depend: [packetevents]`,
  `softdepend: [floodgate, ViaVersion]`, command `pandaeye` (alias `peye`), permissions (4.10).
- Works on Paper 1.21.x and Folia/Canvas 1.21.x, Java 21+. Detect Folia with
  `Class.forName("io.papermc.paper.threadedregions.RegionizedServer")`.
- Never use `BukkitScheduler`. Use only Paper's regionised schedulers, which exist on Paper too:
  `player.getScheduler()` (entity), `Bukkit.getGlobalRegionScheduler()`, `Bukkit.getAsyncScheduler()`.
  Every entity-scheduler call must pass a retired callback and handle a null return (entity removed).
- Hot load (PlugManX `plugman load`) and hot unload (`plugman unload`) are first-class: on enable,
  players already online get a session with `hot_loaded = true` and a warm-up (no local checks for that
  player until 40 movement packets and one confirmed teleport OR 10 s, whichever comes first). On
  disable: unregister the PacketEvents listener, unregister Bukkit listeners, cancel all tasks, flush and
  close the cloud connection (max 2 s), stop every thread the plugin started, clear all maps. After
  unload no PandaEye thread may remain and the class loader must be collectable.
- PacketEvents is used through the installed plugin's API (never shaded, never initialised by us):
  register one listener via `PacketEvents.getAPI().getEventManager().registerListener(...)` at priority
  `PacketListenerPriority.MONITOR`; keep the returned `PacketListenerCommon` and unregister it on disable.

### 4.2 Threading model

- Packet listener callbacks run on the player's netty event loop. Incoming and outgoing packets of one
  player are handled on that player's event loop, so per-player packet state (`PacketState`) is owned by
  that thread and needs no locks. Ignore packets that are cancelled at MONITOR (`event.isCancelled()`)
  and packets outside the PLAY state.
- Data read across threads (another player's position history for reach, env flags, VLs, mitigation
  flags) uses immutable records published through volatile fields, lock-free ring buffers or concurrent
  maps. No `synchronized` on the netty path except tiny critical sections in VL bookkeeping.
- World access (blocks, entities) only on the player's region thread through `player.getScheduler()`.
  Two uses: the environment sampler (4.4) and deferred confirmation of local flags (4.5).
- Transport runs on its own daemon threads (4.6). Commands from verdicts run on the global region
  scheduler; per-player actions (setback, mitigation state) on the player's scheduler.
- Wrap every listener body in try/catch(Throwable) -> rate-limited warning log (max 1 per 10 s per
  exception site) and continue.

### 4.3 Packet tracking (`PacketState`, per player, netty thread)

Created lazily on the first PLAY-state packet of a user with a UUID; Bukkit `PlayerJoinEvent` attaches the
`Player` handle and starts the env sampler; `PlayerQuitEvent` and PacketEvents `UserDisconnectEvent` both
remove it (idempotent). Tracked from packets (server-protocol format; PacketEvents decodes with the
server version even when ViaVersion translates for the client):

| Source packet | State |
|---|---|
| S `JOIN_GAME`, `RESPAWN` | entity id, game mode; reset movement baseline; exempt until next confirmed teleport |
| S `PLAYER_POSITION_AND_LOOK` | pending teleport {id, target (resolve relative flags against last position), sent time} |
| C `TELEPORT_CONFIRM` | confirm pending teleport; next position packet resets the baseline |
| C flying packets (`PLAYER_FLYING`, `PLAYER_POSITION`, `PLAYER_ROTATION`, `PLAYER_POSITION_AND_ROTATION`) via `WrapperPlayClientPlayerFlying` | position, rotation, onGround, horizontal collision, ticks since last |
| C `CLIENT_TICK_END` | client tick counter (only trusted when the client protocol >= 768 (1.21.2), or newer than PacketEvents knows) |
| S `ENTITY_VELOCITY` (own entity id), S `EXPLOSION` (player knockback) | pending velocity {vector, time} |
| S `PLAYER_ABILITIES`, C `PLAYER_ABILITIES` | flying, allowFlight, flySpeed, creative/invulnerable |
| S `CHANGE_GAME_STATE` reason game mode | game mode |
| S `ENTITY_EFFECT` / `REMOVE_ENTITY_EFFECT` (own id) | speed, slowness, jump_boost, levitation, slow_falling, dolphins_grace |
| S `UPDATE_ATTRIBUTES` (own id) | movement_speed (excluding the sprint modifier `minecraft:sprinting`), jump_strength, gravity, step_height, scale, entity_interaction_range, block_interaction_range |
| S `ENTITY_METADATA` (own id) | shared flags: sneaking, sprinting, swimming, gliding; living flags: using item, riptide |
| C `ENTITY_ACTION` | sprint start/stop, elytra start, leave bed |
| C `PLAYER_INPUT` (1.21.2+) | sneak/sprint input flags |
| S `SET_PASSENGERS`, S `ATTACH_ENTITY`, C `VEHICLE_MOVE` | in vehicle |
| S `OPEN_WINDOW`, S/C `CLOSE_WINDOW` | open container id + type |
| S `KEEP_ALIVE` / C `KEEP_ALIVE` | keep-alive RTT (ids matched) |
| S `PING` / C `PONG` (sent by other plugins) | passive pong RTT (ids matched); PandaEye never sends PING |
| C `INTERACT_ENTITY` (ATTACK), C `ANIMATION`, C `PLAYER_BLOCK_PLACEMENT`, C `PLAYER_DIGGING`, C `CLICK_WINDOW`, C `USE_ITEM`, C `HELD_ITEM_CHANGE` | combat / world / inventory events for the cloud and protocol checks |

A per-server `EntityIndex` maps entity id -> player state for tracked players (filled on join, removed on
quit) so attacks can attach the target's position history.

`PositionHistory`: per player, lock-free ring of the last 64 received positions `(time_us, x, y, z)`,
written by the owner's netty thread, readable from other threads (immutable entries + volatile head).

Ping estimate per player: median of the last 5 pong RTTs if any in the last 30 s, else the last
keep-alive RTT, else 100 ms.

### 4.4 Environment sampler (region thread, 2 Hz)

One repeating task per player (`player.getScheduler().runAtFixedRate(plugin, task, retired, 10, 10)`).
Computes `EnvFlag` bits (wire.proto) with cheap API calls: `isInWater`, `isInLava`, `isClimbing`,
`isGliding`, `isRiptiding`, `isSwimming`, `isInsideVehicle`, block types at feet / below feet (ice family,
slime, honey, cobweb, powder snow, bubble column, soul sand/soil), NEAR_SOLID (any block with a non-empty
collision shape in the hitbox expanded by 1), NEAR_ENTITY (`getNearbyEntities(1.5, 1.5, 1.5)` non-empty).
Publishes `EnvSnapshot{flags, time}` to the player's state (volatile) and keeps "last time each flag was
set" so checks can ask "was the player in water within the last 1000 ms". Sends an `EnvSample` event
when flags change and at least every 5 s. Must cost < 20 microseconds per sample.

### 4.5 Local checks and deferred confirmation

Local checks (algorithms in CHECKS.md): `timer.balance`, `movement.speed`, `movement.fly`,
`movement.step`, `movement.groundspoof`, `protocol.pitch`, `protocol.position`, `protocol.slot`,
`protocol.selfhit`, `protocol.cursor`, `protocol.flood`, `protocol.abilities`, `inventory.closed`.

- Checks run on the netty thread with packet state only. Movement checks that need the world produce a
  *candidate*; the candidate is confirmed on the player's region thread one tick later by looking at the
  blocks/entities around the recorded positions (`Confirmation`). Max 4 outstanding confirmations per
  player and 10 per second per player; extra candidates are dropped (counted). A candidate is
  discarded if the player's current position is more than 16 blocks from the candidate position.
- A confirmed violation increases the check's VL, is logged, alerts (4.9), and is sent as a `LocalFlag`
  event. VLs decay (CHECKS.md per check). In `live` mode a confirmed movement violation above the
  check's setback VL sets the player back to the last legitimate position with `player.teleportAsync`.
- Exempt players: Bedrock (Floodgate API via reflection, or UUID with most significant bits == 0),
  `pandaeye.bypass` permission, creative/spectator for movement, dead players, players in warm-up.
  Exempt players still produce events (marked `exempt`) but no flags.

### 4.6 Telemetry and transport (`plugin/common`)

- Event selection: `MoveSample` for every flying packet while the player is in *focus* (attacked or was
  attacked in the last 5 s, a local flag in the last 30 s, a `WatchRequest` active, or `telemetry.full`
  is on); otherwise at 2 Hz (`ticks_since_last` tells the cloud how many ticks were skipped). All other
  event kinds are always sent. `StateChange` on join and on change.
- `EventSink`: lock-free MPSC queue (bounded 100k events; overflow drops newest and counts).
- `CloudClient` (one daemon thread `PandaEye-Cloud` + the JDK `HttpClient` WebSocket):
  - every `batch-interval-ms` (default 50) drains up to `max_batch_events` events into a `Batch`,
    serialises `ClientFrame`, compresses with aircompressor zstd when > 256 bytes, and sends. Only one
    WebSocket send may be outstanding; sends are chained.
  - keeps unacked batch bytes in a `PendingBuffer` bounded by `max-buffer-mb` (default 64) and
    `max-buffer-age` (default 10 min); overflow drops the oldest and counts.
  - reconnects with exponential backoff 1 s -> 60 s with +-20% jitter; after a fatal
    `CODE_UNAUTHENTICATED` waits 10 minutes; logs state changes once.
  - while disconnected, events keep accumulating into batches in the PendingBuffer (degraded mode).
  - `Hello.server_id` is a UUID kept in `plugins/PandaEye/server-id`; batch seq persists in
    `plugins/PandaEye/state.properties` (`next-seq`, `clean-shutdown=true|false`; +1 000 000 after an
    unclean start).
- The JDK WebSocket and executors are closed on disable (`HttpClient.close()`), no thread may survive.

### 4.7 Verdict handling

On `Verdict`: log it; alert staff; if `shadow` (local mode shadow, or `RemoteConfig.enforcement` false,
or `verdict.shadow` true) -> `VerdictAck{executed=false, note="shadow"}` and stop. Otherwise:
- ALERT: alert only.
- MITIGATE: set the player's mitigation set + strength (cleared on quit) -> listeners in 4.8 apply it.
- UNMITIGATE: clear it.
- KICK: run `punishments.kick-command` if the player is online.
- BAN: run `punishments.ban-command` (works for offline players too).
- UNBAN: run `punishments.unban-command`.
Placeholders: `%player%`, `%uuid%`, `%reason%`, `%checks%`, `%verdict_id%`, `%duration%`. All
placeholder values have line breaks and `§`/`&` colour codes stripped (commands must be single line).
Commands run on the global region scheduler via `Bukkit.dispatchCommand(console, cmd)`.

### 4.8 Mitigations (live mode only; listeners are not registered in shadow mode)

| Mitigation | Implementation |
|---|---|
| HIT_CANCEL | `EntityDamageByEntityEvent` (damager = mitigated player): cancel with probability 0.15 + 0.35*strength |
| DAMAGE_REDUCE | same event, `setDamage(damage * (1 - 0.3*strength))` |
| VELOCITY_BOOST | `PlayerVelocityEvent` for the mitigated player: multiply velocity by 1 + 0.25*strength |
| SETBACK | confirmed movement flags set the player back even below the setback VL |
| PLACE_CANCEL | `BlockPlaceEvent`: cancel with probability 0.2 + 0.4*strength |

### 4.9 Alerts and logs

- Console: one line per confirmed flag, throttled per player per check (max 1 line per 5 s, with the
  count of suppressed flags). Prefix `[PandaEye]`.
- In game: players with `pandaeye.alerts` who toggled alerts on (`/pandaeye alerts`). Default: on for
  holders of the permission in `live` mode, off in `shadow` mode.
- Flag log: `plugins/PandaEye/logs/flags-YYYY-MM-DD.jsonl` (one JSON object per confirmed flag: time,
  player, uuid, check, vl, info, data, ping, client version, mode), written by a single async writer
  thread, retained 14 days. This is what the shadow rollout uses to measure false positives.

### 4.10 Commands and permissions

`/pandaeye status | alerts [on|off] | player <name> | debug <name> | reload` (alias `/peye`).
`status` shows mode, cloud state, connection RTT, probe latency p50/p95, pending batches, dropped events,
tracked players, packet-handler cost (avg and p99 microseconds per packet). `reload` reloads thresholds and
alert settings only; `mode` and `cloud.*` need a restart (or plugman reload).
Permissions: `pandaeye.command` (op), `pandaeye.alerts` (op), `pandaeye.admin` (op),
`pandaeye.bypass` (default false).

### 4.11 `config.yml` (defaults)

```yaml
# shadow = observe only. Never sends, cancels or modifies packets, never punishes or mitigates.
# live   = enforcement allowed when the cloud also allows it.
mode: shadow
server-name: ""            # shown in the dashboard; empty = the server folder name
network-id: ""             # set the same value on every backend and the proxy of one network
cloud:
  enabled: true
  endpoint: "wss://cloud.pandaeye.dev/v1/connect"
  license-key: ""
  batch-interval-ms: 50
  max-buffer-mb: 64
  max-buffer-age-minutes: 10
  send-ip-hash: false
telemetry:
  full: false              # true = every movement packet for every player
  focus-seconds: 5
  idle-sample-hz: 2
checks:                    # every check: enabled, and the thresholds from CHECKS.md
  timer.balance: { enabled: true }
  movement.speed: { enabled: true }
  movement.fly: { enabled: true }
  movement.step: { enabled: true }
  movement.groundspoof: { enabled: true }
  protocol.pitch: { enabled: true }
  protocol.position: { enabled: true }
  protocol.slot: { enabled: true }
  protocol.selfhit: { enabled: true }
  protocol.cursor: { enabled: true }
  protocol.flood: { enabled: true }
  protocol.abilities: { enabled: true }
  inventory.closed: { enabled: true }
alerts:
  console: true
  in-game-default: false
  log-file: true
exempt:
  bedrock: true
setbacks:
  enabled: true            # live mode only
punishments:
  kick-command: "kick %player% Unfair advantage detected"
  ban-command: "ban %player% Unfair advantage (%verdict_id%)"
  unban-command: "pardon %player%"
mitigations:
  enabled: true            # live mode only
debug: false
```

## 5. Cloud (`cloud/`)

### 5.1 Binary

`pandaeye <command> [flags]`:

| Command | Runs |
|---|---|
| `all` | embedded NATS JetStream + gateway + worker (all partitions) + verdict (all partitions) + api in one process (small deployments, Pterodactyl, tests) |
| `gateway` | WebSocket ingest + verdict delivery |
| `worker` | analysis workers for leased partitions |
| `verdict` | verdict service for leased partitions |
| `api` | REST API (dashboard + public API + status) and retention janitor |
| `migrate` | apply database migrations and exit |
| `replay` | re-run checks over stored stream data or a clip (`--player`, `--since`, `--until`) and print signals |
| `version` | print version |

Configuration: environment variables (all `PE_` prefixed), optionally a YAML file (`--config`) with the
same keys in lower case (`PE_GATEWAY_LISTEN` = `gateway.listen`). Env overrides file. Keys:

| Key | Default | Meaning |
|---|---|---|
| `PE_GATEWAY_LISTEN` | `:8080` | gateway HTTP listen address (`/v1/connect`, `/healthz`, `/metrics`, `/status`) |
| `PE_API_LISTEN` | `:8081` | API listen address |
| `PE_PUBLIC_URL` | `` | dashboard base URL for evidence links |
| `PE_NATS_URL` | `` | external NATS; empty in `all` = embedded server |
| `PE_NATS_DIR` | `./data/nats` | embedded JetStream storage dir |
| `PE_PARTITIONS` | `16` | partition count (fixed per deployment; stored in KV, mismatch = refuse to start) |
| `PE_DB_DRIVER` | `sqlite` | `sqlite` or `postgres` |
| `PE_DB_DSN` | `./data/pandaeye.db` | DSN |
| `PE_CLICKHOUSE_DSN` | `` | empty = telemetry disabled (no-op sink) |
| `PE_LICENSE_MODE` | `static` | `static` or `pandascript` |
| `PE_LICENSE_URL` | `` | pandascript license worker base URL |
| `PE_LICENSE_PRODUCT` | `pandaeye` | product id |
| `PE_STATIC_LICENSES` | `` | `key[:maxServers],key2...` for static mode |
| `PE_SERVICE_TOKEN` | `` | token the dashboard worker uses for the API |
| `PE_EVENTS_MAX_AGE` | `24h` | JetStream PE_EVENTS retention |
| `PE_EVENTS_MAX_BYTES` | `10GiB` | |
| `PE_REGION` | `local` | shown on /status |
| `PE_LOG_LEVEL` | `info` | |
| `PE_ML_MODEL` | `` | ONNX model path (only with `-tags onnx`) |

Logging: `log/slog` JSON to stdout. Graceful shutdown on SIGINT/SIGTERM (drain consumers, close
connections with `Error{CODE_SHUTTING_DOWN}`, flush stores) within 10 s.

### 5.2 Packages

```
internal/wire/pandaeyev1   generated protobuf (protoc-gen-go), committed
internal/codec             WebSocket message framing: header byte, zstd encode/decode with limits
internal/config            config loading/validation
internal/license           Verifier interface; static, pandascript (HTTP, cached), memoizing wrapper
internal/gateway           WebSocket server, per-connection session, rate limits, outbound delivery
internal/stream            NATS connection/embedded server, stream+consumer provisioning, partitioning,
                           partition leases (KV), publish/consume helpers
internal/geom              Vec3, AABB, ray/AABB intersection, angles, eye heights
internal/worker            partition consumer loop, PlayerState, check registry, clip ring buffer
internal/checks            cloud checks (one file per family) + shared stats helpers
internal/verdict           scoring engine, leniency, action policy, evidence, consumer loop, discord
internal/store             Store interface + model types
internal/store/sqlstore    database/sql implementation for postgres (pgx stdlib) and sqlite (modernc),
                           embedded migrations per dialect
internal/telemetry         Sink interface; clickhouse implementation; no-op
internal/api               REST API, auth middleware, status endpoint, retention janitor
internal/ml                feature windows; Scorer interface; onnx implementation behind build tag `onnx`
internal/metrics           Prometheus collectors (latency histograms per stage, counters)
internal/testkit           fake plugin client (speaks the wire protocol), event builders, helpers
```

### 5.3 Gateway

- `GET /v1/connect` upgrades to WebSocket (coder/websocket, read limit 1 MiB + 1). Optional
  `X-Forwarded-For` honoured only from `PE_TRUSTED_PROXIES` CIDRs.
- Session: read Hello (10 s timeout) -> verify license (5.9) -> upsert tenant + server -> enforce
  `maxServers` (distinct connected server_ids per tenant, tracked in NATS KV `PE_CONNECTIONS` with TTL
  30 s renewed every 10 s; a second connection with the same server_id replaces the first, which gets
  `Error{CODE_BAD_REQUEST,"replaced by a new connection"}` and is closed) -> subscribe core NATS
  `pe.out.<server_id>` -> send Welcome (RemoteConfig from the tenant row, last_acked_seq from the server
  row) -> deliver pending verdicts for the server (created in the last 24 h, not yet acked; KICK verdicts
  older than 5 min are skipped and marked expired).
- Per batch: decode; reject (non-fatal error + ack) if more than `max_batch_events` events; validate
  every event (player must be 16 bytes or empty for probes, time sane); split events by partition
  (`partition(uuid)`; empty player -> partition 0) keeping order; publish one `StreamBatch` per non-empty
  partition with JetStream msg id `<server_id>:<seq>:<partition>`; wait for all publish acks
  (`PublishAsync` + wait, 5 s timeout -> close connection with `CODE_INTERNAL` so the plugin resends);
  send BatchAck; update `servers.last_batch_seq` (coalesced, at most once per second per server).
- Rate limits: per connection 20 000 events/s (burst 100 000) and 2 MiB/s; per source IP 10 new
  connections/min; per tenant 30 Hellos/min. Exceeding = `Error{CODE_RATE_LIMITED}` (fatal for
  connection floods, non-fatal + dropped batch with ack for event floods).
- `VerdictAck` -> `store.AckVerdict(id, executed, note)`.
- Usage metering: per tenant, the gateway keeps the current number of online players (from join/quit
  events, per server) and writes `usage_daily(tenant, day, peak_players)` (max) once per minute.
- `/status` (public JSON): region, version, uptime, connected servers (count), verdict latency
  p50/p95/p99 over the last hour from probe results, SLO target and whether it is met.
- `/metrics`: Prometheus. Histograms: `pandaeye_probe_rtt_seconds{stage}` with stages
  `gateway_to_worker`, `worker_to_verdict`, `verdict_to_gateway`, `total`; counters for batches,
  events, drops, auth failures, connections.

### 5.4 Event stream (NATS JetStream)

| Name | Subjects | Settings |
|---|---|---|
| stream `PE_EVENTS` | `pe.events.*` | file storage, limits retention, `PE_EVENTS_MAX_AGE`, `PE_EVENTS_MAX_BYTES`, discard old, duplicates 10m |
| stream `PE_SIGNALS` | `pe.signals.*`, `pe.probes.*` | file storage, max age 7 d, duplicates 10m |
| KV `PE_LEASES` | `events.<NN>`, `verdict.<NN>` | TTL 15 s |
| KV `PE_CONNECTIONS` | `<tenant>.<server_id>` | TTL 30 s |
| KV `PE_META` | `partitions` | no TTL |
| core subject | `pe.out.<server_id>` | gateway subscribes while connected |

- `partition(uuid) = fnv1a32(16 uuid bytes) % PE_PARTITIONS`, two-digit zero-padded in subjects
  (`pe.events.07`). Java does not need it (routing happens in the gateway).
- Workers: durable pull consumer `worker-<NN>` on `PE_EVENTS` with filter `pe.events.<NN>`,
  `AckPolicy=All`, `MaxAckPending=10000`, `AckWait=30s`, deliver all. A worker processes a partition
  strictly sequentially and acks the last processed message every 100 messages or 200 ms.
  Verdict services: durable consumers `verdict-<NN>` (filter `pe.signals.<NN>`) and `probe-<NN>`
  (filter `pe.probes.<NN>`) on `PE_SIGNALS`, same settings.
- Leases: a process leases a partition by `kv.Create("events.07", owner)`; renews with
  `kv.Update(key, owner, lastRevision)` every 5 s; on renewal failure it stops consuming that partition
  immediately. `--partitions all|auto|0-7,12` (auto = take free leases up to `--max-partitions`,
  default all). In `all` mode one process leases everything. One partition is consumed by exactly one
  process, so one worker owns a player ("sticky by UUID").

### 5.5 Worker

- `PlayerState` keyed by (tenant_id, player uuid); per-session sub-state (positions, rotations, pending
  attacks) resets when `server_id` changes or on PlayerJoin; statistical accumulators (click stats,
  aim calibration) persist across servers of the same tenant. Evicted 10 min after quit or 15 min of
  silence. Max 50 000 players per worker (LRU eviction beyond).
- For each event: update shared derived state (last move, rotation deltas, tick, env flags, game mode,
  attributes, combat timestamps, latency estimates), then call every check registered for that event
  kind. Checks emit signals through `ctx.Emit(check, points, confidence, info, data)`; the worker
  fills Origin, timestamps, player name and publishes to `pe.signals.<NN>` (msg id
  `<server_id>:<seq>:<partition>:<event index>:<check>`).
- LocalFlag events are forwarded as signals (`local=true`) with points from CHECKS.md.
- Probe events -> publish `ProbeHop` to `pe.probes.<NN>` immediately.
- Clips: ring buffer of the player's last 30 s of events (max 4 000). When a signal with points >= 5 is
  emitted and no clip was saved for the player in the last 60 s, the worker saves the last 25 s as a
  clip (`store.SaveClip`, async bounded queue, zstd-compressed protobuf `ClipData{repeated Event}`)
  and sets `Signal.clip_id`.
- Feature windows (30 s per active player) go to the telemetry sink for ML (5.10) and to the ML scorer
  when a model is loaded.

### 5.6 Verdict service

- State per (tenant, player): per-check accumulator `acc_c` with exponential decay (half-life per check,
  CHECKS.md), last signal confidence per check, actions already taken this session, last alert time.
- On signal: `acc_c = decay(acc_c) + points`; player score `S = sum_c min(acc_c, cap_c)`;
  confidence `C = 1 - prod_c (1 - conf_c)` over checks with `acc_c >= 1`.
- Thresholds (base, leniency 3): ALERT 15, MITIGATE 30, KICK 70, BAN 120. Leniency multiplier
  L1 0.6, L2 0.8, L3 1.0, L4 1.3, L5 1.7. Default leniency 4.
- Actions (each at most once per player session, except ALERT):
  - ALERT when S crosses ALERT, then again each time S grows by 50% since the last alert, max 1/min.
  - MITIGATE when S >= MITIGATE and tenant `mitigations` on. Mitigations by dominant category:
    HEURISTICS/AUTO_CLICKER -> HIT_CANCEL + DAMAGE_REDUCE + VELOCITY_BOOST; MOVEMENT/TICK_SPEED ->
    SETBACK; WORLD_INTERACTION -> PLACE_CANCEL; others -> HIT_CANCEL.
    strength = clamp((S - MITIGATE)/(KICK - MITIGATE), 0.3, 1).
  - KICK when S >= KICK, tenant `autokick`, C >= 0.8, and the player is online.
  - BAN when S >= BAN, tenant `autoban`, C >= 0.9, and either >= 2 distinct checks with `acc_c >= 5`
    or one check marked `certain` in CHECKS.md with `acc_c >= BAN/2`. Signals from hot-loaded sessions
    count half for the first 60 s; replayed signals older than 10 min never trigger KICK/BAN alone.
- Every verdict >= ALERT is stored with evidence: top 20 contributing signals (check, points,
  confidence, info, data, time, clip_id), score, confidence, thresholds used, leniency; published to
  `pe.out.<server_id>` (the server of the newest signal). `shadow` = tenant `enforcement` is false OR
  the origin's `shadow_mode` is true.
- BAN verdicts also create a `bans` row and update reputation; tenants with `reputation_opt_in` share.
- Discord: tenant webhook (optional) receives an embed for verdicts >= `discord_min_action` (default
  MITIGATE): player, server, action, score, confidence, top 5 checks, evidence link. No IPs. Max 10
  posts/min per tenant, 429-aware retry, failures logged once until success.
- Probes: on `ProbeHop`, publish `Outbound{probe_result}` to `pe.out.<server_id>` immediately.

### 5.7 Data stores

`store.Store` (interface) implemented by `sqlstore` for **postgres** and **sqlite** (identical semantics;
the test suite runs against both; postgres tests run when `PE_TEST_POSTGRES_DSN` is set). Tables:

| Table | Columns (types are logical) |
|---|---|
| `schema_migrations` | version int pk, applied_at |
| `tenants` | id text pk, name, license_hash text unique, leniency int default 4, autoban bool default false, autokick bool default false, mitigations bool default true, enforcement bool default false, reputation_opt_in bool default false, discord_webhook text, discord_min_action int default 2, plan text default 'free', created_at, updated_at |
| `servers` | id text pk (plugin server_id), tenant_id fk, name, network_id, platform, platform_version, plugin_version, minecraft_version, last_seen_at, last_batch_seq bigint, online bool, created_at |
| `players` | uuid text pk, last_name, first_seen_at, last_seen_at |
| `player_names` | uuid, name, seen_at (pk uuid+name) |
| `verdicts` | id text pk, tenant_id, server_id, player_uuid, player_name, action int, category int, score real, confidence real, reason, checks text(json array), evidence text(json), shadow bool, status text ('open','confirmed','overturned','expired'), created_at, delivered_at, executed bool, ack_note |
| `bans` | verdict_id pk, tenant_id, player_uuid, active bool, reason, created_at, overturned_at, overturned_by, overturn_note |
| `appeals` | id text pk, tenant_id, verdict_id, player_uuid, message, contact, status ('open','accepted','denied'), created_at, decided_at, decided_by, decision_note |
| `labels` | id text pk, tenant_id, player_uuid, verdict_id, label ('cheat','legit'), source ('confirmed','overturned','manual'), window_start, window_end, created_at |
| `clips` | id text pk, tenant_id, server_id, player_uuid, start_us, end_us, codec int, data blob, created_at |
| `api_keys` | id text pk, tenant_id, name, key_hash text unique, created_at, last_used_at, revoked_at |
| `reputation` | player_uuid pk, risk real, networks_flagged int, confirmed_bans int, updated_at |
| `usage_daily` | tenant_id, day (date text), peak_players int (pk tenant+day) |

Retention (janitor in `api`, daily): clips 90 d, verdicts and evidence 180 d (bans kept while active),
appeals 1 y, labels 2 y, usage 2 y. JetStream `PE_EVENTS` 24 h. ClickHouse TTLs below.

ClickHouse (optional, `internal/telemetry`): tables `signals` (TTL 30 d), `verdicts` (90 d),
`probe_latency` (30 d), `feature_windows` (180 d), `sessions` (90 d), MergeTree ordered by
(tenant_id, time). The sink batches inserts (flush every 1 s or 10 000 rows) and drops on overflow.

### 5.8 API (`api` command)

JSON over HTTP, prefix `/api/v1`. Auth: `Authorization: Bearer <tenant api key>` (tenant scope) or
`Authorization: Bearer <PE_SERVICE_TOKEN>` + `X-PandaEye-Tenant: <tenant id>` (dashboard). Errors:
`{"error": "...", "code": "..."}` with proper status codes. Pagination: `?limit=` (max 200) +
opaque `cursor`.

| Method + path | Purpose |
|---|---|
| `POST /api/v1/auth/license` (service token only) | body `{license_key}` -> verifies with the license verifier -> `{tenant_id}` (creates the tenant row) |
| `GET /api/v1/tenant`, `PATCH /api/v1/tenant` | settings: leniency, autoban, autokick, mitigations, enforcement, reputation_opt_in, discord_webhook, discord_min_action, name (PATCH pushes `RemoteConfig` to connected servers via `pe.out.<server_id>`) |
| `GET /api/v1/servers` | servers with online state, versions, last seen |
| `GET /api/v1/verdicts` | filters: action, status, player, server, since, until |
| `GET /api/v1/verdicts/{id}` | verdict + evidence |
| `POST /api/v1/verdicts/{id}/confirm` | label `cheat` |
| `POST /api/v1/verdicts/{id}/overturn` | body `{by, note}`: verdict overturned, ban inactive, label `legit`, publishes `Verdict{UNBAN}` (and UNMITIGATE) to the player's last server |
| `GET /api/v1/clips/{id}` | decoded clip as JSON (events with kind + fields) for the replay viewer |
| `GET /api/v1/players/{uuid}`, `GET /api/v1/players?name=` | player summary: names, last seen, verdicts, bans, reputation (if opted in) |
| `DELETE /api/v1/players/{uuid}` | GDPR erase for this tenant: verdicts, bans, appeals, labels, clips; returns counts |
| `GET /api/v1/bans?active=true` | bans |
| `GET /api/v1/appeals`, `POST /api/v1/appeals/{id}/decide` | decide `{decision: accept|deny, by, note}`; accept = overturn |
| `POST /api/v1/public/appeals` (no auth) | `{verdict_id, message, contact}`; rate limited 5/h per IP; verdict must be a BAN |
| `GET /api/v1/stats/overview` | counts by action/category for 24 h / 7 d, online servers, players |
| `GET /api/v1/stats/latency` | probe latency percentiles 1 h / 24 h |
| `GET /api/v1/usage` | peak players per day (90 d), plan, free-tier limit (30 peak players) |
| `POST/GET/DELETE /api/v1/apikeys` | manage API keys (plaintext returned once; stored as sha256) |
| `GET /api/v1/export/labels?since=` | NDJSON of labels joined with verdict evidence (ML training) |
| `GET /status` (no auth) | same document as the gateway's /status, aggregated from the store |

### 5.9 Licensing

- `Verifier.Verify(ctx, key) (License{TenantID, MaxServers, Product}, error)`.
- `pandascript`: `GET {PE_LICENSE_URL}/api/client/verify?product={PE_LICENSE_PRODUCT}` with
  `Authorization: Bearer <key>`; 200 `{"valid":true,"productId":...,"maxServers":N,"currentServers":M}`,
  401 `{"valid":false,"message":...}`. Cache valid results 10 min, invalid 1 min (keyed by sha256 of the
  key). On network errors / 5xx: allow if a valid result is cached within 24 h (degraded), else fail with
  a retryable error.
- `static`: keys from `PE_STATIC_LICENSES`.
- `tenant_id = "t_" + hex(sha256(key))[:24]`. Raw keys are never stored or logged.

### 5.10 ML

- `internal/ml/features.go` defines the feature vector (names and order in `ml/FEATURES.md`, versioned)
  computed per player per 30 s window from worker state; written to telemetry `feature_windows`.
- `Scorer` interface; default build = disabled; `-tags onnx` builds an ONNX Runtime implementation
  (github.com/yalue/onnxruntime_go, needs the shared library at runtime). When enabled it emits
  `ml.<model>` signals; their points are 0 unless `PE_ML_POINTS` > 0 (shadow by default).
- `ml/` (Python): dataset export from the API label export + ClickHouse feature windows, training
  (gradient boosting), ONNX export, and `evaluate.py` gate: the model must beat the heuristic baseline
  on a held-out set at an equal or lower false-positive rate, else exit 1. A synthetic data generator
  exists only to test the pipeline and is labelled as such.

## 6. Velocity plugin (`plugin/velocity`)

- Connects with platform VELOCITY (same transport as Paper, `plugin/common`), config
  `plugins/pandaeye/config.yml` (license, endpoint, network-id, mode).
- Events: PlayerJoin (PostLoginEvent: name, protocol, network_session = new random UUID, bedrock via
  Floodgate reflection or UUID msb == 0), brand (PlayerClientBrandEvent -> second PlayerJoin with brand),
  ServerSwitch (ServerConnectedEvent), PlayerQuit (DisconnectEvent).
- Sends the network session to the backend on plugin channel `pandaeye:session` after each server
  connect; the Paper plugin reads it and sends a PlayerJoin update with `network_session`. Messages from
  clients on that channel are dropped at the proxy.
- Verdicts: in live mode the proxy can run proxy commands for BAN/KICK (config), default shadow/log only.

## 7. Dashboard (`dashboard/`, Cloudflare Worker)

- TypeScript + Hono, server-rendered HTML with small client scripts (replay viewer on `<canvas>`), D1
  binding `DB`, secrets `PANDAEYE_API_URL`, `PANDAEYE_SERVICE_TOKEN`, `SESSION_SECRET`.
- D1 tables: `users` (id, tenant_id, username unique per tenant, password_hash = PBKDF2-SHA256 210 000
  iterations + 16-byte salt, role owner|admin|staff|viewer, created_at, disabled), `sessions` (id = 32
  random bytes hex, user_id, csrf, expires_at, created_at), `audit_log` (id, tenant_id, user_id, action,
  target, created_at), `login_attempts` (key, count, window_start) for rate limiting.
- Flows: `/setup` (license key -> API `auth/license` -> tenant -> create owner), `/login`, `/logout`.
  Cookies HttpOnly, Secure, SameSite=Lax; CSRF token on every POST; login rate limit 10 / 15 min.
- Pages: overview, servers, verdicts (filters), verdict detail (evidence table, confidence, clip replay
  viewer: top-down and side view of the suspect and target positions, rotations as view rays, attacks and
  placements as markers, timeline scrubber), players, bans, appeals (one-click overturn = label),
  settings (leniency, autoban, autokick, mitigations, enforcement, reputation, Discord), users (owner),
  API keys, usage, public `/status` page, public `/appeal` form, `/privacy`.
- Roles: viewer reads; staff confirm/overturn/decide appeals; admin settings; owner users + API keys.

## 8. Testing

- Go: `go test -race ./...`; every check has legit and cheat fixtures; gateway integration test with the
  testkit client (auth failure, limit, rate limit, ack/resend/dedup, verdict delivery, pending verdicts
  after reconnect, probe round trip); store tests for sqlite and (if available) postgres.
- Java: JUnit 5 for every local check (simulated packet sequences, legit and cheat), codec (frames
  produced by Java decode in Go and vice versa via committed fixtures in `test/fixtures/`), pending
  buffer and seq logic.
- E2E (`test/e2e`): cloud `all` + real Folia 1.21.11 and Paper 1.21.11 servers with PacketEvents 2.13.x,
  ViaVersion/ViaBackwards and PlugManX; legit bots must produce no confirmed flags and never be kicked;
  cheat clients must be detected per check; `plugman unload/load` cycles must leave no threads or errors;
  the probe RTT must be reported.

## 9. Privacy

See [`PRIVACY.md`](PRIVACY.md). Collected: player UUID, name, client version/brand, gameplay telemetry
(positions, rotations, clicks, placements, inventory clicks, latency). IPs are not collected unless the
owner enables `send-ip-hash` (then only a keyed hash). Retention as in 5.7. Erase per player per tenant
through the API; reputation rows are erased when no tenant holds data about the player anymore.
