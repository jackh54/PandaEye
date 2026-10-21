#!/usr/bin/env bash
# Send a console command to a running server (through the console FIFO).
#   cmd.sh NAME command words...         e.g. cmd.sh paper1 probe dump probe/stats.json
#   cmd.sh NAME --wait 'REGEX' [TIMEOUT] command...   send, then wait for REGEX after the send point
set -euo pipefail
source "$(dirname "$0")/../lib/env.sh"
NAME="${1:?usage: cmd.sh NAME [--wait REGEX [TIMEOUT]] command...}"; shift
DIR="$SERVERS_DIR/$NAME"
WAIT_RE="" WAIT_T=30
if [[ "${1:-}" == --wait ]]; then WAIT_RE="$2"; shift 2; if [[ "${1:-}" =~ ^[0-9]+$ ]]; then WAIT_T="$1"; shift; fi; fi
[[ -p "$DIR/console.in" ]] || die "no console FIFO for $NAME (not started?)"
[[ -f "$DIR/server.pid" ]] || die "$NAME is not running"
OFF=$(stat -c %s "$DIR/logs/console.out" 2>/dev/null || echo 0)
timeout 5 bash -c 'printf "%s\n" "$1" > "$2"' _ "$*" "$DIR/console.in" || die "console FIFO write timed out"
if [[ -n "$WAIT_RE" ]]; then
  exec "$E2E_DIR/servers/wait-for.sh" "$NAME" "$WAIT_RE" "$WAIT_T" "$OFF"
fi
