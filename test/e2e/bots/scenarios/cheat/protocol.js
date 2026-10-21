'use strict'
/** Invalid-protocol cheats (local plugin checks protocol.* and inventory.closed). */
const { laneBot, everyTick, T } = require('./_common')

module.exports = [
  {
    name: 'cheat/pitch',
    kind: 'cheat',
    engine: 'raw',
    checks: ['protocol.pitch'],
    players: ['ch_pitch'],
    description: 'rotation packets with pitch 95 (vanilla clamps to [-90, 90]) every 10 ticks',
    async run (ctx) {
      const { b } = await laneBot(ctx, 'ch_pitch', 27)
      await everyTick(ctx, b, ctx.dur(5000) / 50, (n, i) => {
        if (i % 10 === 0) b.sendMove({ type: 'look', yaw: b.mover.yaw + 1, pitch: 95, onGround: b.mover.onGround })
      })
    },
    verify (tr) {
      const n = T.moves(tr.ch_pitch).filter((m) => m.pitch !== undefined && m.pitch > 94.9).length
      return { ok: n >= 3, summary: `${n} rotation packets with pitch >= 95` }
    }
  },
  {
    name: 'cheat/position-nan',
    kind: 'cheat',
    engine: 'raw',
    checks: ['protocol.position'],
    players: ['ch_posnan'],
    kicked: 'expected',
    description: 'a position packet with x = NaN (vanilla disconnects with "Invalid move player packet")',
    async run (ctx) {
      const { b, lane } = await laneBot(ctx, 'ch_posnan', 28)
      await ctx.ticks(b, 20)
      b.sendMove({ type: 'position', x: NaN, y: lane.y, z: lane.z, onGround: true })
      await ctx.sleep(1500)
      ctx.facts.kicked = b.kicked
    },
    verify (tr) {
      const n = T.inbound(tr.ch_posnan).filter((e) => T.MOVE.has(e.p) && (e.x === 'NaN' || Number.isNaN(e.x))).length
      return { ok: n >= 1, summary: `${n} movement packets with NaN x` }
    }
  },
  {
    name: 'cheat/position-oow',
    kind: 'cheat',
    engine: 'raw',
    checks: ['protocol.position'],
    players: ['ch_posoow'],
    description: 'position packets with x = 3.1e7 (outside the 3.0e7 world limit), 3 times',
    async run (ctx) {
      const { b, lane } = await laneBot(ctx, 'ch_posoow', 29)
      await everyTick(ctx, b, 70, (n, i) => {
        if (i % 20 === 5) b.sendMove({ type: 'position', x: 3.1e7, y: lane.y, z: lane.z, onGround: true })
      })
    },
    verify (tr) {
      const n = T.inbound(tr.ch_posoow).filter((e) => T.MOVE.has(e.p) && T.num(e.x) > 3.0e7).length
      return { ok: n >= 3, summary: `${n} movement packets with |x| > 3.0e7` }
    }
  },
  {
    name: 'cheat/selfhit',
    kind: 'cheat',
    engine: 'raw',
    checks: ['protocol.selfhit'],
    players: ['ch_selfhit'],
    kicked: 'expected',
    description: 'INTERACT_ENTITY attack on the own entity id (vanilla disconnects: invalid_entity_attacked)',
    async run (ctx) {
      const { b } = await laneBot(ctx, 'ch_selfhit', 30)
      ctx.facts.eid = b.entityId
      await everyTick(ctx, b, 30, (n, i) => { if (i % 10 === 0) b.attack(b.entityId) })
      await ctx.sleep(500)
    },
    verify (tr, { facts }) {
      const n = T.inbound(tr.ch_selfhit).filter((e) => e.p === 'INTERACT_ENTITY' && e.eid === facts.eid).length
      return { ok: n >= 1, summary: `${n} attacks on own entity id ${facts.eid}` }
    }
  },
  {
    name: 'cheat/cursor',
    kind: 'cheat',
    engine: 'raw',
    checks: ['protocol.cursor'],
    players: ['ch_cursor'],
    description: 'block placements with cursor x = 1.5 (outside [0, 1]) against the ground in front, 5 times',
    async run (ctx) {
      const { b, lane } = await laneBot(ctx, 'ch_cursor', 31)
      await ctx.give('ch_cursor', 'stone', 64, 0)
      await ctx.probe('slot ch_cursor 0')
      b.input.pitch = 60
      await ctx.ticks(b, 5)
      await everyTick(ctx, b, 60, (n, i) => {
        if (i % 10 === 0) b.place({ x: Math.floor(lane.x) + 2, y: ctx.y0 - 1, z: Math.floor(lane.z) + (i / 10) - 2 }, 'up', { x: 1.5, y: 1.0, z: 0.5 })
      })
    },
    verify (tr) {
      const n = T.inbound(tr.ch_cursor).filter((e) => e.p === 'PLAYER_BLOCK_PLACEMENT' && T.num(e.cx) > 1.49).length
      return { ok: n >= 5, summary: `${n} placements with cursor x 1.5` }
    }
  },
  {
    name: 'cheat/flood',
    kind: 'cheat',
    engine: 'raw',
    checks: ['protocol.flood'],
    players: ['ch_flood'],
    description: '3 movement packets per tick for 3 s while walking (stays under Paper\'s 500/7 s packet limiter)',
    async run (ctx) {
      const { b } = await laneBot(ctx, 'ch_flood', 32)
      b.input.forward = 1
      await ctx.ticks(b, 20)
      b.movementOverride = (r) => {
        const p = { type: 'position', x: r.x, y: r.y, z: r.z, onGround: r.onGround }
        return [r.type ? r : p, p, p]
      }
      await ctx.sleep(3000)
      b.movementOverride = null
      b.input.forward = 0
      await ctx.ticks(b, 10)
    },
    verify (tr) {
      const n = T.ticks(tr.ch_flood).filter((t) => t.packets.filter((p) => T.MOVE.has(p.p)).length >= 3).length
      return { ok: n >= 30, summary: `${n} ticks with >= 3 movement packets` }
    }
  },
  {
    name: 'cheat/abilities',
    kind: 'cheat',
    engine: 'raw',
    checks: ['protocol.abilities'],
    players: ['ch_abilities'],
    description: 'client PLAYER_ABILITIES with flying = true in survival without allowFlight, 5 times',
    async run (ctx) {
      const { b } = await laneBot(ctx, 'ch_abilities', 33)
      await ctx.probe('flight ch_abilities false')
      await everyTick(ctx, b, 100, (n, i) => { if (i % 20 === 0) b.abilities(0x02) })
    },
    verify (tr) {
      const n = T.inbound(tr.ch_abilities).filter((e) => e.p === 'PLAYER_ABILITIES' && e.flying === true).length
      return { ok: n >= 5, summary: `${n} abilities packets with flying=true` }
    }
  },
  {
    name: 'cheat/inventory-closed',
    kind: 'cheat',
    engine: 'raw',
    checks: ['inventory.closed'],
    players: ['ch_invclosed'],
    description: 'CLICK_WINDOW in window id 37 that was never opened, 5 times',
    async run (ctx) {
      const { b } = await laneBot(ctx, 'ch_invclosed', 34)
      await everyTick(ctx, b, 100, (n, i) => { if (i % 20 === 0) b.click(37, 0, 0, 0) })
    },
    verify (tr) {
      const n = T.inbound(tr.ch_invclosed).filter((e) => e.p === 'CLICK_WINDOW' && e.window === 37).length
      return { ok: n >= 5, summary: `${n} clicks in window 37` }
    }
  }
]
