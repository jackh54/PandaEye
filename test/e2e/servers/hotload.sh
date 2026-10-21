#!/usr/bin/env bash
# Hot-load PandaEye into a running server that was provisioned with --profile hotload:
# copies staged/PandaEye.jar into plugins/ and runs `plugman load PandaEye`, then waits for the enable line.
#   hotload.sh NAME [TIMEOUT_S=60]
set -euo pipefail
source "$(dirname "$0")/../lib/env.sh"
NAME="${1:?usage: hotload.sh NAME}"; T="${2:-60}"
DIR="$SERVERS_DIR/$NAME"
[[ -f "$DIR/staged/PandaEye.jar" ]] || die "nothing staged in $DIR/staged (provision with --profile hotload)"
cp "$DIR/staged/PandaEye.jar" "$DIR/plugins/PandaEye.jar"
"$E2E_DIR/servers/cmd.sh" "$NAME" --wait '\[PandaEye\] (Enabling|Enabled)|PandaEye.*(enabled|loaded)|Could not load|Error occurred' "$T" plugman load PandaEye
