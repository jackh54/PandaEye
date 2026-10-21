#!/usr/bin/env bash
# Check that everything docker-compose.yml needs exists before `docker compose up`:
# deploy/.env with the required secrets set, development certificates, NATS creds and the schema.
#
# Usage: deploy/scripts/preflight.sh [deploy-dir]   (default: the directory above this script)
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
dir="${1:-${script_dir}/..}"
status=0

need_file() {
  if [[ ! -s "${dir}/$1" ]]; then
    echo "missing: deploy/$1 ($2)" >&2
    status=1
  fi
}

need_file .env "make -C deploy env"
need_file certs/ca.pem "make -C deploy certs"
need_file certs/nats.pem "make -C deploy certs"
need_file certs/nats-key.pem "make -C deploy certs"
need_file nats/auth/server/resolver.conf "make -C deploy nats-auth"
for role in gateway worker verdict api; do
  need_file "nats/auth/creds/pe-${role}.creds" "make -C deploy nats-auth"
done
need_file clickhouse/init.sql "part of the repository"

if [[ -s "${dir}/.env" ]]; then
  for key in PE_OUTBOUND_KEY PE_LICENSE_PEPPER POSTGRES_PASSWORD CLICKHOUSE_PASSWORD; do
    value="$(sed -n "s/^${key}=//p" "${dir}/.env" | tail -n 1)"
    if [[ -z "${value}" ]]; then
      echo "deploy/.env: ${key} is not set" >&2
      status=1
    elif [[ "${key}" == PE_* && "${#value}" -lt 32 ]]; then
      echo "deploy/.env: ${key} must be at least 32 bytes" >&2
      status=1
    fi
  done
  mode="$(stat -c '%a' "${dir}/.env" 2>/dev/null || stat -f '%Lp' "${dir}/.env")"
  if [[ "${mode}" != "600" && "${mode}" != "400" ]]; then
    echo "warning: deploy/.env has mode ${mode}; chmod 600 it" >&2
  fi
  if ! grep -q '^PE_STATIC_LICENSES=.' "${dir}/.env" && ! grep -q '^PE_LICENSE_MODE=pandascript' "${dir}/.env"; then
    echo "warning: no PE_STATIC_LICENSES and PE_LICENSE_MODE is not pandascript: every Hello will be rejected" >&2
  fi
fi

if [[ "${status}" -eq 0 ]]; then
  echo "preflight ok"
fi
exit "${status}"
