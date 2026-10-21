#!/usr/bin/env bash
# PandaEye e2e harness entry point. See test/e2e/README.md.
#
#   test/e2e/run.sh [phase...] [orchestrator options]
#     phases: baseline (default) cheats shadow hotload cycles ondisable load detect all
#     options: --flavors paper,folia,canvas --flavor paper --repeat 2 --scenarios legit --quick --dry-run
#              --bots 20,40 --keep-servers --report-dir DIR --cycles 5
#   test/e2e/run.sh setup        build the probe plugin, install bot deps, patch server jars into the cache
#
# Examples:
#   test/e2e/run.sh baseline --flavors paper,folia,canvas      # legit suite without PandaEye, twice per flavor
#   test/e2e/run.sh cheats --flavor paper                      # every cheat client once, packet patterns verified
#   test/e2e/run.sh all --dry-run                              # show every step, including PandaEye/cloud ones
#   PE_PANDAEYE_JAR=/path/PandaEye.jar test/e2e/run.sh shadow hotload cycles ondisable --flavors paper,folia
set -euo pipefail
E2E_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$E2E_DIR/lib/env.sh"

setup() {
  command -v java >/dev/null || die "java not found (Java 21+ required)"
  command -v node >/dev/null || die "node not found (Node 22 required)"
  if [[ ! -f "$PE_PROBE_JAR" || -n "$(find "$E2E_DIR/probe-plugin/src" -newer "$PE_PROBE_JAR" -type f 2>/dev/null | head -1)" ]]; then
    log "building probe plugin"; "$E2E_DIR/probe-plugin/build.sh" >/dev/null
  fi
  if [[ ! -d "$E2E_DIR/bots/node_modules/mineflayer" ]]; then
    log "installing bot dependencies"
    (cd "$E2E_DIR/bots" && if [[ -f package-lock.json ]]; then npm ci --no-audit --no-fund; else npm install --no-audit --no-fund; fi) >/dev/null
  fi
  for j in "$PE_JAR_PAPER" "$PE_JAR_FOLIA" "$PE_JAR_CANVAS" "$PE_PLUGIN_PACKETEVENTS" "$PE_PLUGIN_VIAVERSION" "$PE_PLUGIN_VIABACKWARDS" "$PE_PLUGIN_PLUGMAN" "$PE_PLUGIN_KBSYNC"; do
    [[ -f "$j" ]] || die "vendor file missing: $j (set PE_VENDOR_DIR)"
  done
  "$E2E_DIR/servers/prepare-cache.sh" >/dev/null
}

if [[ "${1:-}" == setup ]]; then setup; log "setup done"; exit 0; fi
if [[ " $* " != *" --dry-run "* ]]; then setup; fi
exec node "$E2E_DIR/orchestrator/orchestrate.js" "$@"
