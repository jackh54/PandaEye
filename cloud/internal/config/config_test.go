package config

import (
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

var key32 = strings.Repeat("k", 32)

// prodEnv is a valid environment for every non-`all` role.
func prodEnv(extra ...string) []string {
	return append([]string{
		"PE_NATS_URL=tls://nats.internal:4222",
		"PE_NATS_CREDS=/etc/pandaeye/nats.creds",
		"PE_OUTBOUND_KEY=" + key32,
		"PE_LICENSE_PEPPER=" + key32,
		"PE_DB_DRIVER=postgres",
		"PE_DB_DSN=postgres://pe@db/pe",
	}, extra...)
}

func mustLoad(t *testing.T, role Role, path string, env []string) *Config {
	t.Helper()
	c, err := Load(role, path, env)
	if err != nil {
		t.Fatalf("Load(%s): %v", role, err)
	}
	return c
}

func wantErr(t *testing.T, role Role, env []string, substr string) {
	t.Helper()
	_, err := Load(role, "", env)
	if err == nil || !strings.Contains(err.Error(), substr) {
		t.Fatalf("Load(%s, %v): want error containing %q, got %v", role, env, substr, err)
	}
}

func TestDefaultsAllMode(t *testing.T) {
	c := mustLoad(t, RoleAll, "", nil)
	if c.GatewayListen != ":8080" || c.APIListen != ":8081" || c.MetricsListen != "127.0.0.1:9090" {
		t.Errorf("listen defaults: %+v", c)
	}
	if !c.NATS.Embedded() || c.NATS.Dir != "./data/nats" || c.NATS.Replicas != 1 || c.NATS.Sync != SyncDefault {
		t.Errorf("nats defaults: %+v", c.NATS)
	}
	if c.Partitions != 16 || c.DB.Driver != DriverSQLite || c.DB.DSN.Reveal() != "./data/pandaeye.db" {
		t.Errorf("partitions/db defaults")
	}
	if c.License.Mode != LicenseStatic || c.License.Product != "pandaeye" || c.Region != "local" || c.LogLevel != slog.LevelInfo {
		t.Errorf("license/region/log defaults")
	}
	if c.EventsMaxAge != 24*time.Hour || c.EventsMaxBytes != 10<<30 || c.WorkerMaxMemory != 2<<30 {
		t.Errorf("events/worker defaults: %v %v %v", c.EventsMaxAge, c.EventsMaxBytes, c.WorkerMaxMemory)
	}
	if c.Leases.Mode != LeaseAll || !strings.HasPrefix(c.OwnerID, "all-") || !c.OutboundKey.IsZero() {
		t.Errorf("all-mode defaults: %+v %q", c.Leases, c.OwnerID)
	}
	if g := mustLoad(t, RoleGateway, "", prodEnv()); g.Leases.Mode != LeaseAuto {
		t.Errorf("non-all lease default: %v", g.Leases.Mode)
	}
}

func TestPrecedence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pe.yaml")
	yml := `
region: eu-west
nats:
  dir: /var/lib/pe/nats
events.max.age: 7d
log:
  level: debug
trusted:
  proxies: [10.0.0.0/8, 192.168.1.7]
static.licenses: "A:3:pro,B"
`
	if err := os.WriteFile(path, []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}
	c := mustLoad(t, RoleAll, path, []string{"PE_REGION=us-east", "UNRELATED=1", "PE_UNKNOWN_THING=x"})
	if c.Region != "us-east" {
		t.Errorf("env must override file: %q", c.Region)
	}
	if c.NATS.Dir != "/var/lib/pe/nats" || c.EventsMaxAge != 7*24*time.Hour || c.LogLevel != slog.LevelDebug {
		t.Errorf("file values: %+v %v %v", c.NATS, c.EventsMaxAge, c.LogLevel)
	}
	want := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("192.168.1.7/32")}
	if ps := c.TrustedProxies.Prefixes(); len(ps) != 2 || ps[0] != want[0] || ps[1] != want[1] {
		t.Errorf("proxies %v", ps)
	}
	if !c.TrustedProxies.Contains(netip.MustParseAddr("192.168.1.7")) || c.TrustedProxies.Contains(netip.MustParseAddr("192.168.1.8")) {
		t.Error("Contains")
	}
	if len(c.License.Static) != 2 || c.License.Static[0].Key.Reveal() != "A" || c.License.Static[0].MaxServers != 3 ||
		c.License.Static[0].Plan != "pro" || c.License.Static[1].MaxServers != 0 || c.License.Static[1].Plan != "free" {
		t.Errorf("static licenses %+v", c.License.Static)
	}

	for _, bad := range []string{"nope: 1\n", "Region: x\n", "nats: [1, 2\n"} {
		if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(RoleAll, path, nil); err == nil {
			t.Errorf("file %q accepted", bad)
		}
	}
	if _, err := Load(RoleAll, filepath.Join(dir, "missing.yaml"), nil); err == nil {
		t.Error("missing file accepted")
	}
}

func TestEnvName(t *testing.T) {
	if EnvName("service.token.next") != "PE_SERVICE_TOKEN_NEXT" || EnvName("nats.tls.ca") != "PE_NATS_TLS_CA" {
		t.Fatal("EnvName")
	}
	seen := map[string]bool{}
	for _, k := range Keys() {
		if seen[EnvName(k)] {
			t.Fatalf("duplicate env name %s", EnvName(k))
		}
		seen[EnvName(k)] = true
	}
	doc, err := os.ReadFile("../../../docs/ARCHITECTURE.md")
	if err != nil {
		t.Fatal(err)
	}
	section := string(doc)
	section = section[strings.Index(section, "### 5.1 "):]
	section = section[:strings.Index(section, "### 5.2 ")]
	key := regexp.MustCompile("`(PE_[A-Z0-9_]+)`")
	n := 0
	for _, line := range strings.Split(section, "\n") {
		cells := strings.Split(line, "|")
		if len(cells) < 3 || !strings.Contains(cells[1], "`PE_") {
			continue
		}
		for _, m := range key.FindAllStringSubmatch(cells[1], -1) {
			n++
			if !seen[m[1]] {
				t.Errorf("ARCHITECTURE 5.1 key %s is not loaded", m[1])
			}
		}
	}
	if n < 30 {
		t.Fatalf("found only %d keys in the ARCHITECTURE 5.1 table", n)
	}
}

func TestValidation(t *testing.T) {
	short := "short"
	wantErr(t, Role("nope"), nil, "unknown role")
	wantErr(t, RoleAll, []string{"PE_SERVICE_TOKEN=" + short}, "PE_SERVICE_TOKEN must be empty or at least 32")
	wantErr(t, RoleAll, []string{"PE_SERVICE_TOKEN_NEXT=" + key32}, "requires PE_SERVICE_TOKEN")
	wantErr(t, RoleAll, []string{"PE_SERVICE_TOKEN=" + key32}, "PE_ASSERTION_KEY is required")
	mustLoad(t, RoleAll, "", []string{"PE_SERVICE_TOKEN=" + key32, "PE_ASSERTION_KEY=" + key32})
	wantErr(t, RoleAll, []string{"PE_OUTBOUND_KEY=" + short}, "PE_OUTBOUND_KEY must be at least")
	wantErr(t, RoleGateway, prodEnv("PE_OUTBOUND_KEY="), "PE_OUTBOUND_KEY is required")
	wantErr(t, RoleGateway, prodEnv("PE_LICENSE_PEPPER="), "PE_LICENSE_PEPPER is required")
	mustLoad(t, RoleWorker, "", prodEnv("PE_LICENSE_PEPPER="))
	wantErr(t, RoleWorker, prodEnv("PE_DB_DRIVER=sqlite"), "only allowed in `all`")
	mustLoad(t, RoleMigrate, "", []string{"PE_DB_DRIVER=sqlite"})
	mustLoad(t, RoleReplay, "", []string{"PE_DB_DRIVER=sqlite"})
	wantErr(t, RoleAll, []string{"PE_DB_DRIVER=mysql"}, "PE_DB_DRIVER must be")
	wantErr(t, RoleAll, []string{"PE_DB_DSN="}, "PE_DB_DSN is required")
	wantErr(t, RoleAll, []string{"PE_LICENSE_MODE=pandascript"}, "PE_LICENSE_URL is required")
	wantErr(t, RoleAll, []string{"PE_LICENSE_MODE=pandascript", "PE_LICENSE_URL=http://lic.example"}, "plain https")
	wantErr(t, RoleAll, []string{"PE_LICENSE_MODE=pandascript", "PE_LICENSE_URL=https://u:p@lic.example"}, "plain https")
	mustLoad(t, RoleAll, "", []string{"PE_LICENSE_MODE=pandascript", "PE_LICENSE_URL=https://lic.example"})
	wantErr(t, RoleAll, []string{"PE_LICENSE_MODE=other"}, "PE_LICENSE_MODE must be")
	wantErr(t, RoleAll, []string{"PE_LICENSE_PEPPER=" + short}, "PE_LICENSE_PEPPER must be")
	wantErr(t, RoleGateway, prodEnv("PE_NATS_URL="), "PE_NATS_URL is required")
	wantErr(t, RoleAll, []string{"PE_NATS_REPLICAS=3"}, "must be 1 with the embedded server")
	wantErr(t, RoleAll, []string{"PE_NATS_SYNC=sometimes"}, "PE_NATS_SYNC")
	wantErr(t, RoleWorker, prodEnv("PE_NATS_CREDS="), "PE_NATS_CREDS or PE_NATS_TOKEN is required")
	wantErr(t, RoleWorker, prodEnv("PE_NATS_URL=nats://10.1.2.3:4222"), "use tls://")
	mustLoad(t, RoleWorker, "", prodEnv("PE_NATS_URL=nats://10.1.2.3:4222", "PE_NATS_TLS_CA=/ca.pem"))
	mustLoad(t, RoleWorker, "", prodEnv("PE_NATS_URL=nats://127.0.0.1:4222", "PE_NATS_CREDS="))
	mustLoad(t, RoleWorker, "", prodEnv("PE_NATS_URL=nats://localhost:4222,nats://[::1]:4223", "PE_NATS_CREDS="))
	wantErr(t, RoleWorker, prodEnv("PE_NATS_URL=nats://127.0.0.1:4222,tls://nats.remote:4222", "PE_NATS_CREDS="), "nats.remote")
	wantErr(t, RoleWorker, prodEnv("PE_NATS_TOKEN="+key32), "only one of")
	wantErr(t, RoleWorker, prodEnv("PE_NATS_URL=http://x:1"), "unsupported scheme")
	wantErr(t, RoleWorker, prodEnv("PE_NATS_URL=nats://u:p@127.0.0.1:4222"), "must not carry credentials")
	wantErr(t, RoleAll, []string{"PE_PARTITIONS=0"}, "PE_PARTITIONS")
	wantErr(t, RoleAll, []string{"PE_PARTITIONS=abc"}, "not an integer")
	wantErr(t, RoleAll, []string{"PE_GATEWAY_LISTEN=8080"}, "PE_GATEWAY_LISTEN")
	mustLoad(t, RoleAll, "", []string{"PE_METRICS_LISTEN="})
	wantErr(t, RoleAll, []string{"PE_TLS_CERT=/c.pem"}, "set together")
	wantErr(t, RoleAll, []string{"PE_PUBLIC_URL=dashboard.example"}, "PE_PUBLIC_URL")
	wantErr(t, RoleAll, []string{"PE_TRUSTED_PROXIES=10.0.0.0/33"}, "not a CIDR")
	wantErr(t, RoleAll, []string{"PE_EVENTS_MAX_BYTES=lots"}, "not a size")
	wantErr(t, RoleAll, []string{"PE_EVENTS_MAX_AGE=0s"}, "must be positive")
	wantErr(t, RoleAll, []string{"PE_LOG_LEVEL=loud"}, "PE_LOG_LEVEL")
	wantErr(t, RoleAll, []string{"PE_ML_POINTS=-1"}, "PE_ML_POINTS")
	wantErr(t, RoleAll, []string{"PE_STATIC_LICENSES=a:b"}, "maxServers")
	wantErr(t, RoleAll, []string{"PE_OWNER_ID=has space"}, "PE_OWNER_ID")
	wantErr(t, RoleAll, []string{"PE_LEASES=0-3,20"}, "does not exist")
	wantErr(t, RoleAll, []string{"PE_LEASES=3-1"}, "partition list")
}

func TestTrustedProxies(t *testing.T) {
	c := mustLoad(t, RoleAll, "", nil)
	if !c.TrustedProxies.Empty() || c.RequireTrustedProxy {
		t.Fatal("default trusts nobody and requires nothing")
	}
	c = mustLoad(t, RoleAll, "", []string{"PE_TRUSTED_PROXIES_PRESET=cloudflare", "PE_TRUSTED_PROXIES=127.0.0.1", "PE_REQUIRE_TRUSTED_PROXY=true"})
	cf := netip.MustParseAddr("173.245.48.1")
	if !c.RequireTrustedProxy || !c.TrustedProxies.IsCloudflare(cf) || !c.TrustedProxies.Contains(netip.MustParseAddr("127.0.0.1")) ||
		c.TrustedProxies.Contains(netip.MustParseAddr("8.8.8.8")) {
		t.Fatal("preset")
	}
	if !c.TrustedProxies.PeerAllowed(cf, c.RequireTrustedProxy) || c.TrustedProxies.PeerAllowed(netip.MustParseAddr("8.8.8.8"), c.RequireTrustedProxy) {
		t.Fatal("require")
	}
	wantErr(t, RoleAll, []string{"PE_REQUIRE_TRUSTED_PROXY=true"}, "would refuse every peer")
	wantErr(t, RoleAll, []string{"PE_REQUIRE_TRUSTED_PROXY=maybe"}, "PE_REQUIRE_TRUSTED_PROXY")
	wantErr(t, RoleAll, []string{"PE_TRUSTED_PROXIES_PRESET=akamai"}, "PE_TRUSTED_PROXIES_PRESET")
}

func TestLeases(t *testing.T) {
	c := mustLoad(t, RoleAll, "", []string{"PE_LEASES=5, 0-2,2"})
	if c.Leases.Mode != LeaseList || len(c.Leases.Partitions) != 4 || c.Leases.Partitions[0] != 0 || c.Leases.Partitions[3] != 5 {
		t.Fatalf("leases %+v", c.Leases)
	}
	if c := mustLoad(t, RoleAll, "", []string{"PE_LEASES=auto"}); c.Leases.Mode != LeaseAuto {
		t.Fatal("auto")
	}
}

func TestUnits(t *testing.T) {
	for in, want := range map[string]int64{"0": 0, "1024": 1024, "10GiB": 10 << 30, "512 MiB": 512 << 20, "2gb": 2e9, "1.5KiB": 1536, "7b": 7} {
		if got, err := ParseSize(in); err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "-1", "GiB", "1PiB?", "1e30TiB"} {
		if _, err := ParseSize(bad); err == nil {
			t.Errorf("ParseSize(%q) accepted", bad)
		}
	}
	for in, want := range map[string]time.Duration{"24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "1d12h": 36 * time.Hour, "90s": 90 * time.Second} {
		if got, err := ParseDuration(in); err != nil || got != want {
			t.Errorf("ParseDuration(%q) = %v, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "d", "xd", "5x"} {
		if _, err := ParseDuration(bad); err == nil {
			t.Errorf("ParseDuration(%q) accepted", bad)
		}
	}
}

func TestSecretsNotLogged(t *testing.T) {
	c := mustLoad(t, RoleAll, "", []string{"PE_SERVICE_TOKEN=" + key32 + "TOKEN", "PE_ASSERTION_KEY=" + key32 + "ASSERT", "PE_DB_DSN=postgres://u:hunter2@db/x", "PE_DB_DRIVER=postgres"})
	var b strings.Builder
	slog.New(slog.NewJSONHandler(&b, nil)).Info("cfg", "config", c, "raw", *c)
	for _, s := range []string{"TOKEN", "ASSERT", "hunter2"} {
		if strings.Contains(b.String(), s) {
			t.Fatalf("secret %q logged: %s", s, b.String())
		}
	}
}
