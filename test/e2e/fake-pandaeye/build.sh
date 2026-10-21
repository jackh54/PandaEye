#!/usr/bin/env bash
# Build the fake PandaEye test double -> build/libs/PandaEye-0.0.0-e2efake.jar
set -euo pipefail
cd "$(dirname "$0")"
GRADLE="${GRADLE:-$(command -v gradle || echo /opt/gradle/bin/gradle)}"
"$GRADLE" --no-daemon -q build "$@"
ls -l build/libs/PandaEye-0.0.0-e2efake.jar
