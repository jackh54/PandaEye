'use strict'
/** report.json (everything) + report.md (human summary). Returns 'PASS' | 'FAIL'. */
const fs = require('fs')
const path = require('path')
const { ANOMALY_GROUPS } = require('./compare')

const sum = (o) => Object.values(o || {}).reduce((a, b) => a + b, 0)
const f2 = (v) => (typeof v === 'number' && Number.isFinite(v) ? v.toFixed(2) : '-')

function anomalies (dump) {
  const out = {}
  for (const g of ANOMALY_GROUPS) { const s = sum(dump?.counters?.[g]); if (s) out[g] = s }
  return out
}

function top (o, n = 4) {
  return Object.entries(o || {}).sort((a, b) => b[1] - a[1]).slice(0, n).map(([k, v]) => `${k.replace('play:', '')} ${v}`).join(', ')
}

function writeReport (dir, R) {
  fs.writeFileSync(path.join(dir, 'report.json'), JSON.stringify(R, null, 1))
  const L = []
  let fail = false
  L.push('# PandaEye e2e report', '', `started ${R.startedAt}${R.finishedAt ? ', finished ' + R.finishedAt : ''}`, '')
  L.push(`artifacts: probe \`${R.artifacts.probeJar ? 'ok' : 'missing'}\`, PandaEye jar \`${R.artifacts.pandaeyeJar || 'missing'}\`, cloud \`${R.artifacts.cloudBin || 'missing'}\``, '')
  const P = R.phases

  if (P.baseline) {
    L.push('## Baseline (no PandaEye)', '')
    for (const [fl, f] of Object.entries(P.baseline.flavors || {})) {
      L.push(`### ${fl} ${f.error ? '- ERROR ' + f.error : ''}`, '')
      if (!f.runs?.length) { L.push('not run', ''); continue }
      L.push('| run | scenario | kicks | errors | TPS global / regions | MSPT | wire out pkts | top out types | PE/in-flight anomalies (baseline) |', '|---|---|---|---|---|---|---|---|---|')
      f.runs.forEach((run, i) => run.forEach((r) => {
        const legitKick = r.kind === 'legit' && r.serverKicks.length
        if (legitKick || !r.ok) fail = true
        const t = r.dump?.tick || {}
        L.push(`| ${i + 1} | ${r.scenario} | ${r.serverKicks.length}${legitKick ? ' FAIL' : ''} | ${r.errors.length} | ${f2(t.global_region?.tps)} / ${f2(t.player_regions?.tps)} | ${f2(t.paper_mspt_avg)} | ${sum(r.dump?.counters?.['wire.out'])} | ${top(r.dump?.counters?.['wire.out'])} | ${JSON.stringify(anomalies(r.dump)).replace(/"/g, '')} |`)
      }))
      L.push('')
      if (f.noise && Object.keys(f.noise).length) {
        L.push(`Scenario noise (|run1 - run2| per outbound type, the tolerance basis for the with/without-PandaEye comparison):`, '')
        L.push('| scenario | out types differing | largest differences |', '|---|---|---|')
        for (const [s, nz] of Object.entries(f.noise)) {
          const w = nz['wire.out'] || {}
          L.push(`| ${s} | ${Object.keys(w).length} | ${top(w, 5)} |`)
        }
        L.push('')
      }
    }
  }

  if (P.cheats) {
    L.push(`## Cheat clients against baseline (${P.cheats.flavor}): packet pattern verification`, '')
    L.push('| scenario | target check(s) | pattern | evidence | kicked |', '|---|---|---|---|---|')
    for (const r of P.cheats.results || []) {
      const ok = r.verify?.ok
      if (!ok) fail = true
      const k = r.serverKicks.map((x) => x.cause || x.reason).join(';')
      L.push(`| ${r.scenario} | ${r.checks.join(', ')} | ${ok ? 'OK' : 'FAIL'} | ${r.verify?.summary || r.error || ''} | ${k ? k + (r.expectedKick ? ' (expected)' : '') : '-'} |`)
    }
    L.push('')
  }

  for (const ph of ['shadow', 'hotload']) {
    const s = P[ph]
    if (!s) continue
    L.push(`## Shadow invariant: ${ph === 'shadow' ? 'PandaEye loaded at startup' : 'PandaEye hot-loaded via plugman'}`, '')
    if (s.skipped || s.error) { L.push(s.skipped ? `SKIPPED: ${s.skipped}` : `ERROR: ${s.error}`, ''); continue }
    for (const [fl, f] of Object.entries(s.flavors || {})) {
      L.push(`### ${fl}`, '')
      if (f.hotload) L.push(`hot load: errors ${f.hotload.errors.length}, kicked ${f.hotload.kicked.length}`, '')
      if (f.status) L.push(`pandaeye status: mode=${f.status.mode} cloud=${f.status.cloud} handler avg/p99 us=${f.status.handlerAvgUs}/${f.status.handlerP99Us}`, '')
      L.push('| scenario | result | violations (group type A->B tol) |', '|---|---|---|')
      for (const [sc, c] of Object.entries(f.compare || {})) {
        if (!c.ok) fail = true
        L.push(`| ${sc} | ${c.ok ? 'identical within noise' : 'FAIL'} | ${c.violations.slice(0, 6).map((v) => `${v.group} ${v.type} ${v.a}->${v.b} (tol ${v.tol})`).join('; ')}${c.kicks?.length ? ' kicks: ' + c.kicks.length : ''} |`)
      }
      L.push('')
    }
  }

  if (P.cycles) {
    L.push('## plugman unload/load cycles', '')
    if (P.cycles.skipped || P.cycles.error) L.push(P.cycles.skipped || P.cycles.error, '')
    else {
      L.push('| cycle | disable ms | PandaEye threads after unload | kicked | errors | ok |', '|---|---|---|---|---|---|')
      for (const c of P.cycles.cycles || []) {
        if (!c.ok) fail = true
        L.push(`| ${c.i} | ${f2(c.disable?.disable_ms)} | ${c.jcmdAfterUnload ? c.jcmdAfterUnload.pandaeye.length : '?'} | ${c.kicked.length} | ${c.errors.length} | ${c.ok ? 'OK' : 'FAIL'} |`)
      }
      if (P.cycles.leakedLoaders) { fail = true; L.push('', `class loaders not collected: ${P.cycles.leakedLoaders}`) }
      L.push('')
    }
  }

  if (P.ondisable) {
    L.push('## onDisable with the cloud unreachable (limit 300 ms)', '')
    if (P.ondisable.skipped || P.ondisable.error) L.push(P.ondisable.skipped || P.ondisable.error, '')
    for (const [v, r] of Object.entries(P.ondisable.variants || {})) {
      if (!r.ok) fail = true
      L.push(`- ${v} (${r.endpoint}): ${f2(r.disableMs)} ms, PandaEye threads after 1 s: ${r.threadsAfter1s ? r.threadsAfter1s.length : '?'} -> ${r.ok ? 'OK' : 'FAIL'}`)
    }
    L.push('')
  }

  if (P.load) {
    L.push('## Load', '')
    L.push('| flavor | profile | bots | TPS global | TPS regions | p99 tick interval ms | CPU cores | pkt/s | CPU us/pkt (process) | out chain avg us | in chain avg us | PandaEye handler avg/p99 us |', '|---|---|---|---|---|---|---|---|---|---|---|---|')
    for (const [fl, profs] of Object.entries(P.load.flavors || {})) {
      for (const [pr, byN] of Object.entries(profs)) {
        for (const [n, e] of Object.entries(byN)) {
          L.push(`| ${fl} | ${pr} | ${n} | ${f2(e.tick?.global_region?.tps)} | ${f2(e.tick?.player_regions?.tps)} | ${f2(e.tick?.player_regions?.p99_interval_ms)} | ${f2(e.cpuCores)} | ${Math.round(e.packetsPerSec)} | ${f2(e.cpuNsPerPacket / 1000)} | ${f2((e.costOutAfterEncoderNs?.avg_ns || 0) / 1000)} | ${f2((e.costInDecoderChainNs?.avg_ns || 0) / 1000)} | ${e.status ? `${e.status.handlerAvgUs ?? '-'}/${e.status.handlerP99Us ?? '-'}` : '-'} |`)
        }
      }
    }
    L.push('')
  }

  if (P.detect) {
    L.push('## Detection (PandaEye + cloud)', '')
    if (P.detect.skipped || P.detect.error) L.push(P.detect.skipped || P.detect.error, '')
    L.push('| scenario | expected | observed | result |', '|---|---|---|---|')
    for (const r of [...(P.detect.legit || []), ...(P.detect.cheats || [])]) {
      if (r.detectOk === false) fail = true
      L.push(`| ${r.scenario} | ${r.kind === 'legit' ? 'no confirmed flags, no verdicts' : r.checks.join(', ')} | ${(r.observedChecks || []).join(', ') || '-'} | ${r.detectOk ? 'OK' : 'FAIL'} |`)
    }
    L.push('')
  }

  for (const [p, v] of Object.entries(P)) if (v && v.error) { fail = true; L.push(`phase ${p} ERROR: ${String(v.error).split('\n')[0]}`) }
  const verdict = fail ? 'FAIL' : 'PASS'
  L.splice(2, 0, `**overall: ${verdict}**`, '')
  fs.writeFileSync(path.join(dir, 'report.md'), L.join('\n') + '\n')
  return verdict
}

module.exports = { writeReport }
