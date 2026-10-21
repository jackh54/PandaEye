#!/usr/bin/env bash
# Build the probe plugin -> test/e2e/probe-plugin/build/libs/PandaEyeProbe.jar (uses the system Gradle 8.14).
set -euo pipefail
cd "$(dirname "$0")"
GRADLE="${GRADLE:-$(command -v gradle || echo /opt/gradle/bin/gradle)}"
"$GRADLE" --no-daemon -q build "$@"
ls -l build/libs/PandaEyeProbe.jar
