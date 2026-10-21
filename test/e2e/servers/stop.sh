#!/usr/bin/env bash
# Stop a server gracefully ("stop" on the console), then SIGTERM/SIGKILL after the timeout.
#   stop.sh NAME [TIMEOUT_S=60]
set -uo pipefail
source "$(dirname "$0")/../lib/env.sh"
NAME="${1:?usage: stop.sh NAME [TIMEOUT]}"; TIMEOUT="${2:-60}"
DIR="$SERVERS_DIR/$NAME"
PID="$(cat "$DIR/server.pid" 2>/dev/null || true)"
WPID="$(cat "$DIR/wrapper.pid" 2>/dev/null || true)"
if [[ -z "$PID" ]] || ! kill -0 "$PID" 2>/dev/null; then log "$NAME not running"; exit 0; fi
timeout 5 bash -c 'printf "stop\n" > "$1"' _ "$DIR/console.in" 2>/dev/null || kill -TERM "$PID" 2>/dev/null || true
for _ in $(seq 1 $((TIMEOUT * 2))); do kill -0 "$PID" 2>/dev/null || break; sleep 0.5; done
if kill -0 "$PID" 2>/dev/null; then log "$NAME did not stop in ${TIMEOUT}s: SIGTERM"; kill -TERM "$PID" 2>/dev/null; sleep 10; fi
if kill -0 "$PID" 2>/dev/null; then log "$NAME still alive: SIGKILL"; kill -KILL "$PID" 2>/dev/null; fi
for _ in $(seq 1 20); do [[ -n "$WPID" ]] && kill -0 "$WPID" 2>/dev/null || break; sleep 0.5; done
log "stopped $NAME"
