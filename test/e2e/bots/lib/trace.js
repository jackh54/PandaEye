'use strict'
/** Helpers to analyse probe traces (decoded packets of one player, server receive time t in ms). */

const MOVE = new Set(['PLAYER_FLYING', 'PLAYER_POSITION', 'PLAYER_POSITION_AND_ROTATION', 'PLAYER_ROTATION'])

const inbound = (es) => es.filter((e) => e.d === 'in')
const outbound = (es) => es.filter((e) => e.d === 'out')
const num = (v) => (typeof v === 'string' ? Number(v) : v)

/** Packets grouped per client tick (closed by CLIENT_TICK_END). */
function ticks (es) {
  const out = []
  let cur = []
  for (const e of inbound(es)) {
    if (e.p === 'CLIENT_TICK_END') { out.push({ t: e.t, packets: cur }); cur = [] } else cur.push(e)
  }
  return out
}

/** Movement packets with carried-forward position/rotation and dy/h relative to the previous position packet. */
function moves (es) {
  const out = []
  let x, y, z, yaw, pitch
  for (const e of inbound(es)) {
    if (!MOVE.has(e.p)) continue
    const hasPos = e.x !== undefined
    const px = x; const py = y; const pz = z
    if (hasPos) { x = num(e.x); y = num(e.y); z = num(e.z) }
    const pyaw = yaw; const ppitch = pitch
    if (e.yaw !== undefined) { yaw = num(e.yaw); pitch = num(e.pitch) }
    out.push({
      t: e.t, p: e.p, hasPos, x, y, z, yaw, pitch, g: e.g, hc: !!e.hc,
      dy: hasPos && py !== undefined ? y - py : undefined,
      h: hasPos && px !== undefined ? Math.hypot(x - px, z - pz) : undefined,
      dyaw: e.yaw !== undefined && pyaw !== undefined ? num(e.yaw) - pyaw : 0,
      dpitch: e.pitch !== undefined && ppitch !== undefined ? num(e.pitch) - ppitch : 0
    })
  }
  return out
}

/** CLIENT_TICK_END per second between t0 and t1 (ms, trace clock). */
function tickRate (es, t0 = 0, t1 = Infinity) {
  const ts = inbound(es).filter((e) => e.p === 'CLIENT_TICK_END' && e.t >= t0 && e.t <= t1).map((e) => e.t)
  if (ts.length < 2) return 0
  return (ts.length - 1) / ((ts[ts.length - 1] - ts[0]) / 1000)
}

const count = (es, p, d = 'in') => es.filter((e) => e.d === d && e.p === p).length
const median = (a) => { const s = [...a].sort((x, y) => x - y); return s.length ? s[Math.floor(s.length / 2)] : NaN }
const fmt = (v, n = 3) => (typeof v === 'number' && Number.isFinite(v) ? v.toFixed(n) : String(v))

module.exports = { MOVE, inbound, outbound, ticks, moves, tickRate, count, median, fmt, num }
