#!/usr/bin/env bash
# Show / follow a server's console output.   logs.sh NAME [-f] [-n LINES]
set -euo pipefail
source "$(dirname "$0")/../lib/env.sh"
NAME="${1:?usage: logs.sh NAME [-f] [-n N]}"; shift
exec tail "${@:--n100}" "$SERVERS_DIR/$NAME/logs/console.out"
