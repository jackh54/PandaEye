#!/usr/bin/env bash
# Start a provisioned server headless (console via FIFO, see console-wrapper.py).
#   start.sh NAME [--wait [TIMEOUT_S]]     --wait blocks until "Done (" appears (default 180 s)
set -euo pipefail
source "$(dirname "$0")/../lib/env.sh"
NAME="${1:?usage: start.sh NAME [--wait [TIMEOUT]]}"; shift
DIR="$SERVERS_DIR/$NAME"
[[ -f "$DIR/e2e-server.env" ]] || die "not provisioned: $NAME"
source "$DIR/e2e-server.env"
if [[ -f "$DIR/server.pid" ]] && kill -0 "$(cat "$DIR/server.pid")" 2>/dev/null; then
  die "already running (pid $(cat "$DIR/server.pid"))"
fi
rm -f "$DIR/server.pid" "$DIR/wrapper.pid"
mkdir -p "$DIR/logs"
# console.out offset at start: wait-for only looks at output of this run
: > "$DIR/logs/console.out"

JVM=(java -Xms256M "-Xmx$XMX" -XX:+UseG1GC -XX:MaxGCPauseMillis=100 "-XX:ActiveProcessorCount=$CPUS"
     -Djava.awt.headless=true "-DbundlerRepoDir=$CACHE_DIR" -Dterminal.jline=false -Dterminal.ansi=false
     -Dnet.kyori.ansi.colorLevel=none
     ${PE_E2E_JVM_EXTRA:-} -jar "$SERVER_JAR" --nogui)
nohup python3 "$E2E_DIR/servers/console-wrapper.py" --dir "$DIR" -- "${JVM[@]}" > "$DIR/logs/wrapper.out" 2>&1 &
for _ in $(seq 1 50); do [[ -f "$DIR/server.pid" ]] && break; sleep 0.1; done
[[ -f "$DIR/server.pid" ]] || die "wrapper did not start (see $DIR/logs/wrapper.out)"
log "started $NAME pid $(cat "$DIR/server.pid") ($FLAVOR, port $PORT)"
if [[ "${1:-}" == --wait ]]; then
  exec "$E2E_DIR/servers/wait-for.sh" "$NAME" 'Done \(' "${2:-180}"
fi
