'use strict'
/** Movement / tick cheats (local plugin checks, CHECKS.md section 1). */
const { EAST, laneBot, everyTick, T } = require('./_common')

function longestRun (arr, pred) {
  let best = 0; let cur = 0
  for (const x of arr) { if (pred(x)) { cur++; best = Math.max(best, cur) } else cur = 0 }
  return best
}

function timer (mult, name, laneIdx) {
  return {
    name: `cheat/timer-${Math.round(mult * 100)}`,
    kind: 'cheat',
    engine: 'raw',
    checks: ['timer.balance'],
    players: [name],
    traceSeconds: 200,
    description: `game timer at ${mult}x: CLIENT_TICK_END and movement ${mult * 20} times per second (walks a square). ` +
      'timer.balance needs 60 s calibration + a 20 s window, so the full run is 90 s.',
    async run (ctx) {
      const { b } = await laneBot(ctx, name, laneIdx)
      b.setTickMs(50 / mult)
      ctx.facts.mult = mult
      ctx.facts.startTick = b.tick
      let k = 0
      await everyTick(ctx, b, Math.round(ctx.dur(90000) / (50 / mult)), (n, i) => {
        if (i % 60 === 0) { b.input.yaw = EAST + 90 * (k++ % 4); b.input.forward = 1 }
      })
      b.setTickMs(50)
      b.input.forward = 0
    },
    verify (tr, { facts }) {
      const es = tr[name]
      const te = T.inbound(es).filter((e) => e.p === 'CLIENT_TICK_END')
      if (te.length < 100) return { ok: false, summary: `only ${te.length} tick ends` }
      const rate = T.tickRate(es, te[40].t, te[te.length - 30].t)
      const maxMoves = Math.max(...T.ticks(es).slice(5).map((t) => t.packets.filter((p) => T.MOVE.has(p.p)).length))
      const ok = Math.abs(rate - 20 * facts.mult) < 0.6 && maxMoves <= 2
      return { ok, summary: `tick_end rate ${rate.toFixed(2)}/s (expected ${(20 * facts.mult).toFixed(1)}), max moves/tick ${maxMoves}` }
    }
  }
}

module.exports = [
  timer(1.05, 'ch_timer105', 20),
  timer(1.2, 'ch_timer120', 21),
  {
    name: 'cheat/speed',
    kind: 'cheat',
    engine: 'raw',
    checks: ['movement.speed'],
    players: ['ch_speed'],
    description: 'steady 0.4 blocks/tick on the ground (vanilla sprint is 0.2806)',
    async run (ctx) {
      const { b, lane } = await laneBot(ctx, 'ch_speed', 22)
      b.manualMovement = true
      let x = lane.x
      await everyTick(ctx, b, ctx.dur(10000) / 50, () => {
        x += 0.4
        b.sendMove({ type: 'position', x, y: lane.y, z: lane.z, onGround: true })
      })
    },
    verify (tr) {
      const mv = T.moves(tr.ch_speed).filter((m) => m.h !== undefined)
      const run = longestRun(mv, (m) => Math.abs(m.h - 0.4) < 0.005 && m.g)
      return { ok: run >= 20, summary: `${run} consecutive on-ground moves of 0.400 b/t` }
    }
  },
  {
    name: 'cheat/fly-hover',
    kind: 'cheat',
    engine: 'raw',
    checks: ['movement.fly'],
    players: ['ch_flyhover'],
    description: 'hover 3 blocks above the ground (dy = 0, onGround false) while drifting 0.05 b/t',
    async run (ctx) {
      const { b, lane } = await laneBot(ctx, 'ch_flyhover', 23)
      b.manualMovement = true
      let x = lane.x
      let y = lane.y
      await everyTick(ctx, b, ctx.dur(9000) / 50, (n, i) => {
        if (i < 30) y += 0.1 // climb 3 blocks (also a fly pattern), then hover
        x += 0.05
        b.sendMove({ type: 'position', x, y, z: lane.z, onGround: false })
      })
    },
    verify (tr) {
      const mv = T.moves(tr.ch_flyhover).filter((m) => m.dy !== undefined)
      const run = longestRun(mv, (m) => Math.abs(m.dy) < 1e-6 && m.g === false)
      return { ok: run >= 40, summary: `${run} consecutive airborne moves with dy = 0` }
    }
  },
  {
    name: 'cheat/fly-ascend',
    kind: 'cheat',
    engine: 'raw',
    checks: ['movement.fly'],
    players: ['ch_flyascend'],
    description: 'ascend 0.1 blocks/tick without jumping for 5 s (onGround false)',
    async run (ctx) {
      const { b, lane } = await laneBot(ctx, 'ch_flyascend', 24)
      b.manualMovement = true
      let y = lane.y; let x = lane.x
      await everyTick(ctx, b, ctx.dur(6000) / 50, () => {
        y += 0.1; x += 0.05
        b.sendMove({ type: 'position', x, y, z: lane.z, onGround: false })
      })
      await ctx.tp(b, lane.x, lane.y, lane.z, EAST, 0)
    },
    verify (tr) {
      const mv = T.moves(tr.ch_flyascend).filter((m) => m.dy !== undefined)
      const run = longestRun(mv, (m) => Math.abs(m.dy - 0.1) < 1e-6 && m.g === false)
      return { ok: run >= 20, summary: `${run} consecutive airborne moves with dy = +0.1` }
    }
  },
  {
    name: 'cheat/step',
    kind: 'cheat',
    engine: 'raw',
    checks: ['movement.step'],
    players: ['ch_step'],
    description: 'walk into 1-block steps (heights 1, 2, 3) and step up 1.0 in a single tick, 3 runs',
    async run (ctx) {
      const S = ctx.fx.steps; const y0 = ctx.y0
      const b = await ctx.raw('ch_step')
      const top = (x) => { // support height under the footprint at x (steps at first_x .. first_x+2)
        let t = y0
        for (let i = 0; i < 3; i++) { const bx = S.first_x + i; if (x + 0.3 > bx && x - 0.3 < bx + 1) t = Math.max(t, y0 + i + 1) }
        return t
      }
      for (let run = 0; run < 3; run++) {
        await ctx.tp(b, S.start[0], S.start[1], S.start[2], EAST, 0)
        await ctx.ticks(b, 10)
        b.manualMovement = true
        let x = S.start[0]; let y = y0; let vy = 0
        await everyTick(ctx, b, 40, () => {
          x += 0.2158
          const t = top(x)
          if (t > y + 1e-9 && y >= t - 1.01) { y = t; vy = 0 } // the cheat: full-block step in one tick
          else if (y > t) { y += vy; vy = (vy - 0.08) * 0.98; if (y <= t) { y = t; vy = 0 } }
          b.sendMove({ type: 'position', x, y, z: S.start[2], onGround: Math.abs(y - top(x)) < 1e-9 })
        })
        b.manualMovement = false
      }
    },
    verify (tr) {
      const mv = T.moves(tr.ch_step).filter((m) => m.dy !== undefined)
      let steps = 0
      for (let i = 1; i < mv.length; i++) if (Math.abs(mv[i].dy - 1.0) < 1e-6 && mv[i - 1].g) steps++
      return { ok: steps >= 6, summary: `${steps} single-tick +1.0 steps from the ground` }
    }
  },
  {
    name: 'cheat/groundspoof',
    kind: 'cheat',
    engine: 'raw',
    checks: ['movement.groundspoof'],
    players: ['ch_gspoof'],
    description: 'fall 20 blocks with vanilla gravity but claim onGround=true on every packet (no fall damage), 3 falls',
    async run (ctx) {
      const lane = ctx.lane(25)
      const b = await ctx.raw('ch_gspoof')
      for (let k = 0; k < 3; k++) {
        await ctx.tp(b, lane.x + 3 * k, lane.y + 20, lane.z, EAST, 0)
        b.manualMovement = true
        let y = lane.y + 20; let vy = 0; let landed = 0
        await everyTick(ctx, b, 60, () => {
          y += vy; vy = (vy - 0.08) * 0.98
          if (y <= lane.y) { y = lane.y; vy = 0; landed++ }
          b.sendMove({ type: 'position', x: lane.x + 3 * k, y, z: lane.z, onGround: true })
        })
        b.manualMovement = false
        await ctx.ticks(b, 10)
      }
    },
    verify (tr) {
      const mv = T.moves(tr.ch_gspoof).filter((m) => m.dy !== undefined)
      let pairs = 0
      for (let i = 1; i < mv.length; i++) if (mv[i].g && mv[i - 1].g && mv[i].dy < -0.1 && mv[i - 1].dy < -0.1) pairs++
      return { ok: pairs >= 15, summary: `${pairs} consecutive onGround=true packets falling faster than 0.1 b/t` }
    }
  },
  {
    name: 'cheat/noslow',
    kind: 'cheat',
    engine: 'raw',
    checks: ['movement.noslow'],
    players: ['ch_noslow'],
    description: 'eat golden apples while sprinting at full sprint speed (vanilla scales input by 0.2 and stops sprinting)',
    async run (ctx) {
      const { b } = await laneBot(ctx, 'ch_noslow', 26)
      await ctx.give('ch_noslow', 'golden_apple', 64, 0)
      await ctx.probe('slot ch_noslow 0')
      await ctx.ticks(b, 5)
      Object.assign(b.input, { forward: 1, sprint: true })
      await ctx.ticks(b, 20)
      await everyTick(ctx, b, ctx.dur(10000) / 50, (n, i) => { if (i % 40 === 0) b.useItem(0) })
      b.input.forward = 0; b.input.sprint = false
    },
    verify (tr) {
      const es = T.inbound(tr.ch_noslow)
      let fast = 0; let uses = 0
      for (let i = 0; i < es.length; i++) {
        if (es[i].p !== 'USE_ITEM') continue
        uses++
        const after = T.moves(es.slice(i)).filter((m) => m.h !== undefined).slice(4, 30)
        fast += after.filter((m) => m.h > 0.25).length
      }
      return { ok: uses >= 3 && fast >= 30, summary: `${uses} USE_ITEM, ${fast} moves > 0.25 b/t within 30 ticks after them` }
    }
  }
]
