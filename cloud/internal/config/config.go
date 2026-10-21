// Package config loads and validates the cloud configuration (ARCHITECTURE 5.1): PE_* environment
// variables, optionally a YAML file (--config) whose keys are the variable names in lower case with
// dots (PE_NATS_URL = nats.url; nested maps are flattened). Precedence: environment, file, defaults.
package config

import (
	"log/slog"
	"net/netip"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/identity"
)

// Role is the subcommand a process runs.
type Role string

const (
	RoleAll     Role = "all"
	RoleGateway Role = "gateway"
	RoleWorker  Role = "worker"
	RoleVerdict Role = "verdict"
	RoleAPI     Role = "api"
	RoleMigrate Role = "migrate"
	RoleReplay  Role = "replay"
)

// Valid reports whether r is a known role.
func (r Role) Valid() bool {
	switch r {
	case RoleAll, RoleGateway, RoleWorker, RoleVerdict, RoleAPI, RoleMigrate, RoleReplay:
		return true
	}
	return false
}

// Service reports whether the role runs long-lived services (and so connects to NATS).
func (r Role) Service() bool { return r != RoleMigrate }

// Config is the validated configuration of one process.
type Config struct {
	Role Role

	GatewayListen  string         // PE_GATEWAY_LISTEN
	APIListen      string         // PE_API_LISTEN
	MetricsListen  string         // PE_METRICS_LISTEN; empty disables /metrics
	TLSCert        string         // PE_TLS_CERT
	TLSKey         string         // PE_TLS_KEY
	TrustedProxies []netip.Prefix // PE_TRUSTED_PROXIES
	PublicURL      string         // PE_PUBLIC_URL

	NATS NATS

	// OutboundKey signs SignedOutbound (PE_OUTBOUND_KEY). Empty only in `all`, where the app
	// generates one and persists it in the database.
	OutboundKey identity.Secret
	Partitions  int // PE_PARTITIONS

	DB            DB
	ClickHouseDSN identity.Secret // PE_CLICKHOUSE_DSN; empty disables telemetry
	License       License

	ServiceToken     identity.Secret // PE_SERVICE_TOKEN; empty disables dashboard access
	ServiceTokenNext identity.Secret // PE_SERVICE_TOKEN_NEXT
	AssertionKey     identity.Secret // PE_ASSERTION_KEY

	EventsMaxAge    time.Duration // PE_EVENTS_MAX_AGE
	EventsMaxBytes  int64         // PE_EVENTS_MAX_BYTES
	WorkerMaxMemory int64         // PE_WORKER_MAX_MEMORY
	Region          string        // PE_REGION
	LogLevel        slog.Level    // PE_LOG_LEVEL
	ML              ML

	// OwnerID is the stable owner id of this process in lease tokens (PE_OWNER_ID, default: the
	// role and host name). Only [A-Za-z0-9_-].
	OwnerID string
	// Leases selects the partitions this process takes leases for (PE_LEASES).
	Leases LeaseSelection
}

// NATS configures the event stream connection.
type NATS struct {
	URL      string          // PE_NATS_URL; empty in `all` = embedded server
	Creds    string          // PE_NATS_CREDS (file)
	Token    identity.Secret // PE_NATS_TOKEN
	TLSCA    string          // PE_NATS_TLS_CA (file)
	Dir      string          // PE_NATS_DIR, embedded JetStream storage
	Replicas int             // PE_NATS_REPLICAS
	Sync     string          // PE_NATS_SYNC: SyncDefault or SyncAlways
}

// Embedded reports whether the process runs the in-process NATS server.
func (n NATS) Embedded() bool { return n.URL == "" }

// PE_NATS_SYNC values.
const (
	SyncDefault = "default"
	SyncAlways  = "always"
)

// DB configures the relational store.
type DB struct {
	Driver string          // PE_DB_DRIVER: DriverSQLite or DriverPostgres
	DSN    identity.Secret // PE_DB_DSN (may carry a password)
}

// PE_DB_DRIVER values.
const (
	DriverSQLite   = "sqlite"
	DriverPostgres = "postgres"
)

// License configures licence verification.
type License struct {
	Mode    string          // PE_LICENSE_MODE: LicenseStatic or LicensePandascript
	URL     string          // PE_LICENSE_URL (https only)
	Product string          // PE_LICENSE_PRODUCT
	Pepper  identity.Secret // PE_LICENSE_PEPPER; empty only in `all` (generated, persisted in the DB)
	Static  []StaticLicense // PE_STATIC_LICENSES
}

// PE_LICENSE_MODE values.
const (
	LicenseStatic      = "static"
	LicensePandascript = "pandascript"
)

// StaticLicense is one entry of PE_STATIC_LICENSES (`key[:maxServers[:plan]]`).
type StaticLicense struct {
	Key        identity.Secret
	MaxServers int // 0 = unlimited
	Plan       string
}

// ML configures the optional model scorer.
type ML struct {
	Model  string  // PE_ML_MODEL
	Points float64 // PE_ML_POINTS
}

// LeaseMode is how a process picks partitions to lease.
type LeaseMode string

const (
	// LeaseAuto takes a fair share (ceil(partitions / live members)) of free partitions and releases
	// extras when other members join. Default outside `all`.
	LeaseAuto LeaseMode = "auto"
	// LeaseAll takes every partition. Default in `all`.
	LeaseAll LeaseMode = "all"
	// LeaseList takes only the listed partitions.
	LeaseList LeaseMode = "list"
)

// LeaseSelection is the parsed PE_LEASES value: "auto", "all" or a list like "0-3,7".
type LeaseSelection struct {
	Mode       LeaseMode
	Partitions []int // LeaseList only, sorted, unique
}
