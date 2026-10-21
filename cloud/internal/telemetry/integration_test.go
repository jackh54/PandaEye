package telemetry

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/jackh54/PandaEye/cloud/internal/identity"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// The ClickHouse integration test runs against
//   - PE_TEST_CLICKHOUSE_DSN (an existing server; the schema is applied idempotently), or
//   - a throwaway container when PE_TEST_CLICKHOUSE=docker (image PE_TEST_CLICKHOUSE_IMAGE, default
//     mirror.gcr.io/clickhouse/clickhouse-server:25.8),
//
// and is skipped otherwise.
const defaultImage = "mirror.gcr.io/clickhouse/clickhouse-server:25.8"

func clickhouseDSN(t *testing.T) string {
	t.Helper()
	if dsn := os.Getenv("PE_TEST_CLICKHOUSE_DSN"); dsn != "" {
		return dsn
	}
	if os.Getenv("PE_TEST_CLICKHOUSE") != "docker" {
		t.Skip("set PE_TEST_CLICKHOUSE=docker or PE_TEST_CLICKHOUSE_DSN to run against ClickHouse")
	}
	image := os.Getenv("PE_TEST_CLICKHOUSE_IMAGE")
	if image == "" {
		image = defaultImage
	}
	run := func(extra ...string) ([]byte, error) {
		a := append([]string{"run", "-d", "--rm"}, extra...)
		a = append(a, "-p", "127.0.0.1::9000", "-e", "CLICKHOUSE_USER=pe", "-e", "CLICKHOUSE_PASSWORD=pe",
			"-e", "CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT=1", image)
		return exec.Command("docker", a...).CombinedOutput()
	}
	// ClickHouse wants a high file limit; sandboxes may refuse to raise it, so fall back to the default.
	out, err := run("--ulimit", "nofile=262144:262144")
	if err != nil && strings.Contains(string(out), "rlimit") {
		out, err = run()
	}
	if err != nil {
		t.Fatalf("docker run: %v: %s", err, out)
	}
	id := strings.TrimSpace(string(out))
	if i := strings.LastIndexByte(id, '\n'); i >= 0 {
		id = id[i+1:]
	}
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", id).Run() })
	port, err := exec.Command("docker", "port", id, "9000/tcp").Output()
	if err != nil {
		t.Fatalf("docker port: %v", err)
	}
	addr := strings.TrimSpace(strings.Split(string(port), "\n")[0])
	return "clickhouse://pe:pe@" + addr + "/default?dial_timeout=2s"
}

func openAdmin(t *testing.T, dsn string) driver.Conn {
	t.Helper()
	opts, err := clickhouse.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	c, err := clickhouse.Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	deadline := time.Now().Add(60 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := c.Ping(ctx)
		cancel()
		if err == nil {
			return c
		}
		if time.Now().After(deadline) {
			t.Fatalf("clickhouse not ready: %v", err)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// applySchema runs deploy/clickhouse/init.sql statement by statement.
func applySchema(t *testing.T, c driver.Conn) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "..", "deploy", "clickhouse", "init.sql"))
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, l := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(l), "--") {
			lines = append(lines, l)
		}
	}
	for _, stmt := range strings.Split(strings.Join(lines, "\n"), ";") {
		if stmt = strings.TrimSpace(stmt); stmt != "" {
			if err := c.Exec(context.Background(), stmt); err != nil {
				t.Fatalf("schema: %v\n%s", err, stmt)
			}
		}
	}
}

func count(t *testing.T, c driver.Conn, table, tenantID string) uint64 {
	t.Helper()
	var n uint64
	if err := c.QueryRow(context.Background(), "SELECT count() FROM pandaeye."+table+" WHERE tenant_id = ?", tenantID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestClickHouseIntegration(t *testing.T) {
	dsn := clickhouseDSN(t)
	admin := openAdmin(t, dsn)
	applySchema(t, admin)

	s, err := NewClickHouse(dsn, Options{Database: "pandaeye", FlushInterval: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tid := identity.NewTenantID()
	other := identity.NewUUID()
	// Times must be recent: rows older than a table's TTL are dropped on insert.
	evUs := uint64(time.Now().Add(-time.Minute).UnixMicro())
	sig := signal("aim.snap")
	sig.Origin.TenantId, sig.EventTimeUs = tid, evUs
	s.RecordSignal(sig)
	sig2 := signal("reach.distance")
	sig2.Origin.TenantId, sig2.Player, sig2.EventTimeUs = tid, other.Bytes(), evUs
	s.RecordSignal(sig2)
	vid := identity.NewUUID().String()
	s.RecordVerdict(&pb.VerdictRecord{TenantId: tid, ServerId: server, NetworkId: "net", SessionId: vid, CreatedUnixUs: uint64(time.Now().UnixMicro()),
		ClipIds: []string{"c1"}, ReputationEligible: true,
		Verdict: &pb.Verdict{VerdictId: vid, Player: player.Bytes(), PlayerName: "Steve", Action: pb.Action_ACTION_BAN, Category: pb.Category_CATEGORY_HEURISTICS,
			Mitigations: []pb.Mitigation{pb.Mitigation_MITIGATION_SETBACK}, Checks: []string{"aim.snap"}, Score: 200, Confidence: 0.97, Strength: 0.5}})
	s.RecordSession(&pb.SessionMarker{Origin: &pb.Origin{TenantId: tid, ServerId: server, Platform: pb.Platform_PLATFORM_PAPER}, Player: player.Bytes(),
		PlayerName: "Steve", Kind: pb.SessionMarker_KIND_JOIN, SessionId: vid, SessionStartUs: 1, GatewayRecvUnixUs: uint64(time.Now().UnixMicro())},
		&pb.PlayerJoin{ProtocolVersion: 769, ClientVersion: "1.21.4", Brand: "vanilla"})
	now := time.Now().UnixMicro() / 30_000_000 * 30_000_000
	s.RecordFeatureWindow(FeatureWindow{TenantID: tid, Player: player, ServerID: server, SessionID: vid, WindowStartUs: now, WindowEndUs: now + 30_000_000,
		ClientProtocol: 769, TickClient: true, FeatureVersion: 1, Features: make([]float32, 94), WorkerUnixUs: 1})
	ctx := context.Background()
	if err := s.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if s.Dropped() != 0 {
		t.Fatalf("dropped %d rows", s.Dropped())
	}
	for table, want := range map[string]uint64{"signals": 2, "verdicts": 1, "sessions": 1, "feature_windows": 1} {
		if got := count(t, admin, table, tid); got != want {
			t.Fatalf("%s: %d rows, want %d", table, got, want)
		}
	}
	var (
		check, category, platform string
		points                    float64
		ts                        time.Time
		data                      map[string]float64
	)
	if err := admin.QueryRow(ctx, "SELECT check_id, category, platform, points, time, data FROM pandaeye.signals WHERE tenant_id = ? AND player = toUUID(?)",
		tid, player.String()).Scan(&check, &category, &platform, &points, &ts, &data); err != nil {
		t.Fatal(err)
	}
	if check != "aim.snap" || category != "CATEGORY_HEURISTICS" || platform != "PLATFORM_FOLIA" || points != 5 ||
		!ts.Equal(time.UnixMicro(int64(evUs)+2_000_000)) || data["angle"] != 12.5 {
		t.Fatalf("signal row: %s %s %s %v %v %v", check, category, platform, points, ts, data)
	}
	var action string
	var mitigations []string
	var eligible bool
	if err := admin.QueryRow(ctx, "SELECT action, mitigations, reputation_eligible FROM pandaeye.verdicts WHERE tenant_id = ?", tid).
		Scan(&action, &mitigations, &eligible); err != nil {
		t.Fatal(err)
	}
	if action != "ACTION_BAN" || len(mitigations) != 1 || !eligible {
		t.Fatalf("verdict row %s %v %v", action, mitigations, eligible)
	}

	// Erasure removes the player's rows from every table and keeps everyone else's.
	if err := s.ErasePlayer(ctx, tid, player.String()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		remaining := count(t, admin, "signals", tid) + count(t, admin, "verdicts", tid) + count(t, admin, "sessions", tid) + count(t, admin, "feature_windows", tid)
		if remaining == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("erasure: %d rows remain, want 1 (the other player's signal)", remaining)
		}
		time.Sleep(200 * time.Millisecond)
	}
	var left string
	if err := admin.QueryRow(ctx, "SELECT toString(player) FROM pandaeye.signals WHERE tenant_id = ?", tid).Scan(&left); err != nil || left != other.String() {
		t.Fatalf("remaining row %q %v", left, err)
	}
}
