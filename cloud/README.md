# PandaEye cloud

Go module `github.com/jackh54/PandaEye/cloud`: one binary, `pandaeye`, with a subcommand per role.
The contract is [`../docs/ARCHITECTURE.md`](../docs/ARCHITECTURE.md) section 5; the checks are in
[`../docs/CHECKS.md`](../docs/CHECKS.md).

Requirements: Go 1.26 (`toolchain go1.26.10`). Regenerating protobuf code also needs `protoc` 36.x and
`protoc-gen-go` v1.36.12.

## Build

```sh
cd cloud
go build -ldflags "-X main.version=$(git describe --tags --always)" -o pandaeye ./cmd/pandaeye
./pandaeye version
```

The generated protobuf code in `internal/wire/pandaeyev1` is committed. After changing
`proto/pandaeye/v1/*.proto`, run `scripts/gen-proto.sh` from the repository root (`java` or `all` as
argument also generates the Java lite classes).

## Test

```sh
cd cloud
go build ./... && go vet ./... && go test -race ./...
```

Store tests always run against sqlite. To also run them against PostgreSQL, point
`PE_TEST_POSTGRES_DSN` at an empty database (each test gets its own schema). A throwaway local server:

```sh
PGBIN=/usr/lib/postgresql/16/bin
PGDIR=$(mktemp -d)                       # as root: chown postgres "$PGDIR" and prefix the
                                         # three commands below with "runuser -u postgres --"
$PGBIN/initdb -D "$PGDIR/data" -A trust -U pe >/dev/null
$PGBIN/pg_ctl -D "$PGDIR/data" -o "-p 55432 -k $PGDIR -c listen_addresses=127.0.0.1" -l "$PGDIR/log" -w start
$PGBIN/createdb -h 127.0.0.1 -p 55432 -U pe pandaeye_test

PE_TEST_POSTGRES_DSN="postgres://pe@127.0.0.1:55432/pandaeye_test?sslmode=disable" \
  go test -race -run 'TestPostgres|TestSQLite' -v ./internal/store/sqlstore/

$PGBIN/pg_ctl -D "$PGDIR/data" stop && rm -rf "$PGDIR"
```

Set `PE_TEST_VERBOSE=1` to see logs of the stream and testkit tests. Fuzz the frame decoder with
`go test -fuzz FuzzDecode ./internal/codec/`.

## Run locally (`pandaeye all`)

`all` runs the embedded, in-process NATS server (no listening port), the gateway, worker, verdict
service and API in one process, with sqlite. It migrates the database, and generates
`PE_OUTBOUND_KEY` and `PE_LICENSE_PEPPER` once and keeps them in the database.

```sh
mkdir -p data
PE_STATIC_LICENSES="dev-license-key:5:pro" \
PE_LOG_LEVEL=debug \
./pandaeye all
```

Defaults: plugins connect to `ws://localhost:8080/v1/connect`, the API listens on `:8081`, metrics and
pprof on `127.0.0.1:9090`, data lives in `./data` (`pandaeye.db`, `nats/`). Stop with Ctrl-C (graceful
shutdown within 10 s). Every key of ARCHITECTURE 5.1 can be set as an environment variable or in a YAML
file passed with `--config` (keys in lower case with dots, nesting allowed):

```yaml
gateway.listen: ":8080"
nats:
  dir: /var/lib/pandaeye/nats
static.licenses: "dev-license-key:5:pro"
```

Two keys beyond the 5.1 table: `PE_OWNER_ID` (stable owner id in lease tokens, default
`<role>-<hostname>`) and `PE_LEASES` (`auto` = fair share of the partitions, the default outside `all`;
`all` = every partition, the default in `all`; or a list such as `0-7,12`).

Other roles need PostgreSQL (`PE_DB_DRIVER=postgres`, run `pandaeye migrate` first) and an external NATS
(`PE_NATS_URL`, with credentials and TLS unless loopback); gateway, verdict and API also need
`PE_OUTBOUND_KEY` (rotate with `PE_OUTBOUND_KEY_NEXT`), gateway and API `PE_LICENSE_PEPPER`. Every
process recreates missing `PE_TENANTS` entries from the database at start.

Status: every role is implemented. `pandaeye all` runs embedded NATS, gateway, worker, verdict and API
in one process; the ML scorer is disabled unless built with `-tags onnx` and given `PE_ML_MODEL`.

## Layout

| Package | Purpose |
|---|---|
| `cmd/pandaeye` | subcommands, signal handling |
| `internal/app` | `Deps` (config, logger, metrics, store, bus, secrets), `Service`, `Run` (graceful shutdown) |
| `internal/config` | PE_* / YAML loading and validation |
| `internal/codec` | WebSocket frame codecs with hard limits, protowire pre-scan, field limits |
| `internal/identity` | ids, secrets, HMAC helpers, SignedOutbound, siphash partitioning, verdict ids |
| `internal/geom` | vectors, boxes, rays, look vectors, eye heights |
| `internal/metrics` | Prometheus collectors, `/metrics` + pprof server |
| `internal/netutil` | trusted proxies (Cloudflare preset), client IP extraction |
| `internal/store`, `internal/store/sqlstore` | `store.Store` and its postgres/sqlite implementation, migrations |
| `internal/store/storetest` | the conformance suite every `store.Store` must pass |
| `internal/stream` | NATS JetStream: provisioning, subjects, publishing, leases, consumer epochs, consume loop, replay, KV |
| `internal/checks` | the scoring table (`catalog.go`), the `Detector`/`View` contract and the cloud checks (combat, velocity, autoclicker, scaffold, dig, inventory, latency, forwarded local flags) |
| `internal/testkit` | fake plugin client, event builders, `NewStore`/`NewBus`/`NewDeps` |
| `internal/wire/pandaeyev1` | generated protobuf |
| `internal/gateway` | plugin WebSocket gateway: handshake, batches, backpressure, outbound verdicts, `/status` |
| `internal/license` | licence verifiers (static, pandascript) and the tenant resolver |
| `internal/worker` | partition workers: sessions, dedup, warm start, cloud checks, clips, feature windows, replay |
| `internal/verdict` | scoring with leniency, verdict records, store writer, Discord alerts, reputation |
| `internal/api` | REST API (docs/API.md), metering and janitor jobs |
| `internal/discord` | SSRF-safe webhook client with per-tenant budgets |
| `internal/telemetry` | ClickHouse sink (sessions, signals, feature windows, probes) or a no-op |
| `internal/ml` | the 94-feature v1 vector and the optional ONNX scorer |

Service packages export `New(d *app.Deps) (*Service, error)` and `(*Service) Run(ctx) error`;
component packages (`license`, `telemetry`, `ml`) export `New(d *app.Deps)` and share one instance per
process through `Deps.Shared`. `internal/app` never imports them; `cmd/pandaeye` wires everything.
