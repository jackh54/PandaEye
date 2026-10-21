#!/usr/bin/env node
'use strict'
/**
 * PandaEye e2e orchestrator. See test/e2e/README.md.
 *
 *   node orchestrate.js <phase>... [--flavors paper,folia,canvas] [--flavor paper] [--repeat 2] [--scenarios legit]
 *                       [--quick] [--dry-run] [--bots 20,40] [--keep-servers] [--report-dir DIR]
 *
 * phases (need nothing but the vendor jars):
 *   baseline   legit suite on each flavor without PandaEye, repeated (noise), probe stats + TPS
 *   cheats     every cheat client once against a baseline server, packet pattern verified from probe traces
 * phases that need the PandaEye jar (plugin/paper/build/libs/PandaEye-*.jar):
 *   shadow     legit suite with PandaEye loaded at startup (shadow mode) vs baseline: wire counts, cancels, kicks
 *   hotload    same, but PandaEye is hot-loaded with `plugman load PandaEye` while bots are connected
 *   cycles     plugman unload/load x5 with bots connected: no kicks/errors, no PandaEye-* threads, loaders collected
 *   ondisable  onDisable time with the cloud unreachable (connection refused, and a black-hole TCP endpoint)
 *   load       TPS / CPU with 20-40 bots, with and without PandaEye (+ /pandaeye status handler cost)
 * phases that also need the cloud binary (cloud/bin/pandaeye, `pandaeye all`):
 *   detect     cheats -> expected check ids observed (local flags + cloud verdicts/signals); legit -> no confirmed flags
 *   all        everything that the available artifacts allow
 */
const fs = require('fs')
const path = require('path')
const net = require('net')
const { execFileSync } = require('child_process')

const BOTS = path.join(__dirname, '..', 'bots')
const { Server, RUN_DIR, E2E_DIR, readJsonLenient } = require(path.join(BOTS, 'lib', 'server'))
const { runScenario, listScenarios } = require(path.join(BOTS, 'lib', 'scenario'))
const { RawBot } = require(path.join(BOTS, 'lib', 'rawbot'))
const { sleep, offlineUuid } = require(path.join(BOTS, 'lib', 'util'))
const { compare, noise, kicksOf } = require('./compare')
const { Cloud, verdictChecks } = require('./cloud')
const PE = require('./pandaeye')
const { writeReport } = require('./report')

const REPO = path.resolve(E2E_DIR, '..', '..')
const PORTS = { paper: 25611, folia: 25612, canvas: 25613 }
const PROFILE_PORT = { baseline: 0, pandaeye: 10, hotload: 20, ondisable: 30, load: 40 }

// ---------------------------------------------------------------------------------------------------------------
// options
// ---------------------------------------------------------------------------------------------------------------
function parseArgs () {
  const a = process.argv.slice(2)
  const o = { phases: [], flavors: ['paper', 'folia', 'canvas'], flavor: 'paper', repeat: 2, scenarios: 'legit', quick: false, dryRun: false, bots: [20, 40], keep: false }
  for (let i = 0; i < a.length; i++) {
    const k = a[i]
    if (!k.startsWith('--')) { o.phases.push(k); continue }
    if (k === '--flavors') o.flavors = a[++i].split(',')
    else if (k === '--flavor') o.flavor = a[++i]
    else if (k === '--repeat') o.repeat = Number(a[++i])
    else if (k === '--scenarios') o.scenarios = a[++i]
    else if (k === '--quick') o.quick = true
    else if (k === '--dry-run') o.dryRun = true
    else if (k === '--bots') o.bots = a[++i].split(',').map(Number)
    else if (k === '--keep-servers') o.keep = true
    else if (k === '--report-dir') o.reportDir = a[++i]
    else if (k === '--cycles') o.cycles = Number(a[++i])
    else if (k === '--pandaeye-jar') process.env.PE_PANDAEYE_JAR = path.resolve(a[++i])
    else if (k === '--violation') process.env.PE_E2E_FAKE_VIOLATION = a[++i] // only for the e2e fake PandaEye
    else throw new Error('unknown option ' + k)
  }
  if (!o.phases.length) o.phases = ['baseline']
  o.reportDir = o.reportDir || path.join(RUN_DIR, 'reports', new Date().toISOString().replace(/[:.]/g, '-'))
  return o
}

const O = parseArgs()
const REPORT = { startedAt: new Date().toISOString(), options: O, artifacts: {}, phases: {} }
let logStream
function log (s) {
  const line = `[${new Date().toISOString().slice(11, 19)}] ${s}`
  console.log(line)
  if (logStream) logStream.write(line + '\n')
}

// ---------------------------------------------------------------------------------------------------------------
// artifacts
// ---------------------------------------------------------------------------------------------------------------
function findPandaEyeJar () {
  if (process.env.PE_PANDAEYE_JAR) return fs.existsSync(process.env.PE_PANDAEYE_JAR) ? process.env.PE_PANDAEYE_JAR : null
  const dir = path.join(REPO, 'plugin', 'paper', 'build', 'libs')
  if (!fs.existsSync(dir)) return null
  const jars = fs.readdirSync(dir).filter((f) => /^PandaEye-.*\.jar$/.test(f) && !/-(sources|javadoc|plain)\.jar$/.test(f))
    .map((f) => path.join(dir, f)).sort((x, y) => fs.statSync(y).mtimeMs - fs.statSync(x).mtimeMs)
  return jars[0] || null
}
// cloud/README.md builds with `go build -o pandaeye ./cmd/pandaeye` inside cloud/
const CLOUD_BIN = process.env.PE_CLOUD_BIN || [path.join(REPO, 'cloud', 'bin', 'pandaeye'), path.join(REPO, 'cloud', 'pandaeye')].find((f) => fs.existsSync(f)) || path.join(REPO, 'cloud', 'pandaeye')
const LICENSE = process.env.PE_E2E_LICENSE || 'pe-e2e-static-license-0000000000000001'

function artifacts () {
  const probeJar = path.join(E2E_DIR, 'probe-plugin', 'build', 'libs', 'PandaEyeProbe.jar')
  return {
    probeJar: fs.existsSync(probeJar) ? probeJar : null,
    pandaeyeJar: findPandaEyeJar(),
    cloudBin: fs.existsSync(CLOUD_BIN) ? CLOUD_BIN : null
  }
}

// ---------------------------------------------------------------------------------------------------------------
// servers
// ---------------------------------------------------------------------------------------------------------------
function sh (script, args, opts = {}) {
  const file = path.join(E2E_DIR, 'servers', script)
  if (O.dryRun) { log(`DRY: ${path.relative(REPO, file)} ${args.join(' ')}`); return '' }
  return execFileSync(file, args, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], timeout: opts.timeout || 300000 })
}

async function withServer ({ name, flavor, profile = 'baseline', port, extra = [] }, fn) {
  log(`server ${name}: ${flavor} profile=${profile} port=${port}`)
  sh('provision.sh', [name, '--flavor', flavor, '--port', String(port), '--profile', profile, '--fresh', ...extra])
  if (O.dryRun) { return fn(null) }
  sh('start.sh', [name, '--wait', '240'], { timeout: 300000 })
  const server = new Server({ name })
  try {
    await server.buildArena()
    return await fn(server)
  } finally {
    if (!O.keep) sh('stop.sh', [name, '90'], { timeout: 200000 })
    try { sh('collect-logs.sh', [name, path.join(O.reportDir, 'servers', name)]) } catch (_) {}
  }
}

/** Console lines of interest (errors, exceptions) since offset, optionally only those mentioning a regex. */
function consoleErrors (server, from = 0, re = null) {
  const text = server.readFrom(from)
  return text.split('\n').filter((l) => /\b(ERROR|SEVERE)\]|Exception|Error:/.test(l) && !/EULA|KnockbackSync\] Failed to check for updates|newer plugin version/.test(l))
    .filter((l) => !re || re.test(l)).slice(0, 50)
}

function slim (d) {
  if (!d) return null
  const keep = {}
  for (const [k, v] of Object.entries(d.counters || {})) if (!k.startsWith('player.')) keep[k] = v
  return { counters: keep, costs: d.costs, tick: d.tick, cpu: d.cpu, kicks: d.kicks, quits: d.quits, meta: { pe_reencode_by_default: d.meta?.pe_reencode_by_default, plugins: d.meta?.plugins }, pipelines: d.pipelines }
}

/** Run a list of scenarios on a server: reset, run, dump per scenario. */
async function runSuite (server, scenarios, { tag, trace = false, onEach } = {}) {
  const out = []
  let lane = 0
  for (const m of scenarios) {
    log(`  [${tag}] ${m.name}`)
    await server.probe('reset')
    const from = server.offset()
    const dumpName = `${tag}-${m.name.replace('/', '-')}`
    const r = await runScenario(m, { server, log: (s) => log('    ' + s.trim()), quick: O.quick, trace, laneIndex: lane++, dumpName })
    let d = null
    try { d = readJsonLenient(path.join(server.dir, 'probe', dumpName + '.json')) } catch (e) { r.error = (r.error || '') + ' dump: ' + e.message }
    const rec = {
      scenario: m.name, kind: m.kind, engine: m.engine, chaotic: !!m.chaotic, checks: m.checks || [], players: r.players, suspects: r.suspects,
      ok: r.ok, error: r.error, verify: r.verify, kicked: r.kicked, expectedKick: r.expectedKick,
      serverKicks: kicksOf(d, r.players), startMs: r.startMs, endMs: r.endMs, notes: r.notes, facts: r.facts,
      errors: consoleErrors(server, from), dump: slim(d)
    }
    if (onEach) await onEach(rec, server)
    const v = rec.verify ? ` verify=${rec.verify.ok ? 'OK' : 'FAIL'} (${rec.verify.summary})` : ''
    log(`    -> ${rec.ok ? 'ok' : 'ERROR ' + String(rec.error).split('\n')[0]} kicks=${rec.serverKicks.length}${v}`)
    out.push(rec)
  }
  return out
}

const BASE_CACHE = {} // flavor -> { a, a2 } baseline runs reused by shadow/hotload in the same invocation

/** onEach hook: PandaEye flags of the scenario's players (legit scenarios must have no confirmed flag). */
async function collectFlags (rec, srv) {
  await sleep(6000) // flag log aggregation window is 5 s
  const players = rec.suspects.flatMap((p) => [p, offlineUuid(p)])
  rec.flags = PE.flagsFor(srv.dir, players, rec.startMs - 1000, Date.now())
  rec.confirmedFlags = rec.flags.filter((f) => f.confirmed)
  if (rec.kind === 'legit' && rec.confirmedFlags.length) log(`    LEGIT FLAGGED: ${rec.confirmedFlags.map((f) => f.check).join(', ')}`)
}

const legitScenarios = () => listScenarios(O.scenarios === 'legit' ? 'legit' : O.scenarios).filter((m) => m.kind === 'legit')

// ---------------------------------------------------------------------------------------------------------------
// phases
// ---------------------------------------------------------------------------------------------------------------
async function phaseBaseline () {
  const res = { flavors: {} }
  for (const flavor of O.flavors) {
    const name = `${flavor}-base`
    const f = { runs: [], noise: {}, started: false }
    res.flavors[flavor] = f
    try {
      await withServer({ name, flavor, port: PORTS[flavor] }, async (server) => {
        if (!server) return
        f.started = true
        for (let r = 1; r <= O.repeat; r++) {
          await server.buildArena()
          f.runs.push(await runSuite(server, legitScenarios(), { tag: `base${r}` }))
        }
      })
    } catch (e) {
      f.error = String(e.message || e)
      log(`baseline ${flavor} failed: ${f.error}`)
    }
    if (f.runs.length >= 2) BASE_CACHE[flavor] = { a: f.runs[0], a2: f.runs[1] }
    if (f.runs.length >= 2) {
      for (const rec of f.runs[0]) {
        const rec2 = f.runs[1].find((x) => x.scenario === rec.scenario)
        if (rec2 && rec.dump && rec2.dump) f.noise[rec.scenario] = noise(rec.dump, rec2.dump)
      }
      f.aa = {}
      for (const rec of f.runs[0]) {
        const rec2 = f.runs[1].find((x) => x.scenario === rec.scenario)
        if (rec2 && rec.dump && rec2.dump) f.aa[rec.scenario] = compare(rec.dump, rec2.dump, {}).deltas
      }
    }
  }
  return res
}

async function phaseCheats () {
  const res = { flavor: O.flavor, results: [] }
  const list = listScenarios(O.scenarios === 'legit' ? 'cheat' : O.scenarios).filter((m) => m.kind === 'cheat')
  await withServer({ name: `${O.flavor}-cheat`, flavor: O.flavor, port: PORTS[O.flavor] + 50 }, async (server) => {
    if (!server) { list.forEach((m) => log(`DRY: cheat ${m.name} -> traced + verify()`)); return }
    res.results = await runSuite(server, list, { tag: 'cheat', trace: true })
  })
  return res
}

function needPandaEye (phase) {
  const a = artifacts()
  if (!a.pandaeyeJar) {
    log(`${phase}: SKIPPED - PandaEye jar not found (plugin/paper/build/libs/PandaEye-*.jar or PE_PANDAEYE_JAR)`)
    if (!O.dryRun) return null
    return '<PandaEye.jar>'
  }
  return a.pandaeyeJar
}

async function maybeCloud (phase) {
  const cloud = new Cloud({ bin: CLOUD_BIN, runDir: RUN_DIR, license: LICENSE, log })
  if (O.dryRun) { cloud.plan().forEach((l) => log(`DRY: ${l}`)); return null }
  if (!cloud.available()) { log(`${phase}: cloud binary not found (${CLOUD_BIN}); PandaEye runs without a cloud (degraded mode)`); return null }
  await cloud.start()
  try { await cloud.ensureTenant(); log(`cloud tenant ${cloud.tenantId}`) } catch (e) { log(`cloud tenant: ${e.message}`) }
  return cloud
}

async function phaseShadow (hot = false) {
  const phase = hot ? 'hotload' : 'shadow'
  const jar = needPandaEye(phase)
  if (!jar) return { skipped: 'PandaEye jar missing' }
  const res = { flavors: {} }
  const cloud = await maybeCloud(phase)
  try {
    for (const flavor of O.flavors) {
      const f = { }
      res.flavors[flavor] = f
      const scen = legitScenarios()
      // A and A2: baseline twice (noise); B: with PandaEye
      if (BASE_CACHE[flavor]) {
        Object.assign(f, BASE_CACHE[flavor])
        log(`  ${flavor}: reusing baseline runs of this invocation as A/A2`)
      } else {
        await withServer({ name: `${flavor}-base`, flavor, port: PORTS[flavor] }, async (server) => {
          if (!server) return
          f.a = await runSuite(server, scen, { tag: 'A' })
          await server.buildArena()
          f.a2 = await runSuite(server, scen, { tag: 'A2' })
          BASE_CACHE[flavor] = { a: f.a, a2: f.a2 }
        })
      }
      const profile = hot ? 'hotload' : 'pandaeye'
      await withServer({ name: `${flavor}-${hot ? 'hot' : 'pe'}`, flavor, profile, port: PORTS[flavor] + PROFILE_PORT[profile], extra: ['--pandaeye-jar', jar] }, async (server) => {
        if (!server) { log(`DRY: ${hot ? 'hotload.sh (plugman load PandaEye) with 2 bots connected, then ' : ''}run legit suite, pandaeye status`); return }
        if (hot) {
          // connect two walking bots, then hot-load PandaEye under them
          const bots = await walkers(server, 2, 'hl_')
          const from = server.offset()
          sh('hotload.sh', [server.name, '90'])
          await sleep(5000)
          f.hotload = { errors: consoleErrors(server, from, /PandaEye/i), kicked: bots.filter((b) => b.kicked).map((b) => b.username) }
          await stopWalkers(bots)
        }
        f.status = await pandaeyeStatus(server)
        f.b = await runSuite(server, scen, { tag: 'B', onEach: collectFlags })
        f.statusAfter = await pandaeyeStatus(server)
      })
      if (O.dryRun) continue
      f.compare = {}
      for (const ra of f.a || []) {
        const ra2 = (f.a2 || []).find((x) => x.scenario === ra.scenario)
        const rb = (f.b || []).find((x) => x.scenario === ra.scenario)
        if (!ra.dump || !rb || !rb.dump) continue
        const nz = ra2 && ra2.dump ? noise(ra.dump, ra2.dump) : {}
        const c = compare(ra.dump, rb.dump, nz, { deterministic: ra.engine === 'raw', chaotic: !!ra.chaotic })
        c.kicks = rb.serverKicks
        c.confirmedFlags = (rb.confirmedFlags || []).map((x) => x.check)
        c.ok = c.ok && rb.serverKicks.length === 0 && c.confirmedFlags.length === 0
        f.compare[ra.scenario] = c
        log(`  ${flavor} ${ra.scenario}: ${c.ok ? 'IDENTICAL within noise' : 'VIOLATIONS ' + JSON.stringify(c.violations.slice(0, 5))}`)
      }
    }
  } finally {
    if (cloud) await cloud.stop()
  }
  return res
}

/** N raw vanilla-mover bots walking back and forth in lanes. */
async function walkers (server, n, prefix = 'w_', { offset = 0 } = {}) {
  const arena = server.arena(); const L = arena.fixtures.lanes
  const bots = []
  for (let i = 0; i < n; i++) {
    const name = `${prefix}${String(i).padStart(2, '0')}`
    const b = new RawBot({ username: name, port: server.port, groundY: arena.y0, log })
    try {
      await b.connect()
      b.start()
      await server.probe(`gm ${name} survival`)
      const z = L.origin[2] + ((i + offset) % L.lanes) * L.lane_spacing
      await server.probe(`tp ${name} ${L.origin[0] + (i % 3) * 20} ${L.origin[1]} ${z} -90 0`)
      let k = 0
      b.on('tick', (t) => { if (t % 100 === 0) { b.input.yaw = (k++ % 2) ? 90 : -90 } b.input.forward = 1; b.input.sprint = (t % 300) < 150 })
      bots.push(b)
    } catch (e) { log(`walker ${name}: ${e.message}`) }
    await sleep(150)
  }
  return bots
}
async function stopWalkers (bots) { for (const b of bots) await b.quit() }

async function pandaeyeStatus (server) {
  try {
    const from = server.offset()
    server.cmd('pandaeye status')
    await sleep(1500)
    return PE.parseStatus(server.readFrom(from))
  } catch (e) { return { error: e.message } }
}

async function phaseCycles () {
  const jar = needPandaEye('cycles')
  if (!jar) return { skipped: 'PandaEye jar missing' }
  const n = O.cycles || 5
  const res = { flavor: O.flavor, cycles: [] }
  const cloud = await maybeCloud('cycles')
  try {
    await withServer({ name: `${O.flavor}-pe`, flavor: O.flavor, profile: 'pandaeye', port: PORTS[O.flavor] + 10, extra: ['--pandaeye-jar', jar] }, async (server) => {
      if (!server) { for (let i = 0; i < n; i++) log(`DRY: cycle ${i + 1}: probe disable-timed PandaEye; plugman unload PandaEye; jcmd Thread.print; probe gc; plugman load PandaEye`); return }
      await server.probe('watch PandaEye')
      const bots = await walkers(server, 4, 'cy_')
      const from0 = server.offset()
      for (let i = 0; i < n; i++) {
        const c = { i: i + 1 }
        const from = server.offset()
        await server.probe(`disable-timed PandaEye probe/cycle-${i + 1}-disable.json`, 30000)
        c.disable = readJsonLenient(path.join(server.dir, 'probe', `cycle-${i + 1}-disable.json`))
        await server.cmdWait('plugman unload PandaEye', /unloaded|has been unloaded|Unloaded|not loaded|Error/i, 30000).catch((e) => { c.unloadError = e.message })
        await sleep(1500)
        c.jcmdAfterUnload = PE.pandaeyeThreads(server.pid())
        await server.probe(`gc probe/cycle-${i + 1}-gc.json`, 60000)
        c.gc = readJsonLenient(path.join(server.dir, 'probe', `cycle-${i + 1}-gc.json`))
        await server.cmdWait('plugman load PandaEye', /\[PandaEye\] .*(Enabl|enabl)|PandaEye.*loaded|Error|Could not/i, 60000).catch((e) => { c.loadError = e.message })
        await sleep(8000)
        c.errors = consoleErrors(server, from, /PandaEye|pandaeye|plugman/i)
        c.kicked = bots.filter((b) => b.kicked || b.ended).map((b) => b.username)
        c.ok = !c.unloadError && !c.loadError && c.errors.length === 0 && c.kicked.length === 0 &&
          (c.disable.threads_after?.pandaeye?.length ?? 0) === 0 && (!c.jcmdAfterUnload || c.jcmdAfterUnload.pandaeye.length === 0)
        log(`  cycle ${c.i}: disable ${c.disable.disable_ms?.toFixed?.(1)} ms, threads after unload ${c.jcmdAfterUnload ? c.jcmdAfterUnload.pandaeye.length : '?'}, kicked ${c.kicked.length}, errors ${c.errors.length}`)
        res.cycles.push(c)
      }
      // loaders of all but the current instance must be collected
      await server.probe('gc probe/cycles-final-gc.json', 60000)
      res.finalGc = readJsonLenient(path.join(server.dir, 'probe', 'cycles-final-gc.json'))
      // watched[0] is the instance enabled at server start: Bukkit's help map (CraftServer.helpMap ->
      // GenericCommandHelpTopic -> PluginCommand -> plugin) keeps it, and PlugManX does not clear it (see README).
      // Instances loaded by plugman must all be collected.
      const w = res.finalGc.watched || []
      res.startupLoaderCollected = w.length ? w[0].collected : null
      res.leakedLoaders = w.slice(1).filter((x) => !x.collected && !x.is_current_instance).length
      if (res.leakedLoaders && process.env.PE_E2E_HEAP_ANALYSIS) {
        const hp = path.join(RUN_DIR, 'heap', 'cycles.hprof')
        fs.mkdirSync(path.dirname(hp), { recursive: true })
        try {
          execFileSync('jcmd', [String(server.pid()), 'GC.heap_dump', hp], { timeout: 120000 })
          res.retainers = execFileSync('python3', ['-I', path.join(__dirname, 'hprof-retainers.py'), hp, process.env.PE_E2E_HEAP_CLASS || 'dev.pandascript.pandaeye.paper.PandaEyePlugin', '--max', '2'], { encoding: 'utf8', timeout: 1800000 })
        } catch (e) { res.retainers = 'heap analysis failed: ' + e.message }
        fs.rmSync(hp, { force: true })
      }
      res.errorsTotal = consoleErrors(server, from0, /PandaEye/i)
      await stopWalkers(bots)
    })
  } finally { if (cloud) await cloud.stop() }
  return res
}

async function phaseOnDisable () {
  const jar = needPandaEye('ondisable')
  if (!jar) return { skipped: 'PandaEye jar missing' }
  const res = { flavor: O.flavor, variants: {} }
  // black hole: accepts TCP and never answers (the WebSocket upgrade hangs)
  const sockets = []
  const hole = net.createServer((s) => { sockets.push(s) })
  if (!O.dryRun) await new Promise((r) => hole.listen(18080, '127.0.0.1', r))
  try {
    for (const [variant, endpoint] of [['refused', 'ws://127.0.0.1:1/v1/connect'], ['blackhole', 'ws://127.0.0.1:18080/v1/connect']]) {
      await withServer({ name: `${O.flavor}-od`, flavor: O.flavor, profile: 'pandaeye', port: PORTS[O.flavor] + PROFILE_PORT.ondisable, extra: ['--pandaeye-jar', jar, '--cloud-endpoint', endpoint] }, async (server) => {
        if (!server) { log(`DRY: ${variant}: 4 bots, wait 20 s, probe disable-timed PandaEye (assert <= 300 ms, no PandaEye-* threads)`); return }
        const bots = await walkers(server, 4, 'od_')
        await sleep(20000)
        await server.probe(`disable-timed PandaEye probe/ondisable-${variant}.json`, 30000)
        const d = readJsonLenient(path.join(server.dir, 'probe', `ondisable-${variant}.json`))
        await sleep(1000)
        const th = PE.pandaeyeThreads(server.pid())
        res.variants[variant] = { endpoint, disableMs: d.disable_ms, threadsImmediately: d.threads_after?.pandaeye || [], threadsAfter1s: th ? th.pandaeye : null, ok: d.disable_ms <= 300 && (!th || th.pandaeye.length === 0), kicked: bots.filter((b) => b.kicked).map((b) => b.username) }
        log(`  ondisable ${variant}: ${d.disable_ms} ms, threads after 1 s: ${th ? th.pandaeye.length : '?'}`)
        await stopWalkers(bots)
      })
    }
  } finally {
    sockets.forEach((s) => s.destroy())
    hole.close()
  }
  return res
}

async function phaseLoad () {
  const jar = artifacts().pandaeyeJar
  const res = { flavors: {} }
  for (const flavor of O.flavors) {
    res.flavors[flavor] = {}
    for (const profile of ['baseline', ...(jar || O.dryRun ? ['pandaeye'] : [])]) {
      const entry = res.flavors[flavor][profile] = {}
      await withServer({ name: `${flavor}-load-${profile}`, flavor, profile, port: PORTS[flavor] + PROFILE_PORT.load + (profile === 'pandaeye' ? 5 : 0), extra: profile === 'pandaeye' ? ['--pandaeye-jar', jar || 'missing'] : [] }, async (server) => {
        for (const n of O.bots) {
          if (!server) { log(`DRY: load ${flavor}/${profile} with ${n} bots: 60 s, probe tick+cpu dumps, pandaeye status`); continue }
          const bots = await walkers(server, n, `ld${n}_`)
          await sleep(5000)
          await server.probe('reset')
          const d0 = await server.dump(`load-${n}-start`)
          const t0 = Date.now()
          await sleep(O.quick ? 20000 : 60000)
          const d1 = await server.dump(`load-${n}-end`)
          const wall = (Date.now() - t0) * 1e6
          const cpuNs = d1.cpu.process_cpu_ns - d0.cpu.process_cpu_ns
          const pk = (g) => Object.values(d1.counters[g] || {}).reduce((x, y) => x + y, 0)
          const packets = pk('wire.out') + pk('wire.in')
          const pe = (d1.cpu.thread_group_cpu_ns?.pandaeye || 0) - (d0.cpu.thread_group_cpu_ns?.pandaeye || 0)
          entry[n] = {
            bots: bots.length, tick: d1.tick, cpuCores: cpuNs / wall, packets, packetsPerSec: packets / (wall / 1e9),
            cpuNsPerPacket: cpuNs / Math.max(1, packets), pandaeyeThreadsCpuNs: pe,
            costOutAfterEncoderNs: d1.costs['cost.out.after_encoder_ns'], costInDecoderChainNs: d1.costs['cost.in.decoder_chain_ns'],
            kicked: bots.filter((b) => b.kicked).map((b) => b.username),
            status: profile === 'pandaeye' ? await pandaeyeStatus(server) : undefined
          }
          log(`  load ${flavor}/${profile} ${n} bots: TPS(global) ${d1.tick.global_region.tps?.toFixed(2)} region ${d1.tick.player_regions.tps?.toFixed(2)} CPU ${(cpuNs / wall).toFixed(2)} cores, ${Math.round(packets / (wall / 1e9))} pkt/s`)
          await stopWalkers(bots)
          await sleep(3000)
        }
      })
    }
  }
  return res
}

async function phaseDetect () {
  const jar = needPandaEye('detect')
  if (!jar) return { skipped: 'PandaEye jar missing' }
  const res = { flavor: O.flavor, cheats: [], legit: [] }
  const cloud = await maybeCloud('detect')
  try {
    await withServer({ name: `${O.flavor}-pe`, flavor: O.flavor, profile: 'pandaeye', port: PORTS[O.flavor] + 10, extra: ['--pandaeye-jar', jar] }, async (server) => {
      if (!server) { log('DRY: run every cheat and legit scenario; read plugins/PandaEye/logs/flags-*.jsonl, GET /api/v1/verdicts, pandaeye replay'); return }
      const onEach = async (rec, srv) => {
        await sleep(12000) // flag aggregation is 5 s; cloud pipeline latency
        const players = rec.suspects.flatMap((p) => [p, offlineUuid(p)])
        rec.flags = PE.flagsFor(srv.dir, players, rec.startMs - 1000, Date.now())
        rec.verdicts = []
        rec.signals = []
        if (cloud) {
          for (const p of rec.suspects) {
            try { rec.verdicts.push(...await cloud.verdicts({ player: offlineUuid(p), since: rec.startMs - 1000 })) } catch (e) { rec.cloudError = e.message }
          }
          rec.replayWindow = { since: rec.startMs - 1000, until: Date.now() }
        }
        const seen = new Set([...rec.flags.filter((f) => f.confirmed).map((f) => f.check), ...rec.verdicts.flatMap(verdictChecks), ...rec.signals.map((s) => s.check)])
        rec.observedChecks = [...seen].filter(Boolean).sort()
        if (rec.kind === 'cheat') {
          rec.detected = rec.checks.filter((c) => seen.has(c))
          rec.missed = rec.checks.filter((c) => !seen.has(c))
          rec.detectOk = rec.missed.length === 0
        } else {
          rec.confirmedFlags = rec.flags.filter((f) => f.confirmed)
          rec.detectOk = rec.confirmedFlags.length === 0 && rec.serverKicks.length === 0 && rec.verdicts.length === 0
        }
        log(`    observed: ${rec.observedChecks.join(', ') || '-'}${rec.kind === 'cheat' ? ' missed: ' + (rec.missed.join(', ') || '-') : ''}`)
      }
      const sel = O.scenarios === 'legit' ? listScenarios('all') : listScenarios(O.scenarios)
      res.legit = await runSuite(server, sel.filter((m) => m.kind === 'legit'), { tag: 'detect-legit', onEach })
      res.cheats = await runSuite(server, sel.filter((m) => m.kind === 'cheat'), { tag: 'detect-cheat', trace: true, onEach })
      res.status = await pandaeyeStatus(server)
      res.peaks = PE.peaks(server.dir).length
    })
  } finally { if (cloud) await cloud.stop() }
  // cloud signals per scenario window with `pandaeye replay` (needs the stopped `all` process' data dir)
  if (cloud) {
    for (const rec of [...res.legit, ...res.cheats]) {
      if (!rec.replayWindow) continue
      for (const p of rec.suspects) {
        const rp = await cloud.replay({ player: offlineUuid(p), ...rec.replayWindow })
        rec.signals.push(...rp.signals)
        if (rp.error) rec.replayError = rp.error
      }
      const seen = new Set([...(rec.observedChecks || []), ...rec.signals.map((s) => s.check)])
      rec.observedChecks = [...seen].filter(Boolean).sort()
      if (rec.kind === 'cheat') {
        rec.detected = rec.checks.filter((c) => seen.has(c))
        rec.missed = rec.checks.filter((c) => !seen.has(c))
        rec.detectOk = rec.missed.length === 0
      }
    }
  }
  return res
}

// ---------------------------------------------------------------------------------------------------------------
async function main () {
  fs.mkdirSync(O.reportDir, { recursive: true })
  logStream = fs.createWriteStream(path.join(O.reportDir, 'orchestrator.log'), { flags: 'a' })
  REPORT.artifacts = artifacts()
  log(`report dir ${O.reportDir}`)
  log(`artifacts: probe=${!!REPORT.artifacts.probeJar} pandaeye=${REPORT.artifacts.pandaeyeJar || 'missing'} cloud=${REPORT.artifacts.cloudBin || 'missing'}`)
  if (!REPORT.artifacts.probeJar && !O.dryRun) throw new Error('probe plugin not built (test/e2e/probe-plugin/build.sh)')
  let phases = O.phases
  if (phases.includes('all')) phases = ['baseline', 'cheats', 'shadow', 'hotload', 'cycles', 'ondisable', 'load', 'detect']
  const impl = { baseline: phaseBaseline, cheats: phaseCheats, shadow: () => phaseShadow(false), hotload: () => phaseShadow(true), cycles: phaseCycles, ondisable: phaseOnDisable, load: phaseLoad, detect: phaseDetect }
  for (const p of phases) {
    if (!impl[p]) throw new Error('unknown phase ' + p)
    log(`=== phase ${p}${O.dryRun ? ' (dry run)' : ''}`)
    const t0 = Date.now()
    try {
      REPORT.phases[p] = await impl[p]()
    } catch (e) {
      REPORT.phases[p] = { error: String(e.stack || e) }
      log(`phase ${p} failed: ${e.message}`)
    }
    REPORT.phases[p].durationS = Math.round((Date.now() - t0) / 1000)
    writeReport(O.reportDir, REPORT)
  }
  REPORT.finishedAt = new Date().toISOString()
  const verdict = writeReport(O.reportDir, REPORT)
  log(`report: ${path.join(O.reportDir, 'report.md')}  overall: ${verdict}`)
  logStream.end()
  process.exit(verdict === 'FAIL' ? 1 : 0)
}

main().catch((e) => { console.error(e); process.exit(2) })
