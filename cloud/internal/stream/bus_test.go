package stream

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/identity"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
	"github.com/nats-io/nats.go/jetstream"
)

func TestEmbeddedProvisioningAndMeta(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{Dir: dir, Partitions: 16, EventsMaxAge: 3 * time.Hour, EventsMaxBytes: 1 << 30, OwnerID: "all-test"}
	ctx := ctxT(t, 30*time.Second)
	b, err := Open(ctx, cfg, testLogger(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !b.Embedded() || b.NC.MaxPayload() < MaxPayload {
		t.Fatal("embedded server with 8 MiB payload expected")
	}
	for name, want := range map[string]struct {
		subjects []string
		age, dup time.Duration
	}{
		StreamEvents:   {[]string{"pe.events.*"}, 3 * time.Hour, 10 * time.Minute},
		StreamSignals:  {[]string{"pe.signals.*"}, 7 * 24 * time.Hour, 10 * time.Minute},
		StreamVerdicts: {[]string{"pe.verdicts.*", "pe.acks.*"}, 30 * 24 * time.Hour, time.Hour},
	} {
		st, err := b.JS.Stream(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		c := st.CachedInfo().Config
		if strings.Join(c.Subjects, ",") != strings.Join(want.subjects, ",") || c.MaxAge != want.age || c.Duplicates != want.dup ||
			c.Storage != jetstream.FileStorage || c.Discard != jetstream.DiscardOld {
			t.Errorf("%s config %+v", name, c)
		}
	}
	if c := b.Leases.Bucket(); c != BucketLeases {
		t.Fatal(c)
	}
	st, _ := b.Leases.Status(ctx)
	if st.TTL() != 15*time.Second {
		t.Errorf("lease TTL %v", st.TTL())
	}
	st, _ = b.Connections.Status(ctx)
	if st.TTL() != 30*time.Second {
		t.Errorf("connections TTL %v", st.TTL())
	}
	key := b.Partitioner
	if key.Count() != 16 {
		t.Fatal("partitions")
	}
	u := identity.NewUUID()
	p1 := b.Partitioner.ForUUID(u)
	b.Close()

	// A restart keeps the partition key; a different partition count is refused.
	b2, err := Open(ctx, cfg, testLogger(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if b2.Partitioner.ForUUID(u) != p1 {
		t.Fatal("partition key changed across restarts")
	}
	b2.Close()
	cfg.Partitions = 8
	if _, err := Open(ctx, cfg, testLogger(t), nil); !errors.Is(err, ErrPartitionMismatch) {
		t.Fatalf("partition mismatch: %v", err)
	}
}

func TestExternalConnectionChecks(t *testing.T) {
	srv := startServer(t)
	b := openBus(t, testConfig(srv.ClientURL(), "a"))
	if b.Embedded() {
		t.Fatal("external bus reports embedded")
	}
	small, err := startSmallPayloadServer(t)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctxT(t, 10*time.Second), testConfig(small, "x"), testLogger(t), nil); err == nil || !strings.Contains(err.Error(), "max_payload") {
		t.Fatalf("small max_payload accepted: %v", err)
	}
}

func TestSubjects(t *testing.T) {
	tenant, server := identity.NewTenantID(), identity.NewUUID().String()
	if EventsSubject(7) != "pe.events.07" || SignalsSubject(12) != "pe.signals.12" {
		t.Fatal("partition subjects")
	}
	if s, err := VerdictsSubject(tenant); err != nil || s != "pe.verdicts."+tenant {
		t.Fatal(s, err)
	}
	if s, err := AcksSubject(tenant); err != nil || s != "pe.acks."+tenant {
		t.Fatal(s, err)
	}
	if s, err := OutSubject(tenant, server); err != nil || s != "pe.out."+tenant+"."+server {
		t.Fatal(s, err)
	}
	for _, bad := range [][2]string{{"*", server}, {tenant, "*"}, {tenant, ">"}, {"t_a.b", server}, {tenant, server + ".x"}, {tenant, strings.ToUpper(server)}, {"", server}} {
		if _, err := OutSubject(bad[0], bad[1]); !errors.Is(err, ErrBadToken) {
			t.Errorf("OutSubject(%q, %q) accepted", bad[0], bad[1])
		}
	}
	if _, err := VerdictsSubject("t_x.>"); !errors.Is(err, ErrBadToken) {
		t.Error("wildcard tenant accepted")
	}
	k, _ := ServerKey(tenant, server)
	if gt, gs, err := ParseServerKey(k); err != nil || gt != tenant || gs != server {
		t.Error("server key round trip")
	}
	if EventsMsgID(tenant, server, 42, 3) != tenant+":"+server+":42:03" {
		t.Error("events msg id")
	}
	if SignalMsgID(tenant, server, 42, 3, 5, "reach.distance", 1) != tenant+":"+server+":42:03:5:reach.distance:1" {
		t.Error("signal msg id")
	}
	if p, err := PartitionOf("pe.signals.11"); err != nil || p != 11 {
		t.Error("PartitionOf")
	}
	if PartitionLeaseKey(GroupEvents, 3) != "events.03" || PartitionConsumer(GroupSignals, 3).Name != "signals-03" || PartitionConsumer(GroupEvents, 3).Filter != "pe.events.03" {
		t.Error("lease/consumer names")
	}
}

func TestPublishDedupAndErrors(t *testing.T) {
	srv := startServer(t)
	b := openBus(t, testConfig(srv.ClientURL(), "a"))
	ctx := ctxT(t, 30*time.Second)
	msgs := []Message{
		{Subject: EventsSubject(0), Data: []byte("a"), MsgID: "t:s:1:00"},
		{Subject: EventsSubject(1), Data: []byte("b"), MsgID: "t:s:1:01"},
	}
	acks, err := b.PublishAll(ctx, msgs)
	if err != nil || len(acks) != 2 || acks[0].Duplicate || acks[1].Stream != StreamEvents {
		t.Fatalf("publish: %v %+v", err, acks)
	}
	again, err := b.PublishAll(ctx, msgs)
	if err != nil || !again[0].Duplicate || !again[1].Duplicate || again[0].Sequence != acks[0].Sequence {
		t.Fatalf("re-publish must be de-duplicated: %v %+v", err, again)
	}
	st, _ := b.JS.Stream(ctx, StreamEvents)
	info, _ := st.Info(ctx)
	if info.State.Msgs != 2 {
		t.Fatalf("stream holds %d messages", info.State.Msgs)
	}
	if _, err := b.Publish(ctx, Message{Subject: EventsSubject(0), Data: make([]byte, MaxPayload+1), MsgID: "big"}); !errors.Is(err, ErrPermanent) {
		t.Fatalf("oversized payload: %v", err)
	}
	short, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	if _, err := b.Publish(short, Message{Subject: "pe.nowhere.0", Data: []byte("x"), MsgID: "n"}); !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrPermanent) {
		t.Fatalf("no responders must be retried until the deadline: %v", err)
	}
	done := make(chan error, 1)
	b.PublishThen(ctx, []Message{{Subject: SignalsSubject(2), Data: []byte("s"), MsgID: "sig"}}, func(err error) { done <- err })
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	b.PublishThen(ctx, nil, func(err error) { done <- err })
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestOutbound(t *testing.T) {
	srv := startServer(t)
	b := openBus(t, testConfig(srv.ClientURL(), "a"))
	key := bytes.Repeat([]byte("k"), 32)
	tenant, server := identity.NewTenantID(), identity.NewUUID().String()
	subj, _ := OutSubject(tenant, server)
	sub, err := b.NC.SubscribeSync(subj)
	if err != nil {
		t.Fatal(err)
	}
	out := &pb.Outbound{TenantId: tenant, ServerId: server, Kind: &pb.Outbound_Verdict{Verdict: &pb.Verdict{VerdictId: "v1", Action: pb.Action_ACTION_KICK}}}
	if err := b.PublishOutbound(key, out); err != nil {
		t.Fatal(err)
	}
	m, err := sub.NextMsg(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	got, err := OutboundFor(key, m.Data, tenant, server)
	if err != nil || got.GetVerdict().GetVerdictId() != "v1" {
		t.Fatalf("decode: %v %v", got, err)
	}
	if _, err := OutboundFor(key, m.Data, tenant, identity.NewUUID().String()); !errors.Is(err, ErrBadSignature) {
		t.Fatal("wrong server accepted")
	}
	if _, err := DecodeOutbound(bytes.Repeat([]byte("x"), 32), m.Data); !errors.Is(err, ErrBadSignature) {
		t.Fatal("wrong key accepted")
	}
	tampered := bytes.Clone(m.Data)
	tampered[len(tampered)/2] ^= 0xff
	if _, err := DecodeOutbound(key, tampered); err == nil {
		t.Fatal("tampered message accepted")
	}
	if err := b.PublishOutbound(key, &pb.Outbound{TenantId: "*", ServerId: server}); !errors.Is(err, ErrBadToken) {
		t.Fatal("wildcard tenant published")
	}
}

func TestKVHelpers(t *testing.T) {
	srv := startServer(t)
	b := openBus(t, testConfig(srv.ClientURL(), "a"))
	ctx := ctxT(t, 30*time.Second)
	tenant, server := identity.NewTenantID(), identity.NewUUID().String()

	w, err := b.WatchTenants(ctx)
	if err != nil {
		t.Fatal(err)
	}
	<-w.Ready()
	changes := make(chan TenantSettings, 4)
	w.OnChange(func(_ string, v TenantSettings, deleted bool) {
		if !deleted {
			changes <- v
		}
	})
	s := TenantSettings{TenantID: tenant, Leniency: 4, Enforcement: true, Mitigations: true, Checks: map[string]CheckSettings{"aim.gcd": {Enabled: false}}}
	if _, err := b.PutTenant(ctx, s); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-changes:
		if !got.Enforcement || got.RemoteConfig().Leniency != 4 || got.CheckEnabled("aim.gcd", true) || !got.CheckEnabled("aim.snap", true) {
			t.Fatalf("watched settings %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no change notification")
	}
	if got, ok := w.Get(tenant); !ok || !got.Enforcement {
		t.Fatal("watch Get")
	}
	if got, _, err := b.GetTenant(ctx, tenant); err != nil || !got.Mitigations {
		t.Fatal("GetTenant", err)
	}
	w2, _ := b.WatchTenants(ctx)
	<-w2.Ready()
	if w2.Len() != 1 {
		t.Fatal("initial values not loaded")
	}

	info := ConnectionInfo{ConnectionID: "c1", GatewayID: "g1", InstanceID: "i1", OnlinePlayers: 5}
	rev, err := b.CreateConnection(ctx, tenant, server, info)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.CreateConnection(ctx, tenant, server, info); !errors.Is(err, ErrKeyExists) {
		t.Fatalf("second connection: %v", err)
	}
	info.OnlinePlayers = 6
	rev2, err := b.UpdateConnection(ctx, tenant, server, info, rev)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.UpdateConnection(ctx, tenant, server, info, rev); !errors.Is(err, ErrRevisionMismatch) {
		t.Fatalf("stale revision: %v", err)
	}
	other := identity.NewTenantID()
	if _, err := b.CreateConnection(ctx, other, server, info); err != nil {
		t.Fatal(err)
	}
	list, err := b.ListConnections(ctx, tenant)
	if err != nil || len(list) != 1 || list[0].Info.OnlinePlayers != 6 || list[0].Revision != rev2 {
		t.Fatalf("list %+v %v", list, err)
	}
	if all, _ := b.ListConnections(ctx, ""); len(all) != 2 {
		t.Fatalf("all connections %d", len(all))
	}
	if err := b.DeleteConnection(ctx, tenant, server, rev2); err != nil {
		t.Fatal(err)
	}
	if _, err := b.GetConnection(ctx, tenant, server); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("deleted connection: %v", err)
	}

	if _, err := b.CreateServerRecord(ctx, tenant, server, ServerRecord{SecretHash: identity.HashSecret("s"), InstanceID: "i"}); err != nil {
		t.Fatal(err)
	}
	if r, err := b.AdvanceServerSeq(ctx, tenant, server, 10); err != nil || r.LastAckedSeq != 10 {
		t.Fatal(r, err)
	}
	if r, _ := b.AdvanceServerSeq(ctx, tenant, server, 5); r.LastAckedSeq != 10 {
		t.Fatal("seq went backwards")
	}
	if r, _, _ := b.GetServerRecord(ctx, tenant, server); r.LastAckedSeq != 10 || !identity.CheckSecret(r.SecretHash, "s") {
		t.Fatal("server record")
	}

	er, err := b.WatchErasures(ctx)
	if err != nil {
		t.Fatal(err)
	}
	<-er.Ready()
	player := identity.NewUUID()
	if err := b.PutErasure(ctx, tenant, player, time.UnixMilli(1700000000000)); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "erasure tombstone", func() bool { _, ok := er.Erased(tenant, player); return ok })
	if at, _ := er.Erased(tenant, player); at.UnixMilli() != 1700000000000 {
		t.Fatal("erasure time")
	}
	if _, ok := er.Erased(other, player); ok {
		t.Fatal("tombstone leaked across tenants")
	}
}
