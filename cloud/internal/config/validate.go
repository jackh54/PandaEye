package config

import (
	"errors"
	"fmt"
	"math"
	"net"
	"net/netip"
	"net/url"
	"strings"

	"github.com/jackh54/PandaEye/cloud/internal/identity"
)

// MinKeyBytes is the minimum length of tokens and HMAC keys.
const MinKeyBytes = 32

// MaxPartitions bounds PE_PARTITIONS.
const MaxPartitions = 1024

// Validate checks the configuration for the process role.
func (c *Config) Validate() error {
	var errs []error
	bad := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	all := c.Role == RoleAll

	for _, l := range []struct{ name, addr string }{
		{"PE_GATEWAY_LISTEN", c.GatewayListen}, {"PE_API_LISTEN", c.APIListen}, {"PE_METRICS_LISTEN", c.MetricsListen},
	} {
		if l.addr == "" && l.name == "PE_METRICS_LISTEN" {
			continue
		}
		if _, _, err := net.SplitHostPort(l.addr); err != nil {
			bad("%s: %v", l.name, err)
		}
	}
	if (c.TLSCert == "") != (c.TLSKey == "") {
		bad("PE_TLS_CERT and PE_TLS_KEY must be set together")
	}
	if c.TrustedProxyPreset != "" && c.TrustedProxyPreset != PresetCloudflare {
		bad("PE_TRUSTED_PROXIES_PRESET must be empty or %s", PresetCloudflare)
	}
	if c.RequireTrustedProxy && c.TrustedProxies.Empty() {
		bad("PE_REQUIRE_TRUSTED_PROXY needs PE_TRUSTED_PROXIES or PE_TRUSTED_PROXIES_PRESET (it would refuse every peer)")
	}
	if c.PublicURL != "" {
		if u, err := url.Parse(c.PublicURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			bad("PE_PUBLIC_URL must be an absolute http(s) URL")
		}
	}

	errs = append(errs, c.validateNATS()...)

	for _, k := range []struct {
		name string
		v    identity.Secret
	}{{"PE_OUTBOUND_KEY", c.OutboundKey}, {"PE_OUTBOUND_KEY_NEXT", c.OutboundKeyNext}} {
		if !k.v.IsZero() && k.v.Len() < MinKeyBytes {
			bad("%s must be at least %d bytes", k.name, MinKeyBytes)
		}
	}
	if c.OutboundKey.IsZero() && (c.Role == RoleGateway || c.Role == RoleVerdict || c.Role == RoleAPI) {
		bad("PE_OUTBOUND_KEY is required for %s (at least %d bytes)", c.Role, MinKeyBytes)
	}
	if c.OutboundKey.IsZero() && !c.OutboundKeyNext.IsZero() && !all {
		bad("PE_OUTBOUND_KEY_NEXT requires PE_OUTBOUND_KEY")
	}
	if c.Partitions < 1 || c.Partitions > MaxPartitions {
		bad("PE_PARTITIONS must be 1..%d", MaxPartitions)
	}

	switch c.DB.Driver {
	case DriverPostgres:
	case DriverSQLite:
		if c.Role != RoleAll && c.Role != RoleMigrate && c.Role != RoleReplay {
			bad("PE_DB_DRIVER=sqlite is only allowed in `all` (use postgres for %s)", c.Role)
		}
	default:
		bad("PE_DB_DRIVER must be %s or %s", DriverSQLite, DriverPostgres)
	}
	if c.DB.DSN.IsZero() {
		bad("PE_DB_DSN is required")
	}

	errs = append(errs, c.validateLicense()...)

	for _, t := range []struct {
		name string
		n    int
	}{{"PE_SERVICE_TOKEN", c.ServiceToken.Len()}, {"PE_SERVICE_TOKEN_NEXT", c.ServiceTokenNext.Len()},
		{"PE_ASSERTION_KEY", c.AssertionKey.Len()}, {"PE_ASSERTION_KEY_NEXT", c.AssertionKeyNext.Len()}} {
		if t.n != 0 && t.n < MinKeyBytes {
			bad("%s must be empty or at least %d bytes", t.name, MinKeyBytes)
		}
	}
	if c.ServiceToken.IsZero() && !c.ServiceTokenNext.IsZero() {
		bad("PE_SERVICE_TOKEN_NEXT requires PE_SERVICE_TOKEN")
	}
	if c.AssertionKey.IsZero() && !c.AssertionKeyNext.IsZero() {
		bad("PE_ASSERTION_KEY_NEXT requires PE_ASSERTION_KEY")
	}
	if !c.ServiceToken.IsZero() && c.AssertionKey.IsZero() && (all || c.Role == RoleAPI) {
		bad("PE_ASSERTION_KEY is required when PE_SERVICE_TOKEN is set")
	}

	if c.EventsMaxAge <= 0 {
		bad("PE_EVENTS_MAX_AGE must be positive")
	}
	if c.EventsMaxBytes <= 0 {
		bad("PE_EVENTS_MAX_BYTES must be positive")
	}
	if c.WorkerMaxMemory <= 0 {
		bad("PE_WORKER_MAX_MEMORY must be positive")
	}
	if c.ML.Points < 0 || math.IsNaN(c.ML.Points) || math.IsInf(c.ML.Points, 0) {
		bad("PE_ML_POINTS must be a non-negative number")
	}
	if c.OwnerID == "" || SanitizeOwnerID(c.OwnerID) != c.OwnerID {
		bad("PE_OWNER_ID must be 1..64 characters of [A-Za-z0-9_-]")
	}
	for _, p := range c.Leases.Partitions {
		if p >= c.Partitions {
			bad("PE_LEASES: partition %d does not exist (PE_PARTITIONS=%d)", p, c.Partitions)
			break
		}
	}
	if c.Leases.Mode == LeaseList && len(c.Leases.Partitions) == 0 {
		bad("PE_LEASES: empty partition list")
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	return nil
}

func (c *Config) validateNATS() []error {
	var errs []error
	bad := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	n := c.NATS
	if n.Sync != SyncDefault && n.Sync != SyncAlways {
		bad("PE_NATS_SYNC must be %s or %s", SyncDefault, SyncAlways)
	}
	if n.Replicas < 1 || n.Replicas > 5 {
		bad("PE_NATS_REPLICAS must be 1..5")
	}
	if !c.Role.Service() || (c.Role == RoleReplay && n.Embedded()) {
		return errs // migrate never connects; replay without PE_NATS_URL replays clips only
	}
	if n.Embedded() {
		if c.Role != RoleAll {
			bad("PE_NATS_URL is required for %s (the embedded server exists only in `all`)", c.Role)
		}
		if n.Replicas != 1 {
			bad("PE_NATS_REPLICAS must be 1 with the embedded server")
		}
		if n.Dir == "" {
			bad("PE_NATS_DIR is required for the embedded server")
		}
		return errs
	}
	if n.Creds != "" && !n.Token.IsZero() {
		bad("set only one of PE_NATS_CREDS and PE_NATS_TOKEN")
	}
	for _, raw := range splitList(n.URL) {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			bad("PE_NATS_URL: invalid URL %q", redactURL(raw))
			continue
		}
		scheme := strings.ToLower(u.Scheme)
		switch scheme {
		case "nats", "tls", "ws", "wss":
		default:
			bad("PE_NATS_URL: unsupported scheme %q", u.Scheme)
			continue
		}
		if u.User != nil {
			bad("PE_NATS_URL must not carry credentials; use PE_NATS_CREDS or PE_NATS_TOKEN")
		}
		if isLoopback(u.Hostname()) {
			continue
		}
		if n.Creds == "" && n.Token.IsZero() {
			bad("external NATS %s is not loopback: PE_NATS_CREDS or PE_NATS_TOKEN is required", u.Host)
		}
		if scheme != "tls" && scheme != "wss" && n.TLSCA == "" {
			bad("external NATS %s is not loopback: use tls:// (or wss://) or set PE_NATS_TLS_CA", u.Host)
		}
	}
	return errs
}

func (c *Config) validateLicense() []error {
	var errs []error
	bad := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	l := c.License
	switch l.Mode {
	case LicenseStatic:
	case LicensePandascript:
		u, err := url.Parse(l.URL)
		switch {
		case l.URL == "" || err != nil:
			bad("PE_LICENSE_URL is required for PE_LICENSE_MODE=pandascript")
		case u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "":
			bad("PE_LICENSE_URL must be a plain https:// URL")
		}
		if l.Product == "" {
			bad("PE_LICENSE_PRODUCT is required")
		}
	default:
		bad("PE_LICENSE_MODE must be %s or %s", LicenseStatic, LicensePandascript)
	}
	if !l.Pepper.IsZero() && l.Pepper.Len() < MinKeyBytes {
		bad("PE_LICENSE_PEPPER must be at least %d bytes", MinKeyBytes)
	}
	if l.Pepper.IsZero() && (c.Role == RoleGateway || c.Role == RoleAPI) {
		bad("PE_LICENSE_PEPPER is required outside `all` (at least %d bytes)", MinKeyBytes)
	}
	return errs
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	a, err := netip.ParseAddr(host)
	return err == nil && a.Unmap().IsLoopback()
}

// redactURL hides anything between "://" and the last "@" (credentials) of a URL that did not parse.
func redactURL(raw string) string {
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok {
		scheme, rest = "", raw
	}
	if at := strings.LastIndex(rest, "@"); at >= 0 {
		rest = "REDACTED@" + rest[at+1:]
	}
	if scheme == "" {
		return rest
	}
	return scheme + "://" + rest
}
