# PandaEye detection matrix (v1)

Which cheat-client modules PandaEye v1 detects, with which checks ([CHECKS.md](CHECKS.md)), what signal
to expect, what is honestly not covered, and the e2e harness scenarios that prove it (gate 3 in
[SLO.md](SLO.md)). Module names follow the common names used by Meteor, Wurst, LiquidBounce and ghost
clients; aliases are given where they differ.

## 1. How to read this

- **Covered**: the listed checks detect the module's usual configurations.
- **Partial**: detected only under the stated condition; other configurations evade.
- **Not covered**: v1 has no check for it (section 4).
- **Clients**: *tick* means tick clients only (protocol >= 768, Minecraft 1.21.2+, which send
  `CLIENT_TICK_END`); *all* means every client version. CHECKS.md section 0 limits all local movement
  checks and every tick-based check to tick clients.
- **Max alone (L4)**: the highest action the module's checks can reach on their own at the default
  leniency 4 (thresholds x1.3: ALERT 19.5, MITIGATE 39, KICK 91, BAN 156), from the check and family caps
  of CHECKS.md section 4. KICK always needs two families contributing >= 5 each; BAN needs two families or
  one `certain` check with `acc >= BAN/2` (ARCHITECTURE 5.6), plus confidence and clip conditions. A
  verdict above ALERT therefore usually comes from a combination of modules, which is how cheat clients
  are normally used.

### 1.1 Older clients (ViaVersion)

Clients before 1.21.2 (for example 1.8.9 ghost clients joining through ViaVersion) are not tick clients.
For them v1 has **no** local movement checks (speed, fly, step, groundspoof, noslow), no `timer.balance`,
`protocol.flood` or `dig.speed`, and no `killaura.multi`, `killaura.noswing`, `autoclicker.*`,
`inventory.move`, `inventory.rotate` or `latency.blink`. What remains for them: `reach.distance`,
`killaura.hitbox`, `aim.*`, `velocity.*`, `scaffold.*`, `dig.ray`, `latency.pingspoof`,
`protocol.pitch/position/slot/selfhit/cursor/abilities` and `inventory.closed`. The v1 ML model is also
applied to tick clients only. This is the largest gap of v1 for servers with many legacy-version players.

## 2. Matrix

| Module (aliases) | Checks | Expected signal | Clients | Coverage | Max alone (L4) |
|---|---|---|---|---|---|
| KillAura | `killaura.multi`, `killaura.hitbox`, `killaura.noswing`, `aim.snap`, `aim.precision`, `aim.gcd`, `reach.distance` | multi: attacks on >= 2 entity ids in one client tick. hitbox: >= 3 of the last 8 attacks on players whose rotation rays miss every target box candidate (silent or non-visual rotations). noswing: >= 3 of the last 10 attacks without a main-hand swing in the same tick. snap: >= 40 deg yaw into the attack and >= 30 deg back within 2 ticks, 3 times in 20 attacks. precision: mean angle to the target centre < 1.2 deg, SD < 0.6 over 25 attacks on fast targets. gcd: > 40% of combat rotation deltas off the player's sensitivity grid. reach: see Reach | multi, noswing: tick; others: all | Covered for multi-target, no-swing, snapping and silent-rotation auras. Partial for single-target auras with swing, smoothed and grid-aligned rotations: then only aim.precision / aim.gcd can fire | MITIGATE (killaura family cap 120; `killaura.multi` is certain but its family cap 120 is below BAN at L4) |
| Reach | `reach.distance` | min distance from the eye candidates to the target box candidates over the attack timing window exceeds 3.0 + eye uncertainty by > 0.05 on 2 of the player's last 5 attacks on players | all | Covered from about 3.3 blocks; Partial between about 3.15 and 3.3 (depends on target movement and ping); Not covered below about 3.15 (boxes are expanded by 0.1 on purpose) | MITIGATE (cap 80) |
| Hitboxes | `killaura.hitbox`, `reach.distance` | hits whose rays miss the real box (expanded by 0.1 + 0.1 for rotation timing) | all | Covered for expansions above about 0.2; small expansions are within the margins | MITIGATE |
| AimAssist / AimBot | `aim.gcd`, `aim.snap`, `aim.precision` | assist-generated deltas off the calibrated sensitivity grid in combat while non-combat deltas stay >= 90% on grid; snaps; unnaturally precise tracking | all (precision uses the attack tick's rotation for tick clients) | Partial: grid-aware (quantised) and humanised assists evade `aim.gcd`; weak assists stay above the precision limits | MITIGATE only with several aim checks (family cap 45; `aim.gcd` and `aim.precision` cap 30 each = ALERT) |
| AutoClicker | `autoclicker.consistency`, `autoclicker.pattern`, `autoclicker.cps` (disabled by default) | 40-click windows at 8-16 CPS with >= 95% identical intervals and SD < 0.25 ticks; or a period-2..4 pattern explaining >= 90% of intervals; cps: two windows >= 25 CPS | tick | Covered for fixed-delay and simple-pattern clickers; Partial / Not covered for randomised (distribution-based) clickers; not covered for legacy clients (1.1) | ALERT per check (cap 30); MITIGATE only when both fire (family cap 40) |
| TriggerBot | none specific | - | - | Not covered (section 4) | - |
| Velocity / AntiKB (AntiKnockback) | `velocity.vertical`, `velocity.horizontal` | vertical: the first consecutive move after a velocity packet ignores `vy` (>= 4 of the last 6 eligible velocities); horizontal: shortfall `s > 0.15` on >= 4 of the last 6 matched velocities | all (CHECKS 2.9-2.10 do not restrict them; the matching uses consecutive moves) | Covered for 0% vertical and <= 85% horizontal knockback; Not covered: 90-100% horizontal (shortfall below 15%), explosion knockback (excluded in v1). Jump-resetting is legitimate and accepted by design | MITIGATE (family cap 60) |
| Criticals | `protocol.flood` | packet mode: the extra flying packets before an attack exceed one per tick; a violation needs >= 3 excess ticks within 20 ticks | tick | Partial: only when crits are attempted on >= 3 ticks per second, which 1.9+ attack cooldowns prevent (a sword attacks about 1.6 times per second); Jump, MiniJump and NoGround modes are not covered (section 4) | ALERT (cap 20) |
| Speed (SpeedHack) | `movement.speed` | horizontal displacement above `h[n-1] * friction + acceleration + jump + 0.01`, buffered (>= 3) or a single excess > 0.5 | tick | Covered: vanilla multipliers, bhop, strafe. Partial: boosts within the 0.01 per-tick tolerance; exempt in water, lava, webs, powder snow, honey, bubble columns, climbing, vehicles, near entities and while velocity or a teleport is pending | MITIGATE (cap 50) |
| Fly (Flight) | `movement.fly`, `protocol.abilities`, `movement.groundspoof` | fly: airborne vertical motion deviating from gravity by > 0.03 twice or > 0.5 once, confirmed in open air; abilities: client claims flying without `allowFlight`; groundspoof for modes that fake ground | fly: tick; abilities: all | Covered: vanilla/creative fly, glide, air-jump in open air. Not covered: flying within 1 block of blocks (open-air confirmation) | MITIGATE (movement family cap 80) |
| Step | `movement.step` | `dy` of one packet from the ground above `max(step_height, jump_strength + jump boost)` | tick | Covered: single-packet step (attribute or teleport step) above the limit. Partial: multi-packet step modes (intermediate packets break the consecutive-packet rule; `protocol.flood` only when frequent) | MITIGATE (cap 50) |
| NoFall | `movement.groundspoof` | two consecutive packets claim `onGround` while falling faster than 0.1 per tick, with no supporting block or entity | tick | Covered: packet (constant ground spoof) NoFall. Not covered: a single spoofed packet just before landing, water-bucket and block-placing modes | MITIGATE (cap 40) |
| Timer | `timer.balance` | the minimum arrival offset of client ticks keeps falling (>= 8 of 10 two-second bins are new lows, >= 150 ms total, >= 100 ms without the largest step) | tick | Covered: 1.01x after about 80 s, 1.05x shortly after the 60 s calibration. Not covered: slow timer (< 1x), bursts shorter than the bins | MITIGATE (cap 60) |
| Blink | `latency.blink` | a gap > 500 ms followed by >= 10 client ticks within 100 ms, around combat, not overlapping a reported server stall; 3 in 60 s | tick | Covered in combat. Not covered: blink outside combat, constant fake lag (backtrack) | signal only (cap 18, family 25) |
| NoSlow (NoSlowdown) | `movement.noslow` | moving at unslowed speed while using an item for >= 3 ticks (excess > 0.03 on 5 consecutive packets) | tick | Covered: eating, drinking, blocking, bows. Not covered: sneak, soul sand, cobweb, honey and powder-snow NoSlow | MITIGATE (cap 40) |
| Scaffold (ScaffoldWalk) | `scaffold.face`, `scaffold.ray`, `scaffold.cursor`, `scaffold.reach` | face: the clicked face is not visible from any eye position; ray: no rotation candidate's ray hits the clicked block (>= 3 of 10); cursor: the click point is > 3 deg off every look vector (>= 3 of 10); reach: the block is beyond `block_reach + 0.6` | all (cursor: clients >= 1.11) | Covered: no-rotation scaffolds, impossible faces, cursor/rotation mismatches. Not covered: legit-rotation scaffolds (section 4) | MITIGATE (family cap 100) |
| Tower | `scaffold.*`, `timer.balance` | placement checks as for Scaffold; timer for timer-based towers | scaffold: all; timer: tick | Partial: only when placements are inconsistent with rotations or a timer is used; the vertical motion itself is exempt (blocks within 1 block fail the fly check's open-air confirmation) | MITIGATE via scaffold |
| FastBreak (SpeedMine) | `dig.speed` | FINISH after fewer client ticks than `ceil(1 / breakSpeed) * 0.7 - 1` (survival/adventure) | tick | Covered when the dig takes fewer than `0.7 * expected - 1` ticks: about 1.5x on blocks that take >= 30 ticks, larger speed-ups on faster blocks, and instant re-break. Not covered: small speed-ups on fast blocks, digs during which effects or attributes changed (skipped) | MITIGATE (cap 50) |
| Nuker | `dig.ray`, `dig.speed` | dig START/FINISH on blocks no rotation candidate's ray hits (through walls, around the player), >= 4 of 10 | ray: all; speed: tick | Covered: radius nukers without rotations. Partial: nukers that rotate to every block before digging | MITIGATE (dig family cap 70) |
| InvMove (GUIMove, InvWalk, InventoryMove) | `inventory.move`, `inventory.rotate` | accelerating movement or rotation in ticks whose window clicks show an open screen | tick | Partial: only while the player clicks in a window; opening one's own inventory sends no packet, so walking with an idle open inventory is invisible | ALERT per check (cap 30); MITIGATE only together (family cap 40) |
| ChestStealer (AutoSteal) | `inventory.closed` (clicks after the close only) | - | - | Not covered (section 4) | - |
| FastPlace | none | - | - | Not covered (section 4) | - |
| ElytraFly (ExtraElytra) | none | - | - | Not covered (section 4) | - |
| BoatFly / EntitySpeed | none | - | - | Not covered (section 4) | - |
| Jesus (LiquidWalk) | none | - | - | Not covered (section 4) | - |
| Spider | none | - | - | Not covered (section 4) | - |
| Phase / NoClip | none | - | - | Not covered (section 4) | - |
| PingSpoof | `latency.pingspoof` | keep-alive RTT > paired ping/pong RTT + 150 ms and > 1.6x on 3 of the last 4 paired keep-alives | all | Partial: needs ping packets sent by another plugin (PandaEye sends none; the server alone sends only keep-alives); a fake latency applied equally to keep-alives and pongs is invisible | signal only (cap 18) |
| BadPackets / protocol exploits | `protocol.pitch`, `protocol.position`, `protocol.selfhit` (certain), `protocol.slot`, `protocol.cursor`, `protocol.flood`, `protocol.abilities`, `inventory.closed` | pitch beyond +-90 deg; NaN/Infinity or out-of-world coordinates; attacking one's own entity; held slot outside 0..8; placement cursor outside [0, 1]; more flying packets per tick than allowed; flying claimed without permission; clicks in never-opened or closed windows | flood: tick; others: all | Covered | pitch, position, selfhit: BAN possible alone (certain, family cap 200, with clip and confidence); slot, flood: ALERT; cursor: signal only (cap 15); abilities: ALERT |

### 2.1 Ghost-client configurations

Closet configurations are designed to stay within legitimate ranges; v1 expectations:

| Configuration | Expected v1 result |
|---|---|
| Reach 3.0-3.15 | not detectable by design (box margins) |
| Reach 3.2-3.4 | `reach.distance`, more reliably against slow or stationary targets |
| AimAssist, low strength, humanised | usually not detected; `aim.gcd` only if the assist ignores the sensitivity grid |
| AutoClicker 10-14 CPS with randomised delays | not detected unless the randomisation leaves a dominant interval or a short repeating pattern |
| Velocity 85-95% horizontal | not detected (shortfall below 15%) |
| Velocity "jump reset" timing | legitimate technique, accepted |
| TriggerBot | not detected |
| Hitboxes 0.1-0.2 | not detected (margins) |

## 3. Harness scenarios (covered and partial rows)

The e2e suite (`test/e2e`) should implement these scenario names. `cheat/*` scenarios run a bot that plays
legitimately for 60 s (session seeding, timer calibration, aim calibration) and then uses the module for
the scenario duration D (default 120 s; noted otherwise). Bots are tick clients (1.21.11) unless noted.

| Scenario | Behaviour | Must signal | Target (default leniency 4) |
|---|---|---|---|
| `cheat/killaura_multi` | attacks two players in the same tick | `killaura.multi` | MITIGATE within D |
| `cheat/killaura_silent_rotation` | attacks with server-side rotations not facing the target | `killaura.hitbox` | ALERT within D |
| `cheat/killaura_noswing` | attacks without swing packets | `killaura.noswing` | ALERT within D |
| `cheat/killaura_snap` | snaps 60 deg to the target and back around each attack | `aim.snap` | ALERT within D |
| `cheat/killaura_perfect_aim` | tracks a strafing target's centre exactly at 2-4 blocks | `aim.precision` | signal within D |
| `cheat/reach_3_6` | attacks at 3.6 blocks, moving target | `reach.distance` | MITIGATE within D |
| `cheat/reach_3_3_static` | attacks a stationary target at 3.3 blocks | `reach.distance` | signal within D |
| `cheat/hitboxes_0_4` | attacks aimed 0.4 blocks outside the target box | `killaura.hitbox` | ALERT within D |
| `cheat/aimassist_offgrid` | combat rotations with continuous (non-grid) deltas, calibrated non-combat aim; D = 180 s | `aim.gcd` | signal within D |
| `cheat/autoclicker_fixed_12cps` | 12 CPS, constant interval, during combat | `autoclicker.consistency` | ALERT within D |
| `cheat/autoclicker_pattern_2_3` | alternating 2- and 3-tick intervals | `autoclicker.pattern` | ALERT within D |
| `cheat/autoclicker_30cps` | 30 CPS, tenant check `autoclicker.cps` enabled | `autoclicker.cps` | signal within D |
| `cheat/velocity_zero` | ignores knockback completely | `velocity.vertical`, `velocity.horizontal` | MITIGATE within D |
| `cheat/velocity_horizontal_60` | takes 60% horizontal, full vertical knockback | `velocity.horizontal` | ALERT within D |
| `cheat/criticals_packet_spam` | packet criticals on >= 3 attack ticks per second (no cooldown) | `protocol.flood` | signal within D |
| `cheat/speed_vanilla_1_5x` | 1.5x ground speed | `movement.speed` | MITIGATE within D |
| `cheat/speed_bhop` | bunny-hop speed | `movement.speed` | MITIGATE within D |
| `cheat/speed_strafe` | air strafe acceleration | `movement.speed` | ALERT within D |
| `cheat/fly_vanilla` | creative-style flight in open air | `movement.fly` | MITIGATE within D |
| `cheat/fly_abilities` | sends `PLAYER_ABILITIES` flying without permission | `protocol.abilities` | ALERT within D |
| `cheat/fly_glide` | slow descent in open air | `movement.fly` | MITIGATE within D |
| `cheat/fly_airjump` | jumps in mid-air | `movement.fly` | MITIGATE within D |
| `cheat/step_1_0` | single-packet 1-block step | `movement.step` | ALERT within D |
| `cheat/step_2_0` | single-packet 2-block step | `movement.step` | MITIGATE within D |
| `cheat/nofall_packet` | claims ground on every packet while falling | `movement.groundspoof` | ALERT within D |
| `cheat/timer_1_05` | client runs at 1.05x | `timer.balance` | ALERT within D |
| `cheat/timer_1_20` | client runs at 1.2x | `timer.balance` | ALERT within D |
| `cheat/timer_1_01` | client runs at 1.01x; D = 180 s | `timer.balance` | signal within D |
| `cheat/blink_combat` | holds packets 800 ms then releases them, around attacks, 3 times per minute | `latency.blink` | signal within D |
| `cheat/noslow_eating` | full speed while eating | `movement.noslow` | ALERT within D |
| `cheat/noslow_bow` | full speed while drawing a bow | `movement.noslow` | ALERT within D |
| `cheat/scaffold_no_rotate` | places below itself without rotation packets | `scaffold.ray` | MITIGATE within D |
| `cheat/scaffold_impossible_face` | places against faces not visible from the eye | `scaffold.face` | MITIGATE within D |
| `cheat/scaffold_cursor_mismatch` | click positions inconsistent with the sent rotation | `scaffold.cursor` | ALERT within D |
| `cheat/scaffold_reach` | places 6 blocks away | `scaffold.reach` | ALERT within D |
| `cheat/tower_no_rotate` | towers up without rotations | `scaffold.ray` | ALERT within D |
| `cheat/tower_timer` | towers with a 1.2x timer | `timer.balance` | signal within D |
| `cheat/fastbreak_1_5x` | breaks obsidian (or another block taking >= 30 ticks) 1.5x faster than vanilla | `dig.speed` | ALERT within D |
| `cheat/fastbreak_instant` | START and FINISH in the same tick on hard blocks | `dig.speed` | MITIGATE within D |
| `cheat/nuker_radius` | digs every block within 4 blocks without rotations | `dig.ray` | MITIGATE within D |
| `cheat/invmove_walk_click` | accelerates while clicking in a chest | `inventory.move` | signal within D |
| `cheat/invmove_rotate_click` | rotates while clicking in a chest | `inventory.rotate` | signal within D |
| `cheat/pingspoof_keepalive_delay` | delays keep-alive replies by 400 ms; the harness server runs a plugin that sends ping packets | `latency.pingspoof` | signal within D |
| `cheat/badpackets_pitch` | pitch 95 deg | `protocol.pitch` | MITIGATE within D |
| `cheat/badpackets_nan_position` | NaN coordinate | `protocol.position` | MITIGATE within D |
| `cheat/badpackets_selfhit` | attacks its own entity id | `protocol.selfhit` | MITIGATE within D |
| `cheat/badpackets_slot_range` | held slot 9 | `protocol.slot` | ALERT within D |
| `cheat/badpackets_cursor` | cursor 1.5 on placements | `protocol.cursor` | signal within D |
| `cheat/badpackets_flood` | 3 movement packets per tick | `protocol.flood` | ALERT within D |
| `cheat/badpackets_closed_window_click` | clicks in a window it closed | `inventory.closed` | ALERT within D |

Legit scenarios (no signal expected), from the review's legit cases (ARCHITECTURE 8) plus general play:

| Scenario | Guards against |
|---|---|
| `legit/jump_reset_knockback` | velocity.vertical |
| `legit/speed2_knockback` | movement.speed, velocity.* |
| `legit/explosion_knockback_falling` | movement.fly, velocity.* |
| `legit/levitation_end`, `legit/slow_falling_end` | movement.fly |
| `legit/speed_expiry_sprinting` | movement.speed |
| `legit/ice_stone_edge` | movement.speed |
| `legit/sprint_jump_low_ceiling` | movement.speed, velocity.vertical |
| `legit/crowd_pushing` | movement.speed |
| `legit/riptide_rain` | movement.fly, movement.speed |
| `legit/elytra_start` | movement.fly |
| `legit/retreating_target_2_95_ping150_tps15` | reach.distance |
| `legit/mining_after_combat` | autoclicker.*, dig.* |
| `legit/drag_click_25cps` | autoclicker.consistency, autoclicker.pattern (and documents autoclicker.cps as a policy choice) |
| `legit/low_sensitivity_aim` | aim.gcd |
| `legit/server_forced_rotation` | aim.*, protocol.flood, killaura.hitbox |
| `legit/gui_page_switching` | inventory.* |
| `legit/window_drag_keepalive_delay` | latency.pingspoof |
| `legit/server_stall` | timer.balance, latency.blink |
| `legit/legacy_1_12_sneak_eye_height` | reach.distance, scaffold.* (legacy client through ViaVersion) |
| `legit/pvp_duel_10min` | every combat check |
| `legit/bridging_vanilla` | scaffold.*, movement.* |
| `legit/plugman_reload_cycle` | hot-load sessions (`plugman unload` / `load` while players move and fight) |

## 4. Not covered in v1

| Module | Why v1 cannot see it | Planned approach |
|---|---|---|
| ElytraFly | gliding exempts every local movement check; there is no elytra physics model | cloud-side glide model (vanilla glide physics, firework boosts from `UseItem`) on `MoveSample`s flagged `GLIDING` |
| BoatFly / EntitySpeed | players in vehicles are exempt everywhere; vehicle movement (`VEHICLE_MOVE`) is tracked only as "in vehicle" and not sent to the cloud | forward vehicle movement samples and model each vehicle type (boats on water and ice, horses with their speed attributes) |
| Jesus | water and lava exempt movement checks for 1 s after the environment sampler sees them | fluid movement model (water and lava drag, swimming) plus a "standing on a fluid surface" condition from the sampler |
| Spider | climbing a non-climbable wall moves the player up next to blocks, so the fly check's open-air confirmation discards it | collision-aware vertical model: ascending while horizontally collided, without the CLIMBING environment flag |
| Phase / NoClip | v1 does not simulate block collisions (the server's own movement validation still applies) | confirmation-step test that the player box at the new position overlaps solid collision shapes |
| Legit-rotation scaffolds | the client sends rotations that actually look at the clicked point, so face, ray and cursor checks pass | rotation-dynamics and placement-timing features (the v1 ML feature vector already includes placement features), sprint-bridging speed limits |
| ChestStealer | no check on click timing inside containers; `inventory.closed` catches only clicks after a close | container click-interval statistics and minimum human inter-slot intervals (ML feature `inv_click_interval_min_ms` exists) |
| FastPlace | no placement-rate check; manual clicking can legitimately exceed vanilla's 4-tick held-key cooldown, so a rate limit alone would false-flag | placement cadence per tick combined with held-key context (ML feature `place_peak_per_s` exists) |
| TriggerBot | no check on attack timing relative to the crosshair entering the target; aim is human | reaction-time distribution between the first tick the attack-tick rotation intersects the target and the attack |
| Criticals beyond packet mode (Jump, MiniJump, NoGround) | jump modes are real (if short) jumps; NoGround claims "airborne" while standing, which the fly check discards near blocks | ground-state consistency check: claimed airborne while supported by a block |
| Movement and timer cheats on clients before 1.21.2 | local movement checks are off for non-tick clients (CHECKS section 0) | legacy movement model and tick inference for older protocols |
| Explosion knockback cheats | explosions are excluded from both velocity checks in v1 | model explosion knockback added to existing motion |

## 5. Gate 3 targets

Gate 3 passes when, in the e2e environment of ARCHITECTURE 8 (cloud `all`, real Folia 1.21.11 and Paper
1.21.11 servers with PacketEvents 2.13.x as shipped, ViaVersion/ViaBackwards and PlugManX, tenant at the
default leniency 4):

1. every `cheat/*` scenario of section 3 produces at least one signal of each "Must signal" check within
   its duration (for local checks: a confirmed LocalFlag forwarded as a signal), on Paper and on Folia;
2. every scenario with an ALERT or MITIGATE target reaches a verdict of at least that action within its
   duration (the tenant's enforcement may be off: shadow verdicts count);
3. every `legit/*` scenario produces, for its whole duration, no confirmed local flag, no signal with
   points > 0, no verdict of any action and no kick, on Paper and on Folia;
4. the shadow invariants hold during all scenarios (identical per-type outbound packet counts with and
   without PandaEye, no cancelled or modified packet or event).

A scenario marked partial in section 2 is held to its stated condition only (for example
`cheat/criticals_packet_spam` runs without attack cooldown). Modules in section 4 have no gate-3
scenarios in v1; adding a check for one adds its scenarios here. The report lists, per scenario and
platform: pass/fail, time to the first signal, the peak score and the verdicts produced.
