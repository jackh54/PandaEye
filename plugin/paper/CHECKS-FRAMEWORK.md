# Local check framework (Paper plugin)

How the local checks of `docs/CHECKS.md` are wired into the Paper/Folia plugin, and how to add one. The
three exemplar checks (`timer.balance`, `movement.speed`, `protocol.pitch` in `check/impl/`) use every part
described here.

## Threads and data flow

```
netty thread (per player)                              entity thread (Folia region of the player)
-------------------------                              ------------------------------------------
PandaEyePacketListener (MONITOR, wrapper hygiene)
  -> PacketProcessor updates PacketState
  -> PlayerChecks.dispatch(CheckInput)
       -> Check.handle(input, CheckContext)
            context.flag(v)       -> FlagPipeline (VL, LocalFlag, log, alert, setback)
            context.candidate(v)  -> ConfirmationScheduler --1 tick--> common guards + Confirmer
                                                                       -> confirmed: FlagPipeline
                                                                       -> discarded: counted only
```

- A `Check` instance exists once per player session and is only ever called on that player's netty thread.
  Instance fields need no synchronisation. Never touch Bukkit, the world or another player from `handle`.
- World access happens only in a `Confirmer`, which runs one tick later on the player's entity thread.
- Checks never change gameplay. Setbacks are applied by `SetbackService` through the `Enforcer` (shadow
  mode: logged only). `EnforcerArchitectureTest` fails the build when anything else calls a side-effecting
  Bukkit or PacketEvents method.
- Nothing is dispatched before the session is seeded (hot-load snapshot applied) or while the player is
  exempt from detection (bedrock, bypass permission outside shadow mode).

## Adding a check

1. **Catalog row.** Add the id constant and a `d(...)` row to `check/CheckCatalog.java`: category, points,
   confidence, half-life, cap and `certain` exactly as in CHECKS.md section 4, the local VL decay
   (seconds per 1 VL) and the live setback VL from CHECKS.md section 1 (0 = never). `CheckCatalogTest`
   parses `docs/CHECKS.md` and fails when the row and the document disagree.
2. **Implementation.** Create `check/impl/<Name>Check.java` extending `Check`, with a constructor taking the
   `CheckDescriptor`. Switch over the `CheckInput` record types you need and ignore the rest.
3. **Registration.** Add `.register(CheckCatalog.<ID>, <Name>Check::new)` to `CheckRegistry.defaults()`.
4. **Config.** `config.yml` has `checks.<id>: { enabled: true }`. Any other numeric key in that section is a
   threshold override, read with `context.settings().threshold("key", DEFAULT)`. Keep the default in code;
   the shipped config only lists `enabled`.
5. **Tests.** Add `src/test/java/.../check/impl/<Name>CheckTest.java` using `CheckHarness` (below). Cover the
   legit cases from the check's CHECKS.md section (lag, jitter, teleports, velocity, legacy clients) as
   well as the cheat it must catch.

## Inputs (`CheckInput`)

| Record | Packet |
|---|---|
| `MoveInput(Move)` | every client flying packet (position, rotation, status-only) |
| `TickEndInput(PacketState.TickEnd)` | C CLIENT_TICK_END, with the counters of the tick that ended |
| `Interact(targetEntityId, attack)` | C INTERACT_ENTITY / C ATTACK |
| `Place(...)` | C PLAYER_BLOCK_PLACEMENT |
| `Dig(action, x, y, z, start)` | C PLAYER_DIGGING (`start` is the matching START for FINISH/CANCEL) |
| `HeldItem(slot, change)` | C HELD_ITEM_CHANGE, classified against the server's slot mirror |
| `WindowClick(windowId, slot, button, mode)` | C CLICK_WINDOW |
| `ClientAbilities(flying)` | C PLAYER_ABILITIES |
| `Reset(reason)` | join, dimension change, respawn, tick-rate change, hot-load seed: drop your history |

Inputs arrive after `PacketState` has been updated for the packet, so `state().moves().get(0)` is the packet
being handled.

## `CheckContext`

| Method | Use |
|---|---|
| `state()` | the player's `PacketState` (read-only accessors, below) |
| `settings()` | `CheckSettings` of this check (`enabled`, `threshold(key, fallback)`); re-read on every input |
| `nowNanos()` | arrival time of the packet (`System.nanoTime()` scale); use it, not the wall clock |
| `vl()` | current local VL of this check (linear decay per the catalog) |
| `evaluated(statistic)` | call once per evaluation with the test statistic; feeds the `CheckStats` tuning data |
| `flag(violation)` | a violation that needs no world confirmation (protocol, timer): counts immediately |
| `candidate(violation, positions, movement, confirmer)` | a violation that must pass the confirmation step |

A `Violation` is `severity` (0..1, how far beyond the limit; the cloud scales points with it),
`vlIncrement`, a short human `info` string, numeric `data` (becomes `LocalFlag.data`) and an optional
`variant` (`Violation.of(...)` for none, `withVariant("redundant")`).

## `PacketState` accessors

All values are the client's view, built from packets only. Server-controlled values are `Tracked<T>`:
`applied()` (what the client has provably applied), `latest()`, `isPending()`, `known()`, and the lenient
helpers `anyMatch`, `max`, `min`. A change counts as applied one movement packet after the client
acknowledged it (keep-alive or ping after the change), or after 5 s. Use the most lenient value while
anything is pending.

| Accessor | Content |
|---|---|
| `movementExemption(now)` | CHECKS 1.1 common exemption for movement checks, `null` when they may run. Movement checks must return when it is non-null |
| `moves()` | `MoveHistory`, newest first (`get(0)` = this packet, up to 40); `consecutivePositions(depth)` |
| `positions()` | `PositionHistory` of recent positions, readable lock-free from other threads (attack target history) |
| `x() y() z() yaw() pitch() onGround()` | carried-forward current values; `positionKnown()`, `rotationKnown()` |
| `tick()` | CLIENT_TICK_END count; `client().tickClient()` is false for clients without tick end (no local movement checks) |
| `attributes()` | `AttributeTracker`: `get(key)` as `Tracked<Double>`, `lenientMax(key)` (NaN when unknown); keys `MOVEMENT_SPEED`, `JUMP_STRENGTH`, `GRAVITY`, `STEP_HEIGHT`, `SCALE`, `ENTITY_REACH`, `BLOCK_REACH` |
| `effects()` | `EffectTracker`: `present(key)`, `maxAmplifier(key)`, `minAmplifier(key)` (pending-aware) |
| `gameMode() abilities() inVehicle()` | `Tracked` game mode, abilities (flying, may fly, fly speed), vehicle |
| `flyingLenient() allowFlightLenient() creativeOrSpectatorLenient() glidingLenient() riptideLenient() inVehicleLenient(now)` | lenient combinations of the above |
| `teleports()` | `TeleportTracker`: `pending()`, `unconfirmed()`; the confirming PosRot and the next two position packets are flagged `Move.teleport` / skipped |
| `velocities()` | `VelocityTracker`: `pending()`, `lastAddedNanos()` (velocity, explosion, knockback) |
| `containers()` | `ContainerTracker`: `openId()`, `openType()`, `closedAndApplied(id, ackClock())` |
| `slots()` | `SlotTracker`: carried-item mirror and expected echoes |
| `acks()`, `ackClock()`, `appliedByClient(sentNanos)` | passive acknowledgement of server packets |
| `rtt()` | `RttTracker`: `pingMillis(now)`, `rttHighMillis(now)` |
| `sprinting() sneaking()` | entity-action state |
| `handActiveApplied() useItemTick() releasedSinceUseItem()` | item use (no-slow style checks) |
| `digStart()` | the open START_DIGGING, or null |
| `serverPitchOutOfRange()` | the server itself set a pitch outside [-90, 90] |
| `tickRate() frozen() tickRateNormal() loading() dead()` | server tick state, loading screen, death |
| `signals()` | `ExternalSignals` from other threads: `envRecent(EnvFlag mask, window, now)` (environment sampler: ice, slime, liquids, climbables, low ceiling, soul speed blocks...), `stallOverlaps(from, to)`, `worldUid()`, `detectionExempt()` |

## Confirmation step (CHECKS 1.1)

`candidate(...)` hands the violation to `ConfirmationScheduler`, which runs it one tick later on the
player's entity thread. Before the check's `Confirmer` runs, the common guards discard the candidate when
the world changed, a position's chunk is not loaded or not owned by the player's region, and, for
`movement = true`, when the player is riptiding, gliding, flying, in a vehicle, in creative/spectator, or
received Bukkit velocity in the last 2 s.

The `Confirmer` gets a `ConfirmContext`: `world()` (`WorldView`: `maxFriction`, `minFriction`,
`soulSpeedBlockUnder`, `hasCollisions(Box)`, `exemptNear(Box)`, `collidableEntityNear(Box, r)`),
`positions()` (the positions passed with the candidate, oldest first) and `scale()` (authoritative scale
attribute). It returns `Confirmation.CONFIRMED`, `Confirmation.confirmed(updatedViolation)` (e.g. severity
recomputed with the real friction) or `Confirmation.discarded(reason)`. Confirmers must be fast, must not
keep a reference to the context, and must capture everything they need from the netty side in an immutable
value (see `SpeedCheck.Sample`). `Confirmer.NONE` confirms on the common guards alone.

Limits: at most 4 outstanding confirmations and 2 per second per player, 200 per second overall. A candidate
over the limit is dropped (`candidate` returns false; counted in `/pandaeye status`). Design checks so a
dropped candidate is harmless: buffer violations and raise one candidate per burst.

## Flags, VL and setbacks

Each flag (immediate or confirmed) adds `vlIncrement` to the check's local VL, which decays linearly by 1 per
`vlDecaySeconds`. The `FlagPipeline` then sends a `LocalFlag` (with the move history as context) to the
cloud, writes `logs/flags-YYYY-MM-DD.jsonl` (aggregated per player and check per 5 s), raises a throttled alert (one line per player and check per 5 s, 20 lines/s
overall) and asks `SetbackService` whether the VL reached the catalog's setback VL (live mode only, and only
for timer 2, speed 5, fly 3, step 3, groundspoof 3, noslow 4; every other check never sets back).

`evaluated` / candidate / confirmed / discarded counts are aggregated per check into the 5-minute `CheckStats`
telemetry and `logs/peaks-YYYY-MM-DD.jsonl`, which is how thresholds are tuned in shadow mode.

## Testing with `CheckHarness`

`src/test/java/dev/pandascript/pandaeye/paper/check/CheckHarness.java` runs the real `PacketProcessor`,
`PacketState`, `PlayerChecks` and `CheckHandle` without a server.

```java
CheckHarness h = CheckHarness.of(CheckCatalog.MOVEMENT_SPEED, SpeedCheck::new)
        .join()        // JOIN_GAME survival, abilities, tick rate 20, keep-alive ack, one tick end
        .stand(40);    // warm-up: 40 movement packets
for (int i = 0; i < 20; i++) {
    h.step(0.6, 0, 0, true);   // one tick: position packet + CLIENT_TICK_END, 50 ms
}
h.confirmAll();                // runs pending Confirmers against h.world() (FakeWorld)
assertFalse(h.flags().isEmpty());
```

- Packets: `move`, `step`, `look`, `tickEnd`, `stand(n)`, `advance(nanos)`, `acknowledge()`, and
  `processor()` for any other packet (`serverTeleport`, `teleportConfirm`, `serverRotation`, velocity,
  attributes, effects...).
- Clients: `CheckHarness.of(client, id, factory)` with `TICK_CLIENT` (1.21.11) or `LEGACY_CLIENT` (1.8.9).
- Settings: `settings(id, new CheckSettings(true, Map.of("buffer", 5.0)))`.
- World and signals: `world()` (`FakeWorld`: friction, soul speed, collision and exempt-near predicates),
  `signals()` (`FakeSignals`: `env(flags, nanos)`, `exempt(true)`, `stall(from, to)`, `noWorld()`).
- Results: `flags()` (check, violation, VL, worldConfirmed, move context), `candidates()`, `discards()`.

Run the check tests with `gradle -p plugin :paper:test --tests '*check*'`.
