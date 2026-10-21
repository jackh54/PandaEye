'use strict'
/**
 * Readers for what the PandaEye plugin leaves on disk / prints (ARCHITECTURE 4.5, 4.9, 4.10):
 *   plugins/PandaEye/logs/flags-YYYY-MM-DD.jsonl  (aggregated per player+check per 5 s)
 *   plugins/PandaEye/logs/peaks-YYYY-MM-DD.jsonl  (per player per check per 5 min)
 *   `pandaeye status` console output (packet-handler cost avg/p99 us/packet, cloud state, drops)
 * The line schema is not fixed by the spec, so field names are matched leniently (see FIELD_* below).
 */
const fs = require('fs')
const path = require('path')
const { execFileSync } = require('child_process')

const FIELD_PLAYER = ['uuid', 'player_uuid', 'player', 'playerUuid', 'name', 'player_name', 'playerName']
const FIELD_CHECK = ['check', 'check_id', 'checkId', 'id']
const FIELD_TIME = ['last', 'last_ms', 'time', 'time_ms', 'ts', 't', 'first', 'first_ms', 'timestamp']

function pick (o, keys) { for (const k of keys) if (o[k] !== undefined && o[k] !== null) return o[k]; return undefined }

function toMs (v) {
  if (v === undefined) return undefined
  if (typeof v === 'number') return v > 1e14 ? v / 1000 : (v > 1e11 ? v : v * 1000) // us / ms / s
  const t = Date.parse(v)
  return Number.isNaN(t) ? undefined : t
}

function readJsonl (dir, prefix) {
  const out = []
  if (!fs.existsSync(dir)) return out
  for (const f of fs.readdirSync(dir).sort()) {
    if (!f.startsWith(prefix) || !f.endsWith('.jsonl')) continue
    for (const line of fs.readFileSync(path.join(dir, f), 'utf8').split('\n')) {
      if (!line.trim()) continue
      try { out.push(JSON.parse(line)) } catch (_) { out.push({ _unparsed: line }) }
    }
  }
  return out
}

/** Flags of the given players (names or uuids) in [fromMs, toMs]. Lines without a time are included. */
function flagsFor (serverDir, players, fromMs = 0, toMs_ = Infinity) {
  const dir = path.join(serverDir, 'plugins', 'PandaEye', 'logs')
  const ids = new Set(players.map((p) => String(p).toLowerCase()))
  return readJsonl(dir, 'flags-').filter((o) => {
    const who = String(pick(o, FIELD_PLAYER) ?? '').toLowerCase()
    if (!ids.has(who)) return false
    const t = toMs(pick(o, FIELD_TIME))
    return t === undefined || (t >= fromMs && t <= toMs_)
  }).map((o) => ({ check: pick(o, FIELD_CHECK), confirmed: o.confirmed !== false, count: o.count ?? 1, raw: o }))
}

function peaks (serverDir) { return readJsonl(path.join(serverDir, 'plugins', 'PandaEye', 'logs'), 'peaks-') }

/** Parse `pandaeye status` output (lines after the command). */
function parseStatus (text) {
  const o = { raw: text.split('\n').filter((l) => /PandaEye|pandaeye|mode|cloud|packet|us\/|drop|pending|rtt|player/i.test(l)).slice(0, 60).join('\n') }
  const us = text.match(/avg[^0-9]*([0-9.]+)\s*(?:us|µs)[^0-9]*p99[^0-9]*([0-9.]+)\s*(?:us|µs)/i) || text.match(/([0-9.]+)\s*(?:us|µs)\/packet[^0-9]*([0-9.]+)?/i)
  if (us) { o.handlerAvgUs = Number(us[1]); if (us[2]) o.handlerP99Us = Number(us[2]) }
  const mode = text.match(/mode[:= ]+(shadow|live)/i)
  if (mode) o.mode = mode[1].toLowerCase()
  // real plugin: "cloud: CONNECTED - detail (server id ..)" on its own line; the mode line also contains "cloud enforcement"
  const cloud = text.match(/(?:^|\]:?\s|\n)cloud:\s*([A-Za-z_]+)/i) || text.match(/\bcloud:\s*([A-Za-z_]+)/i)
  if (cloud) o.cloud = cloud[1]
  const enf = text.match(/enforcement[:= ]+(true|false|on|off)/i)
  if (enf) o.enforcement = enf[1]
  const att = text.match(/enforcer calls attempted in shadow:\s*([^;\n]*)/i) || text.match(/enforcer attempts\s*([0-9]+)/i)
  if (att) o.enforcerAttempts = att[1].trim()
  const pk = text.match(/packet handler:\s*([0-9]+) packets/i)
  if (pk) o.handlerPackets = Number(pk[1])
  return o
}

/** PandaEye-* threads in a JVM via jcmd Thread.print (null if jcmd fails). */
function pandaeyeThreads (pid) {
  try {
    const out = execFileSync('jcmd', [String(pid), 'Thread.print'], { encoding: 'utf8', timeout: 30000, stdio: ['ignore', 'pipe', 'ignore'] })
    const names = [...out.matchAll(/^"([^"]+)"/gm)].map((m) => m[1])
    return { total: names.length, pandaeye: names.filter((n) => n.startsWith('PandaEye')) }
  } catch (e) {
    return null
  }
}

module.exports = { flagsFor, peaks, parseStatus, pandaeyeThreads, readJsonl }
