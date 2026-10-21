#!/usr/bin/env node
'use strict'
/**
 * Offline self-test of the comparison and log readers (no servers): node orchestrator/selftest.js
 * Uses synthetic dumps; checks that identical runs pass, noise is tolerated, and each shadow violation fails.
 */
const assert = require('assert')
const fs = require('fs')
const os = require('os')
const path = require('path')
const { compare, noise } = require('./compare')
const PE = require('./pandaeye')

const dump = (wireOut, extra = {}) => ({ counters: { 'wire.out': wireOut, 'wire.in': { 'play:CLIENT_TICK_END': 400 }, ...extra } })
const base = { 'play:CHUNK_DATA': 81, 'play:ENTITY_POSITION_SYNC': 120, 'play:KEEP_ALIVE': 20, 'play:PING': 0, 'play:BUNDLE': 30 }

// identical
assert.ok(compare(dump(base), dump(base)).ok, 'identical must pass')
// noise on ordinary types is tolerated
const a2 = { ...base, 'play:ENTITY_POSITION_SYNC': 126, 'play:KEEP_ALIVE': 21 }
const nz = noise(dump(base), dump(a2))
assert.ok(compare(dump(base), dump({ ...base, 'play:ENTITY_POSITION_SYNC': 131, 'play:KEEP_ALIVE': 21 }), nz).ok, 'noise tolerated')
// PandaEye sends PINGs (transactions) -> fail
let c = compare(dump(base), dump({ ...base, 'play:PING': 40 }), nz)
assert.ok(!c.ok && c.violations.some((v) => v.type === 'play:PING'), 'injected PINGs must fail')
// channel registration -> fail
c = compare(dump(base), dump({ ...base, 'play:PLUGIN_MESSAGE': 1 }), nz)
assert.ok(!c.ok, 'new PLUGIN_MESSAGE must fail')
// setback -> fail
c = compare(dump(base), dump({ ...base, 'play:PLAYER_POSITION_AND_LOOK': 2 }), nz)
assert.ok(!c.ok, 'setback teleport must fail')
// wrapper left attached (re-encode) -> fail
c = compare(dump(base, { 'pe.out.will_reencode': { 'play:JOIN_GAME': 1 } }), dump(base, { 'pe.out.will_reencode': { 'play:JOIN_GAME': 1, 'play:ENTITY_VELOCITY': 7 } }), nz)
assert.ok(!c.ok && c.violations[0].group === 'pe.out.will_reencode', 're-encode must fail')
// cancellation -> fail
c = compare(dump(base), dump(base, { 'inflight.in.dropped': { 'play:ANIMATION': 3 } }), nz)
assert.ok(!c.ok, 'dropped inbound packets must fail')
// fewer chunks beyond noise -> fail (count groups are two-sided)
c = compare(dump(base), dump({ ...base, 'play:CHUNK_DATA': 60 }), nz)
assert.ok(!c.ok, 'missing chunks must fail')

// flag log reader
const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'pe-flags-'))
fs.mkdirSync(path.join(dir, 'plugins', 'PandaEye', 'logs'), { recursive: true })
fs.writeFileSync(path.join(dir, 'plugins', 'PandaEye', 'logs', 'flags-2026-10-21.jsonl'),
  JSON.stringify({ time: Date.now(), name: 'ch_speed', check: 'movement.speed', confirmed: true, count: 3 }) + '\n' +
  JSON.stringify({ time: Date.now(), uuid: 'abc', check_id: 'movement.fly', confirmed: false }) + '\nnot json\n')
const f = PE.flagsFor(dir, ['ch_speed', 'abc'])
assert.strictEqual(f.length, 2)
assert.deepStrictEqual(f.map((x) => [x.check, x.confirmed]), [['movement.speed', true], ['movement.fly', false]])
fs.rmSync(dir, { recursive: true })

const st = PE.parseStatus('[PandaEye] mode: shadow enforcement: false cloud: CONNECTED packet handler: avg 1.25 us p99 7.5 us/packet enforcer attempts 0')
assert.strictEqual(st.mode, 'shadow'); assert.strictEqual(st.handlerAvgUs, 1.25); assert.strictEqual(st.handlerP99Us, 7.5)
console.log('selftest OK')
