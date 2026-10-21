'use strict'
/**
 * RawBot: a raw minecraft-protocol client that behaves like a vanilla 1.21.11 client at the packet level
 * (configuration, brand, chunk batch acks, player_loaded, teleport confirm + PosRot, keep-alive, ping/pong,
 * respawn, CLIENT_TICK_END every tick) and moves with the exact VanillaMover. Cheat scenarios bend exactly one
 * of those behaviours (tick rate, movement values, packet order, replies...).
 *
 * Per tick (default 50 ms, absolute schedule; `tickMs` changes it for timer cheats) the order is:
 *   emit('tick', n)  -> scenario actions (attack/swing/place/dig/use/click are written immediately)
 *   player_input (on change) -> entity_action sprint (on change) -> movement packet -> tick_end
 */
const EventEmitter = require('events')
const mc = require('minecraft-protocol')
const { VanillaMover } = require('./vanilla-mover')
const { sleep, offlineUuid, EYE } = require('./util')

const FACE = { down: 0, up: 1, north: 2, south: 3, west: 4, east: 5 }
const DIG = { start: 0, cancel: 1, finish: 2, drop_all: 3, drop: 4, release_use_item: 5, swap: 6 }

class RawBot extends EventEmitter {
  /**
   * @param {object} o
   * @param {string} o.username
   * @param {string} [o.host] @param {number} o.port
   * @param {number} o.groundY ground surface (arena y0)
   * @param {Array} [o.boxes] platforms for the mover
   * @param {(msg:string)=>void} [o.log]
   */
  constructor (o) {
    super()
    this.o = o
    this.username = o.username
    this.uuid = offlineUuid(o.username)
    this.log = o.log || (() => {})
    this.groundY = o.groundY ?? -60
    this.boxes = o.boxes || []
    this.tickMs = 50
    this.tick = 0
    this.input = { forward: 0, strafe: 0, jump: false, sprint: false, sneak: false, yaw: 0, pitch: 0 }
    this.manualMovement = false // scenario sends movement itself (sendMove)
    this.sendTickEnd = true
    this.ignoreVelocity = false
    this.keepAliveDelayMs = 0
    this.pongDelayMs = 0
    this.movementOverride = null // (r, bot) => r | null
    this.entityId = null
    this.gameMode = 0
    this.health = 20
    this.dead = false
    this.spawned = false
    this.kicked = null
    this.ended = false
    this.seq = 0
    this.stateId = 0
    this.openWindowId = 0
    this.sent = { total: 0 }
    this._held = null
    this._lastInputs = null
    this._sentSprint = false
    this._timer = null
    this.mover = null
    this.velocityLog = []
    this.teleports = 0
    this.lastChunkBatchAt = 0
  }

  /** Resolve once no chunk batch arrived for quietMs (chunk streaming after join/teleport done), max maxMs. */
  async settleChunks (quietMs = 700, maxMs = 6000) {
    const end = Date.now() + maxMs
    await sleep(150)
    while (Date.now() < end && Date.now() - this.lastChunkBatchAt < quietMs) await sleep(100)
  }

  get pos () { return this.mover ? { x: this.mover.x, y: this.mover.y, z: this.mover.z } : null }
  get eye () { const p = this.pos; return p && { x: p.x, y: p.y + EYE, z: p.z } }

  /** Connect and resolve once spawned (first teleport confirmed). */
  connect (timeoutMs = 30000) {
    return new Promise((resolve, reject) => {
      const client = mc.createClient({
        host: this.o.host || '127.0.0.1',
        port: this.o.port,
        username: this.username,
        version: this.o.version || '1.21.11',
        auth: 'offline',
        keepAlive: false, // handled here (delays for latency cheats)
        hideErrors: true,
        clientSettings: { viewDistance: this.o.viewDistance ?? 4 }
      })
      this.client = client
      const to = setTimeout(() => reject(new Error(`${this.username}: spawn timeout`)), timeoutMs)
      client.on('state', (s) => {
        if (s === 'configuration') {
          // vanilla sends its brand during configuration
          const brand = Buffer.from(this.o.brand || 'vanilla', 'utf8')
          const data = Buffer.concat([Buffer.from([brand.length]), brand])
          try { client.write('custom_payload', { channel: 'minecraft:brand', data }) } catch (e) { this.log(`brand: ${e.message}`) }
        }
      })
      client.on('login', (p) => {
        this.entityId = p.entityId
        this.gameMode = p.worldState?.gamemode ?? 0
      })
      client.on('position', (p) => this._onTeleport(p, () => {
        if (!this.spawned) {
          this.spawned = true
          clearTimeout(to)
          this._write('player_loaded', {})
          this.emit('spawn')
          resolve(this)
        }
      }))
      client.on('keep_alive', (p) => {
        const reply = () => this._write('keep_alive', { keepAliveId: p.keepAliveId })
        if (this.keepAliveDelayMs > 0) setTimeout(reply, this.keepAliveDelayMs)
        else reply()
        this.emit('keepAlive', p.keepAliveId)
      })
      client.on('ping', (p) => {
        const reply = () => this._write('pong', { id: p.id })
        if (this.pongDelayMs > 0) setTimeout(reply, this.pongDelayMs)
        else reply()
      })
      client.on('chunk_batch_finished', () => { this.lastChunkBatchAt = Date.now(); this._write('chunk_batch_received', { chunksPerTick: 9.0 }) })
      client.on('entity_velocity', (p) => {
        if (p.entityId !== this.entityId) return
        const v = p.velocity
        this.velocityLog.push({ tick: this.tick, t: Date.now(), ...v })
        this.emit('velocity', v)
        if (!this.ignoreVelocity && this.mover) this.mover.setVelocity(v.x, v.y, v.z)
      })
      client.on('update_health', (p) => {
        this.health = p.health
        if (p.health <= 0 && !this.dead) {
          this.dead = true
          setTimeout(() => this._write('client_command', { actionId: 'perform_respawn' }), 100)
        }
      })
      client.on('respawn', () => { this.dead = false })
      client.on('game_state_change', (p) => { if (p.reason === 'change_game_mode') this.gameMode = p.gameMode })
      client.on('open_window', (p) => { this.openWindowId = p.windowId; this.emit('openWindow', p) })
      client.on('close_window', () => { this.openWindowId = 0 })
      client.on('window_items', (p) => { this.stateId = p.stateId })
      client.on('set_slot', (p) => { this.stateId = p.stateId })
      client.on('held_item_slot', (p) => this._write('held_item_slot', { slotId: p.slot }))
      client.on('kick_disconnect', (p) => { this.kicked = reasonText(p.reason); this.log(`${this.username} kicked: ${this.kicked}`) })
      client.on('disconnect', (p) => { this.kicked = reasonText(p.reason) })
      client.on('error', (e) => { this.log(`${this.username} error: ${e.message}`); this.emit('botError', e) })
      client.on('end', (r) => {
        this.ended = true
        this.stop()
        clearTimeout(to)
        this.emit('end', this.kicked || r)
        if (!this.spawned) reject(new Error(`${this.username}: disconnected before spawn: ${this.kicked || r}`))
      })
    })
  }

  _onTeleport (p, after) {
    const f = p.flags || {}
    const rel = (k) => (typeof f === 'object' ? !!f[k] : false)
    const cur = this.mover || { x: 0, y: 0, z: 0, yaw: 0, pitch: 0, vx: 0, vy: 0, vz: 0 }
    const x = rel('x') ? cur.x + p.x : p.x
    const y = rel('y') ? cur.y + p.y : p.y
    const z = rel('z') ? cur.z + p.z : p.z
    const yaw = rel('yaw') ? cur.yaw + p.yaw : p.yaw
    const pitch = rel('pitch') ? cur.pitch + p.pitch : p.pitch
    if (!this.mover) {
      this.mover = new VanillaMover({ x, y, z, yaw, pitch, groundY: this.groundY, boxes: this.boxes })
    }
    this.mover.teleport(x, y, z, yaw, pitch)
    this.mover.setVelocity(rel('dx') ? cur.vx + p.dx : p.dx, rel('dy') ? cur.vy + p.dy : p.dy, rel('dz') ? cur.vz + p.dz : p.dz)
    this.input.yaw = this.mover.yaw
    this.input.pitch = this.mover.pitch
    this.teleports++
    this._write('teleport_confirm', { teleportId: p.teleportId })
    this._write('position_look', { x, y, z, yaw: this.mover.yaw, pitch: this.mover.pitch, flags: { onGround: false, hasHorizontalCollision: false } })
    this.emit('teleport', { x, y, z, yaw, pitch, id: p.teleportId })
    after && after()
  }

  // ------------------------------------------------------------------------------------------------
  // tick loop
  // ------------------------------------------------------------------------------------------------

  start () {
    if (this._timer || this.ended) return
    this._t0 = performance.now()
    this._n = 0
    this._schedule()
  }

  stop () {
    if (this._timer) clearTimeout(this._timer)
    this._timer = null
  }

  setTickMs (ms) {
    this.tickMs = ms
    this._t0 = performance.now()
    this._n = 0
  }

  _schedule () {
    this._n++
    const target = this._t0 + this._n * this.tickMs
    this._timer = setTimeout(() => this._tick(), Math.max(0, target - performance.now()))
  }

  _tick () {
    this._timer = null
    if (this.ended) return
    const n = ++this.tick
    try {
      this.emit('tick', n)
      if (!this.manualMovement && this.mover && !this.dead) this._movement()
      if (this.sendTickEnd) this._write('tick_end', {})
      this.emit('postTick', n)
    } catch (e) {
      this.log(`${this.username} tick error: ${e.stack || e}`)
    }
    if (!this.ended) this._schedule()
  }

  _movement () {
    const inp = this.input
    const keys = {
      forward: (inp.forward || 0) > 0,
      backward: (inp.forward || 0) < 0,
      left: (inp.strafe || 0) > 0,
      right: (inp.strafe || 0) < 0,
      jump: !!inp.jump,
      shift: !!inp.sneak,
      sprint: !!inp.sprint
    }
    const k = JSON.stringify(keys)
    if (k !== this._lastInputs) { this._write('player_input', { inputs: keys }); this._lastInputs = k }
    const sprinting = !!inp.sprint && (inp.forward || 0) > 0
    if (sprinting !== this._sentSprint) {
      this._write('entity_action', { entityId: this.entityId, actionId: sprinting ? 'start_sprinting' : 'stop_sprinting', jumpBoost: 0 })
      this._sentSprint = sprinting
    }
    let r = this.mover.step(inp)
    if (this.movementOverride) r = this.movementOverride(r, this)
    if (!r) return
    const list = Array.isArray(r) ? r : [r]
    for (const m of list) this.sendMove(m)
  }

  /** Write one movement packet. m: {type?, x,y,z,yaw,pitch,onGround,horizontalCollision} (type inferred if absent). */
  sendMove (m) {
    const flags = { onGround: !!m.onGround, hasHorizontalCollision: !!m.horizontalCollision }
    let type = m.type
    if (type === undefined) type = m.x !== undefined ? (m.yaw !== undefined ? 'position_look' : 'position') : (m.yaw !== undefined ? 'look' : 'flying')
    if (type === null) return
    if (type === 'position_look') this._write('position_look', { x: m.x, y: m.y, z: m.z, yaw: m.yaw, pitch: m.pitch, flags })
    else if (type === 'position') this._write('position', { x: m.x, y: m.y, z: m.z, flags })
    else if (type === 'look') this._write('look', { yaw: m.yaw, pitch: m.pitch, flags })
    else this._write('flying', { flags })
  }

  // ------------------------------------------------------------------------------------------------
  // actions
  // ------------------------------------------------------------------------------------------------

  swing (hand = 0) { this._write('arm_animation', { hand }) }
  /** INTERACT_ENTITY attack (vanilla order: attack packet, then swing). */
  attack (entityId, { swing = true, sneaking = false } = {}) {
    this._write('use_entity', { target: entityId, mouse: 1, sneaking })
    if (swing) this.swing(0)
  }

  /** Block placement against block b={x,y,z} on face (name or id) with cursor {x,y,z} in 0..1. */
  place (b, face, cursor = { x: 0.5, y: 0.5, z: 0.5 }, { hand = 0, swing = true } = {}) {
    this._write('block_place', {
      hand, location: b, direction: typeof face === 'string' ? FACE[face] : face,
      cursorX: cursor.x, cursorY: cursor.y, cursorZ: cursor.z, insideBlock: false, worldBorderHit: false, sequence: ++this.seq
    })
    if (swing) this.swing(hand)
  }

  dig (status, b, face = 'up') {
    this._write('block_dig', { status: typeof status === 'string' ? DIG[status] : status, location: b, face: typeof face === 'string' ? FACE[face] : face, sequence: ++this.seq })
  }

  useItem (hand = 0) {
    this._write('use_item', { hand, sequence: ++this.seq, rotation: { x: this.mover ? this.mover.yaw : 0, y: this.mover ? this.mover.pitch : 0 } })
  }

  releaseUseItem () { this.dig('release_use_item', { x: 0, y: 0, z: 0 }, 'down') }

  heldSlot (slot) { this._write('held_item_slot', { slotId: slot }) }

  click (windowId, slot, button = 0, mode = 0) {
    this._write('window_click', { windowId, stateId: this.stateId, slot, mouseButton: button, mode, changedSlots: [], cursorItem: undefined })
  }

  closeWindow (windowId) { this._write('close_window', { windowId }) }

  abilities (flags) { this._write('abilities', { flags }) }

  /** Hold every outgoing packet (blink) until release(). */
  hold () { if (!this._held) this._held = [] }
  release () {
    const q = this._held
    this._held = null
    if (q) for (const [n, p] of q) this._write(n, p)
    return q ? q.length : 0
  }

  _write (name, params) {
    if (this.ended || !this.client) return
    if (this._held) { this._held.push([name, params]); return }
    try {
      this.client.write(name, params)
      this.sent.total++
      this.sent[name] = (this.sent[name] || 0) + 1
    } catch (e) {
      this.log(`${this.username} write ${name} failed: ${e.message}`)
    }
  }

  async quit (reason = 'bye') {
    this.stop()
    if (!this.ended && this.client) {
      try { this.client.end(reason) } catch (_) {}
      for (let i = 0; i < 20 && !this.ended; i++) await sleep(50)
    }
  }
}

function reasonText (r) {
  if (r == null) return ''
  if (typeof r === 'string') return r
  try {
    if (r.type === 'string') return r.value
    if (r.value?.translate) return r.value.translate.value || JSON.stringify(r)
    if (r.value?.text) return r.value.text.value
    return JSON.stringify(r).slice(0, 300)
  } catch (_) {
    return String(r)
  }
}

module.exports = { RawBot, FACE, DIG }
