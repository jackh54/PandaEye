# PandaEye checks

Every check, where it runs, its algorithm, thresholds, and how it scores. Plugin (local) checks and cloud
checks share ids, categories and scoring. Physics constants are vanilla 1.21.x unless stated.

Conventions

- "consecutive" packets: packet n directly follows packet n-1 with exactly one client tick between them
  (tick-end clients: tick counter differs by 1; other clients: no flying packet without a position in
  between). Checks that need consecutive packets skip otherwise.
- "tick clients": clients whose protocol is >= 768 (1.21.2) or newer than PacketEvents knows; they send
  `CLIENT_TICK_END` natively. Older clients (translated by ViaVersion) are never judged by tick-based
  logic.
- dy, h: vertical and horizontal displacement of one movement packet relative to the previous position.
- VL: local violation level (plugin only). Points: what one confirmed violation adds to the cloud score.
- half-life: decay of the check's accumulator in the verdict service. cap: max contribution of the
  check to the player score. certain: the check alone may justify a BAN (see ARCHITECTURE 5.6).
- Every check is configurable (enabled, thresholds) and every threshold below is the shipped default.
  Defaults are deliberately lenient: the shadow rollout measures per-check peak values on real players
  and thresholds are tightened from that evidence (peak-per-player method), never by guessing.

## Common exemptions (local movement checks)

Skip (no flag, no candidate) when any holds: player exempt (Bedrock, `pandaeye.bypass`), warm-up after
hot load, creative or spectator, dead, in a vehicle, flying (server or client abilities) or flying ended
less than 1 s ago, gliding (metadata or env), riptide (metadata or env), a teleport is pending or was
confirmed less than 3 position packets ago, a velocity/explosion knockback was sent less than
`ping + 1000 ms` ago, a RESPAWN/JOIN_GAME less than 2 s ago, or the env sampler saw within the last
1000 ms: IN_WATER, IN_LAVA, IN_BUBBLE_COLUMN, CLIMBING, IN_WEB, IN_POWDER_SNOW, ON_HONEY.

## Confirmation step (local)

A movement candidate records the positions it used. On the player's region thread one tick later:

- `exemptNear(box)`: any block in `box` that is a liquid, bubble column, ladder/vine/scaffolding/
  climbable, cobweb, powder snow, honey block, slime block, bed, piston/moving piston/piston head, or any
  entity other than the player (boats, shulkers, minecarts, other players, item frames excluded) ->
  discard.
- `movement.speed` recomputes the bound with the real friction (max slipperiness of the blocks under the
  0.6x0.6 footprint at `y - 0.5000001`) and discards if the movement now fits. Soul sand / soul soil
  under the player -> discard (soul speed).
- `movement.fly` and `movement.step` require "open air": no collidable block intersects the hitbox
  (0.6 x 1.8, scaled) at either position expanded by 1.0 horizontally and vertically; otherwise discard.
- `movement.groundspoof` requires: no block with a collision shape intersects the box
  `[x-0.3, y-0.6, z-0.3] .. [x+0.3, y+0.1, z+0.3]` at the last two positions and no entity within 1.5.

## Local checks (plugin)

### timer.balance - TICK_SPEED

Tick clients only. Minimum-offset drift: a legitimate client sends tick `k` at client time
`c0 + 50k` ms, the server receives it at `r_k = c0 + 50k + d_k` with network delay `d_k >= d_min`, so
`offset_k = r_k - 50k` never falls below `c0 + d_min`. A timer cheat makes `offset` fall without bound.

```
on CLIENT_TICK_END at receive time t (ms, double, System.nanoTime based):
  if not started: t0 = t, ticks = 0, minOffset = 0, bucket = 0, calibrateUntil = t + 60_000, last = t; return
  ticks += 1
  offset = (t - t0) - 50 * ticks
  bucket = max(0, bucket - (t - last) * 0.0005)          # 0.5 ms/s clock-drift allowance
  if offset < minOffset:
      if t >= calibrateUntil: bucket += minOffset - offset
      minOffset = offset
  last = t
  if bucket > 300: violation(severity = min(1, (bucket - 300) / 300 + 0.25)); bucket = 0
```

Lag only raises offsets (never flags); catch-up bursts after a stall return offsets to the floor, not
below it. Timer 1.01x flags after ~32 s, 1.05x after ~6 s. VL +1, decay 1 per 120 s. Points 4,
confidence 0.8, half-life 300 s, cap 60. Live setback at VL >= 3. Reset entirely after the player
re-enters the PLAY state from configuration.

### movement.speed - MOVEMENT

Consecutive position packets n-2, n-1, n. With `bf = 0.6` (default block friction):

```
f      = onGround[n-2] ? bf * 0.91 : 0.91          # friction applied at the end of tick n-1
sprint = movementSpeed (without the sprint modifier) * 1.3
a      = onGround[n-1] ? sprint * 0.21600002 / bf^3 : 0.026   # acceleration in tick n
jump   = (onGround[n-1] and dy[n] > 0) ? 0.2 : 0               # sprint-jump boost
bound  = h[n-1] * f + a + jump + 0.01
excess = h[n] - bound
```

`excess > 0` adds 1 to a buffer (buffer decays 0.1 per non-excess packet, min 0); buffer >= 3 or a single
`excess > 0.5` makes a candidate (buffer reset). Confirmation recomputes with the real friction (block
under position n-2 for `f`, under n-1 for `a`). Severity `min(1, excess / 0.3)`. VL +1 (+2 if
excess > 0.3), decay 1 per 30 s. Points `2 + 4*severity`, confidence 0.75, half-life 120 s, cap 50.
Live setback at VL >= 5.

### movement.fly - MOVEMENT

Consecutive packets n-2, n-1, n, airborne at n-1 and n (`onGround` false), not on ground at n-2 either
(so dy[n-1] is a pure air tick). Levitation or slow falling active -> skip. Gravity `g` = gravity
attribute (0.08).

```
expected = (dy[n-1] - g) * 0.98 ; if |expected| < 0.003: expected = 0
diff     = dy[n] - expected
```

Candidate when `diff > 0.03` on two consecutive packets, or `diff > 0.5` once. Confirmation requires
open air. Severity `min(1, diff / 0.5)`. VL +1, decay 1 per 30 s. Points `4 + 6*severity`, confidence 0.8,
half-life 120 s, cap 60. Live setback at VL >= 3.

### movement.step - MOVEMENT

Consecutive packets, `onGround[n-1]` true, `dy[n] > max(stepHeight, jumpStrength + 0.1*(jumpBoost+1 if
active)) + 0.001`. Candidate immediately; confirmation requires open air around position n (pistons,
slime, beds excluded by `exemptNear`). Severity `min(1, (dy - limit) / 0.5)`. VL +1, decay 1 per 60 s.
Points `4 + 4*severity`, confidence 0.8, half-life 120 s, cap 50.

### movement.groundspoof - MOVEMENT

Consecutive packets n-1 and n both claim `onGround = true` while `dy[n-1] < -0.1` and `dy[n] < -0.1`
(still falling). Confirmation: no supporting block or entity (see above). Severity `min(1, |dy[n]|)`.
VL +1, decay 1 per 60 s. Points `3 + 3*severity`, confidence 0.8, half-life 300 s, cap 40.

### protocol.pitch - INVALID_PROTOCOL (certain)

A rotation-bearing packet with `|pitch| > 90.001`. Disabled until the next teleport when the last
server teleport itself carried `|pitch| > 90`. Max one flag per second. Points 25, confidence 1.0,
half-life 3600 s, cap 200.

### protocol.position - INVALID_PROTOCOL (certain)

NaN/Infinity in x, y, z, yaw or pitch, or `|x| > 3.0e7`, `|z| > 3.0e7`, `|y| > 2.0e7`. Max one flag per
second. Points 25, confidence 1.0, half-life 3600 s, cap 200.

### protocol.slot - INVALID_PROTOCOL

`HELD_ITEM_CHANGE` with slot outside 0..8: points 10, confidence 0.95. Change to the slot already held
(known from the last client change or server `SET_HELD_ITEM`; unknown after hot load until the first
change): points 0.5, confidence 0.6. Half-life 600 s, cap 20.

### protocol.selfhit - INVALID_PROTOCOL (certain)

`INTERACT_ENTITY` whose target is the player's own entity id. Points 25, confidence 1.0, half-life
3600 s, cap 200.

### protocol.cursor - INVALID_PROTOCOL

`PLAYER_BLOCK_PLACEMENT` cursor component NaN or outside `[-0.001, 1.001]`. Some building mods encode
data in the cursor, so this is weak. Points 3, confidence 0.6, half-life 600 s, cap 15.

### protocol.flood - INVALID_PROTOCOL

Tick clients only. Flying packets between two tick ends must be `<= 1 + teleport confirms in that tick`.
A tick over the limit is an excess tick; >= 3 excess ticks in the last 20 ticks -> violation (window
reset). Points 3, confidence 0.7, half-life 600 s, cap 20.

### protocol.abilities - INVALID_PROTOCOL

Client `PLAYER_ABILITIES` with flying = true while the server-side allowFlight is known false, game
mode survival/adventure, and the server has not changed abilities in the last 1000 ms. Points 5,
confidence 0.9, half-life 600 s, cap 30.

### inventory.closed - INVENTORY

`CLICK_WINDOW` with window id > 0 that is not the open container (known from `OPEN_WINDOW` /
`CLOSE_WINDOW`; unknown after hot load until the first of those), not within 500 ms after a server-side
close. Points 3, confidence 0.8, half-life 300 s, cap 20.

## Cloud checks (workers)

Geometry (shared, `internal/geom`): Minecraft look vector for yaw/pitch in degrees:
`x = -sin(yaw)*cos(pitch)`, `y = -sin(pitch)`, `z = cos(yaw)*cos(pitch)`. Player hitbox width 0.6,
height 1.8 (times scale). Eye heights: standing 1.62, sneaking 1.27, swimming/gliding/crawling 0.4
(times scale). Target boxes always use the largest possible player box (superset), expanded by
`m = 0.1`. Attack timing window: `[T - ping - 250 ms, T]` over the target's received positions, with
the segments between consecutive positions sampled at 0, 0.25, 0.5, 0.75, 1. Rotation candidates:
`R0` = the last rotation before the event, `R1` = the rotation of the next move sample within 100 ms
(if any), plus R0->R1 interpolations at 0.25 steps (shortest angular path). Eye candidates: the event eye
and the alternative standing/sneaking eye height, plus the next move sample's eye (if within 100 ms).
Skip attacks/placements by players in creative/spectator or in a vehicle, against targets in a vehicle,
with ping > 600 ms, and in the 1 s after a teleport of either player.

### reach.distance - HEURISTICS

`dist` = min distance from any eye candidate to any target box candidate in the window (0 inside).
`limit` = entity_interaction_range (3.0). `excess = dist - limit`. Violation when `excess > 0.05` on 2 of
the player's last 5 attacks on players. Points `3 + 10*min(excess, 1.5)/1.5`, confidence
`0.7 + 0.25*min(excess/0.5, 1)`, half-life 300 s, cap 80.

### killaura.hitbox - HEURISTICS

Miss = no (eye, rotation) candidate ray intersects any target box candidate (expanded by an extra 0.1
for rotation timing). Violation when >= 3 of the last 8 attacks on players missed. Points 5, confidence
0.85, half-life 300 s, cap 80.

### killaura.multi - HEURISTICS (certain)

Tick clients only. Attacks on >= 2 distinct entity ids within one client tick (vanilla picks one target
per tick). Points 10, confidence 0.95, half-life 900 s, cap 120.

### killaura.noswing - HEURISTICS

Tick clients only. Every attack in tick T must be accompanied by a main-hand swing in tick T (evaluated
once an event from a later tick arrives). Violation when >= 3 of the last 10 attacks had no swing.
Points 6, confidence 0.9, half-life 600 s, cap 80.

### aim.gcd - HEURISTICS

Mouse rotations are multiples of `step = (s*0.6 + 0.2)^3 * 8 * 0.15` degrees for sensitivity `s` in
[0, 1]. Calibration (no attack in the last 2 s, not in a vehicle, not within 2 packets after a teleport,
`|pitch| < 89`): keep the last 200 non-zero rotation deltas (both axes) below 10 degrees. For candidate
steps (s from 0 to 1 in 0.005 increments) compute the share of deltas `d` with
`|d/step - round(d/step)| < 0.05` and `round >= 1`; take the largest step with share >= 0.9 over >= 60
deltas -> calibrated (trackpads and fractional DPI never calibrate, which disables the check for them).
In combat (attack in the last 1 s): share of deltas below 30 degrees that are off-grid. Violation when
calibrated and the off-grid share is > 0.4 over >= 30 combat deltas; at most one violation per 30 s.
Points 4, confidence 0.75, half-life 300 s, cap 40.

### aim.snap - HEURISTICS

A snap = `|dyaw| >= 40` in the attack tick or the tick before, followed within 2 ticks by `|dyaw| >= 30`
in the opposite direction. Violation when >= 3 snaps in the last 20 attacks. Points 5, confidence 0.8,
half-life 300 s, cap 50.

### aim.precision - HEURISTICS

For attacks on players at distance 1.5..4 whose target moves > 0.15 blocks/tick: angle between R0 and
the direction from the eye to the target box centre (target position closest to `T - ping/2`). Over the
last 25 such attacks: mean < 1.2 degrees and standard deviation < 0.6 -> violation, at most once per
60 s. Points 4, confidence 0.7, half-life 300 s, cap 40.

### velocity.vertical - HEURISTICS

On `VelocityApplied` (velocity packet) with `vy > 0.15`: the player must have a consecutive position
move with `|dy - vy| < 0.012` within `[T, T + ping + 400 ms]`. A newer velocity in the window replaces
the older one; a teleport cancels it. Not eligible when env had water/lava/web/climbing/powder
snow/bubble column/honey/LOW_CEILING in the last 1 s, or the player was gliding/flying/in a vehicle.
Ignored = window ended with >= 3 position moves and no match. Violation when >= 4 of the last 6 eligible
velocities were ignored. Points 6, confidence 0.85, half-life 300 s, cap 60.

### velocity.horizontal - HEURISTICS

For matched velocities with `|v_xz| >= 0.3` and no horizontal collision on that move (tick clients
report it): `ratio = h / |v_xz|`. Violation when the mean ratio over the last 6 is < 0.55 and >= 4 of them
are < 0.6. Points 5, confidence 0.8, half-life 300 s, cap 50.

### autoclicker.cps / autoclicker.consistency / autoclicker.pattern - AUTO_CLICKER

Tick clients only. Clicks = main-hand swings within 1.5 s of an attack by the player and not while
digging (a dig START less than 10 ticks ago without CANCEL/FINISH). Windows of 40 clicks whose duration
is <= 5 s (CPS >= 8). Intervals in ticks between ticks that have clicks.

- `autoclicker.cps`: two consecutive windows with CPS >= 20 -> points `2 + min(6, (cps - 20)*0.5)`,
  confidence 0.6.
- `autoclicker.consistency`: CPS >= 9 and >= 95% of intervals identical and interval SD < 0.25 ticks ->
  points 4, confidence 0.7.
- `autoclicker.pattern`: CPS >= 9, >= 2 distinct intervals, and a repeating sequence of period 2..4
  explains >= 90% of the intervals -> points 4, confidence 0.7.

Half-life 300 s, cap 50 each.

### scaffold.face - WORLD_INTERACTION

The clicked face must be visible from some eye candidate (superset over block shapes):
UP `E.y > B.y - 0.03`, DOWN `E.y < B.y + 1.03`, EAST `E.x > B.x - 0.03`, WEST `E.x < B.x + 1.03`,
SOUTH `E.z > B.z - 0.03`, NORTH `E.z < B.z + 1.03`. Points 8, confidence 0.95, half-life 600 s, cap 80.

### scaffold.ray - WORLD_INTERACTION

Miss = no (eye, rotation) candidate ray intersects the unit cube of the clicked block (expanded by 0.05)
within `block_reach + 1.5`. Violation when >= 3 of the last 10 placements missed. Points 6, confidence
0.85, half-life 300 s, cap 80.

### scaffold.cursor - WORLD_INTERACTION

Only when the cursor is inside [0, 1]. `P = B + cursor`; mismatch = for every (eye, rotation) candidate
the angle between the look vector and `P - E` is > 3 degrees. Violation when >= 3 of the last 10
placements mismatched. Points 5, confidence 0.8, half-life 300 s, cap 60.

### scaffold.reach - WORLD_INTERACTION

Distance from every eye candidate to the clicked block's cube > `block_reach + 0.6`. Points
`3 + 5*min(excess, 1)`, confidence 0.8, half-life 300 s, cap 50.

### inventory.move - INVENTORY

Tick clients only. A screen is open in ticks with a window click (window 0, or the open container). In
such a tick, a consecutive position move with `h > 0.1` and `h > h_prev * 0.91 + 0.02` (still
accelerating = movement input) with no velocity in the last 1 s and no water/lava/ice/bubble/vehicle/
gliding env is a GUI-move tick. Violation when >= 3 GUI-move ticks in 5 s. Points 4, confidence 0.75,
half-life 300 s, cap 40.

### inventory.rotate - INVENTORY

Tick clients only. A rotation change > 1 degree in a tick that has a window click, where the previous
click was <= 10 ticks earlier (not the first click of a burst), not in a vehicle, not within 2 packets
after a teleport. Violation when >= 4 in 5 s. Points 3, confidence 0.7, half-life 300 s, cap 30.

### latency.pingspoof - LATENCY_ABUSE

Over the last 60 s with >= 2 keep-alive and >= 5 pong samples: median keep-alive RTT > median pong RTT
+ 150 ms and > 1.6x median pong RTT -> violation, at most once per 60 s. Unmatched keep-alive
responses: points 2, at most once per 60 s. Points 4, confidence 0.8, half-life 600 s, cap 40.

### latency.blink - LATENCY_ABUSE

Tick clients only. A blink = a gap > 500 ms between consecutive events followed by >= 10 client ticks
arriving within 100 ms, while the player attacked in the 3 s before the gap or during the burst.
Violation when >= 3 blinks in 60 s. Points 3, confidence 0.6, half-life 300 s, cap 30.

### ml.<model> - by model

See ARCHITECTURE 5.10. Points 0 unless `PE_ML_POINTS` is set.

## Scoring summary

| Check | Where | Category | Points | Conf | Half-life s | Cap | Certain |
|---|---|---|---|---|---|---|---|
| timer.balance | local | TICK_SPEED | 4 | 0.8 | 300 | 60 | |
| movement.speed | local | MOVEMENT | 2..6 | 0.75 | 120 | 50 | |
| movement.fly | local | MOVEMENT | 4..10 | 0.8 | 120 | 60 | |
| movement.step | local | MOVEMENT | 4..8 | 0.8 | 120 | 50 | |
| movement.groundspoof | local | MOVEMENT | 3..6 | 0.8 | 300 | 40 | |
| protocol.pitch | local | INVALID_PROTOCOL | 25 | 1.0 | 3600 | 200 | yes |
| protocol.position | local | INVALID_PROTOCOL | 25 | 1.0 | 3600 | 200 | yes |
| protocol.slot | local | INVALID_PROTOCOL | 0.5 / 10 | 0.6 / 0.95 | 600 | 20 | |
| protocol.selfhit | local | INVALID_PROTOCOL | 25 | 1.0 | 3600 | 200 | yes |
| protocol.cursor | local | INVALID_PROTOCOL | 3 | 0.6 | 600 | 15 | |
| protocol.flood | local | INVALID_PROTOCOL | 3 | 0.7 | 600 | 20 | |
| protocol.abilities | local | INVALID_PROTOCOL | 5 | 0.9 | 600 | 30 | |
| inventory.closed | local | INVENTORY | 3 | 0.8 | 300 | 20 | |
| reach.distance | cloud | HEURISTICS | 3..13 | 0.7..0.95 | 300 | 80 | |
| killaura.hitbox | cloud | HEURISTICS | 5 | 0.85 | 300 | 80 | |
| killaura.multi | cloud | HEURISTICS | 10 | 0.95 | 900 | 120 | yes |
| killaura.noswing | cloud | HEURISTICS | 6 | 0.9 | 600 | 80 | |
| aim.gcd | cloud | HEURISTICS | 4 | 0.75 | 300 | 40 | |
| aim.snap | cloud | HEURISTICS | 5 | 0.8 | 300 | 50 | |
| aim.precision | cloud | HEURISTICS | 4 | 0.7 | 300 | 40 | |
| velocity.vertical | cloud | HEURISTICS | 6 | 0.85 | 300 | 60 | |
| velocity.horizontal | cloud | HEURISTICS | 5 | 0.8 | 300 | 50 | |
| autoclicker.cps | cloud | AUTO_CLICKER | 2..8 | 0.6 | 300 | 50 | |
| autoclicker.consistency | cloud | AUTO_CLICKER | 4 | 0.7 | 300 | 50 | |
| autoclicker.pattern | cloud | AUTO_CLICKER | 4 | 0.7 | 300 | 50 | |
| scaffold.face | cloud | WORLD_INTERACTION | 8 | 0.95 | 600 | 80 | |
| scaffold.ray | cloud | WORLD_INTERACTION | 6 | 0.85 | 300 | 80 | |
| scaffold.cursor | cloud | WORLD_INTERACTION | 5 | 0.8 | 300 | 60 | |
| scaffold.reach | cloud | WORLD_INTERACTION | 3..8 | 0.8 | 300 | 50 | |
| inventory.move | cloud | INVENTORY | 4 | 0.75 | 300 | 40 | |
| inventory.rotate | cloud | INVENTORY | 3 | 0.7 | 300 | 30 | |
| latency.pingspoof | cloud | LATENCY_ABUSE | 4 (2 unmatched) | 0.8 | 600 | 40 | |
| latency.blink | cloud | LATENCY_ABUSE | 3 | 0.6 | 300 | 30 | |

The same table lives in code: `cloud/internal/checks/catalog.go` (authoritative for the cloud, includes
local checks for scoring) and `plugin/paper/.../check/CheckCatalog.java` (local checks only). A test on
each side asserts they match this table.
