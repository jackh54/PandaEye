# PandaEye end-to-end harness (`test/e2e`)

Real Paper / Folia / Canvas 1.21.11 servers with the production plugin stack (PacketEvents 2.13.1-SNAPSHOT b906
with `reEncodeByDefault` as shipped, ViaVersion, ViaBackwards, PlugManX, KnockbackSync), driven by legit bots and
cheat clients, measured by a probe plugin at the netty level. It works **without PandaEye** (baseline) and has
hooks for the PandaEye jar and the `pandaeye all` cloud binary. Contract: `docs/ARCHITECTURE.md` section 8, the
shadow invariants of section 1.1, and `docs/CHECKS.md`.

```
test/e2e/
  run.sh                    entry point (setup + orchestrator)
  lib/env.sh                paths, vendor jars, defaults (all overridable by env)
  servers/                  provision / start / stop / cmd / wait-for / logs / collect-logs / hotload / prepare-cache
    console-wrapper.py      headless console: commands through the FIFO <server>/console.in
    templates/              server.properties, bukkit.yml, spigot.yml, paper configs (low CPU), KnockbackSync config
  probe-plugin/             PandaEyeProbe: wire counters, PacketEvents LOWEST/MONITOR state, kicks, TPS, test commands
  fake-pandaeye/            a test double named "PandaEye" (NOT the product) to exercise the PandaEye phases now;
                            config `e2e-fake.violation: ping|wrap|cancel|leak-thread|slow-disable` = negative controls
  bots/                     Node 22: mineflayer legit bots, vanilla-mover oracle, raw cheat clients, scenario runner
  orchestrator/             phases, comparison, cloud adapter, PandaEye log readers, report,
                            selftest.js (offline test of the comparison), hprof-retainers.py (class loader leaks)
  run/                      (gitignored) servers, caches, reports
```

Vendor files are never committed: `PE_VENDOR_DIR` (default `/home/user/pe-vendor`) must contain
`servers/{paper-1.21.11-132,folia-1.21.11-14,canvas-1.21.11-794}.jar` and the plugins listed in `lib/env.sh`.
Java 21 is enough for all three servers (checked: Paper, Folia and Canvas 1.21.11 start on OpenJDK 21.0.12).

## Quick start

```bash
test/e2e/run.sh setup                                       # build probe, npm ci, patch server jars into run/cache
test/e2e/run.sh baseline --flavors paper,folia,canvas       # legit suite x2 per flavor without PandaEye
test/e2e/run.sh cheats --flavor paper                       # every cheat client once, packet pattern verified
test/e2e/run.sh all --dry-run                               # print every step incl. PandaEye/cloud phases
# PandaEye phases (real jar, or the fake for a rehearsal):
test/e2e/run.sh shadow hotload cycles ondisable load --flavors paper,folia --pandaeye-jar plugin/paper/build/libs/PandaEye-x.jar
test/e2e/fake-pandaeye/build.sh && test/e2e/run.sh shadow --pandaeye-jar test/e2e/fake-pandaeye/build/libs/PandaEye-0.0.0-e2efake.jar
test/e2e/run.sh shadow --pandaeye-jar <fake> --violation wrap   # negative control: must FAIL
```

Reports: `test/e2e/run/reports/<timestamp>/report.md` (summary) and `report.json` (all probe counters, traces
verdicts, comparisons), `orchestrator.log`, and `servers/<name>/` (console, latest.log, probe dumps, PandaEye logs).
Exit code 0 = PASS, 1 = FAIL. Options: `--flavors`, `--flavor`, `--repeat N`, `--scenarios <names|legit|cheat>`,
`--quick` (shortens > 20 s scenarios to 40 %), `--bots 20,40`, `--cycles 5`, `--keep-servers`, `--report-dir`.

## Servers (`servers/`)

```bash
servers/provision.sh NAME --flavor paper|folia|canvas --port 25601 [--profile baseline|pandaeye|hotload] [--fresh]
       [--no-probe] [--xmx 1536M] [--compression 256] [--pandaeye-jar PATH] [--cloud-endpoint URL] [--license KEY]
       [--mode shadow|live] [--telemetry-full] [--debug] [--kbsync-ping PING|KEEPALIVE] [--extra-plugin JAR]
servers/start.sh NAME --wait 180         # headless, console via FIFO, waits for "Done ("
servers/cmd.sh NAME probe dump probe/x.json
servers/cmd.sh NAME --wait 'OK dump' 20 probe dump probe/x.json
servers/wait-for.sh NAME 'regex' [timeout] [from_byte_offset]
servers/logs.sh NAME -f
servers/hotload.sh NAME                  # copies staged/PandaEye.jar to plugins/, `plugman load PandaEye`
servers/collect-logs.sh NAME OUTDIR
servers/stop.sh NAME
```

Low-CPU settings: `-Xmx1536M`, `-XX:ActiveProcessorCount=2`, view/simulation distance 4, flat world in the
`the_void` biome (no mobs, no features), peaceful, spawn protection 0, `online-mode=false`, `allow-flight=true`
(so vanilla does not kick fly cheats before PandaEye can see them), connection throttle off, 1 chunk worker, spark
and update checks off, Folia/Canvas with 2 region threads. Paper's packet limiter stays at the shipped default
(500 packets / 7 s; the flood cheat is sized below it). KnockbackSync is seeded with `ping_strategy: PING`
(production behaviour: it sends PINGs to combat-tagged players) and no update notification.

Profiles: `baseline` = the five production plugins + probe; `pandaeye` = + `plugins/PandaEye.jar` + generated
`plugins/PandaEye/config.yml` (all ARCHITECTURE 4.11 keys; `mode: shadow`, `cloud.endpoint:
ws://127.0.0.1:8080/v1/connect`, `cloud.license-key: $PE_E2E_LICENSE`); `hotload` = config generated, jar staged
in `staged/` and loaded with PlugManX after start.

## Probe plugin (`probe-plugin/`, `PandaEyeProbe.jar`)

Gradle (system Gradle 8.14), Java 21, `compileOnly` paper-api 1.21.4-R0.1-SNAPSHOT + packetevents-spigot 2.13.0 +
netty 4.2.7, `folia-supported: true`. It never modifies, cancels or delays a packet.

* **Wire taps** (per player channel, attached on PacketEvents `UserLoginEvent`): the first two handlers of the
  pipeline (closest to the socket) parse frames (VarInt length, zlib) and count every packet that really goes on /
  comes off the wire per state:type (`wire.out`, `wire.in`, bytes, per player). Real Paper pipeline (logged in
  every dump): `in-wire, out-wire, ..., splitter, decompress, [in-pre], via-decoder, pe-decoder-knockbacksync,
  pe-decoder-packetevents, [in-dec], decoder, prepender, compress, via-encoder, pe-encoder-knockbacksync,
  pe-encoder-packetevents, [out-enc], encoder, ...`.
* **In-flight accounting**: CRC32 of each packet right after the vanilla encoder (`out-enc`) vs on the wire, and
  on the wire vs right before the vanilla decoder (`in-dec`): `inflight.{out,in}.{matched,modified,dropped,injected}`
  per type, i.e. what PacketEvents listeners (incl. PandaEye's) and other handlers changed, cancelled or added.
  Costs: `cost.out.after_encoder_ns` (everything after the vanilla encoder minus the probe's own parsing) and
  `cost.in.decoder_chain_ns` (decompress -> vanilla decoder), avg/p50/p99.
* **PacketEvents LOWEST + MONITOR listeners**: per type counts, and between LOWEST and MONITOR whether another
  listener cancelled (`cancel_changed`), attached a wrapper (`wrapper_changed`, `wrapper_at_monitor`), toggled the
  re-encode flag, changed buffer indices or bytes (CRC), and `will_reencode` (wrapper attached and flag set at
  MONITOR). The MONITOR listener is re-registered after every plugin enable, so it runs after PandaEye's MONITOR
  listener (PacketEvents keeps insertion order within a priority: `CopyOnWriteArraySet`).
* kicks (`PlayerKickEvent` reason + cause), quits, joins, plugin enable/disable events; TPS from a 1-tick task on
  the global region scheduler and on every player's entity scheduler (works on Folia), Paper TPS/MSPT when
  available, process CPU and CPU per thread group (`pandaeye` = threads named `PandaEye*`, netty, tick, ...).
* `probe trace <player> <s>`: decoded fields of movement/combat/world/inventory/keep-alive/ping packets for one
  player (wrapper hygiene: lastUsedWrapper and reader index restored) - used to verify cheat clients.
* Test utilities (Paper and Folia safe, region/entity schedulers): `arena` (builds all fixtures and writes
  `arena.json`), `tp`, `gm`, `give`, `slot`, `effect`, `velocity`, `heal`, `flight`, `mount`/`dismount`,
  `setblock`, `fill`, `threads`, `watch <plugin>` + `gc <file>` (weak reference to the plugin class loader:
  collected or not), `disable-timed <plugin> <file>` (exact `disablePlugin` duration + PandaEye threads after).
  Every command logs `[probe] OK <cmd>` or `[probe] ERR <cmd> :: why`. `probe help` lists all.

## Bots (`bots/`, Node 22, pinned: mineflayer 4.39.0, minecraft-protocol 1.68.0, minecraft-data 3.117.0)

`node bots/run-scenario.js --list`; run one: `node bots/run-scenario.js --server paper1 --scenario cheat/speed --trace`.

* **vanilla-mover** (`lib/vanilla-mover.js`): exact 1.21.x client movement on open flat ground (walk, sprint,
  sprint-jump, falls, knockback) with float emulation and the `Mth.sin` table; self-test
  `node bots/lib/vanilla-mover.js` (sprint 0.280617 b/t, walk 0.215859, sprint-jump apex 1.252203). Verified on
  the server: the knockback scenario's dy sequence after `ENTITY_VELOCITY(vy 0.3608)` is 0.3608, 0.2752, 0.1913 ...
  and no "moved wrongly" warnings.
* **RawBot** (`lib/rawbot.js`): raw minecraft-protocol client behaving like vanilla at the packet level
  (brand, chunk-batch acks, `player_loaded`, teleport confirm + PosRot, keep-alive, ping/pong, respawn,
  `player_input`, sprint `entity_action`, `CLIENT_TICK_END` every tick on an absolute 50 ms schedule).
* **Legit scenarios** (expected: zero confirmed flags, never kicked):
  `legit/vm-walk, vm-sprint, vm-sprint-jump, vm-idle, vm-ledge, vm-knockback, vm-combat` (exact oracle, raw) and
  `legit/mf-walk, mf-stairs, mf-ledge, mf-swim, mf-pvp, mf-build, mf-chest, mf-teleport, mf-vehicle` (mineflayer,
  seeded human noise: 8-12 CPS jittered clicks, smoothed noisy aim, strafing, placing against visible faces with
  mineflayer's look-at, chest use standing still, console /tp + ender pearls, minecart + boat).
* **Cheat scenarios** (each names the check it targets, `checks` in the module; `--trace` verifies the packet
  pattern from the probe trace):

| scenario | check(s) | what it sends |
|---|---|---|
| cheat/timer-105, timer-120 | timer.balance | tick end + movement 21 / 24 times per second for 90 s |
| cheat/speed | movement.speed | 0.4 b/t on the ground |
| cheat/fly-hover, fly-ascend | movement.fly | hover at dy 0 airborne; ascend 0.1 b/t |
| cheat/step | movement.step | +1.0 block in one tick from the ground (x3 steps x3 runs) |
| cheat/groundspoof | movement.groundspoof | 20-block falls with onGround=true |
| cheat/noslow | movement.noslow | eat golden apples at full sprint speed |
| cheat/pitch | protocol.pitch | pitch 95 |
| cheat/position-nan, position-oow | protocol.position | x = NaN (vanilla kicks), x = 3.1e7 |
| cheat/selfhit | protocol.selfhit | attack own entity id (Paper kicks: "Cannot interact with self!") |
| cheat/cursor | protocol.cursor | placement cursor x 1.5 |
| cheat/flood | protocol.flood | 3 movement packets per tick |
| cheat/abilities | protocol.abilities | client abilities flying=true in survival |
| cheat/inventory-closed | inventory.closed | click in window 37 never opened |
| cheat/fastbreak | dig.speed | START then FINISH digging stone one tick later |
| cheat/reach | reach.distance | aimed attacks from 4.5 blocks |
| cheat/killaura-hitbox | killaura.hitbox | attacks while looking 90 deg away |
| cheat/killaura-multi | killaura.multi | two entities attacked in one tick |
| cheat/noswing | killaura.noswing | attacks without ANIMATION |
| cheat/autoclicker-consistency, -pattern | autoclicker.consistency, .pattern | exact 2-tick intervals (10 CPS); 2,2,1 (12 CPS) |
| cheat/aim-snap | aim.snap | 180 deg snap onto the target in the attack tick and back |
| cheat/velocity | velocity.vertical, .horizontal | ignores knockback from a real attacker |
| cheat/scaffold-face, -ray, -cursor, -reach | scaffold.* | invisible face, look away, wrong cursor, 6 blocks away |
| cheat/dig-ray | dig.ray | digs blocks behind the player |
| cheat/inventory-move, -rotate | inventory.move, .rotate | sprinting / rotating while clicking window 0 |
| cheat/pingspoof | latency.pingspoof | keep-alive replies 500 ms late, pongs immediate (in combat) |
| cheat/blink | latency.blink | holds all packets 1 s then releases, in combat (x4) |

## What is asserted

| phase | needs | asserts |
|---|---|---|
| baseline | vendor jars | every legit scenario runs, no legit player kicked; records per-type wire counts, PacketEvents/in-flight anomalies of the baseline stack, TPS/MSPT; two runs -> per-type noise table |
| cheats | vendor jars | every cheat client connects and its probe trace shows the intended packet pattern (tick rate, dy, pitch, cursor, intervals, delays, ...); expected vanilla kicks only for NaN / selfhit |
| shadow | PandaEye jar | same legit scripts with PandaEye (shadow mode): per-type OUTBOUND wire counts identical within the A/A noise (strict types such as PING, KEEP_ALIVE, PLUGIN_MESSAGE, PLAYER_POSITION_AND_LOOK, ENTITY_VELOCITY, chat, DISCONNECT: only noise tolerated), no increase of any PacketEvents/in-flight anomaly (cancel, wrapper left attached, re-encode, bytes changed, dropped, injected), no kicks |
| hotload | PandaEye jar | same after `plugman load PandaEye` with bots online; no errors, no kicks during the load |
| cycles | PandaEye jar | 5 x (`disable-timed`, `plugman unload`, `jcmd Thread.print`, `probe gc`, `plugman load`) with 4 bots: no kicks, no PandaEye errors, no `PandaEye-*` threads after unload, old class loaders collected |
| ondisable | PandaEye jar | `disablePlugin` <= 300 ms with the cloud refusing connections and with a black-hole TCP endpoint; no `PandaEye-*` threads 1 s later |
| load | (PandaEye jar) | TPS / tick p99 / CPU cores / CPU per packet / probe chain costs at 20 and 40 walking bots, with and without PandaEye; PandaEye's own handler cost from `pandaeye status` |
| detect | PandaEye jar (+ cloud) | cheats: expected check ids observed in `plugins/PandaEye/logs/flags-*.jsonl` (confirmed), cloud verdicts (`checks`, evidence) or `pandaeye replay` signals; legit: no confirmed flags, no verdicts |

## Integration contract (what the orchestrator expects from the plugin and the cloud)

Plugin (`plugin/paper`):
* jar: `plugin/paper/build/libs/PandaEye-*.jar` (newest, not `-sources/-javadoc/-plain`) or `PE_PANDAEYE_JAR`;
  plugin name `PandaEye` (`plugman load PandaEye`, `probe disable-timed PandaEye`), command `pandaeye status`.
* config keys written by `servers/provision.sh` (ARCHITECTURE 4.11): `mode`, `server-name`, `cloud.enabled`,
  `cloud.endpoint` (`ws://127.0.0.1:8080/v1/connect`: loopback `ws://` must be accepted), `cloud.license-key`,
  `telemetry.full`, `checks.*.enabled`, `alerts.*`, `exempt.*`, `debug`.
* `pandaeye status` output parsed leniently (`orchestrator/pandaeye.js#parseStatus`): `mode: shadow`, `cloud: <state>`,
  `avg <n> us ... p99 <n> us` (packet handler), `enforcer attempts <n>`.
* `plugins/PandaEye/logs/flags-YYYY-MM-DD.jsonl`: one JSON object per line with a player field (`uuid` or `name`),
  `check`, `confirmed` (bool), a time (`time`/`last`/`first`, ms), `count`. `peaks-*.jsonl` is collected.
* every thread named `PandaEye-*`; all gone after `disablePlugin` (+1 s); onDisable <= 300 ms.

Cloud (`cloud/`):
* binary `cloud/bin/pandaeye` or `PE_CLOUD_BIN`; started as `pandaeye all` with `PE_GATEWAY_LISTEN=:8080
  PE_API_LISTEN=:8081 PE_DB_DSN=run/cloud/pandaeye.db PE_NATS_DIR=run/cloud/nats PE_LICENSE_MODE=static
  PE_STATIC_LICENSES=<license>:20:pro PE_SERVICE_TOKEN=<48 chars> PE_ASSERTION_KEY=<48 chars>`; ready when
  `GET :8080/healthz` is 200.
* tenant: `POST :8081/api/v1/auth/license {"license_key"}` with `Authorization: Bearer <service token>` -> `tenant_id`.
* reads: `Authorization: Bearer <service token>` + `X-PandaEye-Assertion: b64url(json).b64url(HMAC-SHA256(key, b64url(json)))`,
  json `{tenant,user_id,username,role:"owner",method,path,exp}` (`PE_E2E_ASSERTION_SIGN=json` signs the raw JSON instead).
* `GET /api/v1/verdicts?player=<uuid>&since=<RFC3339>&limit=200[&cursor=]` -> array or `{items|verdicts|data, next_cursor}`;
  check ids read from `checks[]` and `evidence.signals[].check|check_id` / `evidence.checks{}`.
* `pandaeye replay --tenant T --player UUID --since RFC3339 --until RFC3339` prints one signal per line (JSON with
  `check_id`, or text containing the check id).
Confirm/adjust these in `orchestrator/cloud.js` once `cloud/internal/api` exists (marked there).

## Findings from the baseline stack (no PandaEye)

* **KnockbackSync swallows the PONGs to its own PINGs before the PacketEvents plugin sees them.** KnockbackSync
  shades its own PacketEvents; its decoder (`pe-decoder-knockbacksync`) sits before `pe-decoder-packetevents`.
  The probe counts the PONGs on the wire (`wire.in play:PONG`) and as `inflight.in.dropped play:PONG`, while
  PacketEvents-plugin listeners (and therefore PandaEye) see zero PONG events. KnockbackSync's PINGs themselves
  are visible to PacketEvents listeners. PandaEye's passive acknowledgement via other plugins' pings (CHECKS 1.1)
  and latency.pingspoof pairing will not get samples from KnockbackSync on this stack.
* With `reEncodeByDefault=true`, PacketEvents itself attaches wrappers (and will re-encode) for `login:LOGIN_SUCCESS`,
  `configuration:REGISTRY_DATA`, `configuration:CONFIGURATION_END`, `play:JOIN_GAME` and the inbound handshake in
  the baseline; the shadow comparison only fails on an *increase* of these counts.
* Paper 1.21.11 sends a keep-alive about every second (not every 15 s); KnockbackSync pings combat-tagged players
  every 5 ticks.
* **`plugman unload` cannot free the class loader of a plugin that was enabled at server start if it declares a
  command in plugin.yml**: `CraftServer.helpMap -> SimpleHelpMap.helpTopics -> GenericCommandHelpTopic ->
  PluginCommand.owningPlugin` keeps it (help topics are built at startup and PlugManX does not remove them).
  Instances loaded later with `plugman load` are collected after `plugman unload` (verified with the e2e fake on
  Folia). PandaEye declares `/pandaeye` in plugin.yml, so the "class loader collectable" gate can only hold for the
  startup instance if PandaEye removes its help topic on disable (reflection on `SimpleHelpMap.helpTopics`) or
  registers the command at runtime. The cycles phase therefore asserts collection for plugman-loaded instances and
  reports the startup instance separately; `PE_E2E_HEAP_ANALYSIS=1` dumps the heap on a leak and prints the
  retention path (`orchestrator/hprof-retainers.py <dump> <class>`).

## Known limitations

* mineflayer: never sends `CLIENT_TICK_END` (the harness injects one per physics tick, after that tick's movement
  packet), sends positions on exact change (vanilla: > 2.0E-4) with a 1 s reminder (vanilla: 20 ticks), rate-limits
  rotation, stops ticking while its chunk is not loaded, and its physics (prismarine-physics) differ slightly from
  vanilla on stairs/slabs/water/edges. Exact movement assertions therefore use the `vm-*` scenarios.
* vanilla-mover models open flat ground only (no walls, step-up, liquids, ice, sneaking edges, effects other than
  movement speed); diagonal input uses the 1.21.5+ square-movement formula (forward-only is exact).
* Legacy clients: `legit/mf-legacy-1204` runs mineflayer 1.20.4 through ViaBackwards. Clients before 1.18 cannot
  be tested on this world (surface at y = -60, they only know y 0..255; mineflayer 1.12.2 never loads a chunk).
* mineflayer vehicle control is partial: the minecart is mounted with `bot.mount`, the boat is spawned and mounted
  server-side by the probe; paddling input may not move the boat.
* Wire byte-identity across runs is not achievable (entity ids, positions, keep-alive ids, timing), so the shadow
  assertion is per-type counts within measured noise plus the exact in-flight/PacketEvents anomaly counters
  (which are byte-level: CRC of every packet before/after the PacketEvents encoders).
* `cheat/pingspoof` turns on `probe pinger 10` (the probe sends PINGs, like a second anticheat would) because
  KnockbackSync's own PONGs are invisible to PacketEvents listeners; this is the only place the probe sends
  packets, never in shadow-invariant runs.
* Timer/pingspoof need >= 60-90 s by design of the checks; `--quick` shortens them (pattern still verified,
  detection then not expected).
* Cloud adapter (`orchestrator/cloud.js`) is written against the spec; exact response shapes and the assertion
  HMAC input must be confirmed against `cloud/internal/api` when it lands.
