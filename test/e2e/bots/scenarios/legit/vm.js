'use strict'
/**
 * Legit movement with the exact vanilla-mover oracle (raw minecraft-protocol client, CLIENT_TICK_END every tick).
 * Expected: zero confirmed PandaEye flags, never kicked.
 */
const { lookAt, EYE } = require('../../lib/util')

const EAST = -90 // vanilla yaw facing +x
const WEST = 90

async function laneStart (ctx, b, lane, yaw = EAST) {
  await ctx.tp(b, lane.x, lane.y, lane.z, yaw, 0)
  b.input = { forward: 0, strafe: 0, jump: false, sprint: false, yaw, pitch: 0 }
  await ctx.ticks(b, 10) // land/settle
}

async function run (ctx, b, ms, input) {
  Object.assign(b.input, input)
  await ctx.sleep(ms)
}

module.exports = [
  {
    name: 'legit/vm-walk',
    kind: 'legit',
    engine: 'raw',
    players: ['lg_vmwalk'],
    description: 'walk 8 s east, stop, walk 8 s west (exact vanilla physics)',
    async run (ctx) {
      const b = await ctx.raw('lg_vmwalk')
      const lane = ctx.lane(0)
      await laneStart(ctx, b, lane)
      await run(ctx, b, ctx.dur(8000), { forward: 1 })
      await run(ctx, b, 1000, { forward: 0 })
      await run(ctx, b, ctx.dur(8000), { forward: 1, yaw: WEST })
      await run(ctx, b, 1000, { forward: 0 })
    }
  },
  {
    name: 'legit/vm-sprint',
    kind: 'legit',
    engine: 'raw',
    players: ['lg_vmsprint'],
    description: 'sprint 7 s east, stop, sprint 7 s west',
    async run (ctx) {
      const b = await ctx.raw('lg_vmsprint')
      const lane = ctx.lane(1)
      await laneStart(ctx, b, lane)
      await run(ctx, b, ctx.dur(7000), { forward: 1, sprint: true })
      await run(ctx, b, 1000, { forward: 0, sprint: false })
      await run(ctx, b, ctx.dur(7000), { forward: 1, sprint: true, yaw: WEST })
      await run(ctx, b, 1000, { forward: 0, sprint: false })
    }
  },
  {
    name: 'legit/vm-sprint-jump',
    kind: 'legit',
    engine: 'raw',
    players: ['lg_vmsjump'],
    description: 'sprint-jump 8 s east and 8 s west (jump held: re-jumps on landing)',
    async run (ctx) {
      const b = await ctx.raw('lg_vmsjump')
      const lane = ctx.lane(2)
      await laneStart(ctx, b, lane)
      await run(ctx, b, ctx.dur(8000), { forward: 1, sprint: true, jump: true })
      await run(ctx, b, 1500, { forward: 0, sprint: false, jump: false })
      await run(ctx, b, ctx.dur(8000), { forward: 1, sprint: true, jump: true, yaw: WEST })
      await run(ctx, b, 1500, { forward: 0, sprint: false, jump: false })
    }
  },
  {
    name: 'legit/vm-idle',
    kind: 'legit',
    engine: 'raw',
    players: ['lg_vmidle'],
    description: 'stand still 10 s (position reminder every 20 ticks), then look around slowly',
    async run (ctx) {
      const b = await ctx.raw('lg_vmidle')
      const lane = ctx.lane(3)
      await laneStart(ctx, b, lane)
      await ctx.sleep(ctx.dur(10000))
      for (let i = 0; i < 40; i++) { b.input.yaw += 2.5; b.input.pitch = 10 * Math.sin(i / 6); await ctx.ticks(b, 1) }
      await ctx.sleep(1000)
    }
  },
  {
    name: 'legit/vm-ledge',
    kind: 'legit',
    engine: 'raw',
    players: ['lg_vmledge'],
    description: 'walk/sprint off a 3-block ledge three times (fall with gravity 0.08, drag 0.98)',
    async run (ctx) {
      const L = ctx.fx.ledge
      const top = L.top
      const box = { minX: -16, maxX: -11, minZ: -16, maxZ: -11, top: top[1] }
      const b = await ctx.raw('lg_vmledge', { boxes: [box] })
      b.mover.boxes = [box]
      for (let i = 0; i < 3; i++) {
        await ctx.tp(b, top[0], top[1], top[2], WEST, 0)
        b.input = { forward: 0, strafe: 0, jump: false, sprint: false, yaw: WEST, pitch: 0 }
        await ctx.ticks(b, 10)
        Object.assign(b.input, { forward: 1, sprint: i === 1, jump: i === 2 })
        await ctx.sleep(1800)
        Object.assign(b.input, { forward: 0, sprint: false, jump: false })
        await ctx.sleep(800)
      }
    }
  },
  {
    name: 'legit/vm-knockback',
    kind: 'legit',
    engine: 'raw',
    players: ['lg_vmkb'],
    description: 'receive 8 server velocity pulses (setVelocity, like knockback) and apply them exactly, standing and walking',
    async run (ctx) {
      const b = await ctx.raw('lg_vmkb')
      const lane = ctx.lane(4)
      await laneStart(ctx, b, lane)
      for (let i = 0; i < 8; i++) {
        Object.assign(b.input, { forward: i % 2, sprint: i % 4 === 1 })
        await ctx.ticks(b, 10)
        const vx = (i % 2 ? -0.4 : 0.4) * ctx.rng.range(0.8, 1.1)
        await ctx.probe(`velocity lg_vmkb ${vx.toFixed(4)} 0.3608 ${(ctx.rng.range(-0.1, 0.1)).toFixed(4)}`)
        await ctx.ticks(b, 25)
      }
      b.input.forward = 0
      await ctx.sleep(1000)
    }
  },
  {
    name: 'legit/vm-combat',
    kind: 'legit',
    engine: 'raw',
    players: ['lg_vmfight', 'lg_vmdummy'],
    suspects: ['lg_vmfight'],
    description: 'legit raw fighter: approaches a target, jittered 8-12 CPS, attacks only when aimed (<8 deg) and in 2.9 blocks, noisy aim',
    async run (ctx) {
      const c = ctx.fx.combat.center
      const dummy = await ctx.raw('lg_vmdummy')
      await ctx.probe('effect lg_vmdummy resistance 120 4')
      await ctx.tp(dummy, c[0], c[1], c[2], 180, 0)
      const f = await ctx.raw('lg_vmfight')
      await ctx.probe('effect lg_vmfight resistance 120 4')
      await ctx.tp(f, c[0], c[1], c[2] + 6, 180, 0)
      const r = ctx.rng
      let nextClick = 0
      let attacks = 0
      f.on('tick', (n) => {
        const me = f.pos; const t = dummy.pos
        if (!me || !t) return
        const want = lookAt(me.x, me.y + EYE, me.z, t.x + r.gauss(0, 0.08), t.y + 1.1 + r.gauss(0, 0.12), t.z + r.gauss(0, 0.08))
        // smooth, noisy mouse: 60% of the remaining angle per tick + noise
        let dy = ((want.yaw - f.input.yaw) % 360 + 540) % 360 - 180
        f.input.yaw += dy * r.range(0.45, 0.7) + r.gauss(0, 0.6)
        f.input.pitch += (want.pitch - f.input.pitch) * 0.5 + r.gauss(0, 0.3)
        const dist = Math.hypot(t.x - me.x, t.z - me.z)
        f.input.forward = dist > 2.4 ? 1 : 0
        f.input.sprint = dist > 3.5
        dy = Math.abs(((want.yaw - f.input.yaw) % 360 + 540) % 360 - 180)
        if (n >= nextClick) {
          const cps = r.range(8, 12)
          nextClick = n + Math.max(1, Math.round(20 / cps + r.gauss(0, 0.5)))
          if (dist < 2.9 && dy < 8) { f.attack(dummy.entityId); attacks++ } else f.swing()
        }
      })
      await ctx.sleep(ctx.dur(15000))
      f.removeAllListeners('tick')
      f.input.forward = 0
      ctx.facts.attacks = attacks
      ctx.note(`legit attacks: ${attacks}`)
      await ctx.sleep(500)
    }
  }
]
