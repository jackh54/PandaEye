#!/usr/bin/env bash
# Wait until a regex (grep -E) appears in the server's console output.
#   wait-for.sh NAME REGEX [TIMEOUT_S=60] [FROM_BYTE_OFFSET=0]
# Prints the first matching line. Exit 0 = found, 1 = timeout, 2 = server process died.
set -uo pipefail
source "$(dirname "$0")/../lib/env.sh"
NAME="${1:?}"; RE="${2:?}"; TIMEOUT="${3:-60}"; FROM="${4:-0}"
OUT="$SERVERS_DIR/$NAME/logs/console.out"
PIDF="$SERVERS_DIR/$NAME/server.pid"
deadline=$(( $(date +%s) + TIMEOUT ))
while :; do
  if [[ -f "$OUT" ]]; then
    line="$(tail -c +"$((FROM + 1))" "$OUT" | grep -E -m1 -- "$RE" || true)"
    if [[ -n "$line" ]]; then echo "$line"; exit 0; fi
  fi
  if [[ ! -f "$PIDF" ]] || ! kill -0 "$(cat "$PIDF" 2>/dev/null)" 2>/dev/null; then
    echo "server $NAME is not running" >&2; exit 2
  fi
  (( $(date +%s) >= deadline )) && { echo "timeout waiting for /$RE/ on $NAME" >&2; exit 1; }
  sleep 0.5
done
