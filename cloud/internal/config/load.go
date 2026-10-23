package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/netutil"
	"go.yaml.in/yaml/v3"
)

// field is one configuration key: its dotted name (PE_ + upper-cased with underscores is the
// environment name), default and setter.
type field struct {
	key string
	def string
	set func(c *Config, v string) error
}

func str(dst func(*Config) *string) func(*Config, string) error {
	return func(c *Config, v string) error { *dst(c) = v; return nil }
}

func secret(dst func(*Config) *identity.Secret) func(*Config, string) error {
	return func(c *Config, v string) error { *dst(c) = identity.Secret(v); return nil }
}

func integer(dst func(*Config) *int) func(*Config, string) error {
	return func(c *Config, v string) error {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return fmt.Errorf("not an integer: %q", v)
		}
		*dst(c) = n
		return nil
	}
}

func size(dst func(*Config) *int64) func(*Config, string) error {
	return func(c *Config, v string) error {
		n, err := ParseSize(v)
		*dst(c) = n
		return err
	}
}

var fields = []field{
	{"gateway.listen", ":8080", str(func(c *Config) *string { return &c.GatewayListen })},
	{"api.listen", ":8081", str(func(c *Config) *string { return &c.APIListen })},
	{"metrics.listen", "127.0.0.1:9090", str(func(c *Config) *string { return &c.MetricsListen })},
	{"tls.cert", "", str(func(c *Config) *string { return &c.TLSCert })},
	{"tls.key", "", str(func(c *Config) *string { return &c.TLSKey })},
	{"trusted.proxies", "", func(c *Config, v string) error {
		ps, err := parsePrefixes(v)
		c.TrustedProxies = netutil.NewTrustedProxies(ps...)
		return err
	}},
	{"trusted.proxies.preset", "", str(func(c *Config) *string { return &c.TrustedProxyPreset })},
	{"require.trusted.proxy", "false", func(c *Config, v string) (err error) {
		c.RequireTrustedProxy, err = strconv.ParseBool(strings.TrimSpace(v))
		return err
	}},
	{"public.url", "", str(func(c *Config) *string { return &c.PublicURL })},
	{"nats.url", "", str(func(c *Config) *string { return &c.NATS.URL })},
	{"nats.creds", "", str(func(c *Config) *string { return &c.NATS.Creds })},
	{"nats.token", "", secret(func(c *Config) *identity.Secret { return &c.NATS.Token })},
	{"nats.tls.ca", "", str(func(c *Config) *string { return &c.NATS.TLSCA })},
	{"nats.dir", "./data/nats", str(func(c *Config) *string { return &c.NATS.Dir })},
	{"nats.replicas", "1", integer(func(c *Config) *int { return &c.NATS.Replicas })},
	{"nats.sync", SyncDefault, str(func(c *Config) *string { return &c.NATS.Sync })},
	{"outbound.key", "", secret(func(c *Config) *identity.Secret { return &c.OutboundKey })},
	{"outbound.key.next", "", secret(func(c *Config) *identity.Secret { return &c.OutboundKeyNext })},
	{"partitions", "16", integer(func(c *Config) *int { return &c.Partitions })},
	{"db.driver", DriverSQLite, str(func(c *Config) *string { return &c.DB.Driver })},
	{"db.dsn", "./data/pandaeye.db", secret(func(c *Config) *identity.Secret { return &c.DB.DSN })},
	{"db.backup.interval", "6h", func(c *Config, v string) (err error) { c.DB.BackupInterval, err = ParseDuration(v); return err }},
	{"db.backup.keep", "8", integer(func(c *Config) *int { return &c.DB.BackupKeep })},
	{"db.backup.dir", "", str(func(c *Config) *string { return &c.DB.BackupDir })},
	{"clickhouse.dsn", "", secret(func(c *Config) *identity.Secret { return &c.ClickHouseDSN })},
	{"features.dir", "", str(func(c *Config) *string { return &c.Features.Dir })},
	{"features.retention", "90d", func(c *Config, v string) (err error) { c.Features.Retention, err = ParseDuration(v); return err }},
	{"features.max.bytes", "20GiB", size(func(c *Config) *int64 { return &c.Features.MaxBytes })},
	{"license.mode", LicenseStatic, str(func(c *Config) *string { return &c.License.Mode })},
	{"license.url", "", str(func(c *Config) *string { return &c.License.URL })},
	{"license.product", "pandaeye", str(func(c *Config) *string { return &c.License.Product })},
	{"license.pepper", "", secret(func(c *Config) *identity.Secret { return &c.License.Pepper })},
	{"static.licenses", "", func(c *Config, v string) (err error) { c.License.Static, err = parseStaticLicenses(v); return err }},
	{"service.token", "", secret(func(c *Config) *identity.Secret { return &c.ServiceToken })},
	{"service.token.next", "", secret(func(c *Config) *identity.Secret { return &c.ServiceTokenNext })},
	{"assertion.key", "", secret(func(c *Config) *identity.Secret { return &c.AssertionKey })},
	{"assertion.key.next", "", secret(func(c *Config) *identity.Secret { return &c.AssertionKeyNext })},
	{"events.max.age", "24h", func(c *Config, v string) (err error) { c.EventsMaxAge, err = ParseDuration(v); return err }},
	{"events.max.bytes", "10GiB", size(func(c *Config) *int64 { return &c.EventsMaxBytes })},
	{"worker.max.memory", "2GiB", size(func(c *Config) *int64 { return &c.WorkerMaxMemory })},
	{"region", "local", str(func(c *Config) *string { return &c.Region })},
	{"log.level", "info", func(c *Config, v string) error { return c.LogLevel.UnmarshalText([]byte(v)) }},
	{"ml.model", "", str(func(c *Config) *string { return &c.ML.Model })},
	{"ml.points", "0", func(c *Config, v string) (err error) {
		c.ML.Points, err = strconv.ParseFloat(strings.TrimSpace(v), 64)
		return err
	}},
	{"owner.id", "", str(func(c *Config) *string { return &c.OwnerID })},
	{"leases", "", func(c *Config, v string) (err error) { c.Leases, err = parseLeases(v); return err }},
}

// EnvName returns the environment variable of a dotted key ("nats.url" -> "PE_NATS_URL").
func EnvName(key string) string { return "PE_" + strings.ToUpper(strings.ReplaceAll(key, ".", "_")) }

// Keys returns every configuration key in dotted form.
func Keys() []string {
	out := make([]string, len(fields))
	for i, f := range fields {
		out[i] = f.key
	}
	return out
}

// Load builds the configuration of role from defaults, the optional YAML file at path and environ
// (os.Environ() form; unknown PE_ variables are ignored, unknown file keys are errors), then
// validates it.
func Load(role Role, path string, environ []string) (*Config, error) {
	if !role.Valid() {
		return nil, fmt.Errorf("config: unknown role %q", role)
	}
	values := map[string]string{}
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("config: %w", err)
		}
		file, err := parseYAML(data)
		if err != nil {
			return nil, fmt.Errorf("config: %s: %w", path, err)
		}
		for k, v := range file {
			if !slices.ContainsFunc(fields, func(f field) bool { return f.key == k }) {
				return nil, fmt.Errorf("config: %s: unknown key %q", path, k)
			}
			values[k] = v
		}
	}
	env := map[string]string{}
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(k, "PE_") {
			env[k] = v
		}
	}
	c := &Config{Role: role}
	var errs []error
	for _, f := range fields {
		v, ok := env[EnvName(f.key)]
		if !ok {
			v, ok = values[f.key]
		}
		if !ok {
			v = f.def
		}
		if err := f.set(c, v); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", EnvName(f.key), err))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	c.applyRoleDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) applyRoleDefaults() {
	if c.TrustedProxyPreset == PresetCloudflare {
		c.TrustedProxies.SetCloudflare(netutil.CloudflarePrefixes())
	}
	if c.OwnerID == "" {
		host, _ := os.Hostname()
		c.OwnerID = SanitizeOwnerID(string(c.Role) + "-" + host)
	}
	if c.Leases.Mode == "" {
		c.Leases.Mode = LeaseAuto
		if c.Role == RoleAll {
			c.Leases.Mode = LeaseAll
		}
	}
}

// SanitizeOwnerID maps s to the owner id alphabet [A-Za-z0-9_-], at most 64 characters.
func SanitizeOwnerID(s string) string {
	b := []byte(s)
	for i, ch := range b {
		if !ownerIDChar(ch) {
			b[i] = '_'
		}
	}
	if len(b) > 64 {
		b = b[:64]
	}
	return string(b)
}

func ownerIDChar(ch byte) bool {
	return ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-'
}

// parseYAML flattens a YAML document into dotted lower-case keys. Lists become comma-separated values.
func parseYAML(data []byte) (map[string]string, error) {
	// Walk the node tree rather than decoding into any: decoding turns "0123" into 83 and long digit
	// strings into floats, which would silently change peppers and ids. Scalars keep their source text.
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	out := map[string]string{}
	if len(doc.Content) == 0 {
		return out, nil
	}
	var flatten func(prefix string, n *yaml.Node) error
	flatten = func(prefix string, n *yaml.Node) error {
		if n.Kind == yaml.AliasNode {
			n = n.Alias
		}
		switch n.Kind {
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				k := n.Content[i].Value
				if k != strings.ToLower(k) {
					return fmt.Errorf("key %q must be lower case", k)
				}
				key := k
				if prefix != "" {
					key = prefix + "." + k
				}
				if err := flatten(key, n.Content[i+1]); err != nil {
					return err
				}
			}
		case yaml.SequenceNode:
			parts := make([]string, 0, len(n.Content))
			for _, e := range n.Content {
				if e.Kind == yaml.AliasNode {
					e = e.Alias
				}
				if e.Kind != yaml.ScalarNode {
					return fmt.Errorf("key %q: lists may only hold scalars", prefix)
				}
				parts = append(parts, e.Value)
			}
			return set(out, prefix, strings.Join(parts, ","))
		case yaml.ScalarNode:
			if n.Tag == "!!null" {
				return set(out, prefix, "")
			}
			return set(out, prefix, n.Value)
		default:
			return fmt.Errorf("key %q: unsupported YAML node", prefix)
		}
		return nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, errors.New("the top level must be a mapping")
	}
	if err := flatten("", root); err != nil {
		return nil, err
	}
	return out, nil
}

// set records a flattened key, rejecting a key given twice ("nats: {dir: a}" and "nats.dir: b").
func set(out map[string]string, key, v string) error {
	if key == "" {
		return errors.New("a value needs a key")
	}
	if _, dup := out[key]; dup {
		return fmt.Errorf("key %q is set more than once", key)
	}
	out[key] = v
	return nil
}

func parsePrefixes(v string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, s := range splitList(v) {
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(s)
		if err != nil {
			return nil, fmt.Errorf("not a CIDR or IP: %q", s)
		}
		out = append(out, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))
	}
	return out, nil
}

func parseStaticLicenses(v string) ([]StaticLicense, error) {
	var out []StaticLicense
	for _, s := range splitList(v) {
		parts := strings.Split(s, ":")
		if len(parts) > 3 || parts[0] == "" {
			return nil, errors.New("entries must be key[:maxServers[:plan]]")
		}
		l := StaticLicense{Key: identity.Secret(parts[0]), Plan: "free"}
		if len(parts) > 1 && parts[1] != "" {
			n, err := strconv.Atoi(parts[1])
			if err != nil || n < 0 {
				return nil, errors.New("maxServers must be a non-negative integer")
			}
			l.MaxServers = n
		}
		if len(parts) > 2 && parts[2] != "" {
			l.Plan = parts[2]
		}
		out = append(out, l)
	}
	return out, nil
}

func parseLeases(v string) (LeaseSelection, error) {
	v = strings.TrimSpace(v)
	switch v {
	case "":
		return LeaseSelection{}, nil
	case string(LeaseAuto), string(LeaseAll):
		return LeaseSelection{Mode: LeaseMode(v)}, nil
	}
	seen := map[int]bool{}
	for _, s := range splitList(v) {
		lo, hi, isRange := strings.Cut(s, "-")
		a, err := strconv.Atoi(lo)
		b := a
		if err == nil && isRange {
			b, err = strconv.Atoi(hi)
		}
		if err != nil || a < 0 || b < a || b >= MaxPartitions {
			return LeaseSelection{}, fmt.Errorf("want auto, all or a partition list like 0-3,7 (below %d): %q", MaxPartitions, v)
		}
		for p := a; p <= b; p++ {
			seen[p] = true
		}
	}
	sel := LeaseSelection{Mode: LeaseList}
	for p := range seen {
		sel.Partitions = append(sel.Partitions, p)
	}
	sort.Ints(sel.Partitions)
	return sel, nil
}

func splitList(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// LogValue keeps configuration dumps readable; secrets are redacted by their own type.
func (c *Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("role", string(c.Role)),
		slog.String("region", c.Region),
		slog.Bool("nats_embedded", c.NATS.Embedded()),
		slog.Int("partitions", c.Partitions),
		slog.String("db_driver", c.DB.Driver),
		slog.String("license_mode", c.License.Mode),
		slog.String("owner_id", c.OwnerID),
		slog.String("leases", string(c.Leases.Mode)),
	)
}
