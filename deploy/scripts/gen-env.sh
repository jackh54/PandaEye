#!/usr/bin/env bash
# Create deploy/.env from deploy/.env.example with every secret set to a fresh random value.
# Refuses to overwrite an existing file.
#
# Usage: deploy/scripts/gen-env.sh [out-file]   (default: deploy/.env)
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
example="${script_dir}/../.env.example"
out="${1:-${script_dir}/../.env}"

command -v openssl >/dev/null 2>&1 || { echo "openssl is required" >&2; exit 1; }
if [[ -e "${out}" ]]; then
  echo "${out} exists; refusing to overwrite it" >&2
  exit 1
fi

keys=(PE_OUTBOUND_KEY PE_LICENSE_PEPPER PE_SERVICE_TOKEN PE_ASSERTION_KEY)
passwords=(POSTGRES_PASSWORD CLICKHOUSE_PASSWORD)

declare -A value
for k in "${keys[@]}"; do value["${k}"]="$(openssl rand -hex 32)"; done
for k in "${passwords[@]}"; do value["${k}"]="$(openssl rand -hex 24)"; done

umask 077
tmp="$(mktemp "${out}.XXXXXX")"
trap 'rm -f -- "${tmp}"' EXIT

# Replace each commented "# NAME=" placeholder line with "NAME=<random>"; copy everything else.
while IFS= read -r line || [[ -n "${line}" ]]; do
  if [[ "${line}" =~ ^#\ ([A-Z0-9_]+)=$ ]] && [[ -n "${value[${BASH_REMATCH[1]}]+set}" ]]; then
    printf '%s=%s\n' "${BASH_REMATCH[1]}" "${value[${BASH_REMATCH[1]}]}"
  else
    printf '%s\n' "${line}"
  fi
done < "${example}" > "${tmp}"

for k in "${keys[@]}" "${passwords[@]}"; do
  grep -q "^${k}=" "${tmp}" || { echo "placeholder for ${k} not found in ${example}" >&2; exit 1; }
done

mv -- "${tmp}" "${out}"
trap - EXIT
echo "wrote ${out} (mode 600). Set PE_STATIC_LICENSES or the pandascript licence settings before starting."
