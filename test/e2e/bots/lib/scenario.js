'use strict'
/**
 * Scenario framework. A scenario module exports:
 *   name        'legit/vm-sprint' | 'cheat/timer-120' ...
 *   kind        'legit' | 'cheat'
 *   engine      'raw' | 'mineflayer'
 *   checks      check ids the scenario is expected to trigger (cheat) - CHECKS.md ids
 *   players     usernames it uses (<= 16 chars); traced players for verification
 *   suspects    players whose flags/verdicts count (default: all players); targets/dummies are excluded
 *   kicked      'expected' when vanilla/Paper itself is expected to kick the cheater (e.g. NaN position)
 *   description what it does
 *   run(ctx)    performs it
 *   verify(tr, ctx) -> { ok, summary }   (cheats) checks the probe trace shows the intended packet pattern
 */
const path = require('path')
const fs = require('fs')
const { RawBot } = require('./rawbot')
const { createMf, quitMf } = require('./mf')
const { rng, sleep, offlineUuid } = require('./util')

const SCEN_DIR = path.join(__dirname, '..', 'scenarios')

function listScenarios (filter) {
  const out = []
  for (const kind of ['legit', 'cheat']) {
    const d = path.join(SCEN_DIR, kind)
    if (!fs.existsSync(d)) continue
    for (const f of fs.readdirSync(d).sort()) {
      if (!f.endsWith('.js') || f.startsWith('_')) continue
      const mod = require(path.join(d, f))
      const list = Array.isArray(mod) ? mod : [mod]
      for (const m of list) out.push(m)
    }
  }
  if (!filter || filter === 'all') return out
  const parts = filter.split(',').map((s) => s.trim()).filter(Boolean)
  // exact names first; a part that matches no exact name is a prefix (e.g. "cheat/scaffold") or a kind
  return out.filter((m) => parts.some((p) => {
    if (out.some((o) => o.name === p || o.name.split('/')[1] === p)) return m.name === p || m.name.split('/')[1] === p
    return m.kind === p || m.name.startsWith(p)
  }))
}

class Context {
  constructor ({ server, log, seed, quick = false, laneIndex = 0 }) {
    this.server = server
    this.log = log || ((m) => console.log(m))
    this.rng = rng(seed || 'pandaeye')
    this.quick = quick
    this.arena = server.arena()
    this.y0 = this.arena.y0
    this.fx = this.arena.fixtures
    this.laneIndex = laneIndex
    this.bots = []
    this.mfBots = []
    this.facts = {}
    this.notes = []
    this.errors = []
  }

  /** Scale a long duration (> 20 s) for --quick verification runs; short scenarios run unchanged. */
  dur (ms) { return this.quick && ms > 20000 ? Math.round(ms * 0.4) : ms }
  sleep (ms) { return sleep(ms) }
  note (s) { this.notes.push(s); this.log(`  note: ${s}`) }

  lane (i = this.laneIndex) {
    const l = this.fx.lanes
    return { x: l.origin[0], y: l.origin[1], z: l.origin[2] + (i % l.lanes) * l.lane_spacing, length: l.length }
  }

  /** Connect a RawBot, prepare it (survival, healed, empty inventory) and start its tick loop. */
  async raw (username, opts = {}) {
    const b = new RawBot({
      username, port: this.server.port, host: this.server.host, groundY: this.y0, boxes: opts.boxes, log: this.log, brand: opts.brand
    })
    this.bots.push(b)
    await b.connect()
    b.start()
    await b.settleChunks() // let the spawn chunks finish streaming: fewer timing-dependent chunk packets
    if (opts.prepare !== false) await this.prepare(username, opts)
    return b
  }

  async mf (username, opts = {}) {
    const bot = await createMf({ username, port: this.server.port, host: this.server.host, log: this.log, version: opts.version })
    this.mfBots.push(bot)
    await this.settle(bot)
    if (opts.prepare !== false) await this.prepare(username, opts)
    return bot
  }

  async prepare (username, { gamemode = 'survival', clear = true } = {}) {
    await this.server.probe(`gm ${username} ${gamemode}`)
    await this.server.probe(`heal ${username}`)
    await this.server.probe(`cleareffects ${username}`)
    if (clear) await this.server.probe(`clearinv ${username}`)
  }

  /** Teleport through the probe (teleportAsync) and wait until a RawBot confirmed it. */
  async tp (who, x, y, z, yaw = 0, pitch = 0) {
    const name = typeof who === 'string' ? who : (who.username)
    const before = who.teleports
    await this.server.probe(`tp ${name} ${x} ${y} ${z} ${yaw} ${pitch}`)
    if (who instanceof RawBot) {
      for (let i = 0; i < 100 && who.teleports === before; i++) await sleep(20)
      await sleep(120)
      await who.settleChunks()
    } else {
      await sleep(400)
      const mfb = this.mfBots.find((b) => b.username === name)
      if (mfb) await this.settle(mfb)
    }
  }

  /** mineflayer: wait until chunk batches stopped arriving (700 ms quiet, max 6 s). */
  async settle (bot, quietMs = 700, maxMs = 6000) {
    const end = Date.now() + maxMs
    await sleep(150)
    while (Date.now() < end && Date.now() - (bot.lastChunkBatchAt || 0) < quietMs) await sleep(100)
  }

  give (name, material, n = 1, slot) { return this.server.probe(`give ${name} ${material} ${n}${slot !== undefined ? ' ' + slot : ''}`) }
  probe (args, t) { return this.server.probe(args, t) }

  /** Ticks helper for raw bots: resolves after n ticks of bot. */
  ticks (bot, n) {
    return new Promise((resolve) => {
      if (bot.ended) return resolve()
      let i = 0
      const done = () => { bot.removeListener('postTick', f); bot.removeListener('end', done); resolve() }
      const f = () => { if (++i >= n) done() }
      bot.on('postTick', f)
      bot.once('end', done)
    })
  }

  async cleanup () {
    for (const b of this.bots) await b.quit()
    for (const b of this.mfBots) await quitMf(b)
    this.bots = []
    this.mfBots = []
    await sleep(500)
  }
}

/**
 * Run one scenario against a server. When trace=true every player is traced by the probe and the trace is passed
 * to scenario.verify(). Returns a result record (never throws for scenario failures).
 */
async function runScenario (mod, { server, log, seed, quick, trace = false, laneIndex = 0, dumpName } = {}) {
  const ctx = new Context({ server, log, seed: seed || mod.name, quick, laneIndex })
  const res = {
    name: mod.name, kind: mod.kind, engine: mod.engine, checks: mod.checks || [], players: mod.players || [],
    suspects: mod.suspects || mod.players || [], uuids: {}, startMs: Date.now(), ok: true, error: null, verify: null,
    kicked: [], expectedKick: mod.kicked === 'expected'
  }
  for (const p of res.players) res.uuids[p] = offlineUuid(p)
  const traceSecs = Math.ceil((mod.traceSeconds || 240))
  try {
    if (trace) for (const p of res.players) await server.probe(`trace ${p} ${traceSecs}`)
    const limit = mod.timeoutMs || (ctx.quick ? 150000 : 240000)
    let timer
    await Promise.race([
      mod.run(ctx),
      new Promise((resolve, reject) => { timer = setTimeout(() => reject(new Error(`scenario timeout after ${limit} ms`)), limit) })
    ]).finally(() => clearTimeout(timer))
  } catch (e) {
    res.ok = false
    res.error = String(e && e.stack ? e.stack : e)
    log && log(`  scenario ${mod.name} error: ${e && e.message}`)
  }
  for (const b of ctx.bots) if (b.kicked) res.kicked.push({ name: b.username, reason: b.kicked })
  for (const b of ctx.mfBots) if (b.kickedReason) res.kicked.push({ name: b.username, reason: b.kickedReason })
  res.endMs = Date.now()
  res.facts = ctx.facts
  res.notes = ctx.notes
  if (ctx.facts.probePinger) { try { await server.probe('pinger off') } catch (_) {} }
  res.botStats = ctx.bots.map((b) => ({ name: b.username, sent: b.sent, ticks: b.tick, teleports: b.teleports, velocities: b.velocityLog.length }))
  await ctx.cleanup()
  if (trace || dumpName) {
    try {
      const d = await server.dump(dumpName || ('trace-' + mod.name.replace('/', '-')))
      res.dumpFile = dumpName
      if (trace && typeof mod.verify === 'function') {
        const tr = {}
        for (const p of res.players) tr[p] = (d.traces && d.traces[p.toLowerCase()] && d.traces[p.toLowerCase()].entries) || []
        try {
          res.verify = mod.verify(tr, { facts: ctx.facts, dump: d, arena: ctx.arena })
        } catch (e) {
          res.verify = { ok: false, summary: 'verify threw: ' + e.message }
        }
      }
      res.serverKicks = (d.kicks || []).filter((k) => res.players.map((p) => p.toLowerCase()).includes(String(k.name).toLowerCase()))
      res.joins = (d.joins || []).filter((j) => res.players.map((p) => p.toLowerCase()).includes(String(j.name).toLowerCase()))
    } catch (e) {
      res.verify = { ok: false, summary: 'dump failed: ' + e.message }
    }
    await server.probe('trace off')
  }
  return res
}

module.exports = { Context, runScenario, listScenarios }
