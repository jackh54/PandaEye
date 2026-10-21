'use strict'
/** Shared helpers for cheat scenarios (not a scenario file: names starting with "_" are skipped). */
const { lookAt, lookVec, angleDiff, EYE } = require('../../lib/util')
const T = require('../../lib/trace')

const EAST = -90
const NORTH = 180

/** Raw bot at a lane start facing east, settled. */
async function laneBot (ctx, name, laneIdx, opts = {}) {
  const b = await ctx.raw(name, opts)
  const l = ctx.lane(laneIdx)
  await ctx.tp(b, l.x, l.y + (opts.dy || 0), l.z, opts.yaw ?? EAST, 0)
  b.input = { forward: 0, strafe: 0, jump: false, sprint: false, yaw: opts.yaw ?? EAST, pitch: 0 }
  await ctx.ticks(b, 10)
  return { b, lane: l }
}

/**
 * Combat setup: target dummies (legit idle raw clients, resistance V, ignoring knockback so they stay in place)
 * at the combat centre, attacker `dist` blocks south of the first target, aimed at it.
 */
async function combat (ctx, attackerName, targetNames, { dist = 2.5, attackerOpts = {} } = {}) {
  const c = ctx.fx.combat.center
  const targets = []
  for (let i = 0; i < targetNames.length; i++) {
    const t = await ctx.raw(targetNames[i])
    await ctx.probe(`effect ${targetNames[i]} resistance 300 4`)
    const x = c[0] + (targetNames.length > 1 ? (i - (targetNames.length - 1) / 2) * 2 : 0)
    await ctx.tp(t, x, c[1], c[2], 0, 0)
    t.ignoreVelocity = true
    targets.push(t)
  }
  const a = await ctx.raw(attackerName, attackerOpts)
  await ctx.probe(`effect ${attackerName} resistance 300 4`)
  const ax = targetNames.length > 1 ? c[0] : c[0]
  await ctx.tp(a, ax, c[1], c[2] + dist, NORTH, 0)
  aim(a, targets[0])
  await ctx.ticks(a, 10)
  ctx.facts.targets = targets.map((t) => ({ name: t.username, eid: t.entityId, pos: t.pos }))
  ctx.facts.attackerEid = a.entityId
  return { a, targets, t: targets[0] }
}

/** Point the bot's input rotation at the target's body centre (y + 0.9). */
function aim (a, t, { dy = 0.9 } = {}) {
  const me = a.pos; const tp = t.pos || t
  const r = lookAt(me.x, me.y + EYE, me.z, tp.x, tp.y + dy, tp.z)
  a.input.yaw = r.yaw
  a.input.pitch = r.pitch
  return r
}

/** Run fn(n, i) on each of the next `ticks` ticks of bot b. */
function everyTick (ctx, b, ticks, fn) {
  return new Promise((resolve) => {
    let i = 0
    const f = (n) => {
      try { fn(n, i) } catch (e) { ctx.log('tick fn error ' + e.message) }
      if (++i >= ticks) { b.removeListener('tick', f); resolve() }
    }
    b.on('tick', f)
  })
}

/** Angle in degrees between look (yaw,pitch) from eye and the direction to point p. */
function angleToPoint (eye, yaw, pitch, p) {
  const l = lookVec(yaw, pitch)
  const d = { x: p.x - eye.x, y: p.y - eye.y, z: p.z - eye.z }
  const n = Math.hypot(d.x, d.y, d.z) || 1
  return Math.acos(Math.max(-1, Math.min(1, (l.x * d.x + l.y * d.y + l.z * d.z) / n))) * 180 / Math.PI
}

/** Distance from point to an axis-aligned box. */
function distToBox (p, min, max) {
  const dx = Math.max(min.x - p.x, 0, p.x - max.x)
  const dy = Math.max(min.y - p.y, 0, p.y - max.y)
  const dz = Math.max(min.z - p.z, 0, p.z - max.z)
  return Math.hypot(dx, dy, dz)
}

/**
 * Walk the inbound trace and annotate every entry with the player's state at that moment
 * (last position, rotation, tick index): used to judge attacks/placements against geometry.
 */
function withState (es) {
  let x, y, z, yaw = 0, pitch = 0, tick = 0
  const out = []
  for (const e of T.inbound(es)) {
    if (T.MOVE.has(e.p)) {
      if (e.x !== undefined) { x = T.num(e.x); y = T.num(e.y); z = T.num(e.z) }
      if (e.yaw !== undefined) { yaw = T.num(e.yaw); pitch = T.num(e.pitch) }
    }
    if (e.p === 'CLIENT_TICK_END') tick++
    out.push({ ...e, sx: x, sy: y, sz: z, syaw: yaw, spitch: pitch, tick })
  }
  return out
}

/** For each entry of type p: the rotation of the NEXT movement packet in the same tick (R1, CHECKS 2.1). */
function nextRotation (states, idx) {
  for (let i = idx + 1; i < states.length; i++) {
    const e = states[i]
    if (e.p === 'CLIENT_TICK_END') break
    if (T.MOVE.has(e.p) && e.yaw !== undefined) return { yaw: T.num(e.yaw), pitch: T.num(e.pitch) }
  }
  return { yaw: states[idx].syaw, pitch: states[idx].spitch }
}

const attacks = (es) => T.inbound(es).filter((e) => e.p === 'INTERACT_ENTITY' && /ATTACK/.test(String(e.action)))

/** Remove scaffold leftovers and restore the pillar row. */
async function resetPillars (ctx) {
  const P = ctx.fx.pillars; const y0 = ctx.y0
  const x0 = P.first[0]; const x1 = x0 + P.spacing * (P.count - 1)
  await ctx.probe(`fill ${x0 - 2} ${y0} ${-3} ${x1 + 2} ${y0 + 3} ${8} minecraft:air`)
  for (let i = 0; i < P.count; i++) await ctx.probe(`setblock ${x0 + i * P.spacing} ${y0} 0 minecraft:stone`)
}

module.exports = { EAST, NORTH, laneBot, combat, aim, everyTick, angleToPoint, distToBox, withState, nextRotation, attacks, resetPillars, lookAt, angleDiff, EYE, T }
