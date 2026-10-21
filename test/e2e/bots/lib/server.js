'use strict'
/**
 * Node handle for a server started by test/e2e/servers/start.sh: console commands through the FIFO,
 * waiting on console output, probe-plugin commands (which log "[probe] OK <cmd>" / "[probe] ERR <cmd> :: why").
 */
const fs = require('fs')
const path = require('path')
const { sleep } = require('./util')

const E2E_DIR = path.resolve(__dirname, '..', '..')
const RUN_DIR = process.env.PE_E2E_RUN_DIR || path.join(E2E_DIR, 'run')

function escapeRe (s) { return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') }

class Server {
  constructor ({ name, host = '127.0.0.1', port, flavor, dir } = {}) {
    this.name = name
    this.dir = dir || path.join(RUN_DIR, 'servers', name)
    const env = readEnv(path.join(this.dir, 'e2e-server.env'))
    this.host = host
    this.port = port || Number(env.PORT)
    this.flavor = flavor || env.FLAVOR
    this.profile = env.PROFILE
    this.consoleOut = path.join(this.dir, 'logs', 'console.out')
    this.fifo = path.join(this.dir, 'console.in')
    this._arena = null
  }

  pid () {
    try { return Number(fs.readFileSync(path.join(this.dir, 'server.pid'), 'utf8').trim()) } catch (_) { return null }
  }

  running () {
    const p = this.pid()
    if (!p) return false
    try { process.kill(p, 0); return true } catch (_) { return false }
  }

  offset () {
    try { return fs.statSync(this.consoleOut).size } catch (_) { return 0 }
  }

  /** Console output since byte offset. */
  readFrom (from = 0) {
    let fd
    try {
      fd = fs.openSync(this.consoleOut, 'r')
      const size = fs.fstatSync(fd).size
      if (size <= from) return ''
      const buf = Buffer.alloc(size - from)
      fs.readSync(fd, buf, 0, buf.length, from)
      return buf.toString('utf8')
    } catch (_) {
      return ''
    } finally {
      if (fd !== undefined) fs.closeSync(fd)
    }
  }

  cmd (line) {
    if (!this.running()) throw new Error(`server ${this.name} is not running`)
    fs.appendFileSync(this.fifo, line.replace(/\n/g, ' ') + '\n')
  }

  async waitFor (re, { from = 0, timeout = 30000 } = {}) {
    const rx = typeof re === 'string' ? new RegExp(re) : re
    const deadline = Date.now() + timeout
    while (Date.now() < deadline) {
      const text = this.readFrom(from)
      const m = text.match(rx)
      if (m) return m
      if (!this.running()) throw new Error(`server ${this.name} died while waiting for ${rx}`)
      await sleep(200)
    }
    throw new Error(`timeout (${timeout} ms) waiting for ${rx} on ${this.name}`)
  }

  /** Send a command and wait for a regex after the send point. */
  async cmdWait (line, re, timeout = 30000) {
    const from = this.offset()
    this.cmd(line)
    return this.waitFor(re, { from, timeout })
  }

  /** Run a probe command; resolves when "[probe] OK <args>" is logged, rejects on "[probe] ERR <args>". */
  async probe (args, timeout = 30000) {
    const from = this.offset()
    this.cmd('probe ' + args)
    const m = await this.waitFor(new RegExp(`\\[probe\\] (OK|ERR) ${escapeRe(args)}(?: :: (.*))?`), { from, timeout })
    if (m[1] === 'ERR') throw new Error(`probe ${args} failed: ${m[2] || ''}`)
    return m
  }

  async dump (name) {
    const rel = `probe/${name}.json`
    await this.probe(`dump ${rel}`)
    return readJsonLenient(path.join(this.dir, rel))
  }

  arena () {
    if (!this._arena) this._arena = JSON.parse(fs.readFileSync(path.join(this.dir, 'arena.json'), 'utf8'))
    return this._arena
  }

  async buildArena () {
    const from = this.offset()
    this.cmd('probe arena')
    await this.waitFor(/\[probe\] ARENA READY/, { from, timeout: 120000 })
    this._arena = null
    return this.arena()
  }

  async waitDone (timeout = 180000) { return this.waitFor(/Done \(/, { from: 0, timeout }) }
}

function readEnv (f) {
  const o = {}
  try {
    for (const line of fs.readFileSync(f, 'utf8').split('\n')) {
      const i = line.indexOf('=')
      if (i > 0) o[line.slice(0, i)] = line.slice(i + 1)
    }
  } catch (_) {}
  return o
}

/** JSON.parse that tolerates NaN/Infinity literals (Gson writes them with serializeSpecialFloatingPointValues). */
function readJsonLenient (f) {
  const s = fs.readFileSync(f, 'utf8')
  try { return JSON.parse(s) } catch (_) {
    return JSON.parse(s.replace(/:\s*(-?Infinity|NaN)\b/g, ': "$1"'))
  }
}

module.exports = { Server, RUN_DIR, E2E_DIR, readJsonLenient }
