#!/usr/bin/env node
'use strict'
/**
 * Run scenarios against a server started with test/e2e/servers/start.sh.
 *   node run-scenario.js --server paper1 --scenario legit/vm-walk[,cheat/speed|legit|cheat|all] [--trace] [--quick]
 *                        [--list] [--out results.json]
 * --trace records the players' packets with the probe plugin and runs each scenario's verify() (cheat pattern check).
 */
const fs = require('fs')
const { Server } = require('./lib/server')
const { runScenario, listScenarios } = require('./lib/scenario')

function args () {
  const a = process.argv.slice(2)
  const o = { scenario: 'all', trace: false, quick: false }
  for (let i = 0; i < a.length; i++) {
    const k = a[i]
    if (k === '--server') o.server = a[++i]
    else if (k === '--scenario') o.scenario = a[++i]
    else if (k === '--trace') o.trace = true
    else if (k === '--quick') o.quick = true
    else if (k === '--list') o.list = true
    else if (k === '--out') o.out = a[++i]
    else if (k === '--lane') o.lane = Number(a[++i])
    else throw new Error('unknown arg ' + k)
  }
  return o
}

async function main () {
  const o = args()
  const list = listScenarios(o.scenario)
  if (o.list) {
    for (const m of list) console.log(`${m.name.padEnd(34)} ${m.kind.padEnd(6)} ${m.engine.padEnd(10)} ${(m.checks || []).join(',')}`)
    return
  }
  if (!o.server) throw new Error('--server NAME required')
  const server = new Server({ name: o.server })
  const results = []
  for (const m of list) {
    console.log(`== ${m.name} (${m.kind}) on ${server.name}`)
    await server.probe('reset')
    const r = await runScenario(m, { server, log: (s) => console.log(s), quick: o.quick, trace: o.trace, laneIndex: o.lane })
    const v = r.verify ? ` verify=${r.verify.ok ? 'OK' : 'FAIL'} ${r.verify.summary}` : ''
    console.log(`   -> ${r.ok ? 'ran' : 'ERROR ' + r.error} kicked=${JSON.stringify(r.kicked)}${v}`)
    results.push(r)
  }
  if (o.out) fs.writeFileSync(o.out, JSON.stringify(results, null, 2))
  process.exit(results.every((r) => r.ok && (!r.verify || r.verify.ok)) ? 0 : 1)
}

main().catch((e) => { console.error(e); process.exit(2) })
