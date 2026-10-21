'use strict'
const crypto = require('crypto')

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

/** Seeded PRNG (mulberry32) so bot "human noise" is reproducible between the with/without-PandaEye runs. */
function rng (seed) {
  let a = (typeof seed === 'string' ? hash32(seed) : seed) >>> 0
  const next = () => {
    a = (a + 0x6D2B79F5) >>> 0
    let t = a
    t = Math.imul(t ^ (t >>> 15), t | 1)
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61)
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
  next.range = (lo, hi) => lo + (hi - lo) * next()
  next.int = (lo, hi) => Math.floor(lo + (hi - lo + 1) * next())
  next.gauss = (mu = 0, sigma = 1) => {
    const u = Math.max(next(), 1e-12); const v = next()
    return mu + sigma * Math.sqrt(-2 * Math.log(u)) * Math.cos(2 * Math.PI * v)
  }
  next.pick = (arr) => arr[Math.floor(next() * arr.length)]
  return next
}

function hash32 (s) {
  let h = 2166136261
  for (let i = 0; i < s.length; i++) { h ^= s.charCodeAt(i); h = Math.imul(h, 16777619) }
  return h >>> 0
}

/** Offline-mode UUID (what the server assigns with online-mode=false): UUIDv3 of "OfflinePlayer:<name>". */
function offlineUuid (name) {
  const md5 = crypto.createHash('md5').update('OfflinePlayer:' + name, 'utf8').digest()
  md5[6] = (md5[6] & 0x0f) | 0x30
  md5[8] = (md5[8] & 0x3f) | 0x80
  const h = md5.toString('hex')
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`
}

/** Vanilla yaw/pitch (degrees) to look from eye (x,y,z) at point (tx,ty,tz). yaw 0 = +z, 90 = -x. */
function lookAt (x, y, z, tx, ty, tz) {
  const dx = tx - x; const dy = ty - y; const dz = tz - z
  const yaw = Math.atan2(-dx, dz) * 180 / Math.PI
  const pitch = -Math.atan2(dy, Math.hypot(dx, dz)) * 180 / Math.PI
  return { yaw, pitch }
}

/** Look vector for vanilla yaw/pitch in degrees. */
function lookVec (yaw, pitch) {
  const y = yaw * Math.PI / 180; const p = pitch * Math.PI / 180
  return { x: -Math.sin(y) * Math.cos(p), y: -Math.sin(p), z: Math.cos(y) * Math.cos(p) }
}

/** Signed shortest angle difference b - a in degrees, in (-180, 180]. */
function angleDiff (a, b) {
  let d = ((b - a) % 360 + 540) % 360 - 180
  if (d === -180) d = 180
  return d
}

const EYE = 1.62

module.exports = { sleep, rng, hash32, offlineUuid, lookAt, lookVec, angleDiff, EYE }
