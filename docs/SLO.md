# PandaEye service level objectives and release gates

What PandaEye publishes about its own performance, how each number is measured, and the roadmap gates
as measurable tests. Behaviour the spec leaves open is marked **Open** and collected in section 5.

## 1. Published objectives

| SLI | Objective | Window | Source |
|---|---|---|---|
| Verdict pipeline latency, p95: plugin-measured end-to-end probe RTT, in-region | **< 100 ms** | rolling 1 h | `/status`, `GET /api/v1/stats/latency`, `pandaeye_probe_rtt_seconds{stage="plugin"}` |
| Verdict pipeline latency, p99, same measurement | **< 250 ms** | rolling 1 h | as above |
| Gateway availability | **>= 99.9%** of 1-minute intervals per calendar month (proposed; see 1.3) | calendar month | external prober (not computed by `/status` in v1) |

A missed objective does not stop protection: plugins keep their local checks running and buffer events
while the cloud is slow or unreachable (degraded mode), and buffered events are judged on reconnect.
Signals from events older than 10 minutes, or consumed more than 2 minutes after the gateway received
them, produce at most an ALERT (ARCHITECTURE 5.6), so a long outage never turns into delayed
punishments.

### 1.1 The probe RTT

- Every connected server sends one `Probe` event every 10 s. It travels the real verdict path: gateway,
  `pe.events.<NN>` (partition `probe_id % PE_PARTITIONS`, so every partition owner is measured), worker,
  `pe.signals.<NN>` (in the same queue as real signals), verdict service, `pe.out`, gateway, plugin
  ([PROTOCOL.md](PROTOCOL.md) section 9). It is the phase-1 "dummy check": its latency is the latency a
  real cloud verdict has, minus the check's own computation.
- The plugin measures the round trip and reports its latest value in every `Ping.last_probe_rtt_us`
  (every 5 s). The gateway records the reported values (`stage="plugin"`); `ProbeResult` timestamps
  give the per-hop breakdown (`gateway_worker`, `worker_verdict`, `verdict_gateway`, `cloud`).
- **In-region** means game servers connected to the regional deployment that serves their location
  (OPERATIONS 2.3), for example servers in the US Midwest connected to a US-Central deployment. The
  figure on `/status` covers every server connected to that deployment; v1 has no per-server filter, so a
  far-away server connected to the wrong region is included (**Open**).
- **Open:** the spec does not fix where the plugin starts the clock. Because the objective is about the
  delay a verdict really has, the RTT should run from the probe event's creation (`Event.time_us`, which
  includes up to one `batch-interval-ms` of batching, 50 ms by default) to the arrival of its
  `ProbeResult`.
- **Open:** a probe that never returns produces no new value, and `Ping` keeps repeating the last
  reported RTT, so a broken pipeline can look healthy. The objective's intent is that a probe without a
  result within 10 s counts as a sample above 250 ms. Until the protocol reports missing probes, operators
  alert on missing probe completions (OPERATIONS 9.2, "Probes missing").

### 1.2 How `/status` computes it

`/status` (public JSON, gateway and api) returns the region (`PE_REGION`), version, uptime, the number of
connected servers, probe RTT p50/p95/p99 over the last hour, the SLO target (p95 < 100 ms in-region) and
whether it is met. Its RTT figures come from the store table `probe_stats` (per tenant, server and minute:
count, p50, p95, p99, max; kept 30 days).

- **Open:** ARCHITECTURE 5.3 says the `/status` figures are plugin-measured, but 5.6 fills `probe_stats`
  from probe signals at the verdict service, which has only cloud-side timestamps (gateway receive to
  verdict). The plugin-measured value arrives at the gateway in `Ping`. One of the two must change so that
  `probe_stats` holds plugin-measured RTTs.
- **Open:** an hourly percentile cannot be computed exactly from per-minute percentiles. The spec does not
  say how `/status` merges the 60 minute buckets. Acceptable methods, in order of preference: store a
  fixed-bucket histogram per minute and take the percentile of the merged histogram; or report the
  maximum of the per-minute p95 values (conservative). A count-weighted mean of minute percentiles must
  not be published as "p95". `/status` should name its method.
- `met` evaluates the p95 objective only; the p99 objective is published here but not evaluated by
  `/status` (**Open**).
- Tenants see their own servers' percentiles for 1 h and 24 h in `GET /api/v1/stats/latency`.

### 1.3 Gateway availability (proposed)

The spec asks for a published latency SLO and a public status page but defines no availability target;
the target below is a proposal until it is adopted.

- **SLI**: the share of 1-minute intervals in which an external prober in the same region, using a
  dedicated probe tenant and server, completes all of: TLS handshake and WebSocket upgrade on
  `/v1/connect`, `Hello` answered by `Welcome` within 2 s, and `GET /healthz` answered 200.
- **Objective**: >= 99.9% per calendar month (about 43 minutes of failed intervals).
- **Counting**: planned maintenance is not excluded (rolling deploys across >= 2 gateways must not fail
  an interval). Intervals in which the prober itself could not reach the region's network are excluded.
- `/status` does not compute availability in v1; publish the prober's result next to it.

### 1.4 Error budget

The latency objectives allow 5% (p95) and 1% (p99) of probe samples per hour above their thresholds; the
availability objective allows 0.1% failed minutes per month. Alerting follows the budget burn
(OPERATIONS 9.2: probe p95 above 100 ms for 15 minutes, p99 above 250 ms for 15 minutes). When a region
misses the p95 objective in more than 5% of the hours of a 30-day period, latency work takes priority over
feature releases for that region (proposed policy).

## 2. Roadmap gates as tests

Each gate is a test with a fixed setup, a pass criterion and the evidence to keep.

### Gate 1: pipeline latency

| | |
|---|---|
| Setup | the cloud release under test deployed in a US-Central region (state the topology: `pandaeye all` or split); a load generator on a separate host in a Chicago-like location, i.e. with the network RTT a customer game server in that region has to the gateway (record the WebSocket Ping/Pong RTT); 200 simulated players across 10 servers (20 each) with the production event mix (ARCHITECTURE 8 "Load") |
| Duration | 10 minutes warm-up, then at least 1 hour of steady state |
| Pass | over the steady-state hour, computed from every probe sample of the run (not from minute buckets): plugin-measured probe RTT **p95 < 100 ms** and **p99 < 250 ms**; every probe answered; no dropped events (plugin event sink and pending buffer drops 0, `pandaeye_dropped_total` unchanged); no poison batches |
| Also recorded | per-hop probe latency (`stage` histograms); the plugin packet-handler cost, which must stay under 2% of one core at 200 players (ARCHITECTURE 1.1 and 8) |
| Evidence | report with cloud and plugin versions, topology, load location and its RTT, the raw probe samples, the event mix used |

**Open:** "the production event mix" is not specified numerically; the load generator in `test/e2e`
defines it and the report must record it.

### Gate 2: local false positives

Measured on real players on a production server running the plugin in shadow mode
([OPERATIONS 8.7](OPERATIONS.md#87-plugin-rollout-on-a-production-server-paper--folia)).

| | |
|---|---|
| Period | at least **7 consecutive days** AND at least **1000 player-hours** |
| Player-hours | the exposure of the local checks: the sum over Java players of the 5-minute `CheckStats` / peaks-log windows in which the player had a seeded session and at least one local check evaluated something, times 5 minutes. Local movement checks run only for tick clients (1.21.2+, CHECKS.md section 0); report the tick-client share |
| Confirmed flag | a violation that raised the check's VL (after the confirmation step where the check has one): `confirmed` in `CheckStats` and the peaks log, one line in the flags log |
| Criterion A | for **every** local check: confirmed flags <= player-hours / 50 (at most 1 confirmed flag per 50 player-hours; 20 per 1000 player-hours) |
| Criterion B | no staff-verified legitimate player reached the check's setback VL during the period. Setback VLs exist for `timer.balance` (2), `movement.speed` (5) and `movement.fly` (3). **Open:** the other local checks define no setback VL; until they do, every confirmed flag of a staff-verified legitimate player on them must be investigated and explained in the report |
| Staff-verified legitimate players | players staff vouch for (trusted regulars, staff accounts, players watched while flagged), recorded as dashboard `legit` labels or a list attached to the report |
| Pass | A and B hold for every enabled local check. A check that fails can be disabled locally (`checks.<id>.enabled: false`) and the remaining checks promoted |
| Evidence | per-check table (player-hours, confirmed flags, rate, highest peak of a verified legitimate player vs the threshold), the verified-player list, the plugin version and configuration |

Gate 2 is the precondition for moving a server from shadow to live mode (OPERATIONS 8.7 step 6).

### Gate 3: detection matrix

The targets and the harness scenarios are defined in
[DETECTION_MATRIX.md](DETECTION_MATRIX.md#5-gate-3-targets). Summary: in the e2e suite (real Paper and Folia
1.21.11 servers with PacketEvents 2.13.x, ViaVersion/ViaBackwards and PlugManX, against `pandaeye all`),
every "covered" scenario produces its expected signals within its duration, every "partial" scenario does
so under its stated condition, and every legit scenario produces no signal with points > 0, no confirmed
local flag and no verdict.

### Gate 4

Not defined in the specification available to this document (**Open**).

### Gate 5: ML model

| | |
|---|---|
| Test | `python -m pandaeye_ml evaluate --model-dir <model>` ([`ml/pandaeye_ml/evaluate.py`](../ml/pandaeye_ml/evaluate.py)); exit code 0 = pass, 1 = fail |
| Data | windows and labels from trusted tenants only, erased subjects excluded, tick-client windows of the model's feature version, held-out players never seen in training (split by player UUID; [`ml/FEATURES.md`](../ml/FEATURES.md) section 7); never synthetic data |
| Pass (as implemented) | on the held-out windows the model's FPR <= the heuristic baseline's FPR AND the model's TPR > the baseline's TPR (+ `--min-tpr-gain`, default 0), with at least 50 cheat and 50 legit windows and 5 players per class |
| Release | exit 0 and `deployable: true` in the report (non-synthetic data, and the deploy threshold in the model metadata also keeps FPR <= the baseline's on the held-out players); running it once more with `--threshold-mode fixed` tests that the shipped threshold transfers |
| Deployment | ship the model with `PE_ML_POINTS=0` first (signals with 0 points, shadow scoring); raise points only after a further gate-2-style observation of the `ml.<model>` signals |

The labelled set over-represents players the heuristics already flagged (FEATURES.md section 7), so the
gate's FPR is the FPR on that set, not on the whole player population.

## 3. Measurement summary

| Number | Measured where | Window | Notes |
|---|---|---|---|
| Probe RTT p50/p95/p99 | plugin (end to end), reported in `Ping`; stored in `probe_stats`; histogram `stage="plugin"` | 1 h (`/status`), 1 h and 24 h (API) | see the open points in 1.1 and 1.2 |
| Per-hop probe latency | `ProbeResult` timestamps; histograms per `stage` | any | for diagnosis, not published |
| Availability | external prober | month | proposed |
| Local FP rate | `CheckStats` / peaks logs | >= 7 days, >= 1000 player-hours | gate 2 |
| Detection | e2e harness | per scenario | gate 3 |
| ML | `evaluate.py` | held-out set | gate 5 |

## 4. Reporting

`/status` is public and unauthenticated. The monthly figures (hourly p95/p99 attainment per region and
availability) should be published with the method used to merge buckets (1.2) and the excluded intervals
(1.3).

## 5. Open points

1. `/status` is specified as plugin-measured, but `probe_stats` is written from cloud-side probe signals.
2. Merging per-minute percentiles into an hourly percentile is not specified.
3. The probe RTT start point and the handling of probes that never return are not specified; `Ping`
   repeats a stale value when probes stop.
4. No per-server in-region filter: `/status` includes every server connected to the deployment.
5. `/status` evaluates only the p95 objective.
6. No availability objective or prober in the spec; the 99.9% target is a proposal.
7. "Production event mix" of gate 1 is not specified numerically.
8. Setback VLs are defined only for `timer.balance`, `movement.speed` and `movement.fly` (gate 2
   criterion B).
9. Gate 4 is not defined.
