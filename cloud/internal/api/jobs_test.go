package api

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	"github.com/jackh54/PandaEye/cloud/internal/stream"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

func srvID(n int) string {
	return "00000000-0000-4000-8000-" + string([]byte{'0', '0', '0', '0', '0', '0', '0', '0', '0', '0', byte('0' + n/10), byte('0' + n%10)})
}

func TestOnlinePlayers(t *testing.T) {
	c := func(network string, p pb.Platform, n uint32) stream.Connection {
		return stream.Connection{Info: stream.ConnectionInfo{NetworkID: network, Platform: p, OnlinePlayers: n}}
	}
	paper, velocity := pb.Platform_PLATFORM_PAPER, pb.Platform_PLATFORM_VELOCITY
	for _, tc := range []struct {
		conns []stream.Connection
		want  int
	}{
		{nil, 0},
		{[]stream.Connection{c("", paper, 5), c("", paper, 7)}, 12},
		{[]stream.Connection{c("n1", velocity, 40), c("n1", paper, 20), c("n1", paper, 25)}, 40},    // proxy count wins
		{[]stream.Connection{c("n1", paper, 20), c("n1", paper, 25)}, 45},                           // no proxy: sum
		{[]stream.Connection{c("n1", velocity, 30), c("n1", velocity, 12), c("n1", paper, 99)}, 42}, // two proxies
		{[]stream.Connection{c("n1", velocity, 0), c("n1", paper, 9)}, 0},                           // the proxy says 0
		{[]stream.Connection{c("n1", velocity, 40), c("n2", paper, 3), c("", paper, 2)}, 45},
	} {
		if got := onlinePlayers(tc.conns); got != tc.want {
			t.Errorf("%+v: %d, want %d", tc.conns, got, tc.want)
		}
	}
}

func TestMetering(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	st := e.d.Store
	// Tenant A (free): a proxy and two backends of one network plus a standalone server: 40 + 5.
	e.connect(tenantA, srvID(1), stream.ConnectionInfo{NetworkID: "net", Platform: pb.Platform_PLATFORM_VELOCITY, OnlinePlayers: 40})
	e.connect(tenantA, srvID(2), stream.ConnectionInfo{NetworkID: "net", Platform: pb.Platform_PLATFORM_PAPER, OnlinePlayers: 22})
	e.connect(tenantA, srvID(3), stream.ConnectionInfo{NetworkID: "net", Platform: pb.Platform_PLATFORM_PAPER, OnlinePlayers: 19})
	e.connect(tenantA, srvID(4), stream.ConnectionInfo{Platform: pb.Platform_PLATFORM_PAPER, OnlinePlayers: 5})
	// Tenant B (pro): backends only, 35 > 30 but not on the free plan.
	e.connect(tenantB, srvID(5), stream.ConnectionInfo{NetworkID: "b", Platform: pb.Platform_PLATFORM_PAPER, OnlinePlayers: 20})
	e.connect(tenantB, srvID(6), stream.ConnectionInfo{NetworkID: "b", Platform: pb.Platform_PLATFORM_FOLIA, OnlinePlayers: 15})

	day0 := fixedNow
	check(t, e.s.meterOnce(ctx, day0))
	peak := func(tenant string, day time.Time) int {
		us, err := st.ListUsage(ctx, tenant, day)
		check(t, err)
		for _, u := range us {
			if u.Day.Equal(day.UTC().Truncate(24 * time.Hour)) {
				return u.PeakPlayers
			}
		}
		return -1
	}
	if p := peak(tenantA, day0); p != 45 {
		t.Fatalf("tenant A peak %d, want 45", p)
	}
	if p := peak(tenantB, day0); p != 35 {
		t.Fatalf("tenant B peak %d, want 35", p)
	}
	if p := peak(tenantC, day0); p != -1 {
		t.Fatalf("tenant C without connections recorded %d", p)
	}
	ta, _ := st.GetTenant(ctx, tenantA)
	tb, _ := st.GetTenant(ctx, tenantB)
	if !ta.OverLimitSince.Equal(day0) || !tb.OverLimitSince.IsZero() {
		t.Fatalf("over_limit_since: A %v, B %v", ta.OverLimitSince, tb.OverLimitSince)
	}
	if s, _, err := e.d.Bus.GetTenant(ctx, tenantA); err != nil || s.OverLimitSince != day0.UnixMilli() {
		t.Fatalf("PE_TENANTS over_limit_since: %+v %v", s, err)
	}
	// usage_daily keeps the day's maximum: a lower count later does not lower it, and the tenant
	// stays over the limit (since unchanged) the next day too.
	for _, k := range []string{srvID(1), srvID(2), srvID(3), srvID(4)} {
		c, err := e.d.Bus.GetConnection(ctx, tenantA, k)
		check(t, err)
		c.Info.OnlinePlayers = 3
		_, err = e.d.Bus.UpdateConnection(ctx, tenantA, k, c.Info, c.Revision)
		check(t, err)
	}
	check(t, e.s.meterOnce(ctx, day0.Add(time.Hour)))
	if p := peak(tenantA, day0); p != 45 {
		t.Fatalf("peak lowered to %d", p)
	}
	day1 := day0.Add(24 * time.Hour)
	check(t, e.s.meterOnce(ctx, day1))
	if ta, _ := st.GetTenant(ctx, tenantA); !ta.OverLimitSince.Equal(day0) {
		t.Fatalf("over_limit_since moved or cleared the day after: %v", ta.OverLimitSince)
	}
	if p := peak(tenantA, day1); p != 6 { // proxy 3 + standalone 3
		t.Fatalf("day 1 peak %d", p)
	}
	// Two days under the limit clear it.
	check(t, e.s.meterOnce(ctx, day0.Add(48*time.Hour)))
	if ta, _ := st.GetTenant(ctx, tenantA); !ta.OverLimitSince.IsZero() {
		t.Fatalf("over_limit_since not cleared: %v", ta.OverLimitSince)
	}
	if s, _, _ := e.d.Bus.GetTenant(ctx, tenantA); s.OverLimitSince != 0 {
		t.Fatal("PE_TENANTS not cleared")
	}
	// A paid tenant never keeps the flag.
	check(t, st.SetOverLimitSince(ctx, tenantB, day0))
	check(t, e.s.meterOnce(ctx, day0.Add(48*time.Hour)))
	if tb, _ := st.GetTenant(ctx, tenantB); !tb.OverLimitSince.IsZero() {
		t.Fatal("paid tenant over the free limit")
	}
}

func TestRunServesAndLeasesJobs(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	check(t, err)
	addr := ln.Addr().String()
	ln.Close()
	e := newEnv(t, "PE_API_LISTEN="+addr)
	e.s.meterEvery, e.s.janitorEvery = 50*time.Millisecond, time.Hour
	e.connect(tenantC, srvID(7), stream.ConnectionInfo{Platform: pb.Platform_PLATFORM_PAPER, OnlinePlayers: 4})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.s.Run(ctx) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		us, _ := e.d.Store.ListUsage(context.Background(), tenantC, fixedNow)
		if len(us) == 1 && us[0].PeakPlayers == 4 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("metering did not run under its lease")
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Run serves the same handler on PE_API_LISTEN.
	res, err := http.Get("http://" + addr + "/status")
	check(t, err)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("GET /status on %s: %d", addr, res.StatusCode)
	}
	for _, key := range []string{stream.LeaseMetering, stream.LeaseJanitor} {
		if _, err := e.d.Bus.Leases.Get(context.Background(), key); err != nil {
			t.Errorf("lease %s not held: %v", key, err)
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestJanitor(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	st := e.d.Store
	now := fixedNow
	day := 24 * time.Hour
	// Old and recent rows of tenant A.
	oldV := verdictFixture(tenantA, "0f000000-0000-5000-8000-0000000000a1", srv1, player1, "Steve", pb.Action_ACTION_ALERT, now.Add(-200*day))
	newV := verdictFixture(tenantA, "0f000000-0000-5000-8000-0000000000a2", srv1, player1, "Steve", pb.Action_ACTION_ALERT, now.Add(-10*day))
	for _, v := range []*store.Verdict{oldV, newV} {
		_, err := st.UpsertVerdict(ctx, v)
		check(t, err)
	}
	oldClip := e.clip(tenantA, "c1000000-0000-4000-8000-0000000000a1", srv1, player1, "")
	oldClip.CreatedAt = now.Add(-100 * day)
	check(t, st.SaveClip(ctx, oldClip))
	check(t, st.InsertAudit(ctx, &store.AuditEntry{TenantID: tenantA, Principal: "system", Action: "old", CreatedAt: now.Add(-400 * day)}))
	check(t, st.InsertAudit(ctx, &store.AuditEntry{TenantID: tenantA, Principal: "system", Action: "new", CreatedAt: now.Add(-day)}))
	var h store.ProbeHistogram
	h.Observe(1000)
	check(t, st.RecordProbeStats(ctx, tenantA, srv1, store.ProbeSourcePlugin, now.Add(-40*day), h, 1000))
	check(t, st.RecordProbeStats(ctx, tenantA, srv1, store.ProbeSourcePlugin, now.Add(-day), h, 1000))
	check(t, st.RecordPeakPlayers(ctx, tenantA, now.Add(-800*day), 9))
	check(t, st.UpsertPlayer(ctx, tenantA, player2, "Alex", srv1, now.Add(-800*day)))
	// Erasure tombstones: one 9 days old (expired), one 1 day old whose KV entry was lost.
	p1, _ := identity.ParseCanonicalUUID(player1)
	pOld, _ := identity.ParseCanonicalUUID("aaaaaaaa-0000-4000-8000-0000000000ee")
	check(t, e.d.Bus.PutErasure(ctx, tenantA, pOld, now.Add(-9*day)))
	check(t, st.BeginErasure(ctx, tenantA, player1, now.Add(-day)))

	check(t, e.s.janitorOnce(ctx, now))

	if _, err := st.GetVerdict(ctx, tenantA, oldV.ID); !errors.Is(err, store.ErrNotFound) {
		t.Error("180-day-old verdict kept")
	}
	if _, err := st.GetVerdict(ctx, tenantA, newV.ID); err != nil {
		t.Error("recent verdict deleted")
	}
	if _, err := st.GetClip(ctx, tenantA, oldClip.ID); !errors.Is(err, store.ErrNotFound) {
		t.Error("90-day-old clip kept")
	}
	es, _, err := st.ListAudit(ctx, tenantA, store.Page{})
	check(t, err)
	if len(es) != 1 || es[0].Action != "new" {
		t.Errorf("audit after purge: %+v", es)
	}
	ps, err := st.ListProbeStats(ctx, tenantA, "", store.ProbeSourcePlugin, time.Time{})
	check(t, err)
	if len(ps) != 1 {
		t.Errorf("probe stats after purge: %d", len(ps))
	}
	if us, _ := st.ListUsage(ctx, tenantA, time.Time{}); len(us) != 0 {
		t.Errorf("2-year-old usage kept: %v", us)
	}
	if _, err := st.GetPlayer(ctx, tenantA, player2); !errors.Is(err, store.ErrNotFound) {
		t.Error("2-year-old player kept")
	}
	kOld, _ := stream.ErasureKey(tenantA, pOld)
	if _, err := e.d.Bus.Erasures.Get(ctx, kOld); !errors.Is(err, stream.ErrKeyNotFound) {
		t.Errorf("9-day-old tombstone kept: %v", err)
	}
	k1, _ := stream.ErasureKey(tenantA, p1)
	en, err := e.d.Bus.Erasures.Get(ctx, k1)
	if err != nil {
		t.Fatalf("lost tombstone not re-published: %v", err)
	}
	var ms int64
	check(t, json.Unmarshal(en.Value(), &ms))
	if ms != now.Add(-day).UnixMilli() {
		t.Errorf("tombstone time %d", ms)
	}
}
