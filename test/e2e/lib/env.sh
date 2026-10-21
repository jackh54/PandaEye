# shellcheck shell=bash
# Common paths and defaults for the PandaEye e2e harness. Source it; every value can be overridden by env.
E2E_DIR="${E2E_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
REPO_DIR="${REPO_DIR:-$(cd "$E2E_DIR/../.." && pwd)}"
RUN_DIR="${PE_E2E_RUN_DIR:-$E2E_DIR/run}"            # gitignored: servers, caches, reports
VENDOR_DIR="${PE_VENDOR_DIR:-/home/user/pe-vendor}"   # downloaded server jars and plugins (never committed)
SERVERS_DIR="$RUN_DIR/servers"
CACHE_DIR="$RUN_DIR/cache"                             # shared paperclip bundler repo (mojang jar, libraries)

# Vendor artifacts (override with PE_JAR_PAPER etc.)
PE_JAR_PAPER="${PE_JAR_PAPER:-$VENDOR_DIR/servers/paper-1.21.11-132.jar}"
PE_JAR_FOLIA="${PE_JAR_FOLIA:-$VENDOR_DIR/servers/folia-1.21.11-14.jar}"
PE_JAR_CANVAS="${PE_JAR_CANVAS:-$VENDOR_DIR/servers/canvas-1.21.11-794.jar}"
PE_PLUGIN_PACKETEVENTS="${PE_PLUGIN_PACKETEVENTS:-$VENDOR_DIR/plugins/packetevents-spigot-2.13.1-SNAPSHOT-b906.jar}"
PE_PLUGIN_VIAVERSION="${PE_PLUGIN_VIAVERSION:-$VENDOR_DIR/plugins/viaversion-5.12.0.jar}"
PE_PLUGIN_VIABACKWARDS="${PE_PLUGIN_VIABACKWARDS:-$VENDOR_DIR/plugins/viabackwards-5.12.0.jar}"
PE_PLUGIN_PLUGMAN="${PE_PLUGIN_PLUGMAN:-$VENDOR_DIR/plugins/plugmanx-3.2.1.jar}"
PE_PLUGIN_KBSYNC="${PE_PLUGIN_KBSYNC:-$VENDOR_DIR/plugins/knockbacksync-1.3.6-dev-b143.jar}"

# Probe plugin (built from test/e2e/probe-plugin)
PE_PROBE_JAR="${PE_PROBE_JAR:-$E2E_DIR/probe-plugin/build/libs/PandaEyeProbe.jar}"

# PandaEye artifacts (built by the plugin/cloud engineers; optional)
PE_PANDAEYE_JAR_GLOB="${PE_PANDAEYE_JAR_GLOB:-$REPO_DIR/plugin/paper/build/libs/PandaEye-*.jar}"
PE_CLOUD_BIN="${PE_CLOUD_BIN:-$REPO_DIR/cloud/bin/pandaeye}"
PE_CLOUD_GATEWAY="${PE_CLOUD_GATEWAY:-ws://127.0.0.1:8080/v1/connect}"
PE_CLOUD_API="${PE_CLOUD_API:-http://127.0.0.1:8081}"
PE_E2E_LICENSE="${PE_E2E_LICENSE:-pe-e2e-static-license-0000000000000001}"

# JVM sizing: keep servers small, 4 CPUs are shared
PE_E2E_XMX="${PE_E2E_XMX:-1536M}"
PE_E2E_CPUS="${PE_E2E_CPUS:-2}"

find_pandaeye_jar() {
  local f
  # newest PandaEye-*.jar that is not a sources/javadoc/plain jar
  for f in $(ls -t $PE_PANDAEYE_JAR_GLOB 2>/dev/null); do
    case "$f" in *-sources.jar|*-javadoc.jar|*-plain.jar) continue ;; esac
    echo "$f"; return 0
  done
  return 1
}

server_jar_for() {
  case "$1" in
    paper) echo "$PE_JAR_PAPER" ;;
    folia) echo "$PE_JAR_FOLIA" ;;
    canvas) echo "$PE_JAR_CANVAS" ;;
    *) echo "unknown flavor $1" >&2; return 1 ;;
  esac
}

die() { echo "error: $*" >&2; exit 1; }
log() { echo "[e2e $(date +%H:%M:%S)] $*" >&2; }
