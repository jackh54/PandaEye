#!/usr/bin/env bash
# Copy everything worth keeping from a server dir into OUTDIR:
# console output, latest.log, crash reports, probe dumps, arena.json, PandaEye logs (flags/peaks jsonl) and files.
#   collect-logs.sh NAME OUTDIR
set -uo pipefail
source "$(dirname "$0")/../lib/env.sh"
NAME="${1:?usage: collect-logs.sh NAME OUTDIR}"; OUT="${2:?}"
DIR="$SERVERS_DIR/$NAME"
mkdir -p "$OUT"
cp -f "$DIR/logs/console.out" "$DIR/logs/latest.log" "$DIR/logs/commands.log" "$OUT/" 2>/dev/null
cp -f "$DIR/arena.json" "$DIR/e2e-server.env" "$DIR/server.properties" "$OUT/" 2>/dev/null
[[ -d "$DIR/crash-reports" ]] && cp -rf "$DIR/crash-reports" "$OUT/"
[[ -d "$DIR/probe" ]] && mkdir -p "$OUT/probe" && cp -rf "$DIR/probe/." "$OUT/probe/"
if [[ -d "$DIR/plugins/PandaEye" ]]; then
  mkdir -p "$OUT/pandaeye"
  cp -rf "$DIR/plugins/PandaEye/logs" "$OUT/pandaeye/" 2>/dev/null
  cp -f "$DIR/plugins/PandaEye/config.yml" "$DIR/plugins/PandaEye/state.properties" \
        "$DIR/plugins/PandaEye/remote-config.properties" "$DIR/plugins/PandaEye/executed.json" "$OUT/pandaeye/" 2>/dev/null
  [[ -d "$DIR/plugins/PandaEye/spool" ]] && du -sb "$DIR/plugins/PandaEye/spool" > "$OUT/pandaeye/spool-size.txt"
fi
log "collected $NAME logs into $OUT"
