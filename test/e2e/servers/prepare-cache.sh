#!/usr/bin/env bash
# Patch every server jar once into the shared bundler cache (mojang jar + libraries), so the first real start
# of each flavor does not download/patch while the clock of a test runs.   prepare-cache.sh [paper folia canvas]
set -euo pipefail
source "$(dirname "$0")/../lib/env.sh"
mkdir -p "$CACHE_DIR"
for f in "${@:-paper folia canvas}"; do
  for flavor in $f; do
    jar="$(server_jar_for "$flavor")"
    name="$(basename "$jar" .jar)"
    if ls "$CACHE_DIR"/versions/*/"${flavor}"-*.jar >/dev/null 2>&1; then log "$flavor already patched"; continue; fi
    log "patching $name"
    (cd "$CACHE_DIR" && java -DbundlerRepoDir="$CACHE_DIR" -Dpaperclip.patchonly=true -jar "$jar" >/dev/null 2>&1) || die "patch failed for $flavor"
  done
done
ls "$CACHE_DIR/versions"/*/ 2>/dev/null
