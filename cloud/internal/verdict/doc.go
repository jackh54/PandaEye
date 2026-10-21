// Package verdict is the verdict service (ARCHITECTURE 5.6): scoring, leniency, the action policy,
// evidence, delivery, Discord alerts and the leased store writer.
//
// Partition consumers. For every leased pe.signals.<NN> (signals and session markers, in order) the
// service starts a consumer epoch, rebuilds its state from the last 2 h of the partition (rebuild
// mode: scores, sessions and the ALERT ladder, never actions) and then consumes the durable
// consumer. Messages are de-duplicated by stream sequence and by an LRU of applied msg ids. State
// per (tenant, player): per-check accumulators decayed on normalised event time
// (t = event_time_us + origin.clock_offset_us), the open sessions with the actions they already
// had (sessions not seen live load those from the store when an action is due), the ALERT ladder
// and the last WatchRequest. Erasure tombstones (KV PE_ERASURES) drop late data and existing state.
//
// Scoring: acc_c = acc_c * 2^(-dt/halflife_c) + points (dt = max(0, t - last_t)); S = sum over
// families of min(familyCap, sum over its checks of min(acc_c, cap_c)); C = 1 - prod(1 - conf_c)
// over checks with acc_c >= 1, conf_c being the highest signal confidence since the accumulator was
// last empty. Thresholds ALERT 15 / MITIGATE 30 / KICK 70 / BAN 120 times the leniency multiplier
// (L1 0.6, L2 0.8, L3 1.0, L4 1.3, L5 1.7; default 4). Signals of hot-loaded sessions count half
// until the session's seed marker and for 60 s after.
//
// Policy (once per session except ALERT): ALERT when S reaches ALERT and again each time S grows
// 50 % (at most once a minute); MITIGATE (S >= MITIGATE, session open; also re-issued on a rejoin
// with a still-high score), KICK (S >= KICK, C >= 0.8, session open, >= 2 families with >= 5),
// BAN (C >= 0.9, a committed clip among the contributing signals, and S >= BAN with >= 2 families
// with >= 5, or a certain check with acc >= BAN/2). Stale signals (received > 2 min ago, or a
// normalised event time > 10 min old), exempt/bedrock sessions, signals without a session and
// tenants over their free-tier limit for 3 days produce ALERT at most; a legit label or an
// overturned verdict covering the signal window holds back KICK and BAN. Every verdict the ladder
// earns is produced; it is shadow when the tenant's enforcement is off, the origin plugin is in
// shadow mode, or the tenant's autoban/autokick/mitigations flag forbids the action ("would have
// banned"). An online player at S >= ALERT/2 gets a WatchRequest{60 s} (once per 2 min).
//
// Delivery, in order: VerdictRecord to pe.verdicts.<tenant> (PubAck), SignedOutbound to
// pe.out.<tenant>.<server> (the open session's server; for BAN without one, a connected server of the
// same network, else the newest signal's server), then Discord for verdicts >=
// discord_min_action. Verdict ids are identity.VerdictID over tenant, player, session, action and
// the triggering signal's stream sequence, so re-processing reproduces the same verdicts. Probe
// signals are answered at once with Outbound{probe_result}; their cloud-side latency goes to
// probe_stats (source "cloud", gateway receive to verdict service).
//
// Store writer (lease "writer"): consumes PE_VERDICTS, upserts verdicts by id (payload, clips),
// applies AckRecords (the store creates the bans row of an executed non-shadow BAN with the
// executing server) and records reputation contributions (ContributeReputation, 5.11). It records
// each newly stored verdict in telemetry and skips records of erased players.
//
// For the api: ContributeReputation (call after confirming a verdict), ReputationEligible,
// TrustedTenant, Evidence (the evidence_json shape) and ThresholdsFor.
package verdict
