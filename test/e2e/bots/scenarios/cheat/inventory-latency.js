'use strict'
/** Inventory movement/rotation cheats and latency abuse (cloud checks inventory.*, latency.*). */
const { laneBot, combat, aim, everyTick, T } = require('./_common')

function invTicks (es) {
  // ticks with a window-0 click, annotated with that tick's movement
  return T.ticks(es).map((t) => ({
    click: t.packets.some((p) => p.p === 'CLICK_WINDOW' && p.window === 0),
    moves: t.packets.filter((p) => T.MOVE.has(p.p))
  }))
}

module.exports = [
  {
    name: 'cheat/inventory-move',
    kind: 'cheat',
    engine: 'raw',
    checks: ['inventory.move'],
    players: ['ch_invmove'],
    description: 'keeps sprinting while clicking in the player inventory (window 0) every tick, three 1.5 s bursts',
    async run (ctx) {
      const { b } = await laneBot(ctx, 'ch_invmove', 35)
      await ctx.give('ch_invmove', 'cobblestone', 16, 0)
      Object.assign(b.input, { forward: 1, sprint: true })
      await ctx.ticks(b, 20)
      for (let burst = 0; burst < 3; burst++) {
        await everyTick(ctx, b, 30, (n, i) => b.click(0, 36, 0, 0)) // pick up / put back hotbar slot 0
        await ctx.ticks(b, 20)
      }
      b.input.forward = 0; b.input.sprint = false
    },
    verify (tr) {
      const it = invTicks(tr.ch_invmove)
      const mv = T.moves(tr.ch_invmove)
      let moving = 0
      let j = 0
      for (const t of it) {
        j += t.moves.length
        if (t.click && t.moves.length) { const m = mv[j - 1]; if (m && m.h > 0.1) moving++ }
      }
      return { ok: moving >= 60, summary: `${moving} click ticks with a > 0.1 b/t move` }
    }
  },
  {
    name: 'cheat/inventory-rotate',
    kind: 'cheat',
    engine: 'raw',
    checks: ['inventory.rotate'],
    players: ['ch_invrotate'],
    description: 'rotates 7 deg per tick while clicking in the player inventory every tick, three 1.5 s bursts',
    async run (ctx) {
      const { b } = await laneBot(ctx, 'ch_invrotate', 36)
      await ctx.give('ch_invrotate', 'cobblestone', 16, 0)
      for (let burst = 0; burst < 3; burst++) {
        await everyTick(ctx, b, 30, () => { b.input.yaw += 7; b.click(0, 36, 0, 0) })
        await ctx.ticks(b, 20)
      }
    },
    verify (tr) {
      const it = invTicks(tr.ch_invrotate)
      const n = it.filter((t) => t.click && t.moves.some((m) => m.yaw !== undefined)).length
      return { ok: n >= 60, summary: `${n} click ticks with a rotation change` }
    }
  },
  {
    name: 'cheat/pingspoof',
    kind: 'cheat',
    engine: 'raw',
    checks: ['latency.pingspoof'],
    players: ['ch_pingspoof', 'ch_pingspoof_a'],
    suspects: ['ch_pingspoof'],
    traceSeconds: 150,
    description: 'answers every KEEP_ALIVE 500 ms late while answering PINGs immediately; a legit attacker hits it every ' +
      'second so KnockbackSync keeps it combat-tagged and pings it (ping_strategy PING), 75 s',
    async run (ctx) {
      const c = ctx.fx.combat.center
      const v = await ctx.raw('ch_pingspoof')
      await ctx.probe('effect ch_pingspoof resistance 300 4')
      await ctx.tp(v, c[0], c[1], c[2], 180, 0)
      v.keepAliveDelayMs = 500
      const a = await ctx.raw('ch_pingspoof_a')
      await ctx.probe('effect ch_pingspoof_a resistance 300 4')
      await ctx.tp(a, c[0], c[1], c[2] - 2.6, 0, 0)
      await everyTick(ctx, a, ctx.dur(75000) / 50, (n, i) => {
        aim(a, v)
        const d = Math.hypot(v.pos.x - a.pos.x, v.pos.z - a.pos.z)
        a.input.forward = d > 2.8 ? 1 : 0
        if (i % 20 === 10 && d < 3.0) a.attack(v.entityId)
        if (i % 20 === 0) { v.input.forward = d > 3.5 ? 1 : 0; v.input.yaw = 180 }
      })
    },
    verify (tr) {
      const es = tr.ch_pingspoof
      const delays = (outP, inP) => {
        const sent = new Map()
        const d = []
        for (const e of es) {
          if (e.d === 'out' && e.p === outP) sent.set(String(e.id), e.t)
          if (e.d === 'in' && e.p === inP && sent.has(String(e.id))) { d.push(e.t - sent.get(String(e.id))); sent.delete(String(e.id)) }
        }
        return d
      }
      const ka = delays('KEEP_ALIVE', 'KEEP_ALIVE')
      const pg = delays('PING', 'PONG')
      const mk = T.median(ka); const mp = T.median(pg)
      return { ok: ka.length >= 4 && mk >= 450 && (pg.length === 0 || mp < 150), summary: `${ka.length} keep-alives, median reply ${T.fmt(mk, 0)} ms; ${pg.length} pings, median pong ${T.fmt(mp, 0)} ms` }
    }
  },
  {
    name: 'cheat/blink',
    kind: 'cheat',
    engine: 'raw',
    checks: ['latency.blink'],
    players: ['ch_blink', 'ch_blink_t'],
    suspects: ['ch_blink'],
    description: 'in combat (attacks every 600 ms), holds every outgoing packet for 1 s then releases them at once, 4 times',
    async run (ctx) {
      const { a, t } = await combat(ctx, 'ch_blink', ['ch_blink_t'])
      let blinks = 0
      await everyTick(ctx, a, ctx.dur(18000) / 50, (n, i) => {
        aim(a, t)
        if (i % 12 === 6) a.attack(t.entityId)
        if (i % 80 === 30) { a.hold(); blinks++ }
        if (i % 80 === 50) a.release()
      })
      a.release()
      ctx.facts.blinks = blinks
    },
    verify (tr) {
      const es = T.inbound(tr.ch_blink).filter((e) => e.t > 0)
      let n = 0
      for (let i = 1; i < es.length; i++) {
        if (es[i].t - es[i - 1].t < 900) continue
        const burst = es.slice(i).filter((e) => e.t - es[i].t < 150 && e.p === 'CLIENT_TICK_END').length
        if (burst >= 10) n++
      }
      return { ok: n >= 3, summary: `${n} gaps > 900 ms followed by >= 10 tick ends within 150 ms` }
    }
  }
]
