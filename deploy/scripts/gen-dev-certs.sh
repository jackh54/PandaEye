#!/usr/bin/env bash
# Generate a development CA and certificates for the docker-compose stack:
#   <out>/ca.pem                  CA certificate (mounted into PandaEye services as PE_NATS_TLS_CA)
#   <out>/nats.pem, nats-key.pem  NATS client-port certificate (nats, localhost, 127.0.0.1, pe-nats-N)
#   <out>/route.pem, route-key.pem  NATS cluster-route certificate (server + client auth, *.internal)
#   <out>/private/ca-key.pem      CA key (never mounted into a container)
# Development only: production certificates come from your own CA or ACME.
#
# Usage: deploy/scripts/gen-dev-certs.sh [--force] [out-dir]   (default out-dir: deploy/certs)
set -euo pipefail

force=0
if [[ "${1:-}" == "--force" ]]; then
  force=1
  shift
fi
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
out="${1:-${script_dir}/../certs}"

command -v openssl >/dev/null 2>&1 || { echo "openssl is required" >&2; exit 1; }

mkdir -p "${out}/private"
chmod 700 "${out}/private"
if [[ -e "${out}/ca.pem" && "${force}" -eq 0 ]]; then
  echo "${out}/ca.pem exists; pass --force to replace all certificates" >&2
  exit 1
fi

tmp="$(mktemp -d)"
trap 'rm -rf -- "${tmp}"' EXIT
umask 077

days_ca=3650
days_leaf=397

openssl req -x509 -new -nodes -sha256 -days "${days_ca}" \
  -newkey ec -pkeyopt ec_paramgen_curve:P-256 \
  -keyout "${out}/private/ca-key.pem" -out "${out}/ca.pem" \
  -subj "/CN=PandaEye development CA" \
  -addext "basicConstraints=critical,CA:TRUE" \
  -addext "keyUsage=critical,keyCertSign,cRLSign" 2>/dev/null

# leaf <name> <common name> <subjectAltName> <extendedKeyUsage>
leaf() {
  local name="$1" cn="$2" san="$3" eku="$4" serial
  serial="$(openssl rand -hex 16)"
  openssl req -new -nodes -sha256 \
    -newkey ec -pkeyopt ec_paramgen_curve:P-256 \
    -keyout "${out}/${name}-key.pem" -out "${tmp}/${name}.csr" \
    -subj "/CN=${cn}" 2>/dev/null
  printf 'basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature\nextendedKeyUsage=%s\nsubjectAltName=%s\n' \
    "${eku}" "${san}" > "${tmp}/${name}.ext"
  openssl x509 -req -sha256 -days "${days_leaf}" \
    -in "${tmp}/${name}.csr" -CA "${out}/ca.pem" -CAkey "${out}/private/ca-key.pem" \
    -set_serial "0x${serial}" \
    -extfile "${tmp}/${name}.ext" -out "${out}/${name}.pem" 2>/dev/null
}

leaf nats "nats" \
  "DNS:nats,DNS:localhost,DNS:pe-nats-1,DNS:pe-nats-2,DNS:pe-nats-3,IP:127.0.0.1" \
  "serverAuth"
leaf route "pe-nats-route" \
  "DNS:pe-nats-1.internal,DNS:pe-nats-2.internal,DNS:pe-nats-3.internal,DNS:nats,DNS:localhost" \
  "serverAuth,clientAuth"

# Certificates are public. The NATS container reads the leaf keys as root; PandaEye services only
# need ca.pem (readable by the distroless nonroot user, uid 65532).
chmod 644 "${out}/ca.pem" "${out}/nats.pem" "${out}/route.pem"
chmod 600 "${out}/nats-key.pem" "${out}/route-key.pem" "${out}/private/ca-key.pem"

openssl verify -CAfile "${out}/ca.pem" "${out}/nats.pem" "${out}/route.pem"
echo "certificates written to ${out}"
