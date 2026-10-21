'use strict'
/**
 * Legit scenarios with mineflayer (human-like noise from a seeded RNG). Expected: zero confirmed flags, no kicks.
 * mineflayer physics are close to, not exactly, vanilla (see lib/mf.js); vm-* scenarios are the exact oracle.
 */
const { ticks, waitAt, walkTo, aimLoop, angleTo, Vec3 } = require('../../lib/mf')
const { sleep } = require('../../lib/util')

const DEG = Math.PI / 180
// mineflayer yaw: 0 = north (-z), PI/2 = west (-x), -PI/2 = east (+x)
const MF_EAST = -Math.PI / 2
const MF_WEST = Math.PI / 2

async function holdMove (bot, ms, { yaw, sprint = false, jump = false, rng, noiseDeg = 1.5 }) {
  const end = Date.now() + ms
  bot.setControlState('forward', true)
  bot.setControlState('sprint', sprint)
  bot.setControlState('jump', jump)
  while (Date.now() < end) {
    bot.look(yaw + rng.gauss(0, noiseDeg) * DEG, rng.gauss(0, 2) * DEG, false)
    await ticks(bot, 6)
  }
  bot.clearControlStates()
  await ticks(bot, 10)
}

function legacy (version, name, laneIdx) {
  return {
    name: `legit/mf-legacy-${version.replace(/\./g, '')}`,
    kind: 'legit',
    engine: 'mineflayer',
    players: [name],
    description: `mineflayer ${version} client through ViaVersion/ViaBackwards: walk, sprint, sprint-jump (no CLIENT_TICK_END: non-tick client)`,
    async run (ctx) {
      const bot = await ctx.mf(name, { version })
      const lane = ctx.lane(laneIdx)
      await ctx.tp(name, lane.x, lane.y, lane.z, -90, 0)
      await waitAt(bot, lane.x, lane.y, lane.z)
      await holdMove(bot, 4000, { yaw: MF_EAST, rng: ctx.rng })
      await holdMove(bot, 4000, { yaw: MF_EAST, sprint: true, jump: true, rng: ctx.rng })
      await holdMove(bot, 4000, { yaw: MF_WEST, sprint: true, rng: ctx.rng })
    }
  }
}

module.exports = [
  // 1.20.4 through ViaBackwards. Pre-1.18 clients cannot be used on this world: its surface is at y = -60 and
  // clients before 1.18 only know y 0..255 (mineflayer 1.12.2 never loads a chunk; checked 2026-10-21).
  legacy('1.20.4', 'lg_mf1204', 13),
  {
    name: 'legit/mf-walk',
    kind: 'legit',
    engine: 'mineflayer',
    players: ['lg_mfwalk'],
    description: 'mineflayer walk / sprint / sprint-jump loops on flat ground with heading noise',
    async run (ctx) {
      const bot = await ctx.mf('lg_mfwalk')
      const lane = ctx.lane(10)
      await ctx.tp('lg_mfwalk', lane.x, lane.y, lane.z, -90, 0)
      await waitAt(bot, lane.x, lane.y, lane.z)
      const r = ctx.rng
      await holdMove(bot, ctx.dur(4000), { yaw: MF_EAST, rng: r })
      await holdMove(bot, ctx.dur(4000), { yaw: MF_EAST, sprint: true, rng: r })
      await holdMove(bot, ctx.dur(5000), { yaw: MF_EAST, sprint: true, jump: true, rng: r })
      await holdMove(bot, ctx.dur(4000), { yaw: MF_WEST, rng: r })
      await holdMove(bot, ctx.dur(5000), { yaw: MF_WEST, sprint: true, jump: true, rng: r })
    }
  },
  {
    name: 'legit/mf-stairs',
    kind: 'legit',
    engine: 'mineflayer',
    players: ['lg_mfstairs'],
    description: 'walk up oak stairs, then up a bottom-slab staircase, then back down',
    async run (ctx) {
      const bot = await ctx.mf('lg_mfstairs')
      const s = ctx.fx.stairs; const sl = ctx.fx.slabs
      await ctx.tp('lg_mfstairs', s.start[0], s.start[1], s.start[2], -90, 0)
      await waitAt(bot, s.start[0], s.start[1], s.start[2])
      const up = await walkTo(bot, s.end[0], s.end[2], { rng: ctx.rng, timeoutMs: 12000 })
      ctx.note(`stairs reached top: ${up} y=${bot.entity.position.y.toFixed(2)}`)
      await ctx.tp('lg_mfstairs', sl.start[0], sl.start[1], sl.start[2], -90, 0)
      await waitAt(bot, sl.start[0], sl.start[1], sl.start[2])
      const up2 = await walkTo(bot, sl.end[0], sl.end[2], { rng: ctx.rng, timeoutMs: 15000 })
      ctx.note(`slabs reached top: ${up2} y=${bot.entity.position.y.toFixed(2)}`)
      await walkTo(bot, sl.start[0], sl.start[2], { rng: ctx.rng, timeoutMs: 15000 })
      await ticks(bot, 20)
    }
  },
  {
    name: 'legit/mf-ledge',
    kind: 'legit',
    engine: 'mineflayer',
    players: ['lg_mfledge'],
    description: 'walk, sprint and sprint-jump off a 3-block ledge',
    async run (ctx) {
      const bot = await ctx.mf('lg_mfledge')
      const L = ctx.fx.ledge
      for (const mode of [{}, { sprint: true }, { sprint: true, jump: true }]) {
        await ctx.tp('lg_mfledge', L.top[0], L.top[1], L.top[2], 90, 0)
        await waitAt(bot, L.top[0], L.top[1], L.top[2])
        await ticks(bot, 10)
        await walkTo(bot, L.below[0], L.below[2], { ...mode, rng: ctx.rng, timeoutMs: 6000 })
        await ticks(bot, 20)
      }
    }
  },
  {
    name: 'legit/mf-swim',
    kind: 'legit',
    engine: 'mineflayer',
    players: ['lg_mfswim'],
    description: 'walk into a 3-deep pool and swim across briefly (jump held to stay at the surface)',
    async run (ctx) {
      const bot = await ctx.mf('lg_mfswim')
      const P = ctx.fx.pool
      await ctx.tp('lg_mfswim', P.entry[0], P.entry[1], P.entry[2], 90, 0)
      await waitAt(bot, P.entry[0], P.entry[1], P.entry[2])
      await walkTo(bot, P.entry[0] - 3, P.entry[2], { rng: ctx.rng, timeoutMs: 4000 })
      bot.setControlState('jump', true)
      bot.setControlState('forward', true)
      bot.look(MF_WEST, 0, false)
      await sleep(ctx.dur(4000))
      bot.clearControlStates()
      await sleep(1500)
      ctx.note(`in water: ${bot.entity.isInWater} y=${bot.entity.position.y.toFixed(2)}`)
      await ctx.tp('lg_mfswim', P.entry[0], P.entry[1], P.entry[2], 90, 0)
      await ticks(bot, 20)
    }
  },
  {
    name: 'legit/mf-pvp',
    kind: 'legit',
    engine: 'mineflayer',
    players: ['lg_mfpvpa', 'lg_mfpvpb'],
    description: 'two bots fight: sprint hits, knockback taken normally, 8-12 CPS jittered clicks, smoothed noisy aim, strafing',
    async run (ctx) {
      const c = ctx.fx.combat.center
      const a = await ctx.mf('lg_mfpvpa')
      const b = await ctx.mf('lg_mfpvpb')
      for (const n of ['lg_mfpvpa', 'lg_mfpvpb']) {
        await ctx.probe(`effect ${n} resistance 120 3`)
        await ctx.probe(`effect ${n} regeneration 120 2`)
      }
      await ctx.tp('lg_mfpvpa', c[0], c[1], c[2] - 4, 0, 0)
      await ctx.tp('lg_mfpvpb', c[0], c[1], c[2] + 4, 180, 0)
      await waitAt(a, c[0], c[1], c[2] - 4)
      await waitAt(b, c[0], c[1], c[2] + 4)
      const fighters = [[a, b], [b, a]]
      const stops = []
      let hits = 0; let swings = 0
      for (const [me, other] of fighters) {
        const r = ctx.rng
        const target = () => me.players[other.username]?.entity || null
        stops.push(aimLoop(me, target, r))
        let nextClick = Date.now() + r.range(100, 400)
        let strafeUntil = 0
        const f = () => {
          const t = target()
          if (!t) return
          const dist = me.entity.position.distanceTo(t.position)
          me.setControlState('forward', dist > 2.6)
          me.setControlState('sprint', dist > 3.2)
          if (Date.now() > strafeUntil) {
            const s = r.pick(['left', 'right', 'none'])
            me.setControlState('left', s === 'left'); me.setControlState('right', s === 'right')
            strafeUntil = Date.now() + r.range(400, 1400)
          }
          if (Date.now() >= nextClick) {
            nextClick = Date.now() + 1000 / r.range(8, 12) * (1 + r.gauss(0, 0.12))
            if (dist < 3.0 && angleTo(me, t.position.offset(0, 1.0, 0)) < 20) { me.attack(t); hits++ } else { me.swingArm(); swings++ }
          }
        }
        me.on('physicsTick', f)
        stops.push(() => me.removeListener('physicsTick', f))
      }
      await sleep(ctx.dur(18000))
      stops.forEach((s) => s())
      a.clearControlStates(); b.clearControlStates()
      ctx.facts.hits = hits; ctx.facts.swings = swings
      ctx.note(`pvp hits=${hits} air swings=${swings}`)
      await sleep(800)
    }
  },
  {
    name: 'legit/mf-build',
    kind: 'legit',
    engine: 'mineflayer',
    players: ['lg_mfbuild'],
    description: 'place a row of planks against visible top faces (mineflayer looks at the face), then a 2-block pillar',
    async run (ctx) {
      const bot = await ctx.mf('lg_mfbuild')
      const B = ctx.fx.build
      const x0 = B.min[0] + 2; const z0 = B.min[2] + 3; const y0 = ctx.y0
      await ctx.give('lg_mfbuild', 'oak_planks', 64, 0)
      await ctx.probe('slot lg_mfbuild 0')
      await ctx.probe(`fill ${B.min[0]} ${y0} ${B.min[2]} ${B.max[0]} ${y0 + 3} ${B.max[2]} minecraft:air`)
      await ctx.tp('lg_mfbuild', x0 + 0.5, y0, z0 + 0.5, -90, 30)
      await waitAt(bot, x0 + 0.5, y0, z0 + 0.5)
      await ticks(bot, 10)
      let placed = 0
      for (let i = 2; i <= 4; i++) {
        const ref = bot.blockAt(new Vec3(x0 + i, y0 - 1, z0))
        try { await bot.placeBlock(ref, new Vec3(0, 1, 0)); placed++ } catch (e) { ctx.note(`place ${i}: ${e.message}`) }
        await ticks(bot, ctx.rng.int(4, 8))
      }
      for (let k = 0; k < 2; k++) {
        const ref = bot.blockAt(new Vec3(x0 + 2, y0 + k, z0))
        try { await bot.placeBlock(ref, new Vec3(0, 1, 0)); placed++ } catch (e) { ctx.note(`pillar ${k}: ${e.message}`) }
        await ticks(bot, ctx.rng.int(5, 9))
      }
      ctx.facts.placed = placed
      ctx.note(`placed ${placed} blocks`)
    }
  },
  {
    name: 'legit/mf-chest',
    kind: 'legit',
    engine: 'mineflayer',
    players: ['lg_mfchest'],
    description: 'open a chest while standing still, take items, put some back, close',
    async run (ctx) {
      const bot = await ctx.mf('lg_mfchest')
      const C = ctx.fx.chest
      await ctx.tp('lg_mfchest', C.stand[0], C.stand[1], C.stand[2], 180, 30)
      await waitAt(bot, C.stand[0], C.stand[1], C.stand[2])
      await ticks(bot, 10)
      const block = bot.blockAt(new Vec3(C.block[0], C.block[1], C.block[2]))
      try {
        const win = await bot.openContainer(block)
        await sleep(400 + ctx.rng.int(0, 300))
        const types = win.containerItems().map((i) => i.type)
        for (const t of types.slice(0, 2)) { await win.withdraw(t, null, 1); await sleep(250 + ctx.rng.int(0, 250)) }
        await sleep(300)
        const inv = bot.inventory.items()
        if (inv[0]) await win.deposit(inv[0].type, null, 1)
        await sleep(400)
        win.close()
        ctx.facts.chestItems = types.length
      } catch (e) {
        ctx.note(`chest: ${e.message}`)
      }
      await ticks(bot, 20)
    }
  },
  {
    name: 'legit/mf-teleport',
    kind: 'legit',
    engine: 'mineflayer',
    players: ['lg_mftp'],
    description: 'console /tp while walking (3x) and two ender pearl throws',
    async run (ctx) {
      const bot = await ctx.mf('lg_mftp')
      const lane = ctx.lane(12)
      await ctx.tp('lg_mftp', lane.x, lane.y, lane.z, -90, 0)
      await waitAt(bot, lane.x, lane.y, lane.z)
      bot.setControlState('forward', true)
      for (let i = 0; i < 3; i++) {
        await sleep(1500)
        await ctx.tp('lg_mftp', lane.x + 5 * i, lane.y, lane.z + 0.5, -90 + 10 * i, 0)
      }
      bot.clearControlStates()
      await ticks(bot, 20)
      await ctx.give('lg_mftp', 'ender_pearl', 4, 0)
      await ctx.probe('slot lg_mftp 0')
      await sleep(500)
      for (let i = 0; i < 2; i++) {
        await bot.look(MF_EAST, 20 * DEG, true)
        await ticks(bot, 5)
        bot.activateItem()
        await sleep(3000)
        ctx.note(`after pearl ${i}: x=${bot.entity.position.x.toFixed(1)}`)
      }
    }
  },
  {
    name: 'legit/mf-vehicle',
    kind: 'legit',
    engine: 'mineflayer',
    players: ['lg_mfride'],
    description: 'ride a minecart (sit, then dismount) and a boat (paddle briefly) - best effort, mineflayer vehicle support is partial',
    async run (ctx) {
      const bot = await ctx.mf('lg_mfride')
      const R = ctx.fx.rails; const P = ctx.fx.pool
      try {
        await ctx.tp('lg_mfride', R.start[0] + 1, R.start[1], R.start[2] + 1.2, 180, 20)
        await waitAt(bot, R.start[0] + 1, R.start[1], R.start[2] + 1.2)
        await ticks(bot, 20)
        const cart = bot.nearestEntity((e) => e.name === 'minecart')
        if (cart) {
          await bot.mount(cart)
          await sleep(2500)
          ctx.note(`minecart mounted: ${!!bot.vehicle}`)
          bot.dismount()
          await sleep(1500)
        } else ctx.note('no minecart found')
      } catch (e) { ctx.note(`minecart: ${e.message}`) }
      try {
        // boat: the probe spawns a boat at the player in the pool and seats the player (server-side mount)
        const bx = (P.min[0] + P.max[0]) / 2 + 0.5; const bz = (P.min[2] + P.max[2]) / 2 + 0.5
        await ctx.tp('lg_mfride', bx, P.max[1] + 1, bz, 90, 10)
        await waitAt(bot, bx, P.max[1] + 1, bz)
        await ticks(bot, 10)
        await ctx.probe('mount lg_mfride boat')
        await sleep(800)
        for (let i = 0; i < 60; i++) { try { bot.moveVehicle(0, 1) } catch (_) {} await ticks(bot, 1) }
        ctx.note(`boat mounted: ${!!bot.vehicle}`)
        await ctx.probe('dismount lg_mfride')
        await sleep(1500)
      } catch (e) { ctx.note(`boat: ${e.message}`) }
      await ctx.tp('lg_mfride', P.entry[0], P.entry[1], P.entry[2], 90, 0)
      await ticks(bot, 20)
    }
  }
]
