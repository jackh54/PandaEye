package verdict

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/app"
	"github.com/jackh54/PandaEye/cloud/internal/discord"
	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	"github.com/jackh54/PandaEye/cloud/internal/stream"
	"github.com/jackh54/PandaEye/cloud/internal/testkit"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"
)

const testWebhook = "https://discord.com/api/webhooks/123456789012345678/abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_abcd"

// fakeNotifier records Discord posts.
type fakeNotifier struct {
	mu    sync.Mutex
	urls  []string
	posts []discord.VerdictEmbed
}

func (f *fakeNotifier) PostVerdict(_ context.Context, url string, e discord.VerdictEmbed) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.urls = append(f.urls, url)
	f.posts = append(f.posts, e)
	return nil
}

func (f *fakeNotifier) get() ([]string, []discord.VerdictEmbed) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.urls), slices.Clone(f.posts)
}

func eventually(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// live is a running verdict service on its own `all`-mode dependencies.
type live struct {
	t      *testing.T
	d      *app.Deps
	sink   *recordingSink
	notify *fakeNotifier
	stop   func()
}

func newDeps(t *testing.T) *app.Deps {
	return testkit.NewDeps(t, "PE_PUBLIC_URL=https://dash.example")
}

func startService(t *testing.T, d *app.Deps, sink *recordingSink, n *fakeNotifier) func() {
	t.Helper()
	s, err := New(d, WithNotifier(n), WithTelemetry(sink), func(o *options) { o.probes = 200 * time.Millisecond })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("verdict service: %v", err)
				}
			case <-time.After(20 * time.Second):
				t.Error("verdict service did not stop")
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

func newLive(t *testing.T) *live {
	l := &live{t: t, d: newDeps(t), sink: &recordingSink{}, notify: &fakeNotifier{}}
	l.stop = startService(t, l.d, l.sink, l.notify)
	return l
}

// tenant creates a tenant in the database and KV PE_TENANTS with every auto-action, enforcement, the
// reputation opt-in, a paid plan and a Discord webhook.
func (l *live) tenant(mod ...func(*store.TenantPatch)) string {
	l.t.Helper()
	ctx := context.Background()
	id := identity.NewTenantID()
	if err := l.d.Store.CreateTenant(ctx, store.NewTenant(id, "Net", id, time.Now())); err != nil {
		l.t.Fatal(err)
	}
	yes, plan := true, "pro"
	sealed, err := discord.Seal(l.d.Secrets.OutboundKey.Bytes(), id, testWebhook)
	if err != nil {
		l.t.Fatal(err)
	}
	p := store.TenantPatch{Autoban: &yes, Autokick: &yes, Enforcement: &yes, ReputationOptIn: &yes, Plan: &plan, DiscordWebhookEnc: &sealed}
	for _, m := range mod {
		m(&p)
	}
	if _, err := l.d.Store.UpdateTenant(ctx, id, p, time.Now()); err != nil {
		l.t.Fatal(err)
	}
	if err := app.PublishTenant(ctx, l.d.Store, l.d.Bus, id); err != nil {
		l.t.Fatal(err)
	}
	return id
}

func (l *live) player(tenant string) *tplayer {
	return &tplayer{clock: time.Now, tenant: tenant, id: identity.NewUUID(), name: "Alex", server: testkit.NewServerID(),
		network: "net-1", session: identity.NewUUID().String()}
}

// publish stores an envelope on the player's pe.signals partition.
func (l *live) publish(player []byte, e *pb.SignalEnvelope) {
	l.t.Helper()
	var part int
	if sig := e.GetSignal(); sig != nil && sig.CheckId == "probe" {
		part = l.d.Bus.Partitioner.ForProbe(sig.ProbeId)
	} else {
		var err error
		if part, err = l.d.Bus.Partitioner.ForPlayer(player); err != nil {
			l.t.Fatal(err)
		}
	}
	data, err := proto.Marshal(e)
	if err != nil {
		l.t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := l.d.Bus.Publish(ctx, stream.Message{Subject: stream.SignalsSubject(part), Data: data, MsgID: identity.NewUUID().String()}); err != nil {
		l.t.Fatal(err)
	}
}

// publishVerdicts stores a VerdictRecord or AckRecord on PE_VERDICTS.
func (l *live) publishVerdicts(subject string, m proto.Message, msgID string) {
	l.t.Helper()
	data, err := proto.Marshal(m)
	if err != nil {
		l.t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := l.d.Bus.Publish(ctx, stream.Message{Subject: subject, Data: data, MsgID: msgID}); err != nil {
		l.t.Fatal(err)
	}
}

// outbox subscribes to pe.out.<tenant>.<server> and verifies every SignedOutbound.
type outbox struct {
	t      *testing.T
	sub    *nats.Subscription
	keys   [][]byte
	tenant string
	server string
}

func (l *live) outbox(tenant, server string) *outbox {
	l.t.Helper()
	subj, err := stream.OutSubject(tenant, server)
	if err != nil {
		l.t.Fatal(err)
	}
	sub, err := l.d.Bus.NC.SubscribeSync(subj)
	if err != nil {
		l.t.Fatal(err)
	}
	l.t.Cleanup(func() { sub.Unsubscribe() })
	return &outbox{t: l.t, sub: sub, keys: l.d.Secrets.OutboundKeys(), tenant: tenant, server: server}
}

func (o *outbox) next(timeout time.Duration) (*pb.Outbound, error) {
	m, err := o.sub.NextMsg(timeout)
	if err != nil {
		return nil, err
	}
	return stream.OutboundFor(m.Data, o.tenant, o.server, o.keys...)
}

// verdicts reads outbound messages until n verdicts arrived (watch requests are skipped).
func (o *outbox) verdicts(n int) []*pb.Verdict {
	o.t.Helper()
	var out []*pb.Verdict
	deadline := time.Now().Add(20 * time.Second)
	for len(out) < n {
		ob, err := o.next(time.Until(deadline))
		if err != nil {
			o.t.Fatalf("got %d of %d verdicts: %v", len(out), n, err)
		}
		if v := ob.GetVerdict(); v != nil {
			out = append(out, v)
		}
	}
	return out
}

// quiet asserts that no further verdict arrives within d.
func (o *outbox) quiet(d time.Duration) {
	o.t.Helper()
	deadline := time.Now().Add(d)
	for {
		ob, err := o.next(time.Until(deadline))
		if errors.Is(err, nats.ErrTimeout) {
			return
		}
		if err != nil {
			o.t.Fatal(err)
		}
		if v := ob.GetVerdict(); v != nil {
			o.t.Fatalf("unexpected verdict %v", v.Action)
		}
	}
}

func actionsOf(vs []*pb.Verdict) []pb.Action {
	var out []pb.Action
	for _, v := range vs {
		out = append(out, v.Action)
	}
	return out
}

func TestServiceEndToEnd(t *testing.T) {
	l := newLive(t)
	ctx := context.Background()
	tenant := l.tenant()
	tp := l.player(tenant)
	box := l.outbox(tenant, tp.server)
	for _, e := range []*pb.SignalEnvelope{
		tp.marker(pb.SessionMarker_KIND_JOIN),
		tp.signal("killaura.hitbox", 20),
		tp.signal("reach.distance", 20),
		tp.signal("reach.distance", 60),
		tp.signal("killaura.noswing", 80, withClip("clip-1")),
	} {
		l.publish(tp.id.Bytes(), e)
	}
	vs := box.verdicts(4)
	if got := actionsOf(vs); !slices.Equal(got, []pb.Action{aA, aM, aK, aB}) {
		t.Fatalf("delivered %v", got)
	}
	for _, v := range vs {
		if v.Shadow || v.EvidenceUrl != "https://dash.example/verdicts/"+v.VerdictId {
			t.Fatalf("verdict %+v", v)
		}
	}
	ban := vs[3]
	if ban.AppealCode != identity.AppealCode(l.d.Secrets.OutboundKey.Bytes(), ban.VerdictId) {
		t.Fatal("appeal code")
	}

	// Store writer: every verdict persisted with its payload, evidence and clip links.
	eventually(t, 10*time.Second, "stored verdicts", func() bool {
		got, err := l.d.Store.ListVerdictsBySession(ctx, tenant, tp.session)
		return err == nil && len(got) == 4
	})
	sv, err := l.d.Store.GetVerdict(ctx, tenant, ban.VerdictId)
	if err != nil {
		t.Fatal(err)
	}
	var payload pb.Verdict
	if err := proto.Unmarshal(sv.Payload, &payload); err != nil || payload.VerdictId != ban.VerdictId || sv.ServerID != tp.server ||
		sv.AppealCode != ban.AppealCode || !slices.Equal(sv.ClipIDs, []string{"clip-1"}) || len(sv.Evidence) < 50 || sv.Status != store.StatusOpen {
		t.Fatalf("stored ban %+v", sv)
	}

	// Discord: MITIGATE and above (default discord_min_action), with the right embed.
	eventually(t, 5*time.Second, "discord posts", func() bool { _, p := l.notify.get(); return len(p) == 3 })
	urls, posts := l.notify.get()
	if urls[0] != testWebhook {
		t.Fatal("webhook url not decrypted")
	}
	e := posts[2]
	if e.Action != "BAN" || e.Player != "Alex" || e.PlayerUUID != tp.id.String() || e.Server != "srv-"+tp.server[:4] || e.Shadow ||
		!near(e.Score, ban.Score) || !near(e.Confidence, ban.Confidence) || len(e.Checks) == 0 || len(e.Checks) > 5 ||
		e.EvidenceURL != ban.EvidenceUrl {
		t.Fatalf("discord embed %+v", e)
	}
	if posts[0].Action != "MITIGATE" || posts[1].Action != "KICK" {
		t.Fatalf("discord actions %v %v", posts[0].Action, posts[1].Action)
	}

	// The plugin acks: the executed BAN creates the bans row with the executing server.
	acks, _ := stream.AcksSubject(tenant)
	l.publishVerdicts(acks, &pb.AckRecord{TenantId: tenant, ServerId: tp.server, VerdictId: ban.VerdictId, Executed: true,
		Note: "banned", AckedUnixUs: uint64(time.Now().UnixMicro())}, stream.AckRecordMsgID(ban.VerdictId))
	l.publishVerdicts(acks, &pb.AckRecord{TenantId: tenant, ServerId: tp.server, VerdictId: vs[1].VerdictId, Executed: true,
		AckedUnixUs: uint64(time.Now().UnixMicro())}, stream.AckRecordMsgID(vs[1].VerdictId))
	eventually(t, 10*time.Second, "ban row", func() bool {
		b, err := l.d.Store.GetBan(ctx, tenant, ban.VerdictId)
		return err == nil && b.ServerID == tp.server && b.Active
	})
	if _, err := l.d.Store.GetBan(ctx, tenant, vs[1].VerdictId); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("a MITIGATE must not create a ban")
	}

	// Telemetry: four signals (live) and four verdicts (stored once each); the BAN record is
	// reputation eligible (non-shadow, cloud checks, opted-in paid tenant).
	eventually(t, 5*time.Second, "telemetry", func() bool { s, v := l.sink.counts(); return s == 4 && v == 4 })
	l.sink.mu.Lock()
	for _, r := range l.sink.verdicts {
		if r.Verdict.Action == aB && !r.ReputationEligible {
			t.Error("ban record not reputation eligible")
		}
		if r.Verdict.Action != aB && r.ReputationEligible {
			t.Error("only bans are reputation eligible")
		}
	}
	l.sink.mu.Unlock()

	// Not confirmed yet: no contribution. Staff confirms (api): the contribution appears.
	if rep, _ := l.d.Store.PlayerReputation(ctx, "t_other", tp.id.String()); len(rep) != 0 {
		t.Fatal("contribution before confirmation")
	}
	if err := l.d.Store.SetVerdictStatus(ctx, tenant, ban.VerdictId, store.StatusConfirmed); err != nil {
		t.Fatal(err)
	}
	if ok, err := ContributeReputation(ctx, l.d.Store, tenant, ban.VerdictId, time.Now()); err != nil || !ok {
		t.Fatalf("contribute after confirm: %v %v", ok, err)
	}
	if rep, _ := l.d.Store.PlayerReputation(ctx, "t_other", tp.id.String()); len(rep) != 1 || rep[0].Weight != ReputationWeight {
		t.Fatalf("reputation %+v", rep)
	}
}

func TestProbeThroughPipeline(t *testing.T) {
	l := newLive(t)
	tenant := l.tenant()
	server := testkit.NewServerID()
	box := l.outbox(tenant, server)
	now := uint64(time.Now().UnixMicro())
	l.publish(nil, &pb.SignalEnvelope{Kind: &pb.SignalEnvelope_Signal{Signal: &pb.Signal{
		Origin:  &pb.Origin{TenantId: tenant, ServerId: server, ServerName: "probe-srv"},
		CheckId: "probe", ProbeId: 7, EventTimeUs: now, GatewayRecvUnixUs: now - 2000, WorkerUnixUs: now - 1000}}})
	ob, err := box.next(15 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	pr := ob.GetProbeResult()
	if pr == nil || pr.ProbeId != 7 || pr.GatewayRecvUnixUs != now-2000 || pr.WorkerUnixUs != now-1000 || pr.VerdictUnixUs < now ||
		ob.SourceGatewayRecvUnixUs != now-2000 {
		t.Fatalf("probe result %+v", ob)
	}
	eventually(t, 15*time.Second, "cloud probe stats", func() bool {
		st, err := l.d.Store.ListProbeStats(context.Background(), tenant, server, store.ProbeSourceCloud, time.Now().Add(-time.Hour))
		return err == nil && len(st) == 1 && st[0].Histogram.Count() == 1
	})
}

func TestRebuildAfterHandover(t *testing.T) {
	d := newDeps(t)
	sink, n := &recordingSink{}, &fakeNotifier{}
	l := &live{t: t, d: d, sink: sink, notify: n}
	l.stop = startService(t, d, sink, n)
	tenant := l.tenant()
	tp := l.player(tenant)
	box := l.outbox(tenant, tp.server)
	for _, e := range []*pb.SignalEnvelope{tp.marker(pb.SessionMarker_KIND_JOIN), tp.signal("killaura.hitbox", 30), tp.signal("aim.snap", 20)} {
		l.publish(tp.id.Bytes(), e)
	}
	if got := actionsOf(box.verdicts(2)); !slices.Equal(got, []pb.Action{aA, aM}) {
		t.Fatalf("first owner: %v", got)
	}
	eventually(t, 10*time.Second, "stored", func() bool {
		vs, err := d.Store.ListVerdictsBySession(context.Background(), tenant, tp.session)
		return err == nil && len(vs) == 2
	})
	l.stop() // graceful: acks what completed, releases the leases

	// A new owner rebuilds the partition: no actions are repeated, the score is restored.
	startService(t, d, &recordingSink{}, &fakeNotifier{})
	l.publish(tp.id.Bytes(), tp.signal("reach.distance", 60))
	vs := box.verdicts(1)
	if vs[0].Action != aK {
		t.Fatalf("after rebuild: %v", vs[0].Action)
	}
	if vs[0].Score < 109 || vs[0].Score > 111 { // 30 + 20 + 60, barely decayed
		t.Fatalf("score not restored: %v", vs[0].Score)
	}
	box.quiet(time.Second)
}

func TestBanRouting(t *testing.T) {
	l := newLive(t)
	ctx := context.Background()
	tenant := l.tenant()

	// The player quit; another server of the same network is connected: the BAN goes there.
	tp := l.player(tenant)
	other := testkit.NewServerID()
	if _, err := l.d.Bus.CreateConnection(ctx, tenant, other, stream.ConnectionInfo{ConnectionID: "c1", NetworkID: "net-1",
		Platform: pb.Platform_PLATFORM_PAPER}); err != nil {
		t.Fatal(err)
	}
	otherBox := l.outbox(tenant, other)
	l.publish(tp.id.Bytes(), tp.marker(pb.SessionMarker_KIND_JOIN))
	l.publish(tp.id.Bytes(), tp.marker(pb.SessionMarker_KIND_QUIT))
	for range 4 {
		l.publish(tp.id.Bytes(), tp.signal("protocol.pitch", 25, local(), withClip("c")))
	}
	vs := otherBox.verdicts(1)
	if vs[0].Action != aB {
		t.Fatalf("network route got %v", vs[0].Action)
	}
	eventually(t, 10*time.Second, "stored ban", func() bool {
		v, err := l.d.Store.GetVerdict(ctx, tenant, vs[0].VerdictId)
		return err == nil && v.ServerID == other
	})

	// No connected server of the network: the newest signal's server (pending until it reconnects).
	tp2 := l.player(tenant)
	tp2.network = "net-2"
	l.publish(tp2.id.Bytes(), tp2.marker(pb.SessionMarker_KIND_JOIN))
	l.publish(tp2.id.Bytes(), tp2.marker(pb.SessionMarker_KIND_QUIT))
	for range 4 {
		l.publish(tp2.id.Bytes(), tp2.signal("protocol.pitch", 25, local(), withClip("c")))
	}
	eventually(t, 10*time.Second, "offline ban", func() bool {
		vs, _, err := l.d.Store.ListVerdicts(ctx, tenant, store.VerdictFilter{Action: aB, PlayerUUID: tp2.id.String()}, store.Page{})
		return err == nil && len(vs) == 1 && vs[0].ServerID == tp2.server
	})
	pending, err := l.d.Store.PendingVerdicts(ctx, tenant, store.PendingQuery{ServerID: tp2.server, Now: time.Now(), DeliveredBefore: time.Now()})
	if err != nil || !slices.ContainsFunc(pending, func(v store.Verdict) bool { return v.Action == aB }) {
		t.Fatalf("ban not pending for the offline server: %v %v", len(pending), err)
	}
}

func TestStoreWriter(t *testing.T) {
	l := newLive(t)
	ctx := context.Background()
	tenant := l.tenant()
	subj, _ := stream.VerdictsSubject(tenant)
	acks, _ := stream.AcksSubject(tenant)
	server := testkit.NewServerID()
	record := func(action pb.Action, shadow bool, checks ...string) *pb.VerdictRecord {
		id := identity.NewUUID().String()
		p := identity.NewUUID()
		return &pb.VerdictRecord{TenantId: tenant, ServerId: server, SessionId: identity.NewUUID().String(),
			CreatedUnixUs: uint64(time.Now().UnixMicro()), EvidenceJson: `{"score":1}`, ClipIds: []string{"clip-x"},
			Verdict: &pb.Verdict{VerdictId: id, Player: p.Bytes(), PlayerName: "P", Action: action, Shadow: shadow, Checks: checks,
				Score: 200, Confidence: 0.99, CreatedUnixMs: uint64(time.Now().UnixMilli())}}
	}
	ack := func(r *pb.VerdictRecord, executed bool, msgID string) {
		l.publishVerdicts(acks, &pb.AckRecord{TenantId: tenant, ServerId: server, VerdictId: r.Verdict.VerdictId, Executed: executed,
			AckedUnixUs: uint64(time.Now().UnixMicro())}, msgID)
	}
	stored := func(r *pb.VerdictRecord) *store.Verdict {
		var v *store.Verdict
		eventually(t, 10*time.Second, "stored "+r.Verdict.Action.String(), func() bool {
			var err error
			v, err = l.d.Store.GetVerdict(ctx, tenant, r.Verdict.VerdictId)
			return err == nil
		})
		return v
	}

	// An ack for an unknown verdict is skipped without blocking the stream.
	l.publishVerdicts(acks, &pb.AckRecord{TenantId: tenant, VerdictId: identity.NewUUID().String(), Executed: true}, "junk-ack")

	// Idempotent: the same record twice (republished under another msg id) and the ack twice.
	ban := record(aB, false, "killaura.hitbox", "protocol.pitch")
	l.publishVerdicts(subj, ban, "a1")
	l.publishVerdicts(subj, ban, "a2")
	v := stored(ban)
	if v.PlayerUUID == "" || len(v.Payload) == 0 || string(v.Evidence) != `{"score":1}` {
		t.Fatalf("stored %+v", v)
	}
	// Confirmed before the ack arrives: the writer adds the reputation contribution on the ack.
	if err := l.d.Store.SetVerdictStatus(ctx, tenant, ban.Verdict.VerdictId, store.StatusConfirmed); err != nil {
		t.Fatal(err)
	}
	ack(ban, true, "ack-1")
	ack(ban, true, "ack-2")
	eventually(t, 10*time.Second, "ban + contribution", func() bool {
		_, berr := l.d.Store.GetBan(ctx, tenant, ban.Verdict.VerdictId)
		rep, _ := l.d.Store.PlayerReputation(ctx, "t_other", v.PlayerUUID)
		return berr == nil && len(rep) == 1 && rep[0].Contributions == 1
	})

	// A shadow BAN that was "executed" (e.g. logged) never creates a ban; a local-only BAN never
	// contributes; a record of an erased player is not stored.
	shadowBan := record(aB, true, "killaura.hitbox")
	localBan := record(aB, false, "protocol.pitch")
	erased := record(aB, false, "killaura.hitbox")
	pid, _ := identity.UUIDFromBytes(erased.Verdict.Player)
	if err := l.d.Bus.PutErasure(ctx, tenant, pid, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond) // the service's erasure watch sees the tombstone
	for _, r := range []*pb.VerdictRecord{shadowBan, localBan, erased} {
		l.publishVerdicts(subj, r, r.Verdict.VerdictId)
	}
	stored(shadowBan)
	stored(localBan)
	if err := l.d.Store.SetVerdictStatus(ctx, tenant, localBan.Verdict.VerdictId, store.StatusConfirmed); err != nil {
		t.Fatal(err)
	}
	ack(shadowBan, true, "ack-3")
	ack(localBan, true, "ack-4")
	sentinel := record(aA, false)
	l.publishVerdicts(subj, sentinel, "sentinel")
	stored(sentinel) // everything before it was processed
	if _, err := l.d.Store.GetBan(ctx, tenant, shadowBan.Verdict.VerdictId); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("shadow ban created a bans row")
	}
	lv, _ := l.d.Store.GetVerdict(ctx, tenant, localBan.Verdict.VerdictId)
	if rep, _ := l.d.Store.PlayerReputation(ctx, "t_other", lv.PlayerUUID); len(rep) != 0 || !lv.Executed {
		t.Fatalf("local-only ban contributed: %+v", rep)
	}
	if _, err := l.d.Store.GetVerdict(ctx, tenant, erased.Verdict.VerdictId); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("record of an erased player stored")
	}
	if _, nv := l.sink.counts(); nv != 4 {
		t.Fatalf("telemetry verdicts %d, want 4 (each stored record once)", nv)
	}
}

func TestReputationNeedsTrustedTenant(t *testing.T) {
	now := time.Now()
	for name, c := range map[string]struct {
		ti   store.TrustInputs
		want bool
	}{
		"paid, opted in":    {store.TrustInputs{Plan: "pro", OptIn: true, CreatedAt: now}, true},
		"not opted in":      {store.TrustInputs{Plan: "pro", CreatedAt: now}, false},
		"free and new":      {store.TrustInputs{Plan: "free", OptIn: true, CreatedAt: now, DistinctPlayers: 500}, false},
		"free, old, few":    {store.TrustInputs{Plan: "free", OptIn: true, CreatedAt: now.AddDate(0, -2, 0), DistinctPlayers: 49}, false},
		"free, old, enough": {store.TrustInputs{Plan: "free", OptIn: true, CreatedAt: now.AddDate(0, -2, 0), DistinctPlayers: 50}, true},
	} {
		if got := TrustedTenant(c.ti, now); got != c.want {
			t.Errorf("%s: %v", name, got)
		}
	}
	if ReputationEligible(&store.Verdict{Action: aB, Checks: []string{"timer.balance"}}) ||
		!ReputationEligible(&store.Verdict{Action: aB, Checks: []string{"timer.balance", "aim.snap"}}) ||
		ReputationEligible(&store.Verdict{Action: aB, Shadow: true, Checks: []string{"aim.snap"}}) ||
		ReputationEligible(&store.Verdict{Action: aK, Checks: []string{"aim.snap"}}) {
		t.Fatal("ReputationEligible")
	}
}
