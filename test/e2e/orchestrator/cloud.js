'use strict'
/**
 * Adapter for the PandaEye cloud binary in `all` mode (ARCHITECTURE 5.1, 5.8). Written against the spec while
 * cloud/ is being built; every assumption that is not pinned by the spec is configurable by env and listed in
 * README "Integration contract".
 *
 *   start: PE_GATEWAY_LISTEN=:8080 PE_API_LISTEN=:8081 PE_DB_DSN=<run>/cloud/pandaeye.db PE_NATS_DIR=<run>/cloud/nats
 *          PE_STATIC_LICENSES=<license>:20:pro PE_SERVICE_TOKEN=<48 chars> PE_ASSERTION_KEY=<48 chars>  pandaeye all
 *   ready: GET http://127.0.0.1:8080/healthz == 200
 *   tenant: POST /api/v1/auth/license {license_key} (service token, no assertion) -> {tenant_id}
 *   reads : Authorization: Bearer <service token> + X-PandaEye-Assertion: b64url(json).b64url(hmac_sha256(key, b64url(json)))
 *           json = {tenant, user_id, username, role, method, path, exp}
 *   verdicts: GET /api/v1/verdicts?player=<uuid>&since=<RFC3339>&limit=200 (+cursor)
 *   signals : `pandaeye replay --tenant T --player UUID --since RFC3339 --until RFC3339` (stdout lines; JSON or text)
 */
const fs = require('fs')
const path = require('path')
const crypto = require('crypto')
const { spawn, execFile } = require('child_process')

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
const b64url = (buf) => Buffer.from(buf).toString('base64').replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')

class Cloud {
  constructor ({ bin, runDir, license, gateway = 'http://127.0.0.1:8080', api = 'http://127.0.0.1:8081', log = console.log }) {
    this.bin = bin
    this.dir = path.join(runDir, 'cloud')
    this.license = license
    this.gateway = gateway
    this.apiBase = api
    this.log = log
    this.serviceToken = process.env.PE_E2E_SERVICE_TOKEN || 'e2e-service-token-' + 'x'.repeat(30)
    this.assertionKey = process.env.PE_E2E_ASSERTION_KEY || 'e2e-assertion-key-' + 'y'.repeat(30)
    this.signMode = process.env.PE_E2E_ASSERTION_SIGN || 'payload_b64' // or 'json'
    this.proc = null
    this.tenantId = null
  }

  available () { return !!this.bin && fs.existsSync(this.bin) }

  env () {
    return {
      ...process.env,
      PE_GATEWAY_LISTEN: process.env.PE_GATEWAY_LISTEN || ':8080',
      PE_API_LISTEN: process.env.PE_API_LISTEN || ':8081',
      PE_METRICS_LISTEN: process.env.PE_METRICS_LISTEN || '127.0.0.1:9090',
      PE_DB_DRIVER: 'sqlite',
      PE_DB_DSN: path.join(this.dir, 'pandaeye.db'),
      PE_NATS_DIR: path.join(this.dir, 'nats'),
      PE_LICENSE_MODE: 'static',
      PE_STATIC_LICENSES: process.env.PE_STATIC_LICENSES || `${this.license}:20:pro`,
      PE_SERVICE_TOKEN: this.serviceToken,
      PE_ASSERTION_KEY: this.assertionKey,
      PE_REGION: 'e2e',
      PE_LOG_LEVEL: process.env.PE_LOG_LEVEL || 'info'
    }
  }

  /** Commands that start() would run (for --dry-run). */
  plan () {
    const e = this.env()
    const keys = ['PE_GATEWAY_LISTEN', 'PE_API_LISTEN', 'PE_DB_DRIVER', 'PE_DB_DSN', 'PE_NATS_DIR', 'PE_LICENSE_MODE', 'PE_STATIC_LICENSES', 'PE_SERVICE_TOKEN', 'PE_ASSERTION_KEY']
    return [
      keys.map((k) => `${k}=${k.includes('TOKEN') || k.includes('KEY') ? '<redacted>' : e[k]}`).join(' ') + ` ${this.bin} all`,
      `GET ${this.gateway}/healthz`,
      `POST ${this.apiBase}/api/v1/auth/license {"license_key":"<license>"}  (Bearer <service token>)`,
      `GET ${this.apiBase}/api/v1/verdicts?player=<uuid>&since=<t>  (Bearer <service token> + X-PandaEye-Assertion)`,
      `${this.bin} replay --tenant <tenant> --player <uuid> --since <t0> --until <t1>`
    ]
  }

  async start ({ fresh = true } = {}) {
    if (!this.available()) throw new Error(`cloud binary not found: ${this.bin}`)
    if (fresh) fs.rmSync(this.dir, { recursive: true, force: true })
    fs.mkdirSync(this.dir, { recursive: true })
    const out = fs.openSync(path.join(this.dir, 'cloud.log'), 'a')
    this.proc = spawn(this.bin, ['all'], { env: this.env(), cwd: this.dir, stdio: ['ignore', out, out] })
    this.proc.on('exit', (c, s) => { this.log(`cloud exited code=${c} signal=${s}`); this.proc = null })
    const deadline = Date.now() + 60000
    while (Date.now() < deadline) {
      if (!this.proc) throw new Error('cloud exited during start (see run/cloud/cloud.log)')
      try {
        const r = await fetch(this.gateway + '/healthz')
        if (r.ok) { this.log('cloud ready'); return }
      } catch (_) {}
      await sleep(500)
    }
    throw new Error('cloud did not become healthy in 60 s')
  }

  async stop () {
    if (!this.proc) return
    const p = this.proc
    p.kill('SIGTERM')
    for (let i = 0; i < 24 && this.proc; i++) await sleep(500)
    if (this.proc) p.kill('SIGKILL')
  }

  assertion (method, pathAndQuery, role = 'owner') {
    const json = JSON.stringify({
      tenant: this.tenantId, user_id: 'e2e', username: 'e2e', role, method, path: pathAndQuery.split('?')[0],
      exp: Math.floor(Date.now() / 1000) + 50
    })
    const payload = b64url(json)
    const mac = crypto.createHmac('sha256', this.assertionKey).update(this.signMode === 'json' ? json : payload).digest()
    return `${payload}.${b64url(mac)}`
  }

  async request (method, p, body, { assertion = true } = {}) {
    const headers = { Authorization: `Bearer ${this.serviceToken}`, 'Content-Type': 'application/json' }
    if (assertion) headers['X-PandaEye-Assertion'] = this.assertion(method, p)
    const r = await fetch(this.apiBase + p, { method, headers, body: body ? JSON.stringify(body) : undefined })
    const text = await r.text()
    let data
    try { data = JSON.parse(text) } catch (_) { data = text }
    if (!r.ok) throw new Error(`${method} ${p} -> ${r.status} ${text.slice(0, 300)}`)
    return data
  }

  async ensureTenant () {
    if (this.tenantId) return this.tenantId
    const r = await this.request('POST', '/api/v1/auth/license', { license_key: this.license }, { assertion: false })
    this.tenantId = r.tenant_id
    return this.tenantId
  }

  async verdicts ({ player, since } = {}) {
    await this.ensureTenant()
    const out = []
    let cursor = ''
    for (let page = 0; page < 20; page++) {
      const q = new URLSearchParams({ limit: '200' })
      if (player) q.set('player', player)
      if (since) q.set('since', new Date(since).toISOString())
      if (cursor) q.set('cursor', cursor)
      const r = await this.request('GET', `/api/v1/verdicts?${q}`)
      const items = Array.isArray(r) ? r : (r.items || r.verdicts || r.data || [])
      out.push(...items)
      cursor = r.next_cursor || r.cursor || ''
      if (!cursor || items.length === 0) break
    }
    return out
  }

  async servers () { await this.ensureTenant(); return this.request('GET', '/api/v1/servers') }
  async status () { const r = await fetch(this.gateway + '/status'); return r.json() }

  /** Signals for one player in a window, via `pandaeye replay`. Returns [{check, points, raw}]. */
  replay ({ player, since, until }) {
    return new Promise((resolve) => {
      if (!this.tenantId) return resolve({ error: 'no tenant', signals: [] })
      const args = ['replay', '--tenant', this.tenantId, '--player', player, '--since', new Date(since).toISOString(), '--until', new Date(until).toISOString()]
      execFile(this.bin, args, { env: this.env(), cwd: this.dir, timeout: 120000, maxBuffer: 64 << 20 }, (err, stdout, stderr) => {
        const signals = []
        for (const line of String(stdout).split('\n')) {
          const s = line.trim()
          if (!s) continue
          try {
            const o = JSON.parse(s)
            signals.push({ check: o.check_id || o.check || o.CheckId, points: o.points, raw: o })
          } catch (_) {
            const m = s.match(/\b([a-z]+\.[a-z_.]+)\b/)
            if (m) signals.push({ check: m[1], raw: s })
          }
        }
        resolve({ error: err ? String(err.message || err) + ' ' + String(stderr).slice(0, 300) : null, signals })
      })
    })
  }
}

/** Check ids mentioned by a verdict (checks[] and evidence contributions). */
function verdictChecks (v) {
  const s = new Set()
  for (const c of v.checks || []) s.add(typeof c === 'string' ? c : c.check || c.check_id)
  const ev = typeof v.evidence === 'string' ? safeJson(v.evidence) : v.evidence
  if (ev) {
    for (const sig of ev.signals || ev.top_signals || []) if (sig.check || sig.check_id) s.add(sig.check || sig.check_id)
    for (const k of Object.keys(ev.checks || ev.contributions || {})) s.add(k)
  }
  s.delete(undefined)
  return [...s]
}

function safeJson (s) { try { return JSON.parse(s) } catch (_) { return null } }

module.exports = { Cloud, verdictChecks }
