# PandaEye checks (v2)

Every check, where it runs, its algorithm, thresholds, and how it scores. Plugin (local) checks and
cloud checks share ids, categories and scoring. Physics are vanilla 1.21.11 client code (verified against
decompiled sources during the design review) unless stated.

## 0. Conventions

- **Tick clients**: client protocol >= 768 (1.21.2) or newer than PacketEvents knows. They send
  `CLIENT_TICK_END` once per client tick and at most one movement packet per tick (plus one PosRot per
  teleport and one Rot per server `PLAYER_ROTATION`). **All tick-based and all local movement checks run
  only for tick clients.** Older clients (ViaVersion-translated 1.8-1.21.1) get protocol checks and the
  cloud combat/world checks with legacy geometry (section 2.1); their local movement checks are off in v1.
- **Consecutive**: packet n is the first movement packet of the tick directly after packet n-1's tick.
  Checks that need consecutive packets skip otherwise (no tick-end seen between, or more than one).
- dy, h: vertical and horizontal displacement of one movement packet relative to the previous position.
- **VL**: local violation level (plugin only). **Points**: what one violation adds to the cloud score.
  **Half-life**: decay of the check's accumulator. **Cap**: max contribution of the check.
  **Family**: the id prefix before the first dot; families have their own cap (section 4).
  **Certain**: the check alone may justify a BAN (ARCHITECTURE 5.6). Checks without a "Live setback" value
  never set the player back.
- Thresholds below are the shipped defaults. They are deliberately lenient. The shadow rollout measures
  per-player peak values on real players and thresholds are tightened from that evidence (peak-per-player
  method), never by guessing. Every check is configurable (enabled, thresholds).

## 1. Local checks (plugin)

### 1.1 Shared machinery

**Client acknowledgement (passive).** PandaEye never sends packets. It observes every server KEEP_ALIVE
and PING that any plugin or the server sends (id + send time, per-id FIFO, entries expire after 30 s) and
the client's KEEP_ALIVE/PONG/TELEPORT_CONFIRM replies. `ackedUpTo` = send time of the newest server
packet the client has provably processed. A server-side change sent at time t is **applied by the client**
once `ackedUpTo >= t` and one more movement packet has arrived. Fallback when no acknowledgement arrives:
5 s after t. (Vulcan, TotemGuard and KnockbackSync send pings constantly, so acknowledgement normally
takes about one RTT.)

**Pending state (old/new pairs).** Every check-relevant server state (abilities, game mode, potion
effects, attributes, vehicle) is kept as `(applied, pending)`. Until the pending value is applied by the
client, checks use the more lenient of the two (higher speed/jump/step/gravity tolerance, flying or
levitating = exempt). Decreases therefore never make a check stricter before the client has them.

**Pending velocity.** Every S `ENTITY_VELOCITY` for the own entity and every S `EXPLOSION` with player
knockback, plus every Bukkit `PlayerVelocityEvent` seen at MONITOR (covers packets other plugins write
silently), adds a pending velocity. It stays pending until a movement matching it is observed (consumed),
or until it is applied by the client plus 2 movement packets, with a 5 s fallback. While any velocity is
pending, movement.speed/fly/step/groundspoof skip.

**Teleports.** A FIFO of pending server teleports (`PLAYER_POSITION_AND_LOOK`, relative flags resolved
against the last known position). `TELEPORT_CONFIRM` pops entries up to its id; the confirm's PosRot sets
the baseline (its `onGround=false` is ignored). Movement checks skip while the FIFO is non-empty and for
3 position packets after it empties. S `PLAYER_ROTATION` and S `FACE_PLAYER` are server rotations: the
next rotation-bearing packet is marked `SERVER_ROTATION`.

**Common exemptions (local movement checks).** Skip when any holds: player exempt (Bedrock via Floodgate
or UUID msb == 0; `pandaeye.bypass` outside shadow mode), session not yet seeded (hot load snapshot not
applied) or fewer than 40 movement packets since join/seed, any required state unknown, creative or
spectator (applied or pending), dead (S `UPDATE_HEALTH` health <= 0 until RESPAWN), in a vehicle, flying
(applied or pending) or flying ended < 1 s ago, gliding (metadata, env, or the provisional elytra
exemption), riptide (metadata, env, or the provisional riptide exemption), levitation or slow falling
(applied or pending), teleport pending (above), velocity pending (above), less than 3 position packets
after JOIN_GAME/RESPAWN and before the client's `PLAYER_LOADED` (1.21.4+), server tick rate not 20 or
the game frozen (S `TICKING_STATE`), the packet was marked cancelled by another plugin and the previous
one too (positions may be inconsistent), or the env sampler saw within the last 1000 ms: IN_WATER,
IN_LAVA, IN_BUBBLE_COLUMN, CLIMBING, IN_WEB, IN_POWDER_SNOW, ON_HONEY, STUCK, NEAR_ENTITY.

**Provisional exemptions.** C `PLAYER_DIGGING RELEASE_USE_ITEM` starts a provisional riptide exemption
for 2 s + ping. C `ENTITY_ACTION START_FLYING_WITH_ELYTRA` starts a provisional gliding exemption until
the server metadata says otherwise (max 2 s). The confirmation step validates them against authoritative
state (`isRiptiding`/`isGliding`, a riptide trident in hand and `isInWaterOrRain`); a player whose
provisional exemptions keep failing validation (3 in 60 s) stops receiving them for 10 minutes.

**Confirmation step.** A movement candidate records the world UID and the positions it used. On the
player's entity scheduler one tick later it is discarded unless the world still matches, the positions
are owned by the current region (`Bukkit.isOwnedByCurrentRegion`) and all touched chunks are loaded. It
then re-evaluates the common exemptions from authoritative server state (`isRiptiding`, `isGliding`,
`isFlying`, `isInsideVehicle`, a `PlayerVelocityEvent` in the last 2 s, game mode) and:

- `exemptNear(box)`: discard if the box (player box at positions n-2..n inflated by 0.3) intersects any
  liquid, bubble column, climbable, cobweb, sweet berry bush, powder snow, honey block, slime block, bed,
  piston/moving piston/piston head, or any entity that is pushable or collidable (players, all living
  entities, boats, minecarts, shulkers, happy ghasts, anything with a collision box). Only non-collidable
  entities are ignored (items, XP orbs, item frames, paintings, markers, displays, projectiles).
- `movement.speed` recomputes the bound with real friction: `f` from the **maximum** slipperiness under the
  0.6x0.6 footprint at `y - 0.5000001` of position n-2, `a` from the **minimum** slipperiness under the
  footprint at position n-1 (both are supersets of what vanilla's single supporting block can produce).
  Soul sand/soul soil under the player -> discard.
- `movement.fly`/`movement.step` require open air: `!world.hasCollisionsIn(box)` for the player box at both
  positions expanded by 1.0 in every direction.
- `movement.groundspoof` requires no collision in `[x-0.3, y-0.6, z-0.3]..[x+0.3, y+0.1, z+0.3]` at the last
  two positions and no collidable entity within 1.5.

Limits: max 4 outstanding confirmations and 2 per second per player, 200 per second server-wide (token
bucket); extra candidates are dropped and counted.

### 1.2 timer.balance - TICK_SPEED

Tick clients only. A legitimate client sends tick k at client time `c0 + 50k` ms and the server receives
it at `r_k = c0 + 50k + d_k` with network delay `d_k >= d_min`, so `offset_k = r_k - 50k` never falls below
`c0 + d_min`. Lag only raises offsets; catch-up after a stall returns to the floor, never below it (the
client never runs faster than 20 TPS and drops catch-up beyond 10 ticks per frame). A timer cheat makes the
floor fall continuously. A one-off drop of the network floor (queue drain, Wi-Fi roam) is a single step.

```
on CLIENT_TICK_END at receive time t (ms, double, System.nanoTime based):
  if not started or reset: t0 = t, ticks = 0, bins = [], start = t; return
  ticks += 1; offset = (t - t0) - 50 * ticks
  bin = floor((t - start) / 2000); binMin[bin] = min(binMin[bin], offset)
  when a bin closes (t passes its end):
     if t - start < 60_000: continue                        # calibration
     runMin, runMinT = minimum over all closed bins before it, and the time it was reached
     newLow[bin] = binMin[bin] < runMin - 2 - 0.0005 * (t_bin - runMinT)   # 0.5 ms/s drift allowance,
                                                                           # measured from when runMin was set
  violation when, over the last 10 closed bins: >= 8 bins were new lows AND the running minimum fell by
  >= 150 ms over those bins AND it still fell by >= 100 ms when the single largest bin-to-bin drop is
  excluded. After a violation, the check starts counting again from the next bin.
```

Timer 1.01x flags after ~60 s + 20 s, 1.05x after the 20-s window; steps and lag never flag. Reset on
re-entry to PLAY from configuration, on S `TICKING_STATE` with a rate other than 20 or frozen, and on
RESPAWN with a dimension change. VL +1, decay 1 per 120 s. Points 6, confidence 0.85. Live setback at
VL >= 2.

### 1.3 movement.speed - MOVEMENT

Consecutive position packets n-2, n-1, n; not sneaking-on-ground at n-1 (edge clipping hides momentum).
With `bf = 0.6` (netty-side default; the confirmation recomputes):

```
f      = onGround[n-2] ? bf * 0.91 : 0.91          # friction applied at the end of tick n-1
sprint = speedLenient * 1.3                          # speedLenient = movement_speed without the sprint
                                                     # modifier, max(applied, pending)
a      = onGround[n-1] ? sprint * 0.21600002 / bf^3 : 0.026
jump   = onGround[n-1] and (dy[n] > 0 or ceilingWithin0.42) ? 0.2 : 0   # ceiling checked in confirmation
bound  = h[n-1] * f + a + jump + 0.01
excess = h[n] - bound
```

`excess > 0` adds 1 to a buffer (buffer decays 0.1 per non-excess packet, min 0); buffer >= 3 or a single
`excess > 0.5` makes a candidate (buffer reset). Severity `min(1, excess / 0.3)`. VL +1 (+2 if
excess > 0.3), decay 1 per 30 s. Points `2 + 4*severity`, confidence 0.75. Live setback at VL >= 5.

### 1.4 movement.fly - MOVEMENT

Consecutive packets n-2, n-1, n, all airborne (`onGround` false at n-2, n-1 and n). Gravity
`g = max(applied, pending)` of the gravity attribute (0.08).

```
expected = (dy[n-1] - g) * 0.98 ; if |expected| < 0.003: expected = 0
diff     = dy[n] - expected
legit if |dy[n] + 0.098| < 0.001                     # client without the chunk below: motion.y = -0.1
```

Candidate when `diff > 0.03` on two consecutive packets, or `diff > 0.5` once. Confirmation requires open
air. Severity `min(1, diff / 0.5)`. VL +1, decay 1 per 30 s. Points `4 + 6*severity`, confidence 0.8.
Live setback at VL >= 3.

### 1.5 movement.step - MOVEMENT

Consecutive packets, `onGround[n-1]` true, `dy[n] > max(stepHeight, jumpStrength + jumpBoost) + 0.001` with
`stepHeight`, `jumpStrength` = max(applied, pending) attribute values and `jumpBoost = 0.1*(amplifier+1)`
if jump boost is applied or pending. Candidate immediately; confirmation requires open air around
position n. Severity `min(1, (dy - limit) / 0.5)`. VL +1, decay 1 per 60 s. Points `4 + 4*severity`,
confidence 0.8. Live setback at VL >= 3.

### 1.6 movement.groundspoof - MOVEMENT

Consecutive packets n-1 and n both claim `onGround = true` while `dy[n-1] < -0.1` and `dy[n] < -0.1`.
Confirmation: no supporting block or entity. Severity `min(1, |dy[n]|)`. VL +1, decay 1 per 60 s.
Points `3 + 3*severity`, confidence 0.8. Live setback at VL >= 3.

### 1.6b movement.noslow - MOVEMENT

Same machinery and preconditions as movement.speed, evaluated while the player is using an item: own
entity metadata says "hand active" (applied by the client), no C `PLAYER_DIGGING RELEASE_USE_ITEM` since,
and at least 3 ticks since the use started. Vanilla scales movement input by 0.2 while using an item, so
`a` is multiplied by 0.2 (`a = sprint * 0.21600002 / bf^3 * 0.2` on the ground, `0.026 * 0.2` in the air;
the 0.2 sprint-jump term stays). Candidate when `excess > 0.03` on 5 consecutive packets; confirmation as
movement.speed. Severity `min(1, excess / 0.15)`. VL +1, decay 1 per 30 s. Points `3 + 3*severity`,
confidence 0.75. Live setback at VL >= 4.

### 1.6c dig.speed - WORLD_INTERACTION

Tick clients only, survival/adventure. On C `PLAYER_DIGGING FINISH` for the block of the last START:
`ticks` = client ticks between START and FINISH. On the player's region thread compute
`perTick = block.getBreakSpeed(player)` (Paper: tool, enchantments, haste/mining fatigue, water, on-ground
and attributes) and `expected = ceil(1 / perTick)` (blocks with `perTick >= 1` break instantly and are
skipped). Candidate when `(ticks + 1) * perTick < 0.7`, exactly the break the vanilla server refuses (the
player state used is the current one, so effects that changed during the dig and attribute changes pending
at the client make the check skip). Confirmation asks the server's ruling one tick later: when the block is
gone the server accepted the break (custom tools, plugins changing mining speed) and the candidate is
discarded; an unreadable chunk discards too. Severity
`min(1, 1 - ticks/expected)`. VL +1, decay 1 per 60 s. Points `3 + 4*severity`, confidence 0.8.

### 1.7 protocol.pitch - INVALID_PROTOCOL (certain)

A rotation-bearing packet with `|pitch| > 90.001`, not marked SERVER_ROTATION, and not after a server
teleport/rotation whose own pitch was outside [-90, 90] (until the next server rotation). Max one flag per
second. Points 25, confidence 1.0.

### 1.8 protocol.position - INVALID_PROTOCOL (certain)

NaN/Infinity in x, y, z, yaw or pitch, or `|x| > 3.0e7`, `|z| > 3.0e7`, `|y| > 2.0e7`. Max one flag per
second. Points 25, confidence 1.0.

### 1.9 protocol.slot - INVALID_PROTOCOL

The plugin mirrors the client's `carriedIndex`: 0 on JOIN_GAME and on re-entry from configuration,
updated only by C `HELD_ITEM_CHANGE`. A server `SET_HELD_ITEM(s)` with `s != mirror` expects one echo
`HELD_ITEM_CHANGE(s)`, which is not flagged. Slot outside 0..8: points 10, confidence 0.95. A change to the
slot equal to the mirror (a truly redundant packet): logged with points 0 until shadow data exists.

### 1.10 protocol.selfhit - INVALID_PROTOCOL (certain)

`INTERACT_ENTITY` (or 26.1+ `ATTACK`) whose target is the player's own entity id (known from JOIN_GAME or
the hot-load snapshot; unknown = skip). Points 25, confidence 1.0.

### 1.11 protocol.cursor - INVALID_PROTOCOL

`PLAYER_BLOCK_PLACEMENT` cursor component NaN or outside `[-0.501, 1.501]` (vanilla accepts a hit location
within 1.0000001 of the block's centre, and shapes larger than a block give values above 1). Building mods
encode data in the cursor, so this is weak. Points 3, confidence 0.6.

### 1.12 protocol.flood - INVALID_PROTOCOL

Tick clients only. Flying packets between two tick ends must be `<= 1 + teleports confirmed in that tick +
server PLAYER_ROTATIONs received in that tick`. >= 3 excess ticks in the last 20 ticks -> violation (window
reset). Points 3, confidence 0.7.

### 1.13 protocol.abilities - INVALID_PROTOCOL

Client `PLAYER_ABILITIES` with flying = true while allowFlight is false in both the applied and pending
state, game mode survival/adventure. Points 5, confidence 0.9.

### 1.14 inventory.closed - INVENTORY

`CLICK_WINDOW` with window id > 0 that was never opened in this session, or that was closed/replaced
(an S `OPEN_WINDOW` while another container is open replaces it) and the client has already applied the
close (passive acknowledgement, fallback 1.5*ping + 250 ms). Unknown after hot load until the first
OPEN_WINDOW/CLOSE_WINDOW. Points 3, confidence 0.8.

## 2. Cloud checks (workers)

### 2.1 Geometry and timing (shared, `internal/geom`)

- Look vector for yaw/pitch in degrees: `x = -sin(yaw)*cos(pitch)`, `y = -sin(pitch)`, `z = cos(yaw)*cos(pitch)`.
- Player hitbox 0.6 x 1.8 (times scale). Eye heights: standing 1.62, sneaking 1.27 (1.54 for clients
  before 1.14), swimming/gliding/crawling 0.4, times scale. Target boxes always use the largest possible
  player box (superset), expanded by `m = 0.1` (plus the item's hitbox margin when `extended_reach`).
- Eye position uncertainty: 0.03 for clients before 1.18.2, 0.0002 after.
- **Eye of an attack/placement/dig.** Since 1.20 the client picks its target every frame from
  `getEyePosition(partialTick)`: the eye lies between the position before the last movement packet and the
  last one. Clicks and attacks go out in the next tick with the last frame's pick, so a moving player clicks
  from up to one tick of movement behind the reported eye. Candidates: the reported eye, the other heights
  at the same feet, 8 points on the segment from the previous position (when the last position arrived
  within 150 ms and the step is under 4 blocks) and, for placements and digs, the next move's eyes, each at
  every candidate height. Heights: placements and digs standing and sneaking; attacks the reported height,
  plus the other of standing/sneaking when the sneaking state changed around the attack. A reduced report
  (swimming/gliding, eye 0.4) also tests standing and sneaking for attacks, and 0.4 for placements and
  digs: the plugin reports these flags while a change is unacknowledged, so without PONGs a stale flag can
  outlast the server's clear, while a real swim or crawl is judged from 0.4. Plain standing and sneaking
  reports never get the 0.4 eye, so a crawl the server does not flag (the vanilla pose-only crawl) is
  judged from the standing eye.
- **Rotation of an attack/placement.** Tick clients pick the target at the start of the tick with the
  rotation that the same tick's movement packet sends: `R1` = the rotation of the next movement packet with
  the same tick number. Candidates: R1, R0 (the previous rotation) and R0->R1 interpolations at 0.25 steps
  (shortest angular path). Non-tick clients: R0, R1 (next move within 100 ms) and interpolations. Precision
  metrics (aim.precision) use R1 only for tick clients.
- **Attack timing window** (what the attacker could have seen): `[T - rttHigh - 2*serverTickMs - 300 ms, T]`
  over the target's received positions, where `rttHigh` = max of the attacker's last 5 RTT samples
  (`Attack.rtt_high_ms`) and `serverTickMs` from the latest EnvSample (50 at 20 TPS). The segments between
  consecutive positions are sampled at 0, 0.25, 0.5, 0.75, 1. `target_history` covers >= 1500 ms.
- **Skip**: attacks/placements by sessions that are bedrock or exempt, players in creative/spectator or a
  vehicle, targets in a vehicle, `extended_reach` or `stab` attacks (reach and hitbox checks), ping > 600 ms,
  the 1 s after a teleport of either player, and samples marked SERVER_ROTATION.

### 2.2 reach.distance - HEURISTICS

`dist` = min distance from any eye candidate to any target box candidate in the window (0 inside).
`limit` = entity_interaction_range (3.0) + eye uncertainty. `excess = dist - limit`. Violation when
`excess > 0.05` on 2 of the player's last 5 attacks on players. Points `3 + 10*min(excess, 1.5)/1.5`,
confidence `0.7 + 0.25*min(excess/0.5, 1)`.

### 2.3 killaura.hitbox - HEURISTICS

Miss = no (eye, rotation) candidate ray intersects any target box candidate (expanded by an extra 0.1 for
rotation timing). Violation when >= 3 of the last 8 attacks on players missed. Points 5, confidence 0.85.

### 2.4 killaura.multi - HEURISTICS (certain)

Tick clients only. Attacks on >= 2 distinct entity ids within one client tick (vanilla picks one target per
tick). Points 10, confidence 0.95.

### 2.5 killaura.noswing - HEURISTICS

Tick clients only. Every INTERACT_ENTITY attack in tick T must be accompanied by a main-hand swing in tick T
(evaluated once an event from a later tick arrives; swings cancelled by other plugins still count, the
plugin reports all client packets). Violation when >= 3 of the last 10 attacks had no swing. Points 6,
confidence 0.9.

### 2.6 aim.gcd - HEURISTICS

Mouse rotations are multiples of `step = (s*0.6 + 0.2)^3 * 8 * 0.15` degrees for sensitivity s (a
continuous value in [0, 1]); spyglass scoping divides the step by 8 (samples marked SCOPING are dropped).

- Calibration (no attack in the last 2 s, not in a vehicle, not SERVER_ROTATION, not within 2 packets after
  a teleport, `|pitch| < 89`): keep the last 300 non-zero deltas (both axes) below 10 degrees; at least
  20% of them must be >= 10 counts of the candidate step. Coarse search s in 0..1 step 0.005, then refine
  s by least squares over `n = round(d/step)`. On-grid: `|d - n*step| < 0.05*step + n*sigma_step +
  2*ulp(|yaw|)`. Calibrated when >= 90% on-grid over >= 80 deltas.
- In combat (attack in the last 1 s): share of deltas < 30 degrees that are off-grid. Violation when
  calibrated, off-grid share > 0.4 over >= 30 combat deltas, AND the non-combat deltas after the combat
  window are still >= 90% on-grid (otherwise recalibrate instead). At most one violation per 30 s.
- Points 4, confidence 0.75, cap 30 (alone it never reaches MITIGATE).

### 2.7 aim.snap - HEURISTICS

A snap = `|dyaw| >= 40` in the attack tick or the tick before, followed within 2 ticks by `|dyaw| >= 30` in
the opposite direction, none of the samples SERVER_ROTATION. Violation when >= 3 snaps in the last 20
attacks. Points 5, confidence 0.8.

### 2.8 aim.precision - HEURISTICS

For attacks on players at distance 1.5..4 whose target moves > 0.15 blocks/tick: angle between the
attack rotation (R1 for tick clients) and the direction from the eye to the target box centre at the
target position closest to `T - ping/2`. Over the last 25 such attacks: mean < 1.2 degrees and standard
deviation < 0.6 -> violation, at most once per 60 s. Points 4, confidence 0.7, cap 30.

### 2.9 velocity.vertical - HEURISTICS

Only `VelocityApplied` with source VELOCITY_PACKET (explosions add to motion and are excluded from both
velocity checks in v1; no velocity is judged while an explosion arrived within `rttHigh + 400 ms`) and
`vy > 0.15`. `on_ground_before` in the event is the onGround state of the move before the velocity. The first consecutive move (`ticks_since_last == 1`) within
`[T, T + rttHigh + 400 ms]` must satisfy one of: `|dy - vy| < 0.012`; a jump reset
`|dy - max(vy, jumpPower)| < 0.012` when onGround on the previous move (`jumpPower` = jump_strength +
jump boost); or `dy <= vy + 0.012` with LOW_CEILING. `|vy| < 0.003` counts as 0. A newer velocity in the
window replaces the older one; a teleport cancels it. Not eligible when env had water/lava/web/climbing/
powder snow/bubble column/honey/STUCK in the last 1 s, or the player was gliding/flying/in a vehicle.
Ignored = the window ended with >= 3 consecutive position moves and no match. Violation when >= 4 of the
last 6 eligible velocities were ignored. Points 6, confidence 0.85.

### 2.10 velocity.horizontal - HEURISTICS

For matched velocities with `|v_xz| >= 0.3` and no horizontal collision on that move (tick clients report
it): `h_min = max(0, |v_xz| - a_max - (onGround[prev] ? 0.2 : 0) - 0.003)` with `a_max` =
movement_speed (no sprint) * 1.3 on the ground, 0.026 in the air. Shortfall `s = (h_min - h) / |v_xz|`
when `h < h_min`, else 0. Violation when >= 4 of the last 6 matched velocities have `s > 0.15`. Points 5,
confidence 0.8.

### 2.11 autoclicker.* - AUTO_CLICKER

Tick clients only. Clicks = main-hand ANIMATION packets within 1.5 s of an attack by the player, excluding:
swings from a dig START until its CANCEL/FINISH (no time limit) plus 6 ticks, and swings in a tick that also
has a PLAYER_BLOCK_PLACEMENT or USE_ITEM. Clicks are counted per tick (several ANIMATION packets between two
tick ends = several clicks). Windows of 40 clicks whose duration is <= 5 s.

- `autoclicker.consistency`: 8 <= CPS <= 16 and >= 95% of the intervals between click ticks identical and
  interval SD < 0.25 ticks -> points 4, confidence 0.7.
- `autoclicker.pattern`: 8 <= CPS <= 16, >= 2 distinct intervals, and a repeating sequence of period 2..4
  explains >= 90% of the intervals -> points 4, confidence 0.7.
- `autoclicker.cps`: two consecutive windows with CPS >= `limit` (default 25) -> points
  `2 + min(6, (cps - limit)*0.5)`, confidence 0.6. **Disabled by default** (drag/butterfly clicking is a
  server policy question).

### 2.12 scaffold.* - WORLD_INTERACTION

Skip placements with `inside_block` set, cursors outside [0, 1] (for scaffold.cursor), and clients before
1.11 for scaffold.cursor (byte cursor).

- `scaffold.face`: the clicked face must be visible from some eye candidate (superset over block shapes):
  UP `E.y > B.y - u`, DOWN `E.y < B.y + 1 + u`, EAST `E.x > B.x - u`, WEST `E.x < B.x + 1 + u`, SOUTH
  `E.z > B.z - u`, NORTH `E.z < B.z + 1 + u` with `u = 0.03 + eye uncertainty`. Points 8, confidence 0.95.
- `scaffold.ray`: miss = no (eye, rotation) candidate ray intersects the unit cube of the clicked block
  (expanded by 0.05) within `block_reach + 1.5`. Violation when >= 3 of the last 10 placements missed.
  Points 6, confidence 0.85.
- `scaffold.cursor`: `P = B + cursor`; mismatch = for every (eye, rotation) candidate the angle between the
  look vector and `P - E` is > 3 degrees (skip when `|P - E| < 0.3`). Violation when >= 3 of the last 10
  placements mismatched. Points 5, confidence 0.8.
- `scaffold.reach`: distance from every eye candidate to the clicked block's cube > `block_reach + 0.6`.
  Points `3 + 5*min(excess, 1)`, confidence 0.8.

### 2.12b dig.ray - WORLD_INTERACTION

For BlockDig START and FINISH events (survival/adventure, not in a vehicle): miss = no (eye, rotation)
candidate ray intersects the unit cube of the block (expanded by 0.05) within `block_reach + 1.5`, using
the event's eye and rotation as R0 and the next move as R1. Violation when >= 4 of the last 10 digs missed
(Nuker / break-through-walls patterns). Points 5, confidence 0.85.

### 2.13 inventory.move / inventory.rotate - INVENTORY

Tick clients only. A tick counts as "screen open" only if it has a window click (window 0 or the open
container), the previous tick also had one, and neither that tick nor the previous one has a C/S
CLOSE_WINDOW or S OPEN_WINDOW.

Focus (full-rate telemetry) starts with the first window click, so the first click tick of a burst is
never judged.

- `inventory.move`: in a screen-open tick, a consecutive position move with `h > 0.1` and
  `h > h_prev * 0.91 + 0.02` (still accelerating = movement input), no pending velocity, and no
  water/lava/ice/bubble/vehicle/gliding env. Violation when >= 3 such ticks in 5 s. Points 4, conf 0.75.
- `inventory.rotate`: a rotation change > 1 degree in a screen-open tick, not in a vehicle, not
  SERVER_ROTATION. Violation when >= 4 in 5 s. Points 3, confidence 0.7.

### 2.14 latency.pingspoof - LATENCY_ABUSE

Keep-alive samples exclude ids that were sent more than once within 30 s (plugin-injected keep-alives).
Each keep-alive sample is paired with the median of pong samples within +-2 s of it. Excess when the
keep-alive RTT > paired pong + 150 ms and > 1.6x paired pong. Violation when >= 3 of the last 4 paired
keep-alives are in excess (needs >= 4 keep-alives); at most once per 60 s. Unmatched responses: 0 points
in v1. Points 3, confidence 0.8, cap 18 (below ALERT alone).

### 2.15 latency.blink - LATENCY_ABUSE

Tick clients only. A blink = a gap > 500 ms between consecutive events followed by >= 10 client ticks
arriving within 100 ms, while the player attacked in the 3 s before the gap or during the burst, and the
gap does not overlap a `ServerStall` from the same server (the plugin reports stalls: >= 20% of active
players silent at once, or a late global-region heartbeat). Violation when >= 3 blinks in 60 s. Points 3,
confidence 0.6.

### 2.16 ml.<model> - by model

See ARCHITECTURE 5.10. Emitted only when `PE_ML_POINTS` > 0; with 0 (shadow) flagged windows are only
counted.

## 3. Forwarded local flags

The worker forwards LocalFlag events as signals with `local = true`, the check's points and confidence
from this document (severity-scaled where the formula says so), and the flag's context moves in the clip.

## 4. Scoring summary

| Check | Where | Category | Points | Conf | Half-life s | Cap | Certain |
|---|---|---|---|---|---|---|---|
| timer.balance | local | TICK_SPEED | 6 | 0.85 | 300 | 60 | |
| movement.speed | local | MOVEMENT | 2..6 | 0.75 | 120 | 50 | |
| movement.fly | local | MOVEMENT | 4..10 | 0.8 | 120 | 60 | |
| movement.step | local | MOVEMENT | 4..8 | 0.8 | 120 | 50 | |
| movement.groundspoof | local | MOVEMENT | 3..6 | 0.8 | 300 | 40 | |
| movement.noslow | local | MOVEMENT | 3..6 | 0.75 | 120 | 40 | |
| dig.speed | local | WORLD_INTERACTION | 3..7 | 0.8 | 300 | 50 | |
| protocol.pitch | local | INVALID_PROTOCOL | 25 | 1.0 | 3600 | 200 | yes |
| protocol.position | local | INVALID_PROTOCOL | 25 | 1.0 | 3600 | 200 | yes |
| protocol.slot | local | INVALID_PROTOCOL | 10 (0 redundant) | 0.95 | 600 | 20 | |
| protocol.selfhit | local | INVALID_PROTOCOL | 25 | 1.0 | 3600 | 200 | yes |
| protocol.cursor | local | INVALID_PROTOCOL | 3 | 0.6 | 600 | 15 | |
| protocol.flood | local | INVALID_PROTOCOL | 3 | 0.7 | 600 | 20 | |
| protocol.abilities | local | INVALID_PROTOCOL | 5 | 0.9 | 600 | 30 | |
| inventory.closed | local | INVENTORY | 3 | 0.8 | 300 | 20 | |
| reach.distance | cloud | HEURISTICS | 3..13 | 0.7..0.95 | 300 | 80 | |
| killaura.hitbox | cloud | HEURISTICS | 5 | 0.85 | 300 | 80 | |
| killaura.multi | cloud | HEURISTICS | 10 | 0.95 | 900 | 120 | yes |
| killaura.noswing | cloud | HEURISTICS | 6 | 0.9 | 600 | 80 | |
| aim.gcd | cloud | HEURISTICS | 4 | 0.75 | 300 | 30 | |
| aim.snap | cloud | HEURISTICS | 5 | 0.8 | 300 | 50 | |
| aim.precision | cloud | HEURISTICS | 4 | 0.7 | 300 | 30 | |
| velocity.vertical | cloud | HEURISTICS | 6 | 0.85 | 300 | 60 | |
| velocity.horizontal | cloud | HEURISTICS | 5 | 0.8 | 300 | 50 | |
| autoclicker.consistency | cloud | AUTO_CLICKER | 4 | 0.7 | 300 | 30 | |
| autoclicker.pattern | cloud | AUTO_CLICKER | 4 | 0.7 | 300 | 30 | |
| autoclicker.cps (off) | cloud | AUTO_CLICKER | 2..8 | 0.6 | 300 | 30 | |
| scaffold.face | cloud | WORLD_INTERACTION | 8 | 0.95 | 600 | 80 | |
| scaffold.ray | cloud | WORLD_INTERACTION | 6 | 0.85 | 300 | 80 | |
| scaffold.cursor | cloud | WORLD_INTERACTION | 5 | 0.8 | 300 | 60 | |
| scaffold.reach | cloud | WORLD_INTERACTION | 3..8 | 0.8 | 300 | 50 | |
| dig.ray | cloud | WORLD_INTERACTION | 5 | 0.85 | 300 | 60 | |
| inventory.move | cloud | INVENTORY | 4 | 0.75 | 300 | 30 | |
| inventory.rotate | cloud | INVENTORY | 3 | 0.7 | 300 | 30 | |
| latency.pingspoof | cloud | LATENCY_ABUSE | 3 | 0.8 | 600 | 18 | |
| latency.blink | cloud | LATENCY_ABUSE | 3 | 0.6 | 300 | 18 | |

Family caps (sum of a family's check contributions is capped): timer 60, movement 80, protocol 200,
inventory 40, reach 80, killaura 120, aim 45, velocity 60, autoclicker 40, scaffold 100, dig 70, latency 25,
ml 50. `Signal.points` carries the raw points of this table (severity-scaled where the formula says so);
confidence is applied separately by the verdict service (ARCHITECTURE 5.6). Variants: `protocol.slot`
has `range` (10 points) and `redundant` (0 points).

### 4.1 Experimental checks

Experimental checks are not yet validated on live traffic: they count towards the score (ALERT, MITIGATE)
but KICK and the BAN ladder also need a stable family (two families over 5 points, at least one of them over
5 from non-experimental checks). The certain-check BAN path never uses them. The first live shadow run
showed these firing on legit players (eye interpolation, plugins that rewrite velocity packets or hide
pongs); they stay experimental until shadow data clears them:

Experimental: `dig.speed`, `dig.ray`, `scaffold.face`, `scaffold.ray`, `scaffold.cursor`, `scaffold.reach`,
`velocity.vertical`, `velocity.horizontal`, `latency.pingspoof`, `latency.blink`.

The same table lives in code: `cloud/internal/checks/catalog.go` (authoritative for the cloud, includes
local checks for scoring) and `plugin/paper/src/main/java/dev/pandascript/pandaeye/paper/check/CheckCatalog.java`
(local checks only). A test on each side parses this table (`docs/CHECKS.md`, section 4) and asserts the
code matches it.
