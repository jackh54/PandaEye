'use strict'
/** Combat cheats (cloud checks: reach, killaura, autoclicker, aim, velocity). Targets are idle raw dummies. */
const { combat, aim, everyTick, withState, nextRotation, attacks, angleToPoint, distToBox, angleDiff, EYE, T } = require('./_common')

/** Swing ticks (tick index of every ANIMATION) from a trace. */
function swingTicks (es) {
  const out = []
  for (const e of withState(es)) if (e.p === 'ANIMATION') out.push(e.tick)
  return out
}

function clicker (name, pattern, label, check) {
  return {
    name: `cheat/autoclicker-${label}`,
    kind: 'cheat',
    engine: 'raw',
    checks: [check],
    players: [name, name + '_t'],
    suspects: [name],
    description: `clicks with tick intervals ${pattern.join(',')} repeating (${(20 * pattern.length / pattern.reduce((a, b) => a + b)).toFixed(0)} CPS); every 4th click attacks the target`,
    async run (ctx) {
      const { a, t } = await combat(ctx, name, [name + '_t'])
      let next = 0; let k = 0; let clicks = 0
      await everyTick(ctx, a, ctx.dur(14000) / 50, (n, i) => {
        aim(a, t)
        if (i < next) return
        next = i + pattern[k++ % pattern.length]
        if (clicks++ % 4 === 0) a.attack(t.entityId); else a.swing()
      })
      ctx.facts.clicks = clicks
    },
    verify (tr) {
      const st = swingTicks(tr[name])
      const iv = []
      for (let i = 1; i < st.length; i++) iv.push(st[i] - st[i - 1])
      let match = 0
      // align to the pattern phase that explains the most intervals
      let best = 0
      for (let ph = 0; ph < pattern.length; ph++) {
        match = 0
        for (let i = 0; i < iv.length; i++) if (iv[i] === pattern[(i + ph) % pattern.length]) match++
        best = Math.max(best, match)
      }
      const share = iv.length ? best / iv.length : 0
      return { ok: iv.length >= 40 && share >= 0.95, summary: `${st.length} swings, ${(share * 100).toFixed(1)}% intervals follow ${pattern.join(',')}` }
    }
  }
}

module.exports = [
  {
    name: 'cheat/reach',
    kind: 'cheat',
    engine: 'raw',
    checks: ['reach.distance'],
    players: ['ch_reach', 'ch_reach_t'],
    suspects: ['ch_reach'],
    description: 'aimed attacks from 4.5 blocks (feet distance; hitbox edge ~4.2 > 3.0), 8 attacks',
    async run (ctx) {
      const { a, t } = await combat(ctx, 'ch_reach', ['ch_reach_t'], { dist: 4.5 })
      await everyTick(ctx, a, 8 * 12, (n, i) => { aim(a, t); if (i % 12 === 6) a.attack(t.entityId) })
    },
    verify (tr, { facts }) {
      const tp = facts.targets[0].pos
      const st = withState(tr.ch_reach)
      const far = st.filter((e) => e.p === 'INTERACT_ENTITY').filter((e) => {
        const eye = { x: e.sx, y: e.sy + EYE, z: e.sz }
        return distToBox(eye, { x: tp.x - 0.3, y: tp.y, z: tp.z - 0.3 }, { x: tp.x + 0.3, y: tp.y + 1.8, z: tp.z + 0.3 }) > 4.0
      }).length
      return { ok: far >= 6, summary: `${far} attacks with eye-to-hitbox distance > 4.0` }
    }
  },
  {
    name: 'cheat/killaura-hitbox',
    kind: 'cheat',
    engine: 'raw',
    checks: ['killaura.hitbox'],
    players: ['ch_kahitbox', 'ch_kahitbox_t'],
    suspects: ['ch_kahitbox'],
    description: 'attacks from 2.5 blocks while looking 90 degrees away from the target, 10 attacks',
    async run (ctx) {
      const { a, t } = await combat(ctx, 'ch_kahitbox', ['ch_kahitbox_t'])
      await everyTick(ctx, a, 10 * 10, (n, i) => {
        const r = aim(a, t)
        a.input.yaw = r.yaw + 90
        if (i % 10 === 5) a.attack(t.entityId)
      })
    },
    verify (tr, { facts }) {
      const tp = facts.targets[0].pos
      const st = withState(tr.ch_kahitbox)
      let away = 0
      st.forEach((e, i) => {
        if (e.p !== 'INTERACT_ENTITY') return
        const r = nextRotation(st, i)
        if (angleToPoint({ x: e.sx, y: e.sy + EYE, z: e.sz }, r.yaw, r.pitch, { x: tp.x, y: tp.y + 0.9, z: tp.z }) > 45) away++
      })
      return { ok: away >= 8, summary: `${away} attacks with the look vector > 45 deg off the target` }
    }
  },
  {
    name: 'cheat/killaura-multi',
    kind: 'cheat',
    engine: 'raw',
    checks: ['killaura.multi'],
    players: ['ch_kamulti', 'ch_kamulti_t1', 'ch_kamulti_t2'],
    suspects: ['ch_kamulti'],
    description: 'attacks two different entities within one client tick, 6 times',
    async run (ctx) {
      const { a, targets } = await combat(ctx, 'ch_kamulti', ['ch_kamulti_t1', 'ch_kamulti_t2'], { dist: 2.2 })
      await everyTick(ctx, a, 6 * 12, (n, i) => {
        aim(a, { x: (targets[0].pos.x + targets[1].pos.x) / 2, y: targets[0].pos.y, z: targets[0].pos.z })
        if (i % 12 === 6) { a.attack(targets[0].entityId); a.attack(targets[1].entityId) }
      })
    },
    verify (tr) {
      const n = T.ticks(tr.ch_kamulti).filter((t) => new Set(t.packets.filter((p) => p.p === 'INTERACT_ENTITY').map((p) => p.eid)).size >= 2).length
      return { ok: n >= 5, summary: `${n} ticks with attacks on 2 distinct entities` }
    }
  },
  {
    name: 'cheat/noswing',
    kind: 'cheat',
    engine: 'raw',
    checks: ['killaura.noswing'],
    players: ['ch_noswing', 'ch_noswing_t'],
    suspects: ['ch_noswing'],
    description: 'aimed attacks from 2.5 blocks without ANIMATION (swing), 10 attacks',
    async run (ctx) {
      const { a, t } = await combat(ctx, 'ch_noswing', ['ch_noswing_t'])
      await everyTick(ctx, a, 10 * 10, (n, i) => { aim(a, t); if (i % 10 === 5) a.attack(t.entityId, { swing: false }) })
    },
    verify (tr) {
      const n = T.ticks(tr.ch_noswing).filter((t) => t.packets.some((p) => p.p === 'INTERACT_ENTITY') && !t.packets.some((p) => p.p === 'ANIMATION')).length
      return { ok: n >= 9, summary: `${n} attack ticks without a swing` }
    }
  },
  clicker('ch_acconst', [2], 'consistency', 'autoclicker.consistency'),
  clicker('ch_acpattern', [2, 2, 1], 'pattern', 'autoclicker.pattern'),
  {
    name: 'cheat/aim-snap',
    kind: 'cheat',
    engine: 'raw',
    checks: ['aim.snap'],
    players: ['ch_aimsnap', 'ch_aimsnap_t'],
    suspects: ['ch_aimsnap'],
    description: 'faces away from the target, snaps 180 deg onto it in the attack tick and back the next tick, 8 attacks',
    async run (ctx) {
      const { a, t } = await combat(ctx, 'ch_aimsnap', ['ch_aimsnap_t'])
      let phase = -1
      await everyTick(ctx, a, 8 * 20, (n, i) => {
        const r = aim(a, t)
        if (i % 20 === 10) { a.attack(t.entityId); phase = 0 } else { a.input.yaw = r.yaw + 180; phase = -1 }
        if (phase === 0) a.input.yaw = r.yaw
      })
    },
    verify (tr) {
      const st = withState(tr.ch_aimsnap)
      const mv = T.moves(tr.ch_aimsnap)
      let snaps = 0
      for (const [i, e] of st.entries()) {
        if (e.p !== 'INTERACT_ENTITY') continue
        const r1 = nextRotation(st, i)
        const d1 = Math.abs(angleDiff(e.syaw, r1.yaw))
        const later = mv.filter((m) => m.t > e.t && m.dyaw !== 0).slice(0, 2)
        const back = later.some((m) => Math.abs(m.dyaw) >= 30)
        if (d1 >= 40 && back) snaps++
      }
      return { ok: snaps >= 6, summary: `${snaps} attacks with a >= 40 deg snap and >= 30 deg snap back` }
    }
  },
  {
    name: 'cheat/velocity',
    kind: 'cheat',
    engine: 'raw',
    checks: ['velocity.vertical', 'velocity.horizontal'],
    players: ['ch_velocity', 'ch_velocity_a'],
    suspects: ['ch_velocity'],
    description: 'ignores knockback: keeps walking back and forth as if no ENTITY_VELOCITY arrived while a legit attacker hits it every 600 ms',
    async run (ctx) {
      const c = ctx.fx.combat.center
      const v = await ctx.raw('ch_velocity')
      await ctx.probe('effect ch_velocity resistance 300 4')
      await ctx.tp(v, c[0], c[1], c[2], 0, 0)
      v.ignoreVelocity = true
      const a = await ctx.raw('ch_velocity_a')
      await ctx.probe('effect ch_velocity_a resistance 300 4')
      await ctx.tp(a, c[0], c[1], c[2] - 2.6, 0, 0)
      v.input.yaw = 180 // face the attacker (north)
      let hits = 0
      const walk = everyTick(ctx, v, ctx.dur(14000) / 50, (n, i) => { v.input.forward = (Math.floor(i / 15) % 2) ? -1 : 1 })
      await everyTick(ctx, a, ctx.dur(14000) / 50, (n, i) => {
        aim(a, v)
        const d = Math.hypot(v.pos.x - a.pos.x, v.pos.z - a.pos.z)
        a.input.forward = d > 2.8 ? 1 : 0
        if (i % 12 === 6 && d < 3.0) { a.attack(v.entityId); hits++ }
      })
      await walk
      v.input.forward = 0
      ctx.facts.hits = hits
      ctx.facts.velocities = v.velocityLog.length
    },
    verify (tr) {
      const es = tr.ch_velocity
      const vel = es.filter((e) => e.d === 'out' && e.p === 'ENTITY_VELOCITY' && T.num(e.vy) > 0.15)
      let ignored = 0
      for (const e of vel) {
        const after = T.moves(es.filter((x) => x.t >= e.t)).filter((m) => m.dy !== undefined).slice(0, 6)
        if (after.length >= 3 && after.every((m) => m.dy < 0.1)) ignored++
      }
      return { ok: vel.length >= 5 && ignored >= Math.min(5, vel.length), summary: `${vel.length} velocities (vy > 0.15) sent, ${ignored} ignored (no rise in the next moves)` }
    }
  }
]
