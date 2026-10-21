package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/codec"
	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/stream"
	"github.com/jackh54/PandaEye/cloud/internal/testkit"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
	"github.com/nats-io/nats.go/jetstream"
)

func eventsStreamConfig() jetstream.StreamConfig {
	return jetstream.StreamConfig{Name: stream.StreamEvents, Subjects: []string{"pe.events.*"}, Storage: jetstream.FileStorage,
		Duplicates: 10 * time.Minute, MaxMsgSize: stream.MaxPayload, MaxBytes: 256 << 20}
}

// TestStreamOutage: while PE_EVENTS cannot store anything the gateway stops reading and retries the
// same msg ids; the batch is acked once the stream is back, and only an outage longer than
// PublishTimeout closes with CODE_INTERNAL.
func TestStreamOutage(t *testing.T) {
	o := fastOptions()
	o.PublishTimeout = 2 * time.Second
	e := newEnv(t, o)
	ctx := ctxT(t, 30*time.Second)
	c, _ := e.connect(newServer(keyPro))
	if err := e.d.Bus.JS.DeleteStream(ctx, stream.StreamEvents); err != nil {
		t.Fatal(err)
	}
	b := testkit.Batch(1, testkit.Probe(1, 1))
	if err := c.SendBatch(ctx, b, codec.Raw); err != nil {
		t.Fatal(err)
	}
	time.Sleep(700 * time.Millisecond)
	if _, err := e.d.Bus.JS.CreateStream(ctx, eventsStreamConfig()); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := c.WaitBatchAck(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if testkit.StreamMessages(t, e.d.Bus, stream.StreamEvents) != 1 {
		t.Fatal("batch not stored after the outage")
	}
	t.Logf("acked %v after the stream came back", time.Since(start).Round(time.Millisecond))

	if err := e.d.Bus.JS.DeleteStream(ctx, stream.StreamEvents); err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	if err := c.SendBatch(ctx, testkit.Batch(2, testkit.Probe(2, 2)), codec.Raw); err != nil {
		t.Fatal(err)
	}
	for {
		f, err := c.Read(ctx)
		if err != nil {
			if code := testkit.CloseCode(err); code != 1011 {
				t.Fatalf("close code %d (%v)", code, err)
			}
			break
		}
		if f.GetBatchAck().GetSeq() == 2 {
			t.Fatal("a batch that was not stored must not be acked")
		}
		if e := f.GetError(); e != nil && (e.Code != pb.Error_CODE_INTERNAL || !e.Fatal) {
			t.Fatalf("error %v", e)
		}
	}
	if el := time.Since(began); el < o.PublishTimeout {
		t.Fatalf("closed after %v, before the publish timeout", el)
	}
}

// TestRenewalNoticesReplacement: when another gateway takes over the PE_CONNECTIONS entry without
// telling this one, the next renewal sees the revision mismatch and closes.
func TestRenewalNoticesReplacement(t *testing.T) {
	e := newEnv(t, fastOptions())
	s := newServer(keyPro)
	c, w := e.connect(s)
	ctx := context.Background()
	cur, err := e.connection(w.TenantId, s.id)
	if err != nil {
		t.Fatal(err)
	}
	takeover := cur.Info
	takeover.ConnectionID, takeover.GatewayID = identity.NewUUID().String(), "elsewhere#1"
	if _, err := e.d.Bus.UpdateConnection(ctx, w.TenantId, s.id, takeover, cur.Revision); err != nil {
		t.Fatal(err)
	}
	if _, code := c.ReadErr(ctxT(t, 5*time.Second)); code != 1000 {
		t.Fatalf("close code %d", code)
	}
	time.Sleep(300 * time.Millisecond)
	if got, err := e.connection(w.TenantId, s.id); err != nil || got.Info.ConnectionID != takeover.ConnectionID {
		t.Fatalf("the replaced connection must not release the new entry: %+v %v", got, err)
	}
}
