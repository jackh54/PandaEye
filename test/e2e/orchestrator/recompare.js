#!/usr/bin/env node
'use strict'
/** Re-run the shadow comparison offline on a report.json (e.g. after changing tolerances):
 *  node orchestrator/recompare.js run/reports/<dir>/report.json */
const fs = require('fs')
const { compare, noise } = require('./compare')
const R = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'))
let bad = 0
for (const ph of ['shadow', 'hotload']) {
  for (const [fl, f] of Object.entries(R.phases[ph]?.flavors || {})) {
    for (const ra of f.a || []) {
      const ra2 = (f.a2 || []).find((x) => x.scenario === ra.scenario)
      const rb = (f.b || []).find((x) => x.scenario === ra.scenario)
      if (!ra.dump || !rb?.dump) continue
      const c = compare(ra.dump, rb.dump, ra2?.dump ? noise(ra.dump, ra2.dump) : {}, { deterministic: ra.engine === 'raw', chaotic: ra.scenario === 'legit/mf-pvp' || !!ra.chaotic })
      const ok = c.ok && !rb.serverKicks.length && !(rb.confirmedFlags || []).length
      if (!ok) bad++
      console.log(`${ph} ${fl} ${ra.scenario.padEnd(24)} ${ok ? 'identical within noise' : 'FAIL ' + JSON.stringify(c.violations.slice(0, 4))}`)
    }
  }
}
process.exit(bad ? 1 : 0)
