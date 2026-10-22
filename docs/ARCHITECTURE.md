# PandaEye architecture (implementation spec, v2)

This document is the contract every PandaEye component is built against. If code and this document
disagree, one of them is a bug. Companion documents: [`CHECKS.md`](CHECKS.md) (every detection check),
[`../proto/pandaeye/v1/wire.proto`](../proto/pandaeye/v1/wire.proto) (plugin <-> cloud protocol) and
[`../proto/pandaeye/v1/stream.proto`](../proto/pandaeye/v1/stream.proto) (internal bus payloads).
v2 incorporates an adversarial design review (physics, live-server safety, distributed systems,
security/privacy, API feasibility, blueprint coverage).

## 1. What PandaEye is

A cloud anticheat for Minecraft Java servers, split like Polar:

| Runs where | What | Why |
|---|---|---|
| Plugin (Paper/Folia, Velocity) | Packet capture, same-tick checks (movement, timer, invalid protocol), setbacks, applying mitigations and punishments | Must act within the tick |
| Cloud (Go) | Combat heuristics, clicks, aim, reach, scaffold, inventory, latency abuse, verdicts, evidence, reputation | Detection delay is fine; heavy math leaves the game server; detection logic never ships in the jar |
| Dashboard (Cloudflare Worker + D1) | Accounts, evidence, replays, appeals, settings, usage, public status page | Reads stored results only, never on the packet hot path |

Loop: plugin streams events -> gateway -> event stream -> workers (score) -> verdict service (decide) ->
gateway -> plugin (apply). Suspicion produces MITIGATE, certainty produces BAN.

Better than Polar by construction: evidence (checks, confidence, replay clip) on every KICK/BAN; degraded
mode (local checks keep running, buffered events are judged on reconnect); pure JVM plugin (no native
libraries, ARM works); free tier + per-peak-player metering; opt-in reputation network; appeal -> label
loop; published latency SLO + public status; Discord alerts; Velocity awareness; public API.

### 1.1 Hard safety invariants (shadow mode)

The plugin has a local `mode`: `shadow` (default) or `live`. In `shadow` mode the plugin MUST:

1. never send a packet to any client (no transactions/pings/keep-alives of its own, no resyncs, no
   registered plugin channels that change the client's channel list);
2. never cancel or modify any packet, and never leave a PacketEvents wrapper attached to an event
   (section 4.2 "Wrapper hygiene"); the listener runs at `MONITOR`;
3. never cancel or modify any Bukkit event, never teleport/set back, never change velocity;
4. never run a punishment command, never apply a mitigation (one exception: UNBAN of a ban this server
   executed, 4.7);
5. never touch the world off the owning thread (Folia rules), never block a netty, region or global thread
   (onDisable completes in <= 300 ms);
6. catch every Throwable in its callbacks (one bad packet must never disconnect a player);
7. bound every queue, file and log (no disk or heap growth under a false-positive storm);
8. cost < 2% of one CPU core at 200 players.

Grim's shadow mode on a production network caused mass "out of order confirmation" kicks because it still
sent confirmation packets; PacketEvents with `reEncodeByDefault` re-serialises any packet a plugin wrapped.
PandaEye's shadow mode sends nothing and changes nothing. Enforcement (setbacks, mitigations, kicks, bans)
requires local `mode: live` AND the last-known `RemoteConfig.enforcement = true` (persisted with a timestamp;
false until a Welcome has ever said otherwise) AND the feature's own switch; verdicts with `shadow = true`
are never enforced.

## 2. Repository layout and toolchains

```
proto/pandaeye/v1/        wire.proto, stream.proto (single source of truth for both languages)
plugin/                   Gradle multi-project (Kotlin DSL), Java 21
  protocol/               generated protobuf-lite classes (package dev.pandascript.pandaeye.protocol.v1)
  common/                 platform-independent: transport, codecs, batching, spool, clocks, util
  paper/                  Paper + Folia plugin "PandaEye"
  velocity/               Velocity plugin "pandaeye"
cloud/                    Go module github.com/jackh54/PandaEye/cloud
  cmd/pandaeye/           single binary, subcommands (5.1)
  internal/...            see 5.2
dashboard/                Cloudflare Worker (TypeScript, Hono) + D1 migrations
ml/                       Python training pipeline (features, training, ONNX export, gate evaluation)
deploy/                   Dockerfile, docker-compose, systemd unit, Pterodactyl notes
test/e2e/                 local end-to-end harness: real Paper/Folia servers + bots + cheat clients
test/fixtures/            cross-language codec fixtures
docs/                     ARCHITECTURE, CHECKS, PROTOCOL, OPERATIONS, PRIVACY, SLO
```

Naming: Java base package `dev.pandascript.pandaeye`; shaded libraries relocate to
`dev.pandascript.pandaeye.libs.<lib>`. Product code, configs and docs never name a specific customer network.

Toolchains (pinned): Java 21 (`--release 21`), Gradle 8.14, `com.gradleup.shadow` 9.2.2 (the last 9.x that runs on
Gradle 8), protobuf gradle plugin
0.9.5, protoc + protobuf-javalite 4.36.2, aircompressor 0.27 (pure-Java zstd, with a DEFLATE fallback),
paper-api `1.21.4-R0.1-SNAPSHOT` (compileOnly), packetevents-spigot `2.13.0` (compileOnly; runtime = the
server's PacketEvents plugin, 2.13.x), velocity-api `3.4.0` (compileOnly). Go 1.26 (`toolchain go1.26.10`),
google.golang.org/protobuf, nats-server/v2 (embedded, in-process only), nats.go `jetstream`,
github.com/coder/websocket, klauspost/compress/zstd, jackc/pgx/v5, modernc.org/sqlite, ClickHouse/clickhouse-go/v2,
prometheus/client_golang, dchest/siphash. Pin the newest versions that build with Go 1.26.

## 3. Protocol summary

Authoritative field list: wire.proto. Key rules:

- One WebSocket per server at `wss://<host>/v1/connect` (plugins refuse `ws://` except to loopback, or when
  `cloud.allow-insecure: true` for a same-host/private network, with a warning at enable).
  Binary messages only. Byte 0 = codec (`0x00` raw, `0x01` zstd frame with content size, `0x02` raw
  DEFLATE). Compressed <= 1 MiB, decompressed <= 8 MiB. Hello is codec 0x00 and <= 16 KiB. The plugin
  compresses frames whose raw size is > 1 KiB. The cloud never compresses.
- First frame: `Hello` within 10 s. Response: `Welcome` or a fatal `Error` + close (codes: 4000 bad request,
  4001 auth, 4002 license limit, 4003 protocol, 4004 duplicate server, 4008 rate limit, 1001 shutting down,
  1011 internal). A first frame that is not a Hello, a second Hello, a non-binary message or an undecodable
  frame is a fatal `CODE_BAD_REQUEST` (4000). On a graceful stop the plugin sends `Goodbye` before closing.
- Server identity is (tenant_id, server_id). `server_id` must be a canonical lowercase UUID. The first
  Welcome for a new server_id carries `server_secret`; the plugin stores it and presents it in every later
  Hello; the cloud stores only its hash. A Hello for an existing server_id with a wrong/missing secret is
  rejected with CODE_DUPLICATE_SERVER. A second live connection with the same server_id but a different
  `instance_id` is rejected with CODE_DUPLICATE_SERVER (cloned plugin folder) unless the existing
  connection has been silent for 15 s.
- `Batch.seq` increases by 1 per batch, assigned when a batch is first sent. Seqs are reserved in blocks of
  100 000 persisted in `state.properties` (a restart resumes above anything used). The gateway publishes
  the batch to the stream, waits for every JetStream PubAck, then sends `BatchAck{seq}`; it never acks a
  batch it did not store, except a poison batch (too large/invalid: `Error{CODE_TOO_LARGE}` + ack + drop +
  counter). The gateway treats a batch as replayed when `replayed` is set or when its normalised send
  time is more than 30 s old.
- De-duplication: JetStream msg id `<tenant_id>:<server_id>:<seq>:<partition>` (window 10 min), plus a
  worker-side per-(tenant, server, partition) last batch seq that never expires, plus
  `Welcome.last_acked_seq` (the plugin drops pending batches with seq <= it).
- Backpressure: limits are enforced by delaying socket reads and BatchAcks, never by dropping data. Welcome
  advertises `max_events_per_s`, `max_bytes_per_s` and `replay_max_events_per_s`; the plugin sends live
  batches first and paces replayed batches to <= 50% of the replay budget.
- Heartbeat: plugin sends `Ping` every `heartbeat_interval_ms` (default 5000) with its latest probe RTT and
  online player count; the gateway answers `Pong` with the current `RemoteConfig`. Either side closes the
  connection after 3 intervals without any frame.
- Verdicts arrive as `ServerFrame.verdict`; the plugin answers each with `VerdictAck` and de-duplicates by
  verdict_id. Undelivered/unacked verdicts are re-sent by the gateway (5.3).
- `Probe` events travel the full pipeline (gateway -> stream -> worker -> verdict service -> gateway), one
  per 10 s per server, and return as `ProbeResult`; this is the phase-1 dummy check. The SLO uses the
  plugin-measured end-to-end RTT (`Ping.last_probe_rtt_us`).
- Event clock: `Event.time_us` (see wire.proto). The gateway estimates per-connection
  `clock_offset_us = min over batches (gateway_recv_us - sent_unix_ms*1000)` and puts it in Origin; cloud
  components use normalised time `t = time_us + clock_offset_us` for decay, staleness and windows and never
  their own arrival time for detection math.

## 4. Paper / Folia plugin (`plugin/paper`)

### 4.1 Platform and lifecycle

- `plugin.yml`: `name: PandaEye`, `main: dev.pandascript.pandaeye.paper.PandaEyePlugin`,
  `api-version: '1.21'`, `folia-supported: true`, `depend: [packetevents]`, `softdepend: [floodgate,
  ViaVersion]`, command `pandaeye` (alias `peye`), permissions (4.10).
- Paper 1.21.x and Folia/Canvas 1.21.x, Java 21+. Detect Folia with
  `Class.forName("io.papermc.paper.threadedregions.RegionizedServer")`.
- Never use `BukkitScheduler`. Use Paper's regionised schedulers (they exist on Paper too):
  `player.getScheduler()`, `Bukkit.getGlobalRegionScheduler()`, `Bukkit.getAsyncScheduler()`. Every entity
  scheduler call passes a retired callback and handles a null return.
- PacketEvents: through the installed plugin's API only (never shaded or initialised by PandaEye). One
  listener registered at `PacketListenerPriority.MONITOR`; keep the returned `PacketListenerCommon` and
  unregister it on disable.
- **Enable (hot load safe)**: load config; create `active = true` (volatile); register listeners; for each
  online player create a session immediately (entity id from PacketEvents `User.getEntityId()`), mark it
  `hot_loaded`, and schedule one task on the player's scheduler that snapshots getEntityId, game mode,
  allowFlight/isFlying/flySpeed, attribute values (without the `minecraft:sprinting` modifier), active
  potion effects with amplifiers, isInsideVehicle, isGliding, isRiptiding, isDead, held slot, client brand
  and bypass permission; publishes it as the session baseline; starts the env sampler (idempotent);
  registers the player in the EntityIndex; and sends PlayerJoin + StateChange. Until the snapshot is
  published the session is unseeded: all local checks and id-based logic are off and unknown state means
  exempt, never a default value. Preload every class used on netty/region paths during enable.
- **Disable (bounded, <= 300 ms)**: unregister the PacketEvents listener, set `active = false` (every
  callback starts with `if (!active) return;` and maintains an in-flight counter), wait up to 100 ms for
  in-flight callbacks to finish; unregister Bukkit listeners; cancel all tasks (entity and region
  schedulers have no `cancelTasks(Plugin)`, so every `ScheduledTask` handle is kept per player and
  cancelled on quit and disable; global/async schedulers use `cancelTasks(plugin)`);
  hand the transport a stop request (4.6) and wait at most 200 ms; spool unacked batches and undrained events
  to `plugins/PandaEye/spool/` (size-capped 64 MiB); clear all maps. No ThreadLocals on foreign threads, no
  executor-less `CompletableFuture.*Async`, no shutdown hooks, no JUL handlers. After unload no
  `PandaEye-*` thread may remain and the class loader must be collectable (e2e asserts both).

### 4.2 Threading and PacketEvents rules

- Packet callbacks run on the player's netty event loop; a player's incoming and outgoing packets are
  handled on that loop, so per-player packet state is owned by it and needs no locks.
- **Wrapper hygiene** (mandatory): every wrapper read goes through one helper:
  `prev = e.getLastUsedWrapper(); if (prev instanceof W w) return extract(w); try { return extract(new W(e)); }
  finally { e.setLastUsedWrapper(prev); }`. Never call `markForReEncode`, `setCancelled`, `setByteBuf` or any
  wrapper setter. Packets where only the leading VarInt matters (entity-id-prefixed client-bound packets:
  ENTITY_METADATA, UPDATE_ATTRIBUTES, ENTITY_EFFECT, REMOVE_ENTITY_EFFECT, ENTITY_VELOCITY, SET_PASSENGERS)
  are first pre-filtered by reading the entity id raw from `e.getByteBuf()` with the reader index restored,
  and wrapped only if the id is the player's own (or a tracked vehicle). CLIENT_TICK_END and ANIMATION
  need no wrapper at all. A JUnit test asserts that `getLastUsedWrapper()` and the buffer reader index are
  unchanged after PandaEye's listener for every packet type it handles.
- PacketEvents facts the code relies on (verified against 2.13.0): `ClientVersion.getById` clamps unknown
  protocols to the newest known version and -1 to 1.7.2, so tick clients are
  `user.getClientVersion().isNewerThanOrEquals(ClientVersion.V_1_21_2)` and `PlayerJoin.protocol_version`
  is -1 when the version is UNKNOWN; `WrapperPlayClientPlayerFlying.getLocation()` returns 0 for parts the
  packet does not carry, so position/rotation are carried forward unless `hasPositionChanged()` /
  `hasRotationChanged()`; the collision getter is `isHorizontalCollision()`; `WrapperPlayServerOpenWindow`
  gives a raw menu-type id (mapped with a 1.21.x table); `WrapperPlayServerExplosion.getKnockback()` is
  nullable; ENTITY_VELOCITY is a `Vector3d` in blocks/tick; attribute modifiers are identified by
  `PropertyModifier.getName()` (`minecraft:sprinting`); sneaking on 1.21.6+ comes only from
  `PLAYER_INPUT.isShift()`; spear attacks are `PLAYER_DIGGING` `DiggingAction.STAB` (+ swing), never
  INTERACT_ENTITY; horse/llama/camel inventories open with `OPEN_HORSE_WINDOW`; the entity-id peek uses
  `ByteBufHelper.readerIndex/readVarInt`.
- Server-bound packets are processed whether or not another plugin cancelled them (the client still sent
  them); the cancelled bit is recorded (`MOVE_FLAG_CANCELLED`). Cancelled client-bound packets are ignored.
- Data read across threads (another player's position history, env flags, VLs, mitigation flags) uses
  immutable records behind volatile fields, lock-free rings or concurrent maps.
- World access only on the player's region thread via `player.getScheduler()`: the env sampler (4.4), the
  confirmation step (CHECKS 1.1) and the hot-load snapshot.
- Transport runs on its own daemon threads (4.6). Verdict commands run on the global region scheduler;
  per-player actions on the player's scheduler.
- **Enforcer**: every side effect that could affect a player (sending a packet, cancelling anything,
  `teleportAsync`, `setVelocity`, `dispatchCommand`, `kick`, mitigation listeners) goes through one
  `Enforcer` interface. The shadow implementation is a no-op that counts attempted calls (shown in
  `/pandaeye status`). An ArchUnit test asserts that no other class calls those APIs.
- Every callback catches Throwable -> rate-limited warning (max 1 per 10 s per site, using only JDK or
  preloaded classes) and continues.

### 4.3 Sessions and packet tracking

- Sessions are created only on S `JOIN_GAME` (a PLAY-state User with a UUID) or during enable for online
  players, keyed by PacketEvents `User` identity, removed with `map.remove(key, session)` on PlayerQuitEvent
  and `UserDisconnectEvent`; packets for users without a session are ignored; an async sweep every 30 s
  removes sessions whose channel is closed or whose player is offline. Each real join gets a new random
  `session` UUID (PlayerJoin.session) and resets all per-session state. The plugin re-announces every online
  session every 60 s with `PlayerJoin{update=true}` so a restarted worker recovers session info.
- Tracked packets (server-protocol format; PacketEvents decodes with the server version even when ViaVersion
  translates for the client):

| Packet | State |
|---|---|
| S `JOIN_GAME`, `RESPAWN` | entity id, game mode; reset movement baseline; carriedIndex mirror = 0 (JOIN_GAME) |
| S `PLAYER_POSITION_AND_LOOK` / C `TELEPORT_CONFIRM` | teleport FIFO (CHECKS 1.1) |
| S `PLAYER_ROTATION`, S `FACE_PLAYER` | server rotation -> next rotation sample marked SERVER_ROTATION |
| C flying packets (`WrapperPlayClientPlayerFlying`) | position, rotation, onGround, horizontal collision |
| C `CLIENT_TICK_END` | client tick counter (tick clients) |
| S `ENTITY_VELOCITY` (own id), S `EXPLOSION` (player knockback), Bukkit `PlayerVelocityEvent` (MONITOR, read-only) | pending velocity |
| S/C `PLAYER_ABILITIES`, S `CHANGE_GAME_STATE` (game mode) | abilities, game mode (pending/applied) |
| S `ENTITY_EFFECT` / `REMOVE_ENTITY_EFFECT` (own id) | speed, slowness, jump_boost, levitation, slow_falling, dolphins_grace |
| S `UPDATE_ATTRIBUTES` (own id) | movement_speed (without `minecraft:sprinting`), jump_strength, gravity, step_height, scale, entity/block interaction range |
| S `ENTITY_METADATA` (own id) | sneaking, sprinting, swimming, gliding, using item, riptide |
| S `UPDATE_HEALTH`, S `DEATH_COMBAT_EVENT` | dead until RESPAWN |
| S `TICKING_STATE` / `TICKING_STEP` | tick rate / frozen (timer, flood) |
| C `PLAYER_LOADED` (1.21.4+) | end of the loading-screen exemption |
| C `ENTITY_ACTION`, C `PLAYER_INPUT` | sprint, sneak, elytra start (provisional gliding exemption) |
| S `SET_PASSENGERS`, C `VEHICLE_MOVE`, C `STEER_VEHICLE` | in vehicle |
| S `OPEN_WINDOW`, S `OPEN_HORSE_WINDOW`, S/C `CLOSE_WINDOW` | open container id + type, replaced/closed ids with ack grace; unknown after RESPAWN/JOIN_GAME until the next open/close |
| S/C `KEEP_ALIVE`, S `PING` / C `PONG` | passive acknowledgement + RTT (per-id FIFO, 30 s expiry) |
| S/C `HELD_ITEM_CHANGE` | carriedIndex mirror, expected echoes |
| C `INTERACT_ENTITY`, C `ATTACK` (26.1+ if the type exists), C `ANIMATION`, C `PLAYER_BLOCK_PLACEMENT`, C `PLAYER_DIGGING` (incl. RELEASE_USE_ITEM, STAB), C `CLICK_WINDOW`, C `USE_ITEM`, C `PLUGIN_MESSAGE` (`minecraft:brand`, `pandaeye:session`) | events for the cloud and protocol checks |

- `EntityIndex`: entity id -> session for tracked players, filled when the session is seeded.
- `PositionHistory`: per player lock-free ring of the last 64 received positions `(time_us, x, y, z)`;
  attacks attach the target's positions of the last 1500 ms.
- RTT: `ping` = median of the last 5 pong RTTs within 30 s, else the last keep-alive RTT, else 100 ms;
  `rttHigh` = max of the last 5 samples.
- Brand: the first `minecraft:brand` value per session only, truncated to 64 chars, control chars stripped.
- `pandaeye:session` is read at packet level (no Bukkit channel registration) and accepted only when
  `proxy.session-channel: true`. Payload (73 bytes): version byte `1`, player UUID (16 bytes), network
  session UUID (16), unix seconds (8, big-endian), HMAC-SHA256(proxy.secret, the preceding 41 bytes) (32).
  It must be no older than 30 s and is accepted at most once per server connect; it may arrive before the
  session exists and is then buffered by UUID for 10 s.
- Bypass permission is evaluated on the player's entity thread at seed time and every 30 s and cached. In
  shadow mode bypass exempts only from enforcement, not from detection
  (`exempt.bypass-detection-in-shadow: false`), so staff can test with cheat clients.
- Bedrock players: only PlayerJoin (bedrock=true), PlayerQuit and StateChange are sent; no checks.

### 4.4 Environment sampler (region thread, 2 Hz)

One repeating task per player (`runAtFixedRate(plugin, task, retired, 10, 10)`). `EnvFlag` bits from cheap
API calls: `isInWater`, `isInLava`, `isClimbing`, `isGliding`, `isRiptiding`, `isSwimming`,
`isInsideVehicle`, `isInBubbleColumn`; at most 3 `getBlockAt` (feet, below, `y - 0.5`) for ice/slime/honey/
soul/cobweb/sweet berry/powder snow (powder snow only from block types: the 1.21.4 API has no
`isInPowderSnow`); block collision shapes are block-local, so overlap tests use
`shape.overlaps(box.clone().shift(-bx, -by, -bz))`; NEAR_SOLID = `world.hasCollisionsIn(hitbox.expand(1))`; LOW_CEILING =
`hasCollisionsIn` on a 0.6x1.0 box above the head; NEAR_ENTITY = any collidable/pushable entity within 1.5;
EXTENDED_REACH_ITEM = the main-hand item is a spear or has an `attack_range` data component (reflection;
false on older APIs). `server_tick_ms` = measured interval between runs / 10. Publishes an `EnvSnapshot`
(volatile) and per-flag "last set" times; sends `EnvSample` on change and at least every 5 s. Budget
< 20 microseconds per sample; idle players drop to 1 Hz when the budget is exceeded.

### 4.5 Local checks, stall detector, flags

- Local checks (CHECKS section 1): `timer.balance`, `movement.speed`, `movement.fly`, `movement.step`,
  `movement.groundspoof`, `movement.noslow`, `dig.speed`, `protocol.pitch`, `protocol.position`, `protocol.slot`, `protocol.selfhit`,
  `protocol.cursor`, `protocol.flood`, `protocol.abilities`, `inventory.closed`.
- A confirmed violation increases the check's VL, is logged, alerts (4.9), and is sent as a `LocalFlag`
  with the player's last <= 40 movement packets as context. In live mode with enforcement, a confirmed
  movement violation at or above the check's setback VL (or any, while SETBACK mitigation is active) sets
  the player back to the last legitimate position with `player.teleportAsync`.
- Tuning data: every local check exposes its test statistic. Per player per check per 5 minutes the plugin
  aggregates (max statistic, evaluated, candidates, discarded by confirmation, confirmed), writes it to
  `logs/peaks-YYYY-MM-DD.jsonl` (same bounds as the flag log) and sends a `CheckStats` event. Gate 2
  (local false positives) is defined in SLO.md: >= 7 days and >= 1000 player-hours with <= 1 confirmed flag
  per 50 player-hours per check, and no staff-verified legitimate player ever reaching a setback VL.
- Stall detector (daemon thread, 50 ms period): a stall is in progress when >= 20% of active players (min 3)
  have sent nothing for 400 ms, or the global-region heartbeat (a 1-tick repeating task) is > 400 ms late.
  Stalls are sent as `ServerStall` events and excuse blink and timer bins.

### 4.6 Telemetry and transport (`plugin/common`)

- Event selection: `MoveSample` for every flying packet while the player is in focus (attacked or was
  attacked in the last 5 s, took velocity or explosion knockback in the last 3 s, placed or dug a block in
  the last 3 s, clicked in a window or had a container open in the last 3 s, a local flag in the last
  30 s, a `WatchRequest`
  active (clamped to <= 300 s, <= 20 concurrent), or `telemetry.full`); otherwise at 2 Hz
  (`ticks_since_last` tells the cloud how many ticks were skipped). All other event kinds are always sent.
  A per-player ring of the last 40 movement packets is kept for LocalFlag context.
- `EventSink`: bounded MPSC queue (100k events; overflow drops newest and counts).
- `CloudClient`, all network I/O on one daemon thread `PandaEye-Cloud` plus the JDK HttpClient (built with
  `connectTimeout(5 s)` and a daemon executor whose threads are named `PandaEye-Http-*`; `buildAsync` is
  only ever called from `PandaEye-Cloud`):
  - every `batch-interval-ms` (default 50) drains events into a `Batch` (<= `max_batch_events` events and
    <= 512 KiB raw), serialises it, compresses it (zstd; DEFLATE if aircompressor fails to initialise or
    throws, permanently, logged once) and sends. One WebSocket send outstanding at a time. Incoming
    messages arrive in parts (`onBinary(..., last)`): parts are accumulated up to 1 MiB and
    `ws.request(1)` is called after each callback. Decompression checks the declared size first
    (`ZstdDecompressor.getDecompressedSize` must be 0..8 MiB) before allocating.
  - `PendingBuffer` keeps unacked batches, bounded by `max-buffer-mb` (64) and `max-buffer-age-minutes` (10);
    overflow drops the oldest and counts.
  - reconnects with exponential backoff 1 s -> 60 s +-20% jitter (honours `retry_after_ms`); after
    CODE_UNAUTHENTICATED waits 10 min; after CODE_DUPLICATE_SERVER waits 30 s and logs how to regenerate
    `server-id`; logs each state change once.
  - while disconnected, batches accumulate in the PendingBuffer (degraded mode); on reconnect, live
    batches go first and pending ones are paced (3).
  - stop: set stopping, stop the scheduler, cancel a pending `buildAsync`, best-effort
    `sendClose(1001)` with a 100 ms timeout, then always `abort()`, `executor.shutdownNow()`,
    `awaitTermination(100 ms)`. Never `HttpClient.close()` (it blocks on in-flight operations).
  - spool: on stop, unacked batches and undrained events are written to `plugins/PandaEye/spool/` and
    resent (as replayed) after the next enable; files older than 1 h are discarded.
- Files in `plugins/PandaEye/`: `server-id`, `server-secret` (0600), `state.properties` (seq block,
  clean-shutdown), `remote-config.properties` (last RemoteConfig + timestamp), `executed.json` (verdict ids
  executed in the last 7 days + bans executed here), `spool/`, `logs/`.

### 4.7 Verdict handling

On `Verdict`: de-duplicate by verdict_id (executed set); log; alert staff. Not enforced (ack
`executed=false` with the reason) when: shadow (local mode, last-known enforcement false, or
`verdict.shadow`), KICK older than 5 min, BAN older than 24 h, a BAN older than the player's last UNBAN, the
player name cannot be resolved locally, or the circuit breaker is open. Otherwise:

- ALERT: alert only. MITIGATE: set the player's mitigation set + strength (cleared on quit). UNMITIGATE:
  clear it.
- KICK: if `punishments.kick-command` is empty, `player.kick(message)` on the player's scheduler; else
  run the command. Only for online players.
- BAN: run `punishments.ban-command` (works for offline players); record the verdict id in `executed.json`.
- UNBAN: runs in ANY mode when `supersedes` is a ban this server executed (it only removes PandaEye's own
  enforcement); otherwise ack `executed=false, note="not executed here"`.
- Placeholders: `%player%` resolved locally from the UUID (online name, else the offline player cache; must
  match `^[A-Za-z0-9_.]{1,16}$`), `%uuid%`, `%reason%` and `%checks%` restricted to `[A-Za-z0-9 ._,()x-]` and
  128 chars, `%verdict_id%`, `%appeal_code%`, `%appeal_url%`, `%duration%`. No line breaks, no colour codes.
- Circuit breaker: at most `punishments.max-bans-per-hour` (3) bans and `max-kicks-per-10min` (5) kicks;
  beyond that, stop executing, ack `executed=false, note="circuit-open"`, alert console and staff until
  `/pandaeye breaker reset`.
- On enable, check that the labels of `ban-command` and `unban-command` resolve
  (`Bukkit.getCommandMap().getCommand`) and belong to the same plugin; warn loudly otherwise.

### 4.8 Mitigations (live mode with enforcement; listeners are not registered otherwise)

| Mitigation | Implementation |
|---|---|
| HIT_CANCEL | `EntityDamageByEntityEvent` (damager = mitigated player): cancel with probability 0.15 + 0.35*strength |
| DAMAGE_REDUCE | same event, `setDamage(damage * (1 - 0.3*strength))` |
| VELOCITY_BOOST | `PlayerVelocityEvent` for the mitigated player: multiply velocity by 1 + 0.25*strength |
| SETBACK | confirmed movement flags set the player back even below the setback VL |
| PLACE_CANCEL | `BlockPlaceEvent`: cancel with probability 0.2 + 0.4*strength |

### 4.9 Alerts and logs (bounded)

- Console: per player per check max 1 line per 5 s (with suppressed counts), globally max 20 lines/s.
- In game: holders of `pandaeye.alerts` who toggled alerts on (`/pandaeye alerts`); `alerts.in-game-default:
  auto` = on in live mode, off in shadow mode. Recipients are kept in a set updated on join/quit/toggle (no per-flag permission scan).
- Flag log `plugins/PandaEye/logs/flags-YYYY-MM-DD.jsonl`: aggregated per player+check per 5 s (count, max
  severity, first/last info); global cap 50 lines/s then one summary line; bounded writer queue (10k);
  daily size cap 256 MiB; writing stops while free disk < 1 GiB; files kept 14 days. Drops are shown in
  `/pandaeye status`. `/pandaeye forget <uuid>` removes a player's lines from local logs.

### 4.10 Commands and permissions

`/pandaeye status | alerts [on|off] | player <name> | debug <name> | breaker [reset] | forget <uuid> | reload`
(alias `/peye`). `status` shows mode, enforcement, cloud state, connection RTT, probe RTT p50/p95, pending
batches, dropped events/logs, tracked players, unseeded sessions, packet-handler cost (avg and p99 us/packet),
confirmations dropped. `reload` reloads thresholds and alert settings only. Permissions: `pandaeye.command`,
`pandaeye.alerts`, `pandaeye.admin` (op), `pandaeye.bypass` (default false).

### 4.11 `config.yml` (defaults)

```yaml
# shadow = observe only: never sends, cancels or modifies packets, never punishes or mitigates.
# live   = enforcement allowed when the dashboard also enables enforcement.
mode: shadow
server-name: ""
network-id: ""
cloud:
  enabled: true
  endpoint: "wss://eye.pandascript.dev/v1/connect"
  allow-insecure: false    # true allows ws:// to a non-loopback host (same host / private network only)
  license-key: ""
  batch-interval-ms: 50
  max-buffer-mb: 64
  max-buffer-age-minutes: 10
telemetry:
  full: false
  focus-seconds: 5
  idle-sample-hz: 2
checks:                    # enabled + thresholds from CHECKS.md, per check id
  timer.balance: { enabled: true }
  movement.speed: { enabled: true }
  movement.fly: { enabled: true }
  movement.step: { enabled: true }
  movement.groundspoof: { enabled: true }
  movement.noslow: { enabled: true }
  dig.speed: { enabled: true }
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
  in-game-default: auto        # auto | on | off
  log-file: true
exempt:
  bedrock: true
  bypass-detection-in-shadow: false
proxy:
  session-channel: false
  secret: ""
setbacks:
  enabled: true
punishments:
  kick-command: ""                                   # empty = player.kick(message)
  kick-message: "Unfair advantage detected"
  ban-command: "ban %player% Unfair advantage. Appeal at %appeal_url% with code %appeal_code%"
  unban-command: "pardon %player%"
  appeal-url: ""
  max-bans-per-hour: 3
  max-kicks-per-10min: 5
mitigations:
  enabled: true
debug: false
```

## 5. Cloud (`cloud/`)

### 5.1 Binary and configuration

`pandaeye <command> [flags]`: `all` (embedded NATS + gateway + worker + verdict + api, the only mode that
may use sqlite), `gateway`, `worker`, `verdict`, `api`, `migrate`, `replay` (re-run checks over stored stream
data or a clip: `--tenant --player --since --until`, prints signals), `version`.

Environment variables (`PE_` prefix), optionally a YAML file (`--config`, keys in lower case with dots).

| Key | Default | Meaning |
|---|---|---|
| `PE_GATEWAY_LISTEN` | `:8080` | `/v1/connect`, `/healthz`, `/status` |
| `PE_API_LISTEN` | `:8081` | REST API |
| `PE_METRICS_LISTEN` | `127.0.0.1:9090` | `/metrics`, pprof |
| `PE_TLS_CERT`, `PE_TLS_KEY` | `` | serve TLS directly (else terminate TLS in front) |
| `PE_TRUSTED_PROXIES` | `` | CIDRs whose X-Forwarded-For / CF-Connecting-IP is honoured (client IP = right-most untrusted) |
| `PE_REQUIRE_TRUSTED_PROXY` | `false` | when true, the gateway and API refuse (close / 403) any TCP peer outside `PE_TRUSTED_PROXIES` (e.g. only Cloudflare ranges may connect) |
| `PE_TRUSTED_PROXIES_PRESET` | `` | `cloudflare` adds Cloudflare's published IPv4/IPv6 ranges (embedded list, refreshable from https://www.cloudflare.com/ips-v4 and ips-v6 at start, falling back to the embedded list) |
| `PE_PUBLIC_URL` | `` | dashboard base URL for evidence and appeal links |
| `PE_NATS_URL` | `` | external NATS; empty in `all` = embedded, in-process only (`DontListen`) |
| `PE_NATS_CREDS` / `PE_NATS_TOKEN`, `PE_NATS_TLS_CA` | `` | required for external NATS that is not loopback |
| `PE_NATS_DIR` | `./data/nats` | embedded JetStream storage |
| `PE_NATS_REPLICAS` | `1` | replicas for all streams and KV buckets |
| `PE_NATS_SYNC` | `default` | embedded file-store sync (`default` 2 min, or `always`) |
| `PE_OUTBOUND_KEY`, `PE_OUTBOUND_KEY_NEXT` | generated once in `all` and persisted in `meta` | HMAC key for SignedOutbound and the root of the webhook-encryption and appeal-code keys (>= 32 bytes; required for gateway/verdict/api in split mode, unused by workers); NEXT is accepted for verification during rotation |
| `PE_PARTITIONS` | `16` | partition count (stored in KV `PE_META` with the siphash key; mismatch = refuse to start) |
| `PE_DB_DRIVER`, `PE_DB_DSN` | `sqlite`, `./data/pandaeye.db` | `sqlite` (WAL, busy_timeout 5000, one writer) or `postgres` |
| `PE_CLICKHOUSE_DSN` | `` | empty = telemetry disabled |
| `PE_LICENSE_MODE` | `static` | `static` or `pandascript` |
| `PE_LICENSE_URL`, `PE_LICENSE_PRODUCT` | ``, `pandaeye` | pandascript license worker (https only) |
| `PE_LICENSE_PEPPER` | random in `all` (persisted in the DB) | HMAC key for license key lookups |
| `PE_STATIC_LICENSES` | `` | `key[:maxServers[:plan]],...` |
| `PE_SERVICE_TOKEN`, `PE_SERVICE_TOKEN_NEXT` | `` | dashboard transport token (>= 32 bytes or empty = disabled) |
| `PE_ASSERTION_KEY`, `PE_ASSERTION_KEY_NEXT` | `` | HMAC key for dashboard user assertions (>= 32 bytes); NEXT accepted during rotation |
| `PE_EVENTS_MAX_AGE`, `PE_EVENTS_MAX_BYTES` | `24h`, `10GiB` | PE_EVENTS retention |
| `PE_WORKER_MAX_MEMORY` | `2GiB` | worker state byte budget (LRU) |
| `PE_REGION` | `local` | shown on /status |
| `PE_LOG_LEVEL` | `info` | |
| `PE_ML_MODEL`, `PE_ML_POINTS` | ``, `0` | ONNX model (`-tags onnx` builds only) |

Logging: `log/slog` JSON. Hello, licences, tokens, webhook URLs and Authorization headers implement
`slog.LogValuer` redaction; a test runs the gateway and API at debug level with known secrets and asserts
none appear in the output. Graceful shutdown on SIGINT/SIGTERM within 10 s (connections get
`Error{CODE_SHUTTING_DOWN, retry_after_ms=jittered}`; leases released; consumers drained; stores flushed).

### 5.2 Packages

```
internal/wire/pandaeyev1   generated protobuf (committed)
internal/codec             frame header, zstd/deflate decode with hard limits, protowire pre-scan
internal/config            loading/validation
internal/license           Verifier: static, pandascript (HTTP, cached, singleflight, global rate limit)
internal/identity          tenant/server identity, secrets, HMAC helpers, siphash partitioning
internal/gateway           WebSocket server, sessions, backpressure, outbound delivery + sweep, metering
internal/stream            embedded NATS, provisioning, leases, consumer epochs, publish/consume helpers
internal/geom              Vec3, AABB, ray/AABB, angles, eye heights
internal/worker            partition consumer loop, sessions, PlayerState, check registry, clips
internal/checks            cloud checks (one file per family) + catalog.go + stats helpers
internal/verdict           scoring engine, leniency, policy, evidence, consumer loop, Discord, store writer
internal/store             Store interface + model types
internal/store/sqlstore    database/sql implementation (postgres via pgx stdlib, sqlite via modernc), migrations
internal/telemetry         Sink interface; clickhouse; no-op
internal/api               REST API, auth (tokens, assertions, API key scopes), status, janitor, erasure, metering
internal/ml                feature windows; Scorer; onnx implementation behind build tag `onnx`
internal/metrics           Prometheus collectors
internal/testkit           fake plugin client, event builders, helpers
```

### 5.3 Gateway

- `GET /v1/connect` upgrades (read limit 1 MiB + 1). Decoding (internal/codec): Hello must be codec 0x00
  and <= 16 KiB; zstd via `zstd.NewReader(nil, WithDecoderMaxMemory(8<<20), WithDecoderMaxWindow(8<<20),
  WithDecoderConcurrency(1), WithDecodeAllCapLimit(true))` after checking the frame header (single frame,
  content size present and <= 8 MiB); DEFLATE via `flate.NewReader` + `io.LimitReader(8 MiB + 1)`; a global
  semaphore caps in-flight decompressed bytes at 256 MiB. Before `proto.Unmarshal` of a Batch, protowire
  counts top-level `events` and rejects more than `max_batch_events` or any event > 64 KiB. After decoding,
  field limits: strings <= 256 B (info <= 512), target_history <= 64, data map <= 32, effects <= 32,
  capabilities <= 32, context <= 40; invalid UTF-8 is replaced.
- Hello: validate fields -> license (5.9; the last successful verification is persisted on the tenant row
  (`license_verified_at`, `license_valid_until` = +24 h, `max_servers`) and used when the verifier is
  unreachable; verifier unavailability is `Error{CODE_INTERNAL, retry_after_ms=30000}`, never
  UNAUTHENTICATED; failures limited per IPv4 /32 and IPv6 /64: 5 per 10 min, then
  1 h block) -> tenant from cache (refreshed from DB, stale-OK on DB errors) -> server identity (3) ->
  register the connection in KV `PE_CONNECTIONS` (`<tenant>.<server>` = {connection_id, gateway_id,
  instance_id, online_players}, TTL 30 s, renewed every 10 s with revision checks; a revision mismatch
  means replaced -> close) -> enforce `maxServers` by Create + recount (delete own key if over) -> subscribe
  `pe.out.<tenant>.<server>` -> Welcome -> deliver pending verdicts.
- Per batch: decode/validate; split events by partition keeping order; publish one StreamBatch per non-empty
  partition (`PublishAsync`, msg id as in 3); wait for all PubAcks. A publish timeout keeps the connection:
  stop reading, retry with the same msg ids, close with CODE_INTERNAL only after 60 s. Permanent errors
  (payload too large, invalid) -> `Error{CODE_TOO_LARGE}` + ack + drop + counter. Then BatchAck and update
  the server's last acked seq (KV `PE_SERVERS`, monotonic, plus async DB write).
- Backpressure budgets per connection (live 20 000 events/s burst 100 000, 2 MiB/s; replay 15 000 events/s)
  and per tenant by plan (free: 3 000 events/s); over budget = stop reading the socket until tokens refill.
  New connections: 30/min per IP and 60 Hellos/min per tenant, but reconnects presenting a valid server
  secret are exempt.
- Outbound: subscription with pending limits matching a bounded per-connection queue (1 000); overflow closes
  the connection (forcing redelivery after reconnect) instead of dropping. Each SignedOutbound is verified
  (HMAC, tenant_id, server_id) before writing. While the tenant's current `enforcement` is false (KV
  `PE_TENANTS`), delivered verdicts are forced to `shadow = true`. `close_connection_id` closes that
  connection; the gateway that accepted a replacing connection sends it when the old connection lives on
  another gateway.
- Verdict delivery sweep every 10 s per connection: re-send verdicts with status `open`, not acked,
  `delivered_at` older than 15 s, created < 24 h ago (KICK < 5 min; UNBAN/UNMITIGATE never expire),
  ordered by created_at. At delivery every pending verdict is re-validated against the tenant's current
  flags (autoban/autokick/mitigations/enforcement): a verdict no longer allowed is delivered with
  `shadow = true` (KICK/BAN) or marked `expired`. Per connection the gateway also drops a verdict_id it
  already delivered in the last 15 s.
- PlayerRisk: after a PlayerJoin (not update) for a tenant with `reputation_opt_in`, the gateway looks up
  the player's reputation (5.11) and sends `PlayerRisk` when risk > 0 (lookup budget applies). The plugin
  only shows it to staff.
  `VerdictAck` -> publish `AckRecord` to `pe.acks.<tenant>`.
- Clock offset estimate per connection (3) goes into Origin.
- `/status` (public JSON): region, version, uptime, connected servers (count), probe RTT p50/p95/p99 over the
  last hour (plugin-measured, from the store), SLO targets (p95 < 100 ms and p99 < 250 ms in-region) and
  whether each is met. The plugin measures RTT from the moment it creates the Probe event to the moment it
  receives the ProbeResult; `Ping.last_probe_rtt_us` is 0 unless a probe completed in the last 30 s (so a
  broken pipeline shows "no data", never a stale good value). The gateway records each reported RTT in
  `probe_stats` as a fixed log-bucket histogram per minute (64 buckets from 100 us to 60 s), so hourly
  percentiles are computed from merged histograms; cloud-side stage timings are stored separately.
- `/metrics` on `PE_METRICS_LISTEN` only, no tenant/server labels.

### 5.4 Event stream (NATS JetStream)

| Name | Subjects / keys | Settings |
|---|---|---|
| stream `PE_EVENTS` | `pe.events.*` | file, limits, `PE_EVENTS_MAX_AGE/BYTES`, discard old, duplicates 10m |
| stream `PE_SIGNALS` | `pe.signals.*` | file, max age 7 d, duplicates 10m |
| stream `PE_VERDICTS` | `pe.verdicts.*`, `pe.acks.*` | file, max age 30 d, duplicates 1 h |
| KV `PE_LEASES` | `events.<NN>`, `signals.<NN>`, `writer`, `metering`, `janitor`, `members.<owner>` | TTL 15 s |
| KV `PE_CONNECTIONS` | `<tenant>.<server>` | TTL 30 s |
| KV `PE_SERVERS` | `<tenant>.<server>` | last acked seq, secret hash, instance id |
| KV `PE_TENANTS` | `<tenant>` | tenant settings JSON (api writes; gateway and verdict watch) |
| KV `PE_META` | `partitions`, `partition_key` | no TTL |
| KV `PE_ERASURES` | `<tenant>.<player>` | erasure tombstones (no TTL; deleted 8 days after the erasure) |
| core | `pe.out.<tenant>.<server>` | SignedOutbound |

- Embedded mode: `server.Options{DontListen: true, JetStream: true, StoreDir, MaxPayload: 8 MiB}` and
  `nats.InProcessServer`. External NATS must have `max_payload >= 8 MiB` (checked at start) and
  credentials (NATS JWT/operator mode, one creds file per role; `deploy/nats/gen-auth`). Per-role subject
  permissions are in OPERATIONS.md: gateway pub `pe.events.>`, `pe.acks.>`, `pe.out.>` (connection
  replacement), sub `pe.out.>`; worker pub `pe.signals.>`; verdict/api pub `pe.out.>`, `pe.verdicts.>`; each
  role gets only the JetStream/KV API subjects for the streams, consumers and buckets it uses. Every process
  provisions streams/buckets idempotently, so `PE_NATS_REPLICAS` and `PE_EVENTS_MAX_*` must be identical on
  all roles.
- Partition: `siphash24(partition_key, 16 uuid bytes) % PE_PARTITIONS`, two-digit zero-padded (`07`).
  Probes use `probe_id % P` (round-robin, so every partition owner is measured). `ServerStall` events are
  copied into every partition's StreamBatch of that batch (every worker needs them for its players);
  other server-level events use `siphash(server_id) % P`.
- **Leases and consumer epochs.** A process takes a lease with `kv.Create(key, ownerToken)` (owner token =
  stable owner id + random epoch), renews every 5 s with `kv.Update(key, token, lastRevision)`; on an Update
  timeout it `Get`s the key and adopts the revision if the value is its token. It self-fences (stops
  fetching, processing and acking) when the last successful renewal is older than 10 s. A new owner waits
  5 s after Create, reads the durable consumer's `AckFloor.Stream`, deletes the consumer and recreates it
  with `DeliverByStartSequence(AckFloor + 1)` (no consumer yet: `DeliverByStartTime(now - 2 min)`, never
  DeliverAll). Graceful shutdown deletes its own leases (revision-checked). In `all` mode a restarted
  process adopts leases whose value has its stable owner id.
- **Consumption.** `Consume()` streaming (no fetch waits), `PullMaxMessages` 100, `AckPolicy=All`,
  `AckWait=30s`, `MaxAckPending=10000`, and `PriorityPolicy=Pinned` with one priority group and
  `PinnedTTL=15s`, so the server itself allows only one active puller per partition in addition to the
  lease. Publishing waits on each `PubAckFuture` of the batch (never `PublishAsyncComplete`), with
  `WithPublishAsyncTimeout(5s)` and a max-pending sized for 16 partitions x 20 batches/s per server. Each consumer skips messages whose stream sequence is <= the last
  applied one. A worker acks position N only after the PubAcks of every signal derived from messages <= N
  have returned; it never blocks the loop on the DB (slow work calls `msg.InProgress()`).
- `PE_TENANTS` is rebuilt from the database at start when missing (it is a cache of the tenants table).
  `/healthz` returns 503 from the moment shutdown begins.
- Retention/erasure: JetStream expiry (24 h events, 7 d signals, 30 d verdict records) is the maximum
  residual time after an erasure; erasure tombstones make workers and the verdict service drop late data.

### 5.5 Worker

- Session state keyed by (tenant, player, origin server_id), from PlayerJoin(update=false) (new
  `session`), updated by PlayerJoin(update=true) and closed by the PlayerQuit with the same server_id.
  Events from Velocity origins never touch backend sessions. Events earlier than the session's last
  normalised time are dropped. Statistical accumulators (click stats, aim calibration) live per
  (tenant, player) and take input only from the most recently joined session. Without a known session,
  events are buffered for at most 5 s (bounded) and otherwise dropped — checks never run without session
  info, and never for bedrock/exempt sessions.
- Dedup: last stream sequence per consumer + last batch seq per (tenant, server, partition).
- Warm start: on lease acquisition the worker first replays the last 60 s of `pe.events.<NN>` before its
  start sequence with an ordered ephemeral consumer in "warm" mode (state only, no signals), so windows and
  sessions are not empty after a handover.
- Per-tenant check configuration (`tenant_checks`: enabled, threshold overrides) is read from KV
  `PE_TENANTS` and applied by the worker and the verdict service (disabled checks emit nothing; local
  checks are configured in the plugin's config.yml).
- Check contract: `internal/checks/api.go` (Detector, Factory, Register, Ctx, Emission),
  `internal/checks/view.go` (the per-player `View` and `View.Apply`, the single place that derives shared
  state), `internal/checks/stats.go` and `internal/checks/checktest` (runs detectors over synthetic event
  sequences exactly as the worker does). One Detector per factory per player session.
- For each event: update derived state (moves, rotations, ticks, env flags, game mode, attributes, combat
  timestamps, latency, stalls), then call the checks registered for that event kind
  (`ctx.Emit(check, points, confidence, info, data)`). Signals go to `pe.signals.<NN>` with msg id
  `<tenant>:<server>:<batch_seq>:<NN>:<event index>:<check>:<emit ordinal>` (session markers:
  `...:<event index>:session`; VerdictRecords use the verdict id, AckRecords `ack:<verdict id>`). Session changes are forwarded
  as SessionMarkers on the same subject (ordered with the signals). LocalFlag events become signals
  (`local = true`). Probe events become `Signal{check_id: "probe", points: 0}`.
- Clips: per player a ring of the last 30 s of events stored as encoded bytes within the worker's byte
  budget (`PE_WORKER_MAX_MEMORY`, LRU eviction per tenant first). A clip (last 25 s, ClipData, zstd) is saved
  when a signal with points >= 5 is emitted or the player's points in the last 60 s reach 10, max one per
  player per 60 s, per-tenant budget by plan (free 30/h). The signal carries `clip_id` only after the clip
  is committed to the store.
- Per-tenant player cap by plan (free: 60 concurrent tracked players); feature windows (30 s) go to
  telemetry and to the ML scorer when one is loaded.

### 5.6 Verdict service

- Input: `pe.signals.<NN>` (signals + session markers, ordered). State per (tenant, player): per-check
  accumulators, per-check confidence, open sessions (server, network, start, hot_loaded, exempt), actions
  taken per session, last alert time, last normalised time; evicted when every accumulator < 0.5 and no
  session has been open for 1 h. Dedup: last stream sequence + LRU of applied signal msg ids.
- Scoring on normalised time `t` with `dt = max(0, t - last_t)`: `acc_c = acc_c * 2^(-dt/halflife_c) + points`.
  Player score `S = sum over families of min(familyCap, sum over checks in family of min(acc_c, cap_c))`;
  confidence `C = 1 - prod_c (1 - conf_c)` over checks with `acc_c >= 1`.
- Thresholds (base, leniency 3): ALERT 15, MITIGATE 30, KICK 70, BAN 120; leniency multipliers L1 0.6,
  L2 0.8, L3 1.0, L4 1.3, L5 1.7; default leniency 4.
- Rebuild: on lease acquisition the verdict service replays the last 2 h of `pe.signals.<NN>` with an
  ordered ephemeral consumer in "rebuild" mode (scores and sessions only, no actions), derives "actions
  taken this session" from stored verdicts by session_id, then switches to its durable consumer.
- The full ladder is always evaluated. When a tenant flag forbids an action (autoban/autokick/mitigations
  off), the verdict is still produced and stored with `shadow = true` ("would have banned"), never
  delivered as enforceable, so the shadow rollout measures what auto-actions would do.
- WatchRequest: when S >= ALERT/2 for an online session, publish `WatchRequest{60 s}` (max once per
  2 min per player); the API offers `POST /api/v1/players/{uuid}/watch`.
- Action policy (once per session except ALERT; `session_id` keyed):
  - ALERT when S crosses ALERT, then each time S grows 50%, max 1/min.
  - MITIGATE when S >= MITIGATE, tenant `mitigations`, session open. Mitigations by dominant category:
    HEURISTICS/AUTO_CLICKER -> HIT_CANCEL + DAMAGE_REDUCE + VELOCITY_BOOST; MOVEMENT/TICK_SPEED -> SETBACK;
    WORLD_INTERACTION -> PLACE_CANCEL; others -> HIT_CANCEL. strength = clamp((S - MITIGATE)/(KICK - MITIGATE), 0.3, 1).
    A rejoin with a still-high score re-issues MITIGATE for the new session.
  - KICK when S >= KICK, tenant `autokick`, C >= 0.8, session open, >= 2 families contributing >= 5 each.
  - BAN when tenant `autoban`, C >= 0.9, at least one committed clip among the contributing signals, and
    either (S >= BAN and >= 2 families contributing >= 5 each) or (one `certain` check with
    `acc_c >= BAN/2`, which suffices on its own even when its family cap keeps S below BAN).
  - Never MITIGATE/KICK/BAN from signals whose `gateway_recv_unix_us` is more than 2 min older than the
    cloud clock (re-consumption) or whose normalised event time is older than 10 min (stale catch-up):
    those produce ALERT at most. Hot-loaded sessions count half until the session's plugin snapshot is
    seeded (marker) and for 60 s after. Exempt/bedrock sessions never get KICK/BAN.
  - Before KICK/BAN, check the store for a `legit` label or an overturned verdict for the player covering
    the signal window; if present, ALERT only.
- Verdict ids are deterministic: UUIDv5(namespace, tenant | player | session | action | triggering signal
  stream seq), so re-processing produces the same verdict.
- Evidence (`evidence_json`): score, confidence, thresholds, leniency, family/check contributions, top 20
  signals (check, points, confidence, info, data, time, clip_id), session info.
- Order of operations: publish `VerdictRecord` to `pe.verdicts.<tenant>` (PubAck) -> publish SignedOutbound
  to `pe.out.<tenant>.<server>` -> Discord. `shadow` = tenant `enforcement` false (current KV) OR origin
  `shadow_mode`. Routing: KICK/MITIGATE/ALERT to the open session's server; BAN to the open session's
  server, else any connected server of the same network_id, else the server of the newest signal
  (pending until it reconnects).
- Store writer (one leased instance, `writer` lease): consumes `PE_VERDICTS`, upserts verdicts by id
  (including the serialized Verdict in `verdicts.payload` for redelivery), applies AckRecords (`acked_at`,
  `executed`, `note`), creates `bans` rows only on executed (ack executed=true) non-shadow BANs, records
  the executing server (`bans.server_id`), updates `reputation_contrib` (5.11).
- Probe signals: written like a verdict (a `probe_stats` minute-bucket update through the store writer
  path) and answered with `Outbound{probe_result}`; they pass through the same consumer queue as real
  signals, so the probe measures the real verdict path. The gateway assigns probes to partitions
  round-robin (`probe_id % PE_PARTITIONS`) so every partition owner is measured.
- Discord: tenant webhook (validated 5.12) gets an embed for verdicts >= `discord_min_action` (default
  MITIGATE): player, server, action, score, confidence, top 5 checks, evidence link; no IPs;
  `allowed_mentions: {parse: []}`; markdown escaped; max 10 posts/min per tenant; 429-aware; failures logged
  once (webhook id only) until success.

### 5.7 Data stores

`store.Store` implemented by `sqlstore` for postgres and sqlite with identical semantics (tests run against
both; postgres when `PE_TEST_POSTGRES_DSN` is set). Every method takes `tenantID` and every query filters by
it. Tables:

| Table | Columns |
|---|---|
| `schema_migrations` | version pk, applied_at |
| `meta` | key pk, value (license pepper in `all` mode) |
| `tenants` | id pk (random `t_` + 20 base32 chars), name, slug unique, leniency 4, autoban false, autokick false, mitigations true, enforcement false, reputation_opt_in false, discord_webhook_enc, discord_min_action 2, plan 'free', max_servers, license_verified_at, license_valid_until, over_limit_since, created_at, updated_at |
| `tenant_checks` | (tenant_id, check_id) pk, enabled, overrides (json) |
| `licenses` | key_hmac pk, tenant_id, created_at, revoked_at |
| `servers` | (tenant_id, id) pk, name, network_id, platform, platform_version, plugin_version, minecraft_version, secret_hash, instance_id, last_seen_at, last_batch_seq, created_at |
| `players` | (tenant_id, uuid) pk, last_name, first_seen_at, last_seen_at, last_server_id |
| `player_names` | (tenant_id, uuid, name) pk, seen_at |
| `verdicts` | id pk, tenant_id, server_id, network_id, player_uuid, player_name, session_id, action, category, score, confidence, reason, checks (json), evidence (json), clip_ids (json), shadow, status ('open','confirmed','overturned','expired','superseded'), supersedes, appeal_code, mitigations (json), strength, ban_duration_s, payload (serialized Verdict), created_at, delivered_at, acked_at, executed, ack_note |
| `bans` | verdict_id pk, tenant_id, server_id, player_uuid, active, reason, created_at, overturned_at, overturned_by, overturn_note, unban_verdict_id, unban_state ('none','pending','done') |
| `appeals` | id pk, tenant_id, verdict_id, player_uuid, message (<= 2000), contact (<= 200), status, created_at, decided_at, decided_by, decision_note |
| `labels` | id pk, tenant_id, player_uuid, verdict_id, label ('cheat','legit'), source ('confirmed','overturned','manual'), window_start, window_end, created_at |
| `clips` | id pk, tenant_id, server_id, player_uuid, start_us, end_us, codec, data, created_at |
| `clip_subjects` | (clip_id, player_uuid) pk, tenant_id (players appearing in a clip, e.g. attack targets) |
| `api_keys` | id pk, tenant_id, name, key_hash unique, scopes (json), created_at, last_used_at, revoked_at |
| `reputation_contrib` | (tenant_id, player_uuid, verdict_id) pk, weight, created_at |
| `usage_daily` | (tenant_id, day) pk, peak_players |
| `probe_stats` | (tenant_id, server_id, minute, source ('plugin','cloud')) pk, count, buckets (64 log buckets, json/blob), max_us |
| `audit_log` | id pk, tenant_id, principal, action, target, detail (json), created_at |
| `erasures` | (tenant_id, player_uuid) pk, requested_at, completed_at, counts (json) |

Retention (janitor, daily): clips 90 d unless referenced by an active ban, open appeal or label; verdicts and
evidence 180 d (bans kept while active, inactive bans 1 y after they end); appeals 1 y, contact deleted 30 d
after decision; labels 2 y; players and player_names 2 y after last seen; reputation_contrib 2 y; erasures
records 3 y (proof of erasure, no gameplay data); usage 2 y; probe_stats 30 d; audit_log 1 y. Dashboard
sessions are deleted at expiry and the D1 audit log after 1 y. Backups are kept 30 d. ClickHouse (optional): tables `signals` (TTL 30 d), `verdicts`
(90 d), `feature_windows` (180 d), `sessions` (90 d), MergeTree ordered by (tenant_id, time); batched inserts
(1 s / 10 000 rows), drop on overflow.

### 5.8 API (`api` command)

JSON, prefix `/api/v1`. Errors `{"error": "...", "code": "..."}`. Pagination `?limit=` (<= 200) + opaque
`cursor`. Per-key rate limit 600/min.

Authentication:
- Dashboard: `Authorization: Bearer <PE_SERVICE_TOKEN>` (constant-time; `PE_SERVICE_TOKEN_NEXT` also
  accepted for rotation) AND `X-PandaEye-Assertion: <base64url(json)>.<base64url(hmac)>` with json
  `{tenant, user_id, username, role, method, path, exp}` signed with `PE_ASSERTION_KEY`, `exp` <= 60 s ahead,
  method and path must match the request. The API enforces the role and records `user:<username>` as
  principal. `X-PandaEye-Client-IP` is honoured only with a valid assertion.
- API keys: `Authorization: Bearer pe_<id>_<secret>` (stored sha256), scopes `read` (default),
  `verdicts:write`, `settings:write`, `erase`. `X-PandaEye-Tenant` is ignored for API keys. Key management,
  `enforcement`/`autoban`/`autokick` changes and erasure require a dashboard assertion with role owner/admin.
- Every lookup by id filters `WHERE id = ? AND tenant_id = ?` and returns 404 otherwise. Every mutating call
  writes `audit_log`. Request bodies never carry the acting principal.

| Method + path | Purpose (role / scope) |
|---|---|
| `POST /api/v1/auth/license` (service token, no assertion) | `{license_key}` -> verify -> `{tenant_id, slug, created}`; creates tenant + licence row on first use |
| `POST /api/v1/auth/rebind` (owner) | `{license_key}` binds an additional/rotated key to the tenant, optionally revoking old ones |
| `GET /api/v1/tenant` (read), `PATCH /api/v1/tenant` (admin / settings:write) | settings (webhook returned masked); PATCH writes DB + KV `PE_TENANTS` and pushes `RemoteConfig` to connected servers |
| `GET /api/v1/servers` | servers with online state (from KV), versions, last seen, online players |
| `GET /api/v1/verdicts`, `GET /api/v1/verdicts/{id}` | filters: action, status, player, server, since, until; detail includes evidence |
| `POST /api/v1/verdicts/{id}/confirm` (staff / verdicts:write) | label `cheat`, status confirmed, reputation contribution if eligible |
| `POST /api/v1/verdicts/{id}/overturn` (staff / verdicts:write) | status overturned, label `legit`, reputation contribution removed; if the BAN was executed: create an UNBAN verdict (`supersedes`) routed to the server that executed it (pending until acked, dashboard shows "unban pending"); always UNMITIGATE to the open session |
| `GET /api/v1/clips/{id}` | decoded clip JSON for the replay viewer (strings escaped by the client) |
| `GET /api/v1/players/{uuid}`, `GET /api/v1/players?name=` | tenant-scoped summary: names, last seen, verdicts, bans; reputation only if opted in and the player joined this tenant in the last 24 h |
| `DELETE /api/v1/players/{uuid}` (owner/admin / erase) | erasure job (5.13); returns counts |
| `GET /api/v1/bans?active=true` | bans incl. unban state |
| `GET /api/v1/appeals`, `POST /api/v1/appeals/{id}/decide` (staff) | `{decision: accept|deny, note}`; accept = overturn |
| `POST /api/v1/public/appeals` (service token; the Worker verified Turnstile) | `{verdict_id, appeal_code, message, contact}`; only executed, non-shadow, active BANs; limits 5/h per client IP, 1 open and 3 total per verdict, 200/day per tenant; same response for unknown and wrong codes |
| `GET /api/v1/stats/overview`, `GET /api/v1/stats/latency` | counts by action/category 24 h / 7 d; probe RTT percentiles 1 h / 24 h from `probe_stats` |
| `GET /api/v1/usage` | peak players per day (90 d), plan, free-tier limit (30 peak players) |
| `POST/GET/DELETE /api/v1/apikeys` (owner) | plaintext returned once |
| `GET /api/v1/export/labels?since=` (read) | NDJSON of labels + evidence for ML, erased subjects excluded |
| `GET /api/v1/audit` (admin) | audit log |
| `GET/PUT /api/v1/checks` (admin / settings:write) | per-tenant cloud check configuration (`tenant_checks`), written to KV |
| `POST /api/v1/players/{uuid}/watch` (staff) | full-rate telemetry for 60-300 s |
| `GET /status` (no auth) | public status document (5.3) |

Free tier: 30 peak players. Over the limit for 3 days, the tenant's verdicts are capped at ALERT and the
dashboard shows a banner; players are never affected by billing state.

Metering loop (one leased `api` instance, every minute): sums `online_players` from live `PE_CONNECTIONS`
values per tenant (per network_id: the proxy's count when a proxy is connected, else the sum of backends) and
writes `usage_daily = max(existing, value)`.

### 5.9 Licensing

- `Verifier.Verify(ctx, key) (License{Valid, MaxServers, Plan}, error)`; keys are checked for format first.
- `pandascript`: `GET {PE_LICENSE_URL}/api/client/verify?product={PE_LICENSE_PRODUCT}` with
  `Authorization: Bearer <key>` -> 200 `{"valid":true,"productId":...,"maxServers":N,"currentServers":M}`
  (productId must equal the product) or 401 `{"valid":false,...}`. Only 401 + valid:false means invalid;
  429, 5xx and timeouts are retryable (`CODE_INTERNAL` with `retry_after_ms`), never UNAUTHENTICATED. Cache
  valid results 10 min and invalid 1 min (keyed by the key's HMAC); on errors allow a key cached valid within
  24 h. Singleflight per key; global outbound limit 20/s.
- `static`: `PE_STATIC_LICENSES`.
- Tenant mapping: `licenses.key_hmac = HMAC-SHA256(PE_LICENSE_PEPPER, key)` -> tenant_id (random). Raw keys
  are never stored or logged. Key rotation = `auth/rebind` (history stays with the tenant).

### 5.10 ML

- `internal/ml/features.go` defines the feature vector (names and order in `ml/FEATURES.md`, versioned)
  per player per 30 s window from worker state; written to telemetry `feature_windows`.
- `Scorer` interface; default build = disabled; `-tags onnx` builds an ONNX Runtime implementation
  (github.com/yalue/onnxruntime_go, shared library at runtime). Emits `ml.<model>` signals with 0 points
  unless `PE_ML_POINTS` > 0.
- `ml/` (Python): dataset export (API label export + ClickHouse feature windows, erased subjects excluded,
  labels only from trusted tenants), training (gradient boosting), ONNX export, `evaluate.py` gate (must beat
  the heuristic baseline on a held-out set at an equal or lower false-positive rate, else exit 1). A
  synthetic generator exists only to test the pipeline and says so.

### 5.11 Reputation network (opt-in)

- `reputation_contrib` rows are created only for BAN verdicts that are non-shadow, executed (ack), confirmed
  by staff (label `cheat`), include at least one cloud-computed check, from tenants that opted in and are
  trusted (paid plan, or older than 30 days with >= 50 distinct players in usage). Overturn or erasure
  deletes the row.
- Risk for a player = f(distinct trusted tenants with a contribution), capped weight per tenant; `risk > 0`
  only when >= 2 tenants agree. `PlayerRisk` is advisory (alert + focus telemetry), never scores or acts,
  and is sent only for players who actually joined the requesting tenant in the last 24 h, with a lookup
  budget of 2x the tenant's peak players per hour.

### 5.12 Webhooks

Accept only `https://{discord.com|discordapp.com|ptb.discord.com|canary.discord.com}/api/webhooks/<17-20
digits>/<60-80 [A-Za-z0-9_-]>` (exact host, no userinfo/port, only `wait`/`thread_id` query). The HTTP
client refuses redirects and refuses loopback/private/link-local/CGNAT/metadata IPs after DNS resolution.
A test message is sent on save. Stored encrypted (AES-GCM with a key derived from `PE_OUTBOUND_KEY`),
returned masked, logged by webhook id only.

### 5.13 Erasure (GDPR)

`DELETE /players/{uuid}` runs a job: SQL rows of the tenant (players, player_names, verdicts, bans, appeals,
labels, clips, clip_subjects, reputation_contrib), clips that contain the subject as a target are deleted,
ClickHouse `ALTER TABLE ... DELETE WHERE tenant_id = ? AND player = ?` on every table, and an erasure
tombstone (`erasures`) published to workers and the verdict service via KV so late data is dropped. Active
bans keep a minimal record (verdict id, uuid, check ids, score, time) under the legitimate-interest exception
until unbanned (documented in PRIVACY.md). JetStream expiry bounds residual data (24 h events, 7 d signals,
30 d verdict records).

## 6. Velocity plugin (`plugin/velocity`)

- Same transport (`plugin/common`), config `plugins/pandaeye/config.yml` (license, endpoint, network-id, mode,
  `session-secret`).
- Events: PlayerJoin (PostLoginEvent: name, protocol, network_session = random UUID, bedrock via Floodgate
  reflection or UUID msb == 0), brand (PlayerClientBrandEvent -> PlayerJoin{update=true}), ServerSwitch
  (ServerConnectedEvent), PlayerQuit (DisconnectEvent).
- After each backend connect, sends `pandaeye:session` = uuid, network_session, unix seconds,
  HMAC-SHA256(session-secret) to the backend; drops client-originated messages on that channel
  (PluginMessageEvent from a player source -> handled).
- Verdicts: in live mode with enforcement the proxy can run proxy commands for KICK/BAN (config), default
  shadow/log only (no punishment commands configured; alerts go to the console). The same circuit breaker
  and placeholder rules as the Paper plugin apply.
- `pandaeye:session` is registered with Velocity so forged messages (from clients or backends) can be
  dropped; registering it also announces the channel to backends, nothing is sent to clients.
- Command `/pandaeyeproxy` (alias `/peyeproxy`; `status`, `breaker [reset]`), so `/pandaeye` still reaches the
  backends.

## 7. Dashboard (`dashboard/`, Cloudflare Worker)

- TypeScript + Hono, server-rendered HTML via Hono JSX/html (auto-escaped; `raw()` never used on data),
  small client scripts (replay viewer on `<canvas>`, text via `fillText`/`textContent`, never innerHTML).
  CSP: `default-src 'self'; script-src 'self' 'nonce-<n>'; style-src 'self' 'nonce-<n>'; img-src 'self'
  data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'`; `X-Content-Type-Options: nosniff`.
- Bindings: D1 `DB`; secrets `PANDAEYE_API_URL`, `PANDAEYE_SERVICE_TOKEN`, `PANDAEYE_ASSERTION_KEY`,
  `PASSWORD_PEPPER`, `TURNSTILE_SECRET` (optional in dev), `TURNSTILE_SITEKEY`.
- D1 tables: `users` (id, tenant_id, username globally unique lowercase, password_hash
  `pbkdf2-sha256$100000$<salt>$<hash>` over `HMAC(PASSWORD_PEPPER, password)`, role owner|admin|staff|viewer,
  created_at, disabled, password_changed_at), `invites` (token_hash, tenant_id, role, expires_at, used_at,
  created_by), `sessions` (id_hash = sha256(id), user_id, csrf, created_at, last_seen_at, expires_at),
  `login_attempts` (key, count, window_start), `audit_log`. Workers PBKDF2 is capped at 100 000 iterations;
  the Workers Paid plan is required for its CPU time.
- Flows: `/setup` (license key + Turnstile, rate-limited 5/h per IP) works only while the tenant has zero
  users (checked in a D1 transaction) and creates the owner; later users join through single-use, expiring
  (48 h), hashed invites created by an owner/admin. `/login` (username + password), rate-limited per IP
  (10 / 15 min) and per username (exponential backoff); session cookie `__Host-pe_session` (HttpOnly,
  Secure, SameSite=Lax), 12 h absolute / 2 h idle, rotated on login, deleted on disable/role/password change.
  CSRF token on every POST. Every API call carries a fresh assertion.
- Pages: overview, servers, verdicts, verdict detail (evidence, confidence, replay viewer: top-down and side
  views of suspect and target positions, rotations as view rays, attacks/placements as markers, timeline
  scrubber), players, bans (with unban state), appeals (one-click overturn = label), settings, users and
  invites (owner/admin), API keys (owner), usage, public `/status`, public `/appeal` (Turnstile), `/privacy`.
- Roles: viewer reads; staff confirm/overturn/decide appeals; admin settings + invites; owner users + API keys.

## 8. Testing (gates)

- Go: `go test -race ./...`; legit and cheat fixtures for every check, including the review's legit cases:
  jump-reset knockback, Speed II knockback, explosion knockback while falling, levitation/slow falling end,
  Speed expiry while sprinting, ice/stone edge crossing, sprint-jump under a ceiling, entity pushing in a
  crowd, riptide in rain, elytra start, retreating target at 2.95 blocks with 150 ms ping at 15 TPS, mining
  after combat, 25 CPS drag clicking, low-sensitivity aim, server-forced rotations, GUI page switching,
  window-drag keep-alive delay, server stalls, legacy 1.12 sneaking eye height. Gateway integration tests:
  auth failure, cross-tenant server_id reuse, wildcard ids, duplicate install, decompression bomb, protobuf
  amplification, backpressure without data loss, ack/resend/dedup, verdict delivery + sweep + pending after
  reconnect, enforcement kill switch, probe round trip. Store tests on sqlite and postgres. Secret-redaction
  test.
- Java: JUnit 5 for every local check (legit and cheat sequences, including the cases above that apply
  locally), wrapper hygiene (lastUsedWrapper and reader index unchanged), codec fixtures shared with Go
  (`test/fixtures/`), pending buffer, seq blocks, spool, circuit breaker, placeholder sanitising.
- Shadow invariant: the ArchUnit Enforcer test; in e2e a probe plugin with a raw netty outbound counter and
  LOWEST/MONITOR listeners runs identical bot scripts with and without PandaEye; per-type outbound packet
  counts must be identical and no event may be cancelled or modified by PandaEye.
- Load: 200 simulated players at the production event mix across 10 servers against `pandaeye all`:
  probe RTT p95 < 100 ms and p99 < 250 ms in-region; packet handler < 2% of a core (JMH or load bench).
- Detection matrix (docs/DETECTION_MATRIX.md): Meteor/Wurst modules and ghost configurations mapped to the
  checks that cover them, and honestly the ones not covered in v1 (elytra/vehicle physics, criticals,
  legit-rotation scaffolds, local movement checks for clients older than 1.21.2), with cheat-client
  harness scenarios for the covered ones.
- E2E (`test/e2e`): cloud `all` + real Folia 1.21.11 and Paper 1.21.11 servers with PacketEvents 2.13.x
  (reEncodeByDefault as shipped), ViaVersion/ViaBackwards and PlugManX; legit bots: no confirmed flags, never
  kicked; wire bytes identical with and without PandaEye for a scripted session; cheat clients detected per
  check; `plugman unload/load` cycles leave no threads and the class loader is collected; onDisable < 300 ms
  with the cloud unreachable; probe RTT reported; listener cost and allocation budgets.

## 9. Privacy

See [`PRIVACY.md`](PRIVACY.md): roles (PandaEye is a processor for tenant data; a controller for the
reputation network and any cross-tenant ML training, with a legitimate-interest assessment and a template
notice for tenants), data collected (UUID, name, client version/brand, gameplay telemetry; no IPs), purposes,
retention (5.7), Art. 22 safeguards (appeal route and code in the ban message, human review via the
dashboard), minors, regions and sub-processors, erasure (5.13).
