'use strict'
/** World-interaction cheats: dig.speed (local), scaffold.* and dig.ray (cloud). */
const { NORTH, everyTick, withState, angleToPoint, distToBox, resetPillars, lookAt, EYE, T } = require('./_common')

const face = (f) => String(f).toUpperCase()

async function stoneSpot (ctx, b, x, yaw) {
  const S = ctx.fx.stone
  await ctx.tp(b, x + 0.5, S.stand[1], S.stand[2], yaw, 0)
  await ctx.ticks(b, 6)
  return { x, y: ctx.y0, z: S.max[2] } // block of the row nearest to the stand line
}

async function pillarBot (ctx, name) {
  await resetPillars(ctx)
  const b = await ctx.raw(name)
  await ctx.give(name, 'stone', 64, 0)
  await ctx.probe(`slot ${name} 0`)
  return b
}

function pillar (ctx, i) {
  const P = ctx.fx.pillars
  return { x: P.first[0] + i * P.spacing, y: ctx.y0, z: P.first[2] }
}

function placements (es) {
  const st = withState(es)
  return st.filter((e) => e.p === 'PLAYER_BLOCK_PLACEMENT').map((e) => ({
    ...e, eye: { x: e.sx, y: e.sy + EYE, z: e.sz }, b: { x: e.bx, y: e.by, z: e.bz }
  }))
}

module.exports = [
  {
    name: 'cheat/fastbreak',
    kind: 'cheat',
    engine: 'raw',
    checks: ['dig.speed'],
    players: ['ch_fastbreak'],
    description: 'START_DIGGING then FINISHED_DIGGING one tick later on stone with an empty hand (vanilla needs ~150 ticks), 6 blocks',
    async run (ctx) {
      const b = await ctx.raw('ch_fastbreak')
      for (let i = 0; i < 6; i++) {
        const blk = await stoneSpot(ctx, b, ctx.fx.stone.min[0] + 1 + 2 * i, NORTH)
        const r = lookAt(b.pos.x, b.pos.y + EYE, b.pos.z, blk.x + 0.5, blk.y + 0.5, blk.z + 1.0)
        b.input.yaw = r.yaw; b.input.pitch = r.pitch
        await ctx.ticks(b, 3)
        await everyTick(ctx, b, 3, (n, k) => {
          if (k === 0) { b.dig('start', blk, 'south'); b.swing() }
          if (k === 1) { b.dig('finish', blk, 'south'); b.swing() }
        })
        await ctx.ticks(b, 8)
      }
    },
    verify (tr) {
      const st = withState(tr.ch_fastbreak).filter((e) => e.p === 'PLAYER_DIGGING')
      let fast = 0
      for (let i = 1; i < st.length; i++) {
        if (/START/.test(st[i - 1].action) && /FINISH/.test(st[i].action) && st[i].tick - st[i - 1].tick <= 2 &&
            st[i].bx === st[i - 1].bx && st[i].bz === st[i - 1].bz) fast++
      }
      return { ok: fast >= 6, summary: `${fast} START->FINISH pairs within <= 2 ticks` }
    }
  },
  {
    name: 'cheat/dig-ray',
    kind: 'cheat',
    engine: 'raw',
    checks: ['dig.ray'],
    players: ['ch_digray'],
    description: 'starts digging stone blocks directly behind the player (looking the other way), 8 digs',
    async run (ctx) {
      const b = await ctx.raw('ch_digray')
      for (let i = 0; i < 8; i++) {
        const blk = await stoneSpot(ctx, b, ctx.fx.stone.min[0] + 1 + i, 0) // yaw 0 = facing south, row is north
        b.input.yaw = 0; b.input.pitch = 10
        await ctx.ticks(b, 2)
        await everyTick(ctx, b, 4, (n, k) => {
          if (k === 0) { b.dig('start', blk, 'south'); b.swing() }
          if (k === 3) b.dig('cancel', blk, 'south')
        })
      }
    },
    verify (tr) {
      const st = withState(tr.ch_digray).filter((e) => e.p === 'PLAYER_DIGGING' && /START/.test(e.action))
      const behind = st.filter((e) => angleToPoint({ x: e.sx, y: e.sy + EYE, z: e.sz }, e.syaw, e.spitch, { x: e.bx + 0.5, y: e.by + 0.5, z: e.bz + 0.5 }) > 90).length
      return { ok: behind >= 8, summary: `${behind} dig starts on blocks > 90 deg behind the look direction` }
    }
  },
  {
    name: 'cheat/scaffold-face',
    kind: 'cheat',
    engine: 'raw',
    checks: ['scaffold.face'],
    players: ['ch_scface'],
    description: 'standing south of a pillar, places against its NORTH face (impossible to see), 5 pillars',
    async run (ctx) {
      const b = await pillarBot(ctx, 'ch_scface')
      for (let i = 0; i < 5; i++) {
        const p = pillar(ctx, i)
        await ctx.tp(b, p.x + 0.5, ctx.y0, p.z + 2.5, NORTH, 0)
        const r = lookAt(b.pos.x, b.pos.y + EYE, b.pos.z, p.x + 0.5, p.y + 0.5, p.z + 0.5)
        b.input.yaw = r.yaw; b.input.pitch = r.pitch
        await ctx.ticks(b, 4)
        await everyTick(ctx, b, 1, () => b.place(p, 'north', { x: 0.5, y: 0.5, z: 0.0 }))
        await ctx.ticks(b, 4)
      }
      await resetPillars(ctx)
    },
    verify (tr) {
      const n = placements(tr.ch_scface).filter((e) => face(e.face) === 'NORTH' && e.eye.z > e.b.z + 1.05).length
      return { ok: n >= 5, summary: `${n} placements on a NORTH face with the eye south of the block` }
    }
  },
  {
    name: 'cheat/scaffold-ray',
    kind: 'cheat',
    engine: 'raw',
    checks: ['scaffold.ray'],
    players: ['ch_scray'],
    description: 'places on the visible top face of a pillar while looking away from it (180 deg), 6 placements',
    async run (ctx) {
      const b = await pillarBot(ctx, 'ch_scray')
      for (let i = 0; i < 6; i++) {
        const p = pillar(ctx, i % 5)
        await ctx.tp(b, p.x + 0.5, ctx.y0, p.z + 2.5, 0, 0) // yaw 0 = south, pillar is north
        b.input.yaw = 0; b.input.pitch = 20
        await ctx.ticks(b, 4)
        await everyTick(ctx, b, 1, () => b.place(p, 'up', { x: 0.5, y: 1.0, z: 0.5 }))
        await ctx.ticks(b, 4)
        await ctx.probe(`setblock ${p.x} ${p.y + 1} ${p.z} minecraft:air`)
      }
      await resetPillars(ctx)
    },
    verify (tr) {
      const n = placements(tr.ch_scray).filter((e) => angleToPoint(e.eye, e.syaw, e.spitch, { x: e.b.x + 0.5, y: e.b.y + 0.5, z: e.b.z + 0.5 }) > 60).length
      return { ok: n >= 6, summary: `${n} placements with the look vector > 60 deg off the clicked block` }
    }
  },
  {
    name: 'cheat/scaffold-cursor',
    kind: 'cheat',
    engine: 'raw',
    checks: ['scaffold.cursor'],
    players: ['ch_sccursor'],
    description: 'looks at the centre of a pillar\'s top face but sends cursor (0.95, 1.0, 0.05), 6 placements',
    async run (ctx) {
      const b = await pillarBot(ctx, 'ch_sccursor')
      for (let i = 0; i < 6; i++) {
        const p = pillar(ctx, i % 5)
        await ctx.tp(b, p.x + 0.5, ctx.y0, p.z + 2.3, NORTH, 0)
        const r = lookAt(b.pos.x, b.pos.y + EYE, b.pos.z, p.x + 0.5, p.y + 1.0, p.z + 0.5)
        b.input.yaw = r.yaw; b.input.pitch = r.pitch
        await ctx.ticks(b, 4)
        await everyTick(ctx, b, 1, () => b.place(p, 'up', { x: 0.95, y: 1.0, z: 0.05 }))
        await ctx.ticks(b, 4)
        await ctx.probe(`setblock ${p.x} ${p.y + 1} ${p.z} minecraft:air`)
      }
      await resetPillars(ctx)
    },
    verify (tr) {
      const n = placements(tr.ch_sccursor).filter((e) => {
        const P = { x: e.b.x + T.num(e.cx), y: e.b.y + T.num(e.cy), z: e.b.z + T.num(e.cz) }
        return T.num(e.cx) >= 0 && T.num(e.cx) <= 1 && angleToPoint(e.eye, e.syaw, e.spitch, P) > 3
      }).length
      return { ok: n >= 6, summary: `${n} in-range cursors > 3 deg away from the look ray` }
    }
  },
  {
    name: 'cheat/scaffold-reach',
    kind: 'cheat',
    engine: 'raw',
    checks: ['scaffold.reach'],
    players: ['ch_screach'],
    description: 'aimed placements on a pillar 6 blocks away (block_interaction_range 4.5), 5 placements',
    async run (ctx) {
      const b = await pillarBot(ctx, 'ch_screach')
      const p = pillar(ctx, 0)
      await ctx.tp(b, p.x + 0.5, ctx.y0, p.z + 7.0, NORTH, 0)
      const r = lookAt(b.pos.x, b.pos.y + EYE, b.pos.z, p.x + 0.5, p.y + 1.0, p.z + 0.5)
      b.input.yaw = r.yaw; b.input.pitch = r.pitch
      await ctx.ticks(b, 4)
      await everyTick(ctx, b, 50, (n, i) => { if (i % 10 === 5) b.place(p, 'up', { x: 0.5, y: 1.0, z: 0.5 }) })
      await resetPillars(ctx)
    },
    verify (tr) {
      const n = placements(tr.ch_screach).filter((e) => distToBox(e.eye, e.b, { x: e.b.x + 1, y: e.b.y + 1, z: e.b.z + 1 }) > 5.1).length
      return { ok: n >= 5, summary: `${n} placements with eye-to-block distance > 5.1` }
    }
  }
]
