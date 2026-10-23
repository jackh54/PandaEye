# PandaEye ML feature vector

**Feature version: 1** (`FEATURE_VERSION = 1`)

This document is the contract between the Go worker (`cloud/internal/ml/features.go`, which computes
the vector) and the Python pipeline in `ml/` (which trains on it). `ml/pandaeye_ml/features.py` mirrors
the table in section 4, and a test parses this file and asserts that names, order, defaults and clip
ranges match. Any change to a name, the order, a definition, a default or a clip range requires a new
feature version (section 9). Models carry the version and the ordered names in their metadata, and the Go
scorer must refuse to load a model whose version or names differ from its own (section 8).

Features summarise the same behaviours as the checks in [`../docs/CHECKS.md`](../docs/CHECKS.md). Where a
feature says "as in CHECKS x.y", it reuses that check's machinery and per-event intermediate values
(eligibility, candidate geometry, miss and match decisions), so the Go implementation can read them from
the check's state rather than recomputing them.

## 1. Scope

- One vector per tracked player session per 30 s window, computed by the worker from its derived state
  and the player's events (ARCHITECTURE 5.5). Windows go to telemetry (`feature_windows`, section 6) and,
  when a model is loaded, to the ML scorer.
- No windows are produced for bedrock or exempt sessions, sessions without session info, players beyond
  the tenant's tracked-player cap, or events processed in warm-start mode (ARCHITECTURE 5.5).
- Windows are produced for every client protocol. Features marked **tick** are computed only for tick
  clients (`PlayerJoin.protocol_version >= 768`, CHECKS 0) and take their default otherwise. **The v1
  model is trained on, and must only be applied to, tick-client windows** (`client_protocol >= 768`;
  model metadata `pandaeye.min_client_protocol`). Older clients' windows are stored for later versions.

## 2. Windows

- **Time.** All window arithmetic uses normalised time `t = Event.time_us + Origin.clock_offset_us`
  (Unix microseconds, ARCHITECTURE 3). Plugin-clock fields inside events (`ServerStall.start_us/end_us`)
  are normalised the same way.
- **Grid.** `window_start_us = floor(t / 30_000_000) * 30_000_000`, `window_end_us = window_start_us +
  30_000_000`. The window is the half-open interval `W = [window_start_us, window_end_us)`.
- **Session.** A window belongs to one session: the player's most recently joined session (the one the
  per-player accumulators follow, ARCHITECTURE 5.5). Only that session's events are used, plus
  server-level `ServerStall` events whose origin `server_id` is the session's server.
- **Membership.** An event belongs to W when its `t` is in W. Counts, shares and statistics are over the
  window's own events and items (attacks, clicks, placements, velocities...) unless a definition says
  otherwise. State-based conditions ("attacked in the last 1 s", calibration, the latest EnvSample, pending
  velocity, teleports) use the worker's derived state at the event, which may come from before W.
- **Lookahead and finalisation.** Some items need later events to be decided (a swing in the same tick, a
  click's attack 1.5 s later, the move answering a velocity, R1 of an attack or placement, a burst after a
  gap). Items are attributed to the window of their own event time and decided with events up to
  `window_end_us + G`, `G = 2_000_000` (2 s). W is finalised and emitted when the first event of the
  session with `t >= window_end_us + G` is processed, when the session ends (PlayerQuit, replacement by a
  newer session), or when housekeeping sees the partition's newest normalised time pass `window_end_us +
  60 s`. Items still undecided at finalisation are excluded from numerators and denominators.
- **Emission.** A window is emitted at most once per (session, window_start_us), only if it contains at
  least one MoveSample. Empty windows are not emitted. A window that is cut short (session start/end,
  worker handover) is emitted with what it has; `active_seconds` shows its coverage.

## 3. Shared definitions

- **Statistics.** `mean = sum(x)/n`. `sd` = population standard deviation `sqrt(sum((x-mean)^2)/n)`.
  Central moments `m_k = sum((x-mean)^k)/n`. `skew = m3 / m2^1.5`. `kurtosis = m4 / m2^2` (Pearson, not
  excess; a normal distribution gives 3). If `m2 < 1e-12`, skew and kurtosis take their default.
  `median`: middle element of the sorted values, mean of the two middle elements for even n. Percentile
  `pQ` (nearest rank): sorted ascending, element at 1-based rank `ceil(Q/100 * n)`.
- **Defaults.** Every feature has a default (table column "default"). It is used when the feature's
  minimum sample count (column "min n") is not met, when its denominator is 0, when it does not apply
  (non-tick client for **tick** features), and when the computation produces NaN or Inf. Defaults are
  written as is and are NOT clipped; several deliberately lie outside the clip range (e.g. -1) so the
  model can tell "undefined" from any real value.
- **Clipping.** Every computed (non-default) value is clipped to the closed range in column "clip".
- **Type.** Every feature is stored and fed to the model as float32. Counts are exact integers in float32.
- **Shares.** `share = numerator / denominator` over the items named in the definition.
- **wrap180(a)** = `a - 360 * round(a / 360)` (degrees, result in [-180, 180]).
- **Consecutive move.** A MoveSample with `ticks_since_last == 1` and `focus == true` (tick clients).
- **Rotation pair.** A MoveSample `n` with `has_rot`, such that the previous MoveSample `n-1` of the session
  exists, `n` is a consecutive move, `n` is not flagged SERVER_ROTATION or IN_VEHICLE, and `n` is not one
  of the first 2 MoveSamples after a TeleportApplied (aim.gcd's "not within 2 packets after a teleport").
  `dyaw = wrap180(yaw[n] - yaw[n-1])`, `dpitch = pitch[n] - pitch[n-1]` (rotations as sent; a position-only
  sample carries the previous rotation forward).
- **Combat pair.** A rotation pair whose `t` has an Attack by the player in `[t - 1 s, t]` (as aim.gcd's
  "in combat"). **Idle pair**: a rotation pair with no Attack by the player in `[t - 2 s, t]` (as aim.gcd's
  calibration condition). Pairs whose last attack is 1-2 s ago are neither.
- **Eligible attack.** An Attack on a tracked player (`target_player` non-empty) that is not skipped by
  CHECKS 2.1 "Skip" (creative/spectator, vehicles, `extended_reach`, `stab`, ping > 600 ms, 1 s after a
  teleport of either player, SERVER_ROTATION).
- **Attack tick.** A client tick (Event.tick > 0) containing at least one Attack (tick clients).
- **Click.** A main-hand Swing counted as a click by autoclicker.* (CHECKS 2.11: within 1.5 s before or
  after an Attack by the player, excluding dig START..CANCEL/FINISH + 6 ticks and ticks with a BlockPlace
  or UseItem). Click ticks are the ticks with >= 1 click; `c_k` = clicks in tick k. **Intervals** `I_j` are
  the tick differences between consecutive click ticks of W, kept only if `I_j <= 10` (longer gaps are
  pauses, not intervals), in order.
- **Eligible placement.** A BlockPlace not skipped by CHECKS 2.1 "Skip" and with `inside_block == false`.
- **Latest CheckStats.** For a local check, the CheckStats event of the session with the largest `t` in
  `(window_end_us - 330 s, window_end_us + G]` for that `check_id` (CheckStats arrive every 5 minutes, so
  consecutive windows share them).

## 4. Feature table (model inputs, in order)

Column "events" names the wire events used. Units: `count` = number of items, `share` in [0, 1].

| # | name | unit | definition | events | min n | default | clip |
|---|---|---|---|---|---|---|---|
| 0 | `active_seconds` | s | `(t_last - t_first) / 1e6` over the session's events in W | all | 1 | 0 | [0, 30] |
| 1 | `move_count` | count | MoveSamples in W | MoveSample | - | 0 | [0, 3000] |
| 2 | `focus_share` | share | MoveSamples with `focus` / move_count | MoveSample | 1 | 0 | [0, 1] |
| 3 | `server_tick_ms_max` | ms | max `server_tick_ms` of EnvSamples in W; if none, of the latest EnvSample before W | EnvSample | 1 | 50 | [0, 1000] |
| 4 | `server_stall_ms` | ms | length of the union of normalised `[start_us, end_us]` of the server's ServerStalls, intersected with W, / 1000 | ServerStall | - | 0 | [0, 30000] |
| 5 | `env_special_share` | share | EnvSamples in W with any of IN_WATER, IN_LAVA, CLIMBING, IN_WEB, IN_POWDER_SNOW, IN_BUBBLE_COLUMN, ON_HONEY, STUCK, IN_VEHICLE, GLIDING, RIPTIDING / EnvSamples in W; if none in W, 1 or 0 from the latest EnvSample before W | EnvSample | 1 | 0 | [0, 1] |
| 6 | `click_count` | count | tick: clicks in W (sum of `c_k`) | Swing, Attack, BlockDig, BlockPlace, UseItem | - | 0 | [0, 2000] |
| 7 | `click_cps_mean` | 1/s | tick: `20 * sum(c_k) / sum(I_j)` where the sums run over the click ticks k that close an interval I_j | Swing | 5 intervals | -1 | [0, 60] |
| 8 | `click_cps_peak` | 1/s | tick: max over click ticks k of the clicks in ticks k-19..k | Swing | 1 click | 0 | [0, 60] |
| 9 | `click_interval_count` | count | tick: number of intervals I_j | Swing | - | 0 | [0, 2000] |
| 10 | `click_interval_mean` | ticks | tick: mean(I) | Swing | 5 | -1 | [1, 10] |
| 11 | `click_interval_sd` | ticks | tick: sd(I) | Swing | 5 | -1 | [0, 5] |
| 12 | `click_interval_skew` | 1 | tick: skew(I) | Swing | 8 | 0 | [-10, 10] |
| 13 | `click_interval_kurtosis` | 1 | tick: kurtosis(I) (Pearson) | Swing | 8 | 0 | [1, 100] |
| 14 | `click_interval_distinct` | count | tick: number of distinct values in I | Swing | 5 | 0 | [1, 10] |
| 15 | `click_interval_mode_share` | share | tick: occurrences of the most frequent value of I / n | Swing | 5 | 0 | [0, 1] |
| 16 | `click_pattern_share` | share | tick: max over p in {2, 3, 4} of `#{j >= p : I_j == I_(j-p)} / (n - p)` (period-p repetition, as autoclicker.pattern) | Swing | 8 | 0 | [0, 1] |
| 17 | `click_doubles_share` | share | tick: click ticks with `c_k >= 2` / click ticks | Swing | 1 click tick | 0 | [0, 1] |
| 18 | `rot_combat_count` | count | tick: combat pairs in W | MoveSample, Attack | - | 0 | [0, 2000] |
| 19 | `rot_combat_yaw_mean` | deg/tick | tick: mean of abs(dyaw) over combat pairs | MoveSample, Attack | 10 | -1 | [0, 180] |
| 20 | `rot_combat_yaw_sd` | deg/tick | tick: sd of abs(dyaw) over combat pairs | MoveSample, Attack | 10 | -1 | [0, 180] |
| 21 | `rot_combat_pitch_mean` | deg/tick | tick: mean of abs(dpitch) over combat pairs | MoveSample, Attack | 10 | -1 | [0, 180] |
| 22 | `rot_combat_pitch_sd` | deg/tick | tick: sd of abs(dpitch) over combat pairs | MoveSample, Attack | 10 | -1 | [0, 180] |
| 23 | `rot_combat_yaw_accel_mean` | deg/tick^2 | tick: mean of abs(dyaw[n] - dyaw[n-1]) over combat pairs n whose previous sample n-1 is also a combat pair | MoveSample, Attack | 10 | -1 | [0, 360] |
| 24 | `rot_idle_count` | count | tick: idle pairs in W | MoveSample, Attack | - | 0 | [0, 2000] |
| 25 | `rot_idle_yaw_mean` | deg/tick | tick: mean of abs(dyaw) over idle pairs | MoveSample | 10 | -1 | [0, 180] |
| 26 | `rot_idle_yaw_sd` | deg/tick | tick: sd of abs(dyaw) over idle pairs | MoveSample | 10 | -1 | [0, 180] |
| 27 | `rot_idle_pitch_mean` | deg/tick | tick: mean of abs(dpitch) over idle pairs | MoveSample | 10 | -1 | [0, 180] |
| 28 | `rot_idle_pitch_sd` | deg/tick | tick: sd of abs(dpitch) over idle pairs | MoveSample | 10 | -1 | [0, 180] |
| 29 | `gcd_calibrated` | bool | 1 if the player's aim.gcd state (CHECKS 2.6) is calibrated when W is finalised, else 0 | MoveSample | - | 0 | [0, 1] |
| 30 | `gcd_combat_ongrid_share` | share | tick: axis deltas (abs(dyaw) and abs(dpitch) separately, non-zero, < 30 deg, SCOPING dropped) of combat pairs that are on-grid by aim.gcd's rule with the calibration current at the sample / such deltas; deltas seen while uncalibrated are not counted | MoveSample, Attack | 10 deltas | -1 | [0, 1] |
| 31 | `gcd_idle_ongrid_share` | share | tick: as above for idle pairs, deltas < 10 deg and abs(pitch) < 89 (aim.gcd calibration filter) | MoveSample | 10 deltas | -1 | [0, 1] |
| 32 | `snap_count` | count | tick: attack ticks in W with a snap as defined by aim.snap (CHECKS 2.7) | MoveSample, Attack | - | 0 | [0, 600] |
| 33 | `snap_share` | share | tick: snap_count / attack ticks in W | MoveSample, Attack | 1 attack tick | 0 | [0, 1] |
| 34 | `aim_precision_mean` | deg | mean of the aim.precision angle (CHECKS 2.8) over the attacks in W eligible for aim.precision | Attack, MoveSample | 3 | -1 | [0, 90] |
| 35 | `aim_precision_sd` | deg | sd of the same angles | Attack, MoveSample | 3 | -1 | [0, 90] |
| 36 | `attack_count` | count | Attack events in W (any target) | Attack | - | 0 | [0, 2000] |
| 37 | `attack_eligible_count` | count | eligible attacks in W | Attack | - | 0 | [0, 2000] |
| 38 | `attack_target_distinct` | count | distinct `target_entity_id` of Attacks in W | Attack | - | 0 | [0, 100] |
| 39 | `reach_p50` | blocks | p50 of `r = dist - (entity_reach - 3.0)` over eligible attacks, with `dist` as in reach.distance (CHECKS 2.2) and `entity_reach` from the Attack (3.0 when 0) | Attack | 1 | -1 | [0, 8] |
| 40 | `reach_p95` | blocks | p95 of the same r | Attack | 1 | -1 | [0, 8] |
| 41 | `reach_max` | blocks | max of the same r | Attack | 1 | -1 | [0, 8] |
| 42 | `hitbox_miss_share` | share | eligible attacks that are misses per killaura.hitbox (CHECKS 2.3) / eligible attacks | Attack, MoveSample | 1 | 0 | [0, 1] |
| 43 | `noswing_share` | share | tick: decided attacks (`stab == false`) without a main-hand Swing in the same tick, as killaura.noswing (CHECKS 2.5) / decided attacks | Attack, Swing | 1 | 0 | [0, 1] |
| 44 | `multi_target_ticks` | count | tick: attack ticks in W with Attacks on >= 2 distinct `target_entity_id` (killaura.multi) | Attack | - | 0 | [0, 600] |
| 45 | `velocity_count` | count | decided VelocityApplied in W that are eligible for velocity.vertical (CHECKS 2.9: VELOCITY_PACKET, `vy > 0.15`, env/state/explosion exclusions) | VelocityApplied, MoveSample, EnvSample | - | 0 | [0, 200] |
| 46 | `velocity_v_taken_ratio` | ratio | median over those velocities of `dy / vy`, dy of the first consecutive move in the response window (velocities without such a move are left out) | VelocityApplied, MoveSample | 1 | -1 | [0, 2] |
| 47 | `velocity_vertical_ignored_share` | share | those velocities decided "ignored" by velocity.vertical / velocity_count | VelocityApplied, MoveSample | 1 | 0 | [0, 1] |
| 48 | `velocity_jump_reset_share` | share | among matched velocities with `on_ground_before == true`: those matched only by the jump-reset branch (`abs(dy - vy) >= 0.012` and `abs(dy - max(vy, jumpPower)) < 0.012`) / such matched velocities | VelocityApplied, MoveSample | 1 | -1 | [0, 1] |
| 49 | `velocity_h_count` | count | matched velocities in W eligible for velocity.horizontal (CHECKS 2.10: `abs(v_xz) >= 0.3`, no horizontal collision) | VelocityApplied, MoveSample | - | 0 | [0, 200] |
| 50 | `velocity_h_taken_ratio` | ratio | median over those of `h / abs(v_xz)`, h = horizontal displacement of the matching move | VelocityApplied, MoveSample | 1 | -1 | [0, 2] |
| 51 | `velocity_h_shortfall_share` | share | those with velocity.horizontal shortfall `s > 0.15` / velocity_h_count | VelocityApplied, MoveSample | 1 | 0 | [0, 1] |
| 52 | `flag_timer_balance` | count | LocalFlag events in W with `check_id` timer.balance | LocalFlag | - | 0 | [0, 100] |
| 53 | `flag_movement_speed` | count | LocalFlags movement.speed | LocalFlag | - | 0 | [0, 100] |
| 54 | `flag_movement_fly` | count | LocalFlags movement.fly | LocalFlag | - | 0 | [0, 100] |
| 55 | `flag_movement_step` | count | LocalFlags movement.step | LocalFlag | - | 0 | [0, 100] |
| 56 | `flag_movement_groundspoof` | count | LocalFlags movement.groundspoof | LocalFlag | - | 0 | [0, 100] |
| 57 | `flag_movement_noslow` | count | LocalFlags movement.noslow | LocalFlag | - | 0 | [0, 100] |
| 58 | `flag_dig_speed` | count | LocalFlags dig.speed (fast break) | LocalFlag | - | 0 | [0, 100] |
| 59 | `flag_protocol_pitch` | count | LocalFlags protocol.pitch | LocalFlag | - | 0 | [0, 100] |
| 60 | `flag_protocol_position` | count | LocalFlags protocol.position | LocalFlag | - | 0 | [0, 100] |
| 61 | `flag_protocol_slot` | count | LocalFlags protocol.slot with `variant != "redundant"` | LocalFlag | - | 0 | [0, 100] |
| 62 | `flag_protocol_selfhit` | count | LocalFlags protocol.selfhit | LocalFlag | - | 0 | [0, 100] |
| 63 | `flag_protocol_cursor` | count | LocalFlags protocol.cursor | LocalFlag | - | 0 | [0, 100] |
| 64 | `flag_protocol_flood` | count | LocalFlags protocol.flood | LocalFlag | - | 0 | [0, 100] |
| 65 | `flag_protocol_abilities` | count | LocalFlags protocol.abilities | LocalFlag | - | 0 | [0, 100] |
| 66 | `flag_inventory_closed` | count | LocalFlags inventory.closed | LocalFlag | - | 0 | [0, 100] |
| 67 | `flag_movement_severity_max` | share | max `severity` of LocalFlags in W whose `check_id` starts with `movement.` | LocalFlag | 1 | 0 | [0, 1] |
| 68 | `timer_offset_slope` | ms/s | tick: per session event with tick > 0, `o = (time_us - base_us)/1000 - 50 * (tick - base_tick)` (plugin clock, base = the session's first such event); split W into 15 bins of 2 s; per bin with >= 2 events and no overlap with a ServerStall, `m_b = min(o)`; ordinary least-squares slope of `m_b` against the bin centre in seconds | any with tick | 4 bins | 0 | [-200, 200] |
| 69 | `cs_movement_candidate_rate` | 1/1000 | `1000 * sum(candidates) / sum(evaluated)` over the latest CheckStats of movement.speed, movement.fly, movement.step, movement.groundspoof and movement.noslow | CheckStats | 1 stats event, sum(evaluated) > 0 | -1 | [0, 1000] |
| 70 | `cs_movement_discard_share` | share | `sum(discarded) / sum(candidates)` over the same CheckStats | CheckStats | sum(candidates) > 0 | -1 | [0, 1] |
| 71 | `cs_speed_max_stat` | blocks/tick | `max_stat` (speed excess) of the latest movement.speed CheckStats | CheckStats | 1 | -1 | [-0.5, 2] |
| 72 | `cs_fly_max_stat` | blocks/tick | `max_stat` (fly diff) of the latest movement.fly CheckStats | CheckStats | 1 | -1 | [-0.5, 5] |
| 73 | `place_count` | count | BlockPlace events in W | BlockPlace | - | 0 | [0, 1000] |
| 74 | `place_eligible_count` | count | eligible placements in W | BlockPlace | - | 0 | [0, 1000] |
| 75 | `place_face_invalid_share` | share | eligible placements failing scaffold.face (CHECKS 2.12) / eligible placements | BlockPlace | 1 | 0 | [0, 1] |
| 76 | `place_ray_miss_share` | share | eligible placements that are scaffold.ray misses / eligible placements | BlockPlace, MoveSample | 1 | 0 | [0, 1] |
| 77 | `place_cursor_mismatch_share` | share | scaffold.cursor mismatches / placements evaluated by scaffold.cursor (cursor in [0, 1], client >= 1.11, distance >= 0.3) | BlockPlace, MoveSample | 1 | 0 | [0, 1] |
| 78 | `place_reach_max` | blocks | max over eligible placements of (min distance from the eye candidates to the clicked block's cube) - (block_reach - 4.5), block_reach 4.5 when 0 (as scaffold.reach) | BlockPlace | 1 | -1 | [0, 10] |
| 79 | `place_peak_per_s` | 1/s | max over BlockPlaces i in W of the BlockPlaces with `t` in `(t_i - 1 s, t_i]` | BlockPlace | 1 | 0 | [0, 40] |
| 80 | `dig_count` | count | BlockDig START events in W | BlockDig | - | 0 | [0, 1000] |
| 81 | `dig_eligible_count` | count | BlockDig START and FINISH events in W evaluated by dig.ray (CHECKS 2.12b) | BlockDig, MoveSample | - | 0 | [0, 2000] |
| 82 | `dig_ray_miss_share` | share | dig.ray misses / dig_eligible_count | BlockDig, MoveSample | 1 | 0 | [0, 1] |
| 83 | `dig_peak_per_s` | 1/s | max over START events i in W of the START events with `t` in `(t_i - 1 s, t_i]` (nuker rate) | BlockDig | 1 | 0 | [0, 60] |
| 84 | `inv_click_count` | count | InventoryClick events in W | InventoryClick | - | 0 | [0, 2000] |
| 85 | `inv_click_interval_min_ms` | ms | min `t` difference between consecutive InventoryClicks in W, / 1000 | InventoryClick | 2 clicks | -1 | [0, 5000] |
| 86 | `inv_move_ticks` | count | tick: ticks in W counted by inventory.move (CHECKS 2.13) | InventoryClick, MoveSample | - | 0 | [0, 600] |
| 87 | `inv_rotate_ticks` | count | tick: ticks in W counted by inventory.rotate | InventoryClick, MoveSample | - | 0 | [0, 600] |
| 88 | `ping_median_ms` | ms | median `rtt_us / 1000` of LatencySample PONG with `unmatched == false` in W; if none, of KEEPALIVE samples in W | LatencySample | 1 | -1 | [0, 5000] |
| 89 | `ping_jitter_ms` | ms | sd of the sample set used for ping_median_ms | LatencySample | 3 | -1 | [0, 5000] |
| 90 | `ka_pong_delta_ms` | ms | median over keep-alive samples in W paired as in latency.pingspoof (CHECKS 2.14) of (keep-alive RTT - paired pong median) | LatencySample | 1 pair | 0 | [-1000, 5000] |
| 91 | `ka_paired_count` | count | keep-alive samples in W with a pong pairing | LatencySample | - | 0 | [0, 100] |
| 92 | `gap_count` | count | tick: consecutive session events a, b (tick > 0) with `t_b` in W, `t_b - t_a > 500 ms`, `tick_b - tick_a <= 2` (the client tick counter stood still: the connection was silent), not overlapping a ServerStall | any with tick | - | 0 | [0, 60] |
| 93 | `gap_burst_count` | count | tick: gaps as above where the last event c with `t_c <= t_b + 100 ms` has `tick_c - tick_b >= 10` (blink burst, latency.blink without its attack condition) | any with tick | - | 0 | [0, 60] |

`N = 94` features. Group summary: context 0-5, clicks 6-17, aim 18-35, combat 36-44, velocity 45-51,
movement (local flags, timer, CheckStats) 52-72, world interaction 73-83, inventory 84-87, latency 88-93.

## 5. Metadata columns (not model inputs)

| column | type | meaning |
|---|---|---|
| `tenant_id` | String | tenant |
| `player` | UUID | player UUID (canonical lowercase string in exports) |
| `server_id` | UUID | the session's server |
| `session_id` | UUID | `PlayerJoin.session` |
| `window_start_us` | Int64 | grid start, normalised Unix microseconds |
| `window_end_us` | Int64 | `window_start_us + 30_000_000` |
| `client_protocol` | Int32 | `PlayerJoin.protocol_version` (-1 unknown) |
| `tick_client` | UInt8 | 1 when `client_protocol >= 768` |
| `hot_loaded` | UInt8 | the session was hot-loaded |
| `feature_version` | UInt16 | 1 |

## 6. ClickHouse table

The worker inserts through the telemetry sink (batched, 1 s / 10 000 rows, drop on overflow,
ARCHITECTURE 5.7). `features` holds the vector in table order; its length must equal N for the row's
`feature_version`. The engine is a ReplacingMergeTree (a MergeTree ordered by tenant and time, as 5.7
requires) so a window re-emitted after a worker handover replaces the earlier row; readers use `FINAL`
or de-duplicate on the sorting key keeping the largest `worker_unix_us`. Erasure (ARCHITECTURE 5.13) runs
`ALTER TABLE feature_windows DELETE WHERE tenant_id = ? AND player = ?`.

```sql
CREATE TABLE IF NOT EXISTS feature_windows
(
    tenant_id        LowCardinality(String),
    player           UUID,
    server_id        UUID,
    session_id       UUID,
    window_start_us  Int64,
    window_end_us    Int64,
    time             DateTime('UTC') DEFAULT toDateTime(intDiv(window_start_us, 1000000), 'UTC'),
    client_protocol  Int32,
    tick_client      UInt8,
    hot_loaded       UInt8,
    feature_version  UInt16,
    features         Array(Float32),
    worker_unix_us   Int64,
    inserted_at      DateTime('UTC') DEFAULT now()
)
ENGINE = ReplacingMergeTree(worker_unix_us)
PARTITION BY toYYYYMM(time)
ORDER BY (tenant_id, time, player, session_id)
TTL time + INTERVAL 180 DAY DELETE;
```

## 7. Labels and the training set

**Label source.** `GET /api/v1/export/labels?since=<RFC 3339>` with `Authorization: Bearer pe_<id>_<secret>`
(scope `read`) returns NDJSON, one label per line, erased subjects already excluded (ARCHITECTURE 5.8).
The pipeline reads these fields and ignores the rest:

| field | required | meaning |
|---|---|---|
| `tenant_id` | yes | tenant |
| `player_uuid` (alias `player`) | yes | player UUID |
| `label` | yes | `cheat` or `legit`; other values are skipped |
| `window_start`, `window_end` | yes | the labelled verdict's signal window; integer Unix time (s, ms or us, by magnitude) or RFC 3339 |
| `source` | no | `confirmed`, `overturned`, `manual` |
| `verdict_id`, `created_at` | no | provenance |
| `category` (or `evidence.category`) | no | the verdict's dominant category (`HEURISTICS`, `CATEGORY_HEURISTICS` or the enum number); used for the per-category gate breakdown |

A line holding only `{"next_cursor": "..."}` means more data: the client repeats the request with
`cursor=<value>`.

**Join.** A window is labelled when it overlaps a label of the same `(tenant_id, player)`:
`window_start_us <= label_end_us AND label_start_us < window_end_us` (the label interval is closed,
the window half-open). It gets `y = 1` for `cheat` labels and `y = 0` for `legit` labels. A window that
overlaps both a `cheat` and a `legit` label is ambiguous and dropped. Windows that overlap no label,
including all windows of players with no label at all, are unlabelled and excluded from training and
evaluation. A cheat window's category is that of the most recently created overlapping cheat label.

**Exclusions** (applied to labels and windows before the join):

- erased subjects: the API already omits them; the pipeline also drops every `(tenant_id, player)` (or
  bare `player`, meaning every tenant) listed in an erasure file, because a feature export may predate an
  erasure. ClickHouse rows are deleted by the erasure job itself.
- untrusted tenants: only tenants on the provided allow-list (ARCHITECTURE 5.10/5.11: paid plan, or older
  than 30 days with >= 50 distinct players) contribute windows or labels. Training refuses to run without
  an allow-list.
- non-tick clients (`client_protocol < 768`) and rows with `feature_version != 1`.

**Split.** Train/test (and train/validation) splits are by player UUID (one group per UUID across all
tenants, since online-mode UUIDs identify the same person everywhere), stratified by whether the player
has any cheat window, so no player contributes windows to both sides.

**Bias warning.** Labels come from verdicts (confirmed = cheat, overturned = legit) plus manual labels, so
labelled windows over-represent players the heuristics already flagged. The gate's false-positive rate is
measured on this set, not on the whole population. Manual `legit` labels on randomly chosen players are
needed before the FPR means "rate among all players".

## 8. Model contract (Go scorer)

- ONNX input `features`: float32 tensor `[batch, N]`, columns in table order, values exactly as written
  to `feature_windows` (defaults and clipping applied).
- Outputs: `label` (int64 `[batch]`, ignore) and `probabilities` (float32 `[batch, 2]`); `P(cheat) =
  probabilities[:, 1]`.
- Metadata (`ModelProto.metadata_props`): `pandaeye.feature_version` ("1"), `pandaeye.feature_names`
  (JSON array of the N names in order), `pandaeye.n_features`, `pandaeye.model_name` (signal check id is
  `ml.<model_name>`), `pandaeye.threshold` (operating threshold on P(cheat), chosen on validation players
  to keep the FPR at or below the heuristic baseline's), `pandaeye.min_client_protocol` (768),
  `pandaeye.trained_on_synthetic` (`true` = never deploy), `pandaeye.created_at`.
- The scorer refuses to load a model whose `feature_version` or `feature_names` differ from the Go
  constants, that says `trained_on_synthetic=true`, or that has no matching gate report
  (`model.gate.json` next to `model.onnx`, README step 4). It scores only windows with `client_protocol
  >= min_client_protocol` and flags those with `P(cheat) >= threshold`. Flagged windows become
  `ml.<model_name>` signals only when `PE_ML_POINTS` > 0; in shadow (the default) they are only counted
  (ARCHITECTURE 5.10).

## 9. Versioning and changelog

Bump `FEATURE_VERSION` (here, in `features.py` and in `features.go`) for any change to the table.
Old rows keep their version; the pipeline trains on one version at a time and models declare theirs.
A new version may add ClickHouse rows with a longer `features` array; the table schema does not change.

- **v1**: initial vector (94 features), including movement.noslow, dig.speed and dig.ray coverage, the
  `on_ground_before` jump-reset share and CheckStats-derived movement features.
