'use strict'
/**
 * Shadow-invariant comparison (ARCHITECTURE 1.1, 8): identical bot scripts with and without PandaEye must produce
 * the same per-type OUTBOUND wire packet counts, and PandaEye must not cancel/modify anything.
 *
 * Inputs are probe dumps per scenario: A = baseline, A2 = baseline repeat (quantifies scenario noise), B = with
 * PandaEye. For every group/type: noise = |A - A2|, tolerance = see tolerance(); a violation is B - A > tol
 * (additions) or, for count groups, |B - A| > tol.
 */

// Outbound types PandaEye could plausibly add in violation of shadow mode (transactions, keep-alives, channel
// registration, setbacks, velocity, chat alerts, kicks): only scenario noise is tolerated.
const STRICT_OUT = new Set([
  'play:PING', 'play:KEEP_ALIVE', 'play:PLUGIN_MESSAGE', 'configuration:PLUGIN_MESSAGE', 'play:PLAYER_POSITION_AND_LOOK',
  'play:PLAYER_ROTATION', 'play:FACE_PLAYER', 'play:ENTITY_VELOCITY', 'play:SYSTEM_CHAT_MESSAGE', 'play:CHAT_MESSAGE',
  'play:DISCONNECT', 'configuration:DISCONNECT', 'play:WINDOW_CONFIRMATION', 'play:SET_SLOT', 'play:CLOSE_WINDOW',
  'play:HELD_ITEM_CHANGE', 'play:CHANGE_GAME_STATE', 'play:PLAYER_ABILITIES', 'play:BLOCK_CHANGE'
])
const PERIODIC = new Set(['play:PING', 'play:KEEP_ALIVE']) // timing-dependent: +-1 per scenario is timing

// Groups compared and how
const COUNT_GROUPS = ['wire.out', 'wire.in']
const ANOMALY_GROUPS = [
  'inflight.out.modified', 'inflight.out.dropped', 'inflight.out.injected',
  'inflight.in.modified', 'inflight.in.dropped', 'inflight.in.injected',
  'pe.out.cancel_changed', 'pe.out.cancelled_at_monitor', 'pe.out.wrapper_changed', 'pe.out.wrapper_at_monitor',
  'pe.out.will_reencode', 'pe.out.reencode_flag_changed', 'pe.out.buffer_index_changed', 'pe.out.buffer_bytes_changed',
  'pe.in.cancel_changed', 'pe.in.cancelled_at_monitor', 'pe.in.wrapper_changed', 'pe.in.wrapper_at_monitor',
  'pe.in.will_reencode', 'pe.in.reencode_flag_changed', 'pe.in.buffer_index_changed', 'pe.in.buffer_bytes_changed'
]

function tolerance (group, type, a, noise) {
  if (group === 'wire.out' && STRICT_OUT.has(type)) return PERIODIC.has(type) ? Math.max(1, noise) : noise
  if (ANOMALY_GROUPS.includes(group)) return Math.max(noise, 0)
  // ordinary traffic (chunks, entity movement, waypoints, sounds ...): driven by bot physics and timing; one A/A
  // sample underestimates it (mineflayer PvP: WAYPOINT 350 vs 296 with A/A noise 24), so 3x noise or 20 %.
  // PandaEye can only change these by cancelling/injecting, which the byte-exact anomaly groups catch.
  return Math.max(2, Math.ceil(3 * noise), Math.ceil(0.2 * a))
}

const g = (d, group) => (d && d.counters && d.counters[group]) || {}

/** Noise table from two baseline dumps of the same scenario. */
function noise (a, a2) {
  const out = {}
  for (const group of [...COUNT_GROUPS, ...ANOMALY_GROUPS]) {
    const ga = g(a, group); const gb = g(a2, group)
    for (const t of new Set([...Object.keys(ga), ...Object.keys(gb)])) {
      const n = Math.abs((ga[t] || 0) - (gb[t] || 0))
      if (n) (out[group] = out[group] || {})[t] = n
    }
  }
  return out
}

/**
 * Compare a run B against baseline A with a noise table. Returns { ok, violations[], deltas{} }.
 * wire.in is reported but not asserted (inbound traffic comes from the same scripts; it is a sanity check).
 */
function compare (a, b, noiseTable = {}) {
  const violations = []
  const deltas = {}
  for (const group of [...COUNT_GROUPS, ...ANOMALY_GROUPS]) {
    const ga = g(a, group); const gb = g(b, group)
    for (const t of new Set([...Object.keys(ga), ...Object.keys(gb)])) {
      const va = ga[t] || 0; const vb = gb[t] || 0
      if (va === vb) continue
      const nz = (noiseTable[group] && noiseTable[group][t]) || 0
      const tol = tolerance(group, t, va, nz)
      const d = vb - va
      ;(deltas[group] = deltas[group] || {})[t] = { a: va, b: vb, d, tol, noise: nz }
      const isAnomaly = ANOMALY_GROUPS.includes(group)
      const strict = group === 'wire.out' && STRICT_OUT.has(t)
      const bad = group === 'wire.in' ? false : (isAnomaly || strict) ? d > tol : Math.abs(d) > tol
      if (bad) violations.push({ group, type: t, a: va, b: vb, d, tol, noise: nz })
    }
  }
  return { ok: violations.length === 0, violations, deltas }
}

/** Legit-player kicks in a dump (players listed). */
function kicksOf (dump, players) {
  const names = new Set(players.map((p) => p.toLowerCase()))
  return ((dump && dump.kicks) || []).filter((k) => names.has(String(k.name).toLowerCase()))
}

module.exports = { compare, noise, tolerance, kicksOf, STRICT_OUT, COUNT_GROUPS, ANOMALY_GROUPS }
