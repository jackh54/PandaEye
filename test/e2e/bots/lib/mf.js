'use strict'
/**
 * mineflayer helpers for the legit scenarios.
 *
 * mineflayer quirks (documented in README "Known limitations"):
 *  - it never sends CLIENT_TICK_END: we inject one per physics tick (written at the start of the next physics tick,
 *    i.e. after that tick's movement packet, preserving the vanilla order "movement, tick_end");
 *  - it sends a position only when the position changed exactly (vanilla: > 2.0E-4) and a reminder every 1000 ms
 *    (vanilla: every 20 ticks), and rate-limits rotation changes (yawSpeed);
 *  - its physics (prismarine-physics) are close to vanilla but not identical (stairs/slab stepping, water, edges),
 *    so for exact movement assertions use the vanilla-mover scenarios (legit/vm-*).
 */
const mineflayer = require('mineflayer')
const { Vec3 } = require('vec3')
const { sleep } = require('./util')

async function createMf ({ username, host = '127.0.0.1', port, log = () => {}, timeoutMs = 45000, version = '1.21.11' }) {
  const bot = mineflayer.createBot({
    host, port, username, version, auth: 'offline', hideErrors: true, viewDistance: 4, checkTimeoutInterval: 60000
  })
  // CLIENT_TICK_END exists since 1.21.2; older (ViaVersion-translated) clients do not send it
  const tickEnd = bot.supportFeature ? !!require('minecraft-data')(version).protocol?.play?.toServer?.types?.packet_tick_end : false
  let first = true
  bot.on('physicsTick', () => {
    if (tickEnd && !first && bot._client.state === 'play') bot._client.write('tick_end', {})
    first = false
  })
  bot.lastChunkBatchAt = 0
  bot._client.on('chunk_batch_finished', () => { bot.lastChunkBatchAt = Date.now() })
  bot.on('kicked', (r) => { bot.kickedReason = typeof r === 'string' ? r : JSON.stringify(r); log(`${username} kicked: ${bot.kickedReason}`) })
  bot.on('error', (e) => log(`${username} error: ${e.message}`))
  await new Promise((resolve, reject) => {
    const to = setTimeout(() => reject(new Error(`${username}: spawn timeout`)), timeoutMs)
    bot.once('spawn', () => { clearTimeout(to); resolve() })
    bot.once('end', (r) => { clearTimeout(to); reject(new Error(`${username}: ended before spawn: ${r}`)) })
  })
  try { await Promise.race([bot.waitForChunksToLoad(), sleep(15000)]) } catch (_) {}
  return bot
}

function ticks (bot, n) {
  return new Promise((resolve) => {
    let i = 0
    const f = () => { if (++i >= n) { bot.removeListener('physicsTick', f); resolve() } }
    bot.on('physicsTick', f)
  })
}

/** Wait until the bot's position is within tol of (x,y,z) after a server teleport. */
async function waitAt (bot, x, y, z, tol = 1.5, timeoutMs = 10000) {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    const p = bot.entity?.position
    if (p && Math.hypot(p.x - x, p.z - z) < tol && Math.abs(p.y - y) < tol + 1) return true
    await sleep(100)
  }
  return false
}

/** Walk toward (x, z) with small human-like heading noise; stops within tol or at timeout. */
async function walkTo (bot, x, z, { sprint = false, jump = false, tol = 0.6, timeoutMs = 20000, rng = Math.random, noiseDeg = 2 } = {}) {
  const deadline = Date.now() + timeoutMs
  bot.setControlState('forward', true)
  bot.setControlState('sprint', sprint)
  bot.setControlState('jump', jump)
  try {
    while (Date.now() < deadline) {
      const p = bot.entity.position
      const dx = x - p.x; const dz = z - p.z
      if (Math.hypot(dx, dz) < tol) return true
      const noise = (typeof rng.gauss === 'function' ? rng.gauss(0, noiseDeg) : 0) * Math.PI / 180
      // mineflayer yaw: atan2(-dx, -dz)
      bot.look(Math.atan2(-dx, -dz) + noise, (typeof rng.gauss === 'function' ? rng.gauss(0, 1.5) : 0) * Math.PI / 180, false)
      await ticks(bot, 2)
    }
    return false
  } finally {
    bot.clearControlStates()
  }
}

/** Smoothly aim at an entity's upper body with noise; returns a stop function. */
function aimLoop (bot, getTarget, rng, { maxStepDeg = 25, noiseDeg = 1.2 } = {}) {
  const f = () => {
    const t = getTarget()
    if (!t || !t.position) return
    const eye = bot.entity.position.offset(0, bot.entity.eyeHeight ?? 1.62, 0)
    const aim = t.position.offset(rng.gauss(0, 0.12), 1.2 + rng.gauss(0, 0.15), rng.gauss(0, 0.12))
    const d = aim.minus(eye)
    const yaw = Math.atan2(-d.x, -d.z)
    const pitch = Math.atan2(d.y, Math.hypot(d.x, d.z))
    let dy = yaw - bot.entity.yaw
    while (dy > Math.PI) dy -= 2 * Math.PI
    while (dy < -Math.PI) dy += 2 * Math.PI
    const max = maxStepDeg * Math.PI / 180
    const stepYaw = Math.max(-max, Math.min(max, dy * (0.55 + rng.range(-0.15, 0.15)))) + rng.gauss(0, noiseDeg) * Math.PI / 180 * 0.3
    const stepPitch = (pitch - bot.entity.pitch) * 0.5 + rng.gauss(0, noiseDeg) * Math.PI / 180 * 0.2
    bot.look(bot.entity.yaw + stepYaw, bot.entity.pitch + stepPitch, true)
  }
  bot.on('physicsTick', f)
  return () => bot.removeListener('physicsTick', f)
}

/** Angle in degrees between the bot's look vector and the direction to a point. */
function angleTo (bot, point) {
  const eye = bot.entity.position.offset(0, 1.62, 0)
  const d = point.minus(eye)
  const len = Math.hypot(d.x, d.y, d.z) || 1
  const yaw = bot.entity.yaw; const pitch = bot.entity.pitch
  const lx = -Math.sin(yaw) * Math.cos(pitch); const ly = Math.sin(pitch); const lz = -Math.cos(yaw) * Math.cos(pitch)
  return Math.acos(Math.max(-1, Math.min(1, (lx * d.x + ly * d.y + lz * d.z) / len))) * 180 / Math.PI
}

async function quitMf (bot) {
  if (!bot) return
  try { bot.quit('bye') } catch (_) {}
  await sleep(300)
}

module.exports = { createMf, ticks, waitAt, walkTo, aimLoop, angleTo, quitMf, Vec3 }
