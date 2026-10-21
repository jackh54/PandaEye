package gateway

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/codec"
	"github.com/jackh54/PandaEye/cloud/internal/geom"
	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/stream"
	"github.com/jackh54/PandaEye/cloud/internal/testkit"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
	"golang.org/x/time/rate"
)

// TestBackpressureWithoutDataLoss replays 10 minutes of buffered batches as fast as the socket takes
// them against a small replay budget: the gateway must slow the plugin down (stop reading), never
// drop, and ack each batch only after it is in the stream.
func TestBackpressureWithoutDataLoss(t *testing.T) {
	o := fastOptions()
	o.ReplayEventsPerS = 4000
	o.LiveEventsPerS, o.LiveEventsBurst = 4000, 4096
	e := newEnv(t, o)
	s := newServer(keyPro)
	c, _ := e.connect(s)

	const batches, perBatch = 600, 20 // one batch per second of a 10 minute outage
	players := make([]*testkit.Player, perBatch)
	parts := map[int]bool{}
	for i := range players {
		players[i] = testkit.NewPlayer(fmt.Sprintf("P%d", i))
		n, _ := e.d.Bus.Partitioner.ForPlayer(players[i].UUID)
		parts[n] = true
	}
	msgsPerBatch := uint64(len(parts))
	start := time.Now().Add(-10 * time.Minute)
	frames := make([][]byte, batches)
	for i := range frames {
		b := &pb.Batch{Seq: uint64(i + 1), SentUnixMs: uint64(start.Add(time.Duration(i) * time.Second).UnixMilli()), Replayed: true}
		for _, p := range players {
			b.Events = append(b.Events, p.Move(geom.V(0.01, 0, 0), geom.Rotation{Yaw: float64(i)}))
		}
		frames[i] = encodeBatch(t, b, codec.Zstd)
	}

	ctx := ctxT(t, 2*time.Minute)
	sendErr := make(chan error, 1)
	began := time.Now()
	go func() {
		for _, f := range frames {
			if err := c.SendRaw(ctx, f); err != nil {
				sendErr <- err
				return
			}
		}
		sendErr <- nil
	}()
	var next uint64 = 1
	for next <= batches {
		f, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read after %d acks: %v", next-1, err)
		}
		a := f.GetBatchAck()
		if a == nil {
			continue
		}
		if a.Seq != next {
			t.Fatalf("ack %d, want %d (in order, none skipped)", a.Seq, next)
		}
		if stored := testkit.StreamMessages(t, e.d.Bus, stream.StreamEvents); stored < a.Seq*msgsPerBatch {
			t.Fatalf("batch %d acked with %d of %d stream messages stored", a.Seq, stored, a.Seq*msgsPerBatch)
		}
		next++
	}
	took := time.Since(began)
	if err := <-sendErr; err != nil {
		t.Fatal(err)
	}
	// 12 000 events at 4 000/s with a 4 096 burst take about 2 s.
	if min := 1500 * time.Millisecond; took < min {
		t.Fatalf("replay finished in %v: the budget was not enforced", took)
	}
	if counterValue(e.g.m.backpressure.WithLabelValues("replay")) < 0.2 {
		t.Fatal("backpressure not recorded")
	}

	stored := testkit.EventBatches(t, e.d.Bus)
	seen := map[string]bool{}
	for _, sb := range stored {
		if !sb.Batch.Replayed {
			t.Fatalf("seq %d not marked replayed", sb.Batch.BatchSeq)
		}
		for _, ev := range sb.Batch.Events {
			u, _ := identity.UUIDFromBytes(ev.Player)
			k := fmt.Sprint(sb.Batch.BatchSeq, u, ev.Tick)
			if seen[k] {
				t.Fatalf("duplicate event %s", k)
			}
			seen[k] = true
		}
	}
	if len(seen) != batches*perBatch {
		t.Fatalf("stored %d of %d events", len(seen), batches*perBatch)
	}
	t.Logf("%d events replayed in %v", len(seen), took.Round(time.Millisecond))
}

func TestTenantBudgetIsShared(t *testing.T) {
	tb := newTenantBudgets(map[string]int{"free": 1000})
	if tb.get("t_a", "pro", 4096) != nil {
		t.Fatal("plans without a budget are unlimited")
	}
	a, b := tb.get("t_a", "free", 4096), tb.get("t_a", "free", 4096)
	if a == nil || a != b || a.Limit() != 1000 || a.Burst() != 5000 {
		t.Fatalf("shared bucket %v %v", a, b)
	}
	if tb.get("t_b", "free", 4096) == a {
		t.Fatal("tenants share a bucket")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if _, err := wait(ctx, a, 6000); err != nil { // 5 000 burst + 1 000 at 1 000/s
		t.Fatal(err)
	}
	if el := time.Since(start); el < 800*time.Millisecond {
		t.Fatalf("waited %v", el)
	}
	if _, err := wait(ctx, rate.NewLimiter(rate.Inf, 1), 1<<30); err != nil {
		t.Fatal(err)
	}
	if tb.get("t_a", "pro", 4096) != nil || len(tb.m) != 1 {
		t.Fatal("plan change to an unlimited plan drops the bucket")
	}
}
