'use strict'
/**
 * vanilla-mover: a minimal, exact re-implementation of the vanilla 1.21.x client movement for OPEN FLAT GROUND
 * (walk / sprint / sprint-jump / fall / knockback), used as an oracle that does not depend on mineflayer's physics.
 *
 * Per client tick (LivingEntity.aiStep + travelInAir + Entity.move, LocalPlayer.sendPosition):
 *   1. small-motion cut-off: |v.y| < 0.003 -> 0; players: horizontalDistanceSqr < 9e-6 -> x = z = 0
 *   2. jump when jumping && onGround (y = max(jumpPower 0.42F, v.y); sprinting adds (-sin(yaw)*0.2, 0, cos(yaw)*0.2))
 *   3. input = normalized key vector * 0.98F (forward only is exact; diagonals use the 1.21.5+ square-movement fix)
 *   4. moveRelative: speed = onGround ? movementSpeed * (0.21600002F / f^3) : (sprinting ? 0.025999999F : 0.02F),
 *      f = block friction (0.6F for grass/stone), rotated by Mth.sin/cos (65536-entry float table)
 *   5. move with collision against the support surface (flat ground + optional boxes), landing zeroes v.y
 *   6. v.y = (v.y - gravity 0.08) * 0.98F ; v.xz *= f * 0.91F (onGround) or 0.91F (air)
 *   7. send PosRot / Pos / Rot / StatusOnly exactly like LocalPlayer.sendPosition (2.0E-4 threshold, 20-tick reminder)
 * Float arithmetic is emulated with Math.fround wherever vanilla uses float.
 *
 * Not modelled (scenarios must avoid them): walls/horizontal collision, step-up, stairs/slabs, water, ice, slime,
 * sneaking edge back-off, potion effects other than the movement_speed attribute, elytra, vehicles.
 */

const F = Math.fround
const SIN = new Float32Array(65536)
for (let i = 0; i < 65536; i++) SIN[i] = Math.sin(i * Math.PI * 2 / 65536)
const DEG_TO_RAD_F = F(Math.PI / 180) // (float)(Math.PI / 180.0)

function idx (v) {
  const t = Math.trunc(v) // (long) cast
  return ((t % 65536) + 65536) % 65536 // & 65535L on a two's-complement long
}
function mthSin (d) { return SIN[idx(d * 10430.378350470453)] }
function mthCos (d) { return SIN[idx(d * 10430.378350470453 + 16384.0)] }

const C = {
  BLOCK_FRICTION: F(0.6),
  ACCEL_NUM: F(0.21600002),
  AIR_SPRINT: F(0.025999999),
  AIR_WALK: F(0.02),
  GRAVITY: 0.08,
  DRAG_Y: F(0.98),
  INPUT_SCALE: F(0.98),
  AIR_FRICTION: F(0.91),
  JUMP_POWER: F(0.42),
  SPRINT_JUMP: 0.2,
  POS_EPS_SQ: 2.0e-4 * 2.0e-4,
  SPEED_BASE: 0.1,
  SPRINT_MULT: 0.3 // ADD_MULTIPLIED_TOTAL modifier minecraft:sprinting
}

function lenF (x, y) { return F(Math.sqrt(F(F(x * x) + F(y * y)))) }

/** LocalPlayer.modifyInput (no item use / sneaking) + modifyInputSpeedForSquareMovement, all in float. */
function inputVector (forward, strafe, opts = {}) {
  let x = F(strafe); let y = F(forward)
  const l = lenF(x, y)
  if (l === 0) return { xxa: 0, zza: 0 }
  // KeyboardInput: Vec2(left-right, forward-back).normalized()
  x = F(x / l); y = F(y / l)
  x = F(x * C.INPUT_SCALE); y = F(y * C.INPUT_SCALE)
  if (opts.usingItem) { x = F(x * F(0.2)); y = F(y * F(0.2)) }
  if (opts.sneaking) { const s = F(opts.sneakSpeed ?? 0.3); x = F(x * s); y = F(y * s) }
  const f = lenF(x, y)
  if (f <= 0) return { xxa: x, zza: y }
  const ux = F(x * F(1 / f)); const uy = F(y * F(1 / f))
  const ax = Math.abs(ux); const ay = Math.abs(uy)
  const h = ay > ax ? F(ax / ay) : F(ay / ax)
  const g = F(Math.sqrt(F(1 + F(h * h))))
  const k = Math.min(F(f * g), 1)
  return { xxa: F(ux * k), zza: F(uy * k) }
}

class VanillaMover {
  /**
   * @param {object} o
   * @param {number} o.x @param {number} o.y @param {number} o.z start position (feet)
   * @param {number} [o.yaw] @param {number} [o.pitch]
   * @param {number} o.groundY flat ground surface height (top of the grass block)
   * @param {Array<{minX:number,maxX:number,minZ:number,maxZ:number,top:number}>} [o.boxes] raised platforms (top surface)
   * @param {number} [o.speedBase] movement_speed attribute base (0.1)
   */
  constructor (o) {
    this.x = o.x; this.y = o.y; this.z = o.z
    this.yaw = F(o.yaw ?? 0); this.pitch = F(o.pitch ?? 0)
    this.vx = 0; this.vy = 0; this.vz = 0
    this.groundY = o.groundY
    this.boxes = o.boxes || []
    this.speedBase = o.speedBase ?? C.SPEED_BASE
    this.onGround = Math.abs(this.y - this.support(this.x, this.z)) < 1e-9
    this.horizontalCollision = false
    this.sprinting = false
    this.noJumpDelay = 0
    // LocalPlayer.sendPosition state
    this.xLast = this.x; this.yLast = this.y; this.zLast = this.z
    this.yawLast = this.yaw; this.pitchLast = this.pitch
    this.lastOnGround = this.onGround; this.lastHorizontalCollision = false
    this.positionReminder = 0
    this.ticks = 0
  }

  /** Highest support surface under the 0.6x0.6 footprint at (x, z). */
  support (x, z) {
    let top = this.groundY
    for (const b of this.boxes) {
      if (x + 0.3 > b.minX && x - 0.3 < b.maxX && z + 0.3 > b.minZ && z - 0.3 < b.maxZ) top = Math.max(top, b.top)
    }
    return top
  }

  /** Server velocity packet (ClientboundSetEntityMotionPacket -> lerpMotion -> setDeltaMovement). */
  setVelocity (vx, vy, vz) { this.vx = vx; this.vy = vy; this.vz = vz }

  /** Server teleport: position/rotation set, motion as given (default unchanged like relative-free teleports keep 0). */
  teleport (x, y, z, yaw, pitch, keepVelocity = false) {
    this.x = x; this.y = y; this.z = z
    if (yaw !== undefined) this.yaw = F(yaw)
    if (pitch !== undefined) this.pitch = F(pitch)
    if (!keepVelocity) { this.vx = 0; this.vy = 0; this.vz = 0 }
    // the client answers with PosRot(x, y, z, yaw, pitch, onGround=false) and resets its "last sent" state
    this.xLast = x; this.yLast = y; this.zLast = z; this.yawLast = this.yaw; this.pitchLast = this.pitch
    this.onGround = false; this.lastOnGround = false; this.positionReminder = 0
  }

  movementSpeed () {
    const v = this.sprinting ? this.speedBase * (1 + C.SPRINT_MULT) : this.speedBase
    return F(v)
  }

  /**
   * One client tick.
   * @param {object} inp { forward: -1..1, strafe: -1..1, jump: bool, sprint: bool, yaw?: number, pitch?: number }
   * @returns {{type: string|null, x:number, y:number, z:number, yaw:number, pitch:number, onGround:boolean,
   *            horizontalCollision:boolean, dy:number, h:number}}
   */
  step (inp = {}) {
    this.ticks++
    if (inp.yaw !== undefined) this.yaw = F(inp.yaw)
    if (inp.pitch !== undefined) this.pitch = F(Math.max(-90, Math.min(90, inp.pitch)))
    // sprinting needs forward input; vanilla stops sprinting without it
    this.sprinting = !!inp.sprint && (inp.forward ?? 0) > 0
    if (this.noJumpDelay > 0) this.noJumpDelay--

    // 1. small-motion cut-off
    if (this.vx * this.vx + this.vz * this.vz < 9.0e-6) { this.vx = 0; this.vz = 0 }
    if (Math.abs(this.vy) < 0.003) this.vy = 0

    // 2. jump
    if (inp.jump) {
      if (this.onGround && this.noJumpDelay === 0) {
        this.vy = Math.max(C.JUMP_POWER, this.vy)
        if (this.sprinting) {
          const g = F(this.yaw * DEG_TO_RAD_F)
          this.vx += -mthSin(g) * C.SPRINT_JUMP
          this.vz += mthCos(g) * C.SPRINT_JUMP
        }
        this.noJumpDelay = 10
      }
    } else {
      this.noJumpDelay = 0
    }

    // 3. input
    const { xxa, zza } = inputVector(inp.forward ?? 0, inp.strafe ?? 0, inp)

    // 4. travelInAir / moveRelative
    const f = this.onGround ? C.BLOCK_FRICTION : F(1.0)
    const g = F(f * C.AIR_FRICTION)
    const speed = this.onGround
      ? F(this.movementSpeed() * F(C.ACCEL_NUM / F(F(f * f) * f)))
      : (this.sprinting ? C.AIR_SPRINT : C.AIR_WALK)
    const lsq = xxa * xxa + zza * zza
    if (lsq >= 1.0e-7) {
      let ix = xxa; let iz = zza
      if (lsq > 1.0) { const l = Math.sqrt(lsq); ix /= l; iz /= l }
      ix *= speed; iz *= speed
      const s = mthSin(F(this.yaw * DEG_TO_RAD_F))
      const c = mthCos(F(this.yaw * DEG_TO_RAD_F))
      this.vx += ix * c - iz * s
      this.vz += iz * c + ix * s
    }

    // 5. move with vertical collision only
    const x0 = this.x; const y0 = this.y; const z0 = this.z
    this.x += this.vx; this.z += this.vz
    const sup = this.support(this.x, this.z)
    let dy = this.vy
    let vertical = false
    if (this.y + dy < sup && this.y >= sup - 1e-7) { dy = sup - this.y; vertical = true }
    else if (this.y + dy < sup && this.y < sup - 1e-7) { /* below a box top: inside/side - not modelled */ }
    this.y += dy
    this.onGround = vertical && this.vy < 0
    if (vertical) this.vy = 0 // Block.updateEntityMovementAfterFallOn
    this.horizontalCollision = false

    // 6. gravity, drag, friction
    this.vy = (this.vy - C.GRAVITY) * C.DRAG_Y
    this.vx *= g; this.vz *= g

    return this._send(x0, y0, z0)
  }

  /** LocalPlayer.sendPosition */
  _send (x0, y0, z0) {
    const d = this.x - this.xLast; const e = this.y - this.yLast; const f = this.z - this.zLast
    const gy = this.yaw - this.yawLast; const hp = this.pitch - this.pitchLast
    this.positionReminder++
    const pos = d * d + e * e + f * f > C.POS_EPS_SQ || this.positionReminder >= 20
    const rot = gy !== 0 || hp !== 0
    let type = null
    if (pos && rot) type = 'position_look'
    else if (pos) type = 'position'
    else if (rot) type = 'look'
    else if (this.lastOnGround !== this.onGround || this.lastHorizontalCollision !== this.horizontalCollision) type = 'flying'
    if (pos) { this.xLast = this.x; this.yLast = this.y; this.zLast = this.z; this.positionReminder = 0 }
    if (rot) { this.yawLast = this.yaw; this.pitchLast = this.pitch }
    this.lastOnGround = this.onGround; this.lastHorizontalCollision = this.horizontalCollision
    return {
      type, x: this.x, y: this.y, z: this.z, yaw: this.yaw, pitch: this.pitch, onGround: this.onGround,
      horizontalCollision: this.horizontalCollision, dy: this.y - y0, h: Math.hypot(this.x - x0, this.z - z0)
    }
  }
}

module.exports = { VanillaMover, mthSin, mthCos, inputVector, CONSTANTS: C }

// self-test: node lib/vanilla-mover.js
if (require.main === module) {
  const m = new VanillaMover({ x: 0.5, y: -60, z: 0.5, groundY: -60 })
  let last
  for (let i = 0; i < 60; i++) last = m.step({ forward: 1, sprint: true })
  console.log('steady sprint h/tick', last.h.toFixed(6), '(vanilla 0.280617)')
  const w = new VanillaMover({ x: 0.5, y: -60, z: 0.5, groundY: -60 })
  for (let i = 0; i < 60; i++) last = w.step({ forward: 1 })
  console.log('steady walk h/tick', last.h.toFixed(6), '(vanilla 0.215859)')
  const j = new VanillaMover({ x: 0.5, y: -60, z: 0.5, groundY: -60 })
  let maxY = -1e9; let air = 0; const hs = []
  for (let i = 0; i < 200; i++) {
    const r = j.step({ forward: 1, sprint: true, jump: true })
    maxY = Math.max(maxY, r.y)
    if (!r.onGround) air++
    if (i > 150) hs.push(r.h)
  }
  console.log('sprint-jump apex', (maxY + 60).toFixed(6), '(vanilla 1.252203) mean h', (hs.reduce((a, b) => a + b) / hs.length).toFixed(4))
}
