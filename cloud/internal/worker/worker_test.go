package worker

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/app"
	"github.com/jackh54/PandaEye/cloud/internal/geom"
	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/stream"
	"github.com/jackh54/PandaEye/cloud/internal/testkit"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go/jetstream"
	"google.golang.org/protobuf/proto"
)

// startWorker runs a worker service on d until the test ends (or stop is called).
func startWorker(t *testing.T, d *app.Deps) (stop func()) {
	t.Helper()
	svc, err := NewWithOptions(d, Options{Telemetry: &recorder{}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()
	stopped := false
	stop = func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("worker: %v", err)
			}
		case <-time.After(15 * time.Second):
			t.Error("worker did not stop")
		}
	}
	t.Cleanup(stop)
	return stop
}

type client struct {
	t      *testing.T
	d      *app.Deps
	server string
	seq    uint64
}

// send stores a StreamBatch of the player's events like the gateway does and returns its partition.
func (c *client) send(p *testkit.Player, events ...*pb.Event) int {
	c.t.Helper()
	c.seq++
	part, err := c.d.Bus.Partitioner.ForPlayer(p.UUID)
	if err != nil {
		c.t.Fatal(err)
	}
	c.sendRaw(part, c.seq, stream.EventsMsgID(testTenant, c.server, c.seq, part), events...)
	return part
}

func (c *client) sendRaw(part int, batchSeq uint64, msgID string, events ...*pb.Event) {
	c.t.Helper()
	sb := &pb.StreamBatch{Origin: origin(c.server, pb.Platform_PLATFORM_PAPER), BatchSeq: batchSeq,
		GatewayRecvUnixUs: uint64(time.Now().UnixMicro()), Events: events}
	data, err := proto.Marshal(sb)
	if err != nil {
		c.t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := c.d.Bus.PublishAll(ctx, []stream.Message{{Subject: stream.EventsSubject(part), Data: data, MsgID: msgID}}); err != nil {
		c.t.Fatal(err)
	}
}

// readSignals reads pe.signals.<part> from the start until want envelopes arrived or timeout.
func readSignals(t *testing.T, d *app.Deps, part, want int, timeout time.Duration) []*pb.SignalEnvelope {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cons, err := d.Bus.JS.OrderedConsumer(ctx, stream.StreamSignals, jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{stream.SignalsSubject(part)}})
	if err != nil {
		t.Fatal(err)
	}
	var out []*pb.SignalEnvelope
	for len(out) < want {
		batch, err := cons.Fetch(want-len(out), jetstream.FetchMaxWait(200*time.Millisecond))
		if err != nil {
			t.Fatal(err)
		}
		for m := range batch.Messages() {
			var env pb.SignalEnvelope
			if err := proto.Unmarshal(m.Data(), &env); err != nil {
				t.Fatal(err)
			}
			out = append(out, &env)
		}
		if ctx.Err() != nil {
			break
		}
	}
	return out
}

func numbered(p *testkit.Player, n int) *pb.Event {
	e := flag(p, "movement.step", 0)
	e.GetFlag().Info = strconv.Itoa(n)
	return e
}

// flagNumbers returns the numbers of the movement.step signals, in stream order.
func flagNumbers(envs []*pb.SignalEnvelope) []int {
	var out []int
	for _, e := range envs {
		if s := e.GetSignal(); s != nil && s.CheckId == "movement.step" {
			n, _ := strconv.Atoi(s.Info)
			out = append(out, n)
		}
	}
	return out
}

func assertExactlyOnce(t *testing.T, nums []int, n int) {
	t.Helper()
	if len(nums) != n {
		t.Fatalf("got %d signals, want %d: %v", len(nums), n, nums)
	}
	for i, x := range nums {
		if x != i+1 {
			t.Fatalf("signal %d is #%d (lost, duplicated or reordered): %v", i, x, nums)
		}
	}
}

func TestWorkerOrderingDedupAndProbes(t *testing.T) {
	d := testkit.NewDeps(t)
	startWorker(t, d)
	c := &client{t: t, d: d, server: testkit.NewServerID()}
	p := testkit.NewPlayer("ord")
	part := c.send(p, p.Join())
	for i := 1; i <= 30; i++ {
		c.send(p, numbered(p, i))
	}
	// The same batch stored again under another msg id (the JetStream window passed): the worker's
	// batch-seq check drops it.
	c.sendRaw(part, 5, "replayed-after-the-window", numbered(p, 99))
	c.send(p, testkit.Probe(7, p.TimeUs))
	envs := readSignals(t, d, part, 33, 20*time.Second)
	if len(envs) != 33 || envs[0].GetSession().GetKind() != pb.SessionMarker_KIND_JOIN {
		t.Fatalf("got %d envelopes", len(envs))
	}
	assertExactlyOnce(t, flagNumbers(envs), 30)
	if last := envs[len(envs)-1].GetSignal(); last.GetCheckId() != "probe" || last.GetProbeId() != 7 {
		t.Fatalf("probe signal %+v", last)
	}
	if extra := readSignals(t, d, part, 34, time.Second); len(extra) != 33 {
		t.Fatalf("%d envelopes; the duplicate batch must not produce signals", len(extra))
	}
}

// A new owner warm-starts over the messages before its start sequence without emitting anything,
// and continues with the rebuilt sessions.
func TestWorkerWarmStartEmitsNothing(t *testing.T) {
	d := testkit.NewDeps(t)
	stop := startWorker(t, d)
	c := &client{t: t, d: d, server: testkit.NewServerID()}
	p := testkit.NewPlayer("warm")
	part := c.send(p, p.Join())
	for i := 1; i <= 5; i++ {
		c.send(p, numbered(p, i))
	}
	if envs := readSignals(t, d, part, 6, 20*time.Second); len(envs) != 6 {
		t.Fatalf("first owner: %d", len(envs))
	}
	stop() // graceful: acks confirmed, leases released
	startWorker(t, d)
	for i := 6; i <= 8; i++ {
		c.send(p, numbered(p, i)) // needs the session rebuilt by the warm start
	}
	envs := readSignals(t, d, part, 9, 20*time.Second)
	assertExactlyOnce(t, flagNumbers(envs), 8)
	if extra := readSignals(t, d, part, 10, time.Second); len(extra) != 9 {
		t.Fatalf("warm start emitted: %d envelopes", len(extra))
	}
}

// startNATS runs a JetStream server two processes (two Deps) share.
func startNATS(t *testing.T) string {
	t.Helper()
	srv, err := server.NewServer(&server.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(),
		MaxPayload: stream.MaxPayload, NoSigs: true, NoLog: true})
	if err != nil {
		t.Fatal(err)
	}
	srv.Start()
	if !srv.ReadyForConnections(10 * time.Second) {
		t.Fatal("nats not ready")
	}
	t.Cleanup(func() { srv.Shutdown(); srv.WaitForShutdown() })
	return srv.ClientURL()
}

// The owner crashes with messages in flight; the next owner takes the partition over after the lease
// expires and nothing is lost or published twice (re-derived signals carry the same msg ids).
func TestWorkerLeaseHandoverWithoutLoss(t *testing.T) {
	url := startNATS(t)
	a := testkit.NewDeps(t, "PE_NATS_URL="+url, "PE_OWNER_ID=owner-a")
	startWorker(t, a)
	c := &client{t: t, d: a, server: testkit.NewServerID()}
	p := testkit.NewPlayer("handover")
	part := c.send(p, p.Join())
	for i := 1; i <= 20; i++ {
		c.send(p, numbered(p, i))
	}
	if envs := readSignals(t, a, part, 21, 20*time.Second); len(envs) != 21 {
		t.Fatalf("owner A: %d", len(envs))
	}
	b := testkit.NewDeps(t, "PE_NATS_URL="+url, "PE_OWNER_ID=owner-b")
	startWorker(t, b) // waits: A holds every lease
	cb := &client{t: t, d: b, server: c.server, seq: c.seq}
	for i := 21; i <= 25; i++ {
		cb.send(p, numbered(p, i)) // in flight while A crashes
	}
	a.Bus.NC.Close() // crash: no release, no further acks
	for i := 26; i <= 40; i++ {
		cb.send(p, numbered(p, i))
	}
	envs := readSignals(t, b, part, 41, 40*time.Second)
	assertExactlyOnce(t, flagNumbers(envs), 40)
	if extra := readSignals(t, b, part, 42, time.Second); len(extra) != 41 {
		t.Fatalf("duplicates after the handover: %d envelopes", len(extra))
	}
}

// The signal that triggered a clip carries clip_id, and the clip is in the store when the signal
// can be read.
func TestWorkerClipCommittedBeforeClipID(t *testing.T) {
	d := testkit.NewDeps(t)
	startWorker(t, d)
	c := &client{t: t, d: d, server: testkit.NewServerID()}
	p := testkit.NewPlayer("clip")
	part := c.send(p, p.Join(), p.State())
	var moves []*pb.Event
	for i := 0; i < 60; i++ {
		moves = append(moves, p.Move(geom.V(0.1, 0, 0), geom.Rotation{Yaw: float64(i)}))
	}
	c.send(p, moves...)
	c.send(p, flag(p, "protocol.position", 1))
	envs := readSignals(t, d, part, 2, 20*time.Second)
	sig := envs[len(envs)-1].GetSignal()
	if sig == nil || sig.GetClipId() == "" {
		t.Fatalf("signal without clip: %+v", envs)
	}
	clip, err := d.Store.GetClip(context.Background(), testTenant, sig.GetClipId())
	if err != nil || clip.PlayerUUID != identity.UUID(p.UUID).String() || len(clip.Data) == 0 {
		t.Fatalf("clip %s not committed: %v", sig.GetClipId(), err)
	}
	// pandaeye replay over the stored clip re-runs the checks and prints the signals.
	var buf lineBuffer
	if err := Replay(context.Background(), d, ReplayOptions{TenantID: testTenant, ClipID: clip.ID}, &buf); err != nil {
		t.Fatal(err)
	}
	if buf.n == 0 {
		t.Fatal("replay of the clip printed no signals")
	}
}

type lineBuffer struct{ n int }

func (b *lineBuffer) Write(p []byte) (int, error) {
	for _, c := range p {
		if c == '\n' {
			b.n++
		}
	}
	return len(p), nil
}

func TestWorkerErasureDropsLateData(t *testing.T) {
	d := testkit.NewDeps(t)
	startWorker(t, d)
	c := &client{t: t, d: d, server: testkit.NewServerID()}
	gone := testkit.NewPlayer("gone")
	part := c.send(gone, gone.Join(), numbered(gone, 1))
	if envs := readSignals(t, d, part, 2, 20*time.Second); len(envs) != 2 {
		t.Fatal("before the erasure")
	}
	if err := d.Bus.PutErasure(context.Background(), testTenant, identity.UUID(gone.UUID), time.Now().Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond) // the worker's mirror sees the tombstone
	c.send(gone, numbered(gone, 2))
	// A barrier on the same partition: another player whose signal must arrive after it.
	other := sameNumberPartition(t, d, part)
	c.send(other, other.Join(), numbered(other, 1))
	envs := readSignals(t, d, part, 4, 20*time.Second)
	for _, e := range envs {
		if s := e.GetSignal(); s != nil && string(s.GetPlayer()) == string(gone.UUID) && s.GetInfo() == "2" {
			t.Fatal("late data of an erased player produced a signal")
		}
	}
	if len(envs) != 4 {
		t.Fatalf("got %d envelopes (barrier)", len(envs))
	}
}

// sameNumberPartition returns a player that hashes to partition part.
func sameNumberPartition(t *testing.T, d *app.Deps, part int) *testkit.Player {
	for i := 0; i < 1000; i++ {
		p := testkit.NewPlayer(fmt.Sprintf("p%d", i))
		if q, _ := d.Bus.Partitioner.ForPlayer(p.UUID); q == part {
			return p
		}
	}
	t.Fatal("no player for the partition")
	return nil
}

func TestReplayNeedsBusForStreamData(t *testing.T) {
	err := Replay(context.Background(), &app.Deps{}, ReplayOptions{TenantID: testTenant, Player: identity.NewUUID().String()}, &lineBuffer{})
	if err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("replay without a bus: %v", err)
	}
}
