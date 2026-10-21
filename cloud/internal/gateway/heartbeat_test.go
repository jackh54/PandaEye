package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/store"
	"github.com/jackh54/PandaEye/cloud/internal/testkit"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
	"google.golang.org/protobuf/proto"
)

func pong(t *testing.T, c *testkit.Client, p *pb.Ping) *pb.Pong {
	t.Helper()
	ctx := ctxT(t, 5*time.Second)
	if err := c.Ping(ctx, p); err != nil {
		t.Fatal(err)
	}
	f, err := c.ReadUntil(ctx, func(f *pb.ServerFrame) bool { return f.GetPong().GetNonce() == p.Nonce && p.Nonce != 0 })
	if err != nil {
		t.Fatal(err)
	}
	return f.GetPong()
}

func TestPongCarriesTheCurrentConfig(t *testing.T) {
	e := newEnv(t, fastOptions())
	s := newServer(keyPro)
	c, w := e.connect(s)
	before := uint64(time.Now().UnixMicro())
	p := pong(t, c, &pb.Ping{Nonce: 7, ClientTimeUs: 123, OnlinePlayers: 42})
	if p.ClientTimeUs != 123 || p.ServerTimeUs < before || !proto.Equal(p.Config, w.Config) {
		t.Fatalf("pong %+v", p)
	}
	e.patchTenant(w.TenantId, store.TenantPatch{Leniency: ptr(2), Autokick: ptr(true)})
	if p := pong(t, c, &pb.Ping{Nonce: 8, OnlinePlayers: 42}); p.Config.Leniency != 2 || !p.Config.Autokick {
		t.Fatalf("config %+v", p.Config)
	}
	testkit.Eventually(t, 5*time.Second, "online players in PE_CONNECTIONS", func() bool {
		conn, err := e.connection(w.TenantId, s.id)
		return err == nil && conn.Info.OnlinePlayers == 42
	})
}

func TestSilentConnectionsAreClosed(t *testing.T) {
	o := fastOptions()
	o.HeartbeatInterval = 200 * time.Millisecond
	e := newEnv(t, o)
	c, _ := e.connect(newServer(keyPro))
	start := time.Now()
	ctx := ctxT(t, 10*time.Second)
	pongs := 0
	for {
		f, err := c.Read(ctx)
		if err != nil {
			if code := testkit.CloseCode(err); code != 1000 {
				t.Fatalf("close code %d (%v)", code, err)
			}
			break
		}
		if f.GetPong() != nil {
			pongs++ // the gateway keeps the plugin's silence timer satisfied
		}
	}
	if el := time.Since(start); el < 550*time.Millisecond || el > 3*time.Second {
		t.Fatalf("closed after %v, want about 3 intervals (600 ms)", el)
	}
	if pongs == 0 {
		t.Fatal("no keep-alive pongs")
	}
}

func TestProbeRTTRecorded(t *testing.T) {
	e := newEnv(t, fastOptions())
	ctx := context.Background()
	s := newServer(keyPro)
	c, w := e.connect(s)
	for i, rtt := range []uint32{20_000, 40_000, 0, 900_000} { // 0 = no probe completed: not recorded
		pong(t, c, &pb.Ping{Nonce: uint64(i + 1), LastProbeRttUs: rtt})
	}
	c.Close()
	var rows []store.ProbeStat
	testkit.Eventually(t, 5*time.Second, "probe stats flushed", func() bool {
		var err error
		rows, err = e.d.Store.ListProbeStats(ctx, w.TenantId, s.id, store.ProbeSourcePlugin, time.Now().Add(-time.Hour))
		return err == nil && len(rows) > 0
	})
	sum := store.MergeProbeStats(rows)
	if sum.Count != 3 || sum.MaxUs != 900_000 || sum.P50 < 40_000 || sum.P50 > 50_000 {
		t.Fatalf("summary %+v", sum)
	}
}
