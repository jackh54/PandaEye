package testkit

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/jackh54/PandaEye/cloud/internal/codec"
	"github.com/jackh54/PandaEye/cloud/internal/geom"
	"github.com/jackh54/PandaEye/cloud/internal/stream"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// fakeGateway answers like a gateway: Welcome or a fatal Error, BatchAck per batch, a Verdict after
// the first batch, Pong per Ping. Acks it receives go to acks.
func fakeGateway(t *testing.T, acks chan<- *pb.VerdictAck) *httptest.Server {
	dec, err := codec.NewDecoder(codec.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dec.Close)
	send := func(ctx context.Context, c *websocket.Conn, f *pb.ServerFrame) error {
		msg, err := codec.EncodeServerFrame(f)
		if err != nil {
			return err
		}
		return c.Write(ctx, websocket.MessageBinary, msg)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		ctx := r.Context()
		_, msg, err := c.Read(ctx)
		if err != nil {
			return
		}
		h, err := dec.DecodeHello(msg)
		if err != nil || h.LicenseKey == "bad" {
			send(ctx, c, &pb.ServerFrame{Kind: &pb.ServerFrame_Error{Error: &pb.Error{Code: pb.Error_CODE_UNAUTHENTICATED, Fatal: true, Message: "invalid licence"}}})
			c.Close(4001, "auth")
			return
		}
		send(ctx, c, &pb.ServerFrame{Kind: &pb.ServerFrame_Welcome{Welcome: &pb.Welcome{ConnectionId: "c1", TenantId: "t_aaaaaaaaaaaaaaaaaaaa", MaxBatchEvents: 4096}}})
		verdictSent := false
		for {
			_, msg, err := c.Read(ctx)
			if err != nil {
				return
			}
			f, err := dec.Decode(ctx, msg)
			if err != nil {
				t.Errorf("gateway decode: %v", err)
				return
			}
			switch k := f.Kind.(type) {
			case *pb.ClientFrame_Batch:
				send(ctx, c, &pb.ServerFrame{Kind: &pb.ServerFrame_BatchAck{BatchAck: &pb.BatchAck{Seq: k.Batch.Seq}}})
				if !verdictSent {
					verdictSent = true
					send(ctx, c, &pb.ServerFrame{Kind: &pb.ServerFrame_Verdict{Verdict: &pb.Verdict{VerdictId: "v1", Action: pb.Action_ACTION_ALERT}}})
				}
			case *pb.ClientFrame_VerdictAck:
				acks <- k.VerdictAck
			case *pb.ClientFrame_Ping:
				send(ctx, c, &pb.ServerFrame{Kind: &pb.ServerFrame_Pong{Pong: &pb.Pong{Nonce: k.Ping.Nonce}}})
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func wsURL(s *httptest.Server) string {
	return "ws" + strings.TrimPrefix(s.URL, "http") + "/v1/connect"
}

func TestClientAgainstFakeGateway(t *testing.T) {
	acks := make(chan *pb.VerdictAck, 1)
	srv := fakeGateway(t, acks)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := Dial(ctx, wsURL(srv), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	w, err := c.Hello(ctx, Hello("good", NewServerID()))
	if err != nil || w.ConnectionId != "c1" || c.Welcome != w {
		t.Fatalf("hello: %v %v", w, err)
	}
	a, b := NewPlayer("Alice"), NewPlayer("Bob")
	b.Pos = geom.V(2, 64, 0)
	events := []*pb.Event{a.Join(), a.State(), b.Join(), a.Move(geom.V(0.1, 0, 0), geom.Rotation{Yaw: -90}), a.Attack(b), a.Swing(), a.Probe(7), Probe(8, a.TimeUs)}
	for i, codecByte := range []byte{codec.Raw, codec.Zstd, codec.Deflate} {
		if err := c.SendBatch(ctx, Batch(uint64(i+1), events...), codecByte); err != nil {
			t.Fatal(err)
		}
		if err := c.WaitBatchAck(ctx, uint64(i+1)); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			v, err := c.ReadVerdict(ctx)
			if err != nil || v.VerdictId != "v1" {
				t.Fatalf("verdict: %v %v", v, err)
			}
			if err := c.AckVerdict(ctx, v.VerdictId, false, "shadow"); err != nil {
				t.Fatal(err)
			}
		}
	}
	select {
	case ack := <-acks:
		if ack.VerdictId != "v1" || ack.Executed || ack.Note != "shadow" {
			t.Fatalf("ack %+v", ack)
		}
	case <-ctx.Done():
		t.Fatal("no verdict ack")
	}
	if err := c.Ping(ctx, &pb.Ping{Nonce: 42}); err != nil {
		t.Fatal(err)
	}
	f, err := c.ReadUntil(ctx, func(f *pb.ServerFrame) bool { return f.GetPong() != nil })
	if err != nil || f.GetPong().Nonce != 42 {
		t.Fatalf("pong %v %v", f, err)
	}
	if mv := events[3].GetMove(); a.Tick != 1 || mv.X != 0.1 || events[4].GetAttack().TargetPlayer == nil {
		t.Fatal("builders")
	}
}

func TestClientServerError(t *testing.T) {
	srv := fakeGateway(t, make(chan *pb.VerdictAck, 1))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := Dial(ctx, wsURL(srv), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	_, err = c.Hello(ctx, Hello("bad", NewServerID()))
	var se *ServerError
	if !errors.As(err, &se) || se.Frame.Code != pb.Error_CODE_UNAUTHENTICATED || !se.Frame.Fatal {
		t.Fatalf("hello: %v", err)
	}
	if _, err := c.Read(ctx); CloseCode(err) != 4001 {
		t.Fatalf("close code %d (%v)", CloseCode(err), err)
	}
}

func TestEnvironmentHelpers(t *testing.T) {
	s := NewStore(t)
	if err := s.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	b := NewBus(t)
	if b.Partitioner.Count() != 4 || b.LeaseManager.Timings().TTL != FastLeases.TTL {
		t.Fatal("bus helper")
	}
	if _, err := b.Publish(context.Background(), stream.Message{Subject: stream.EventsSubject(0), Data: []byte("x"), MsgID: "1"}); err != nil {
		t.Fatal(err)
	}
	d := NewDeps(t)
	if d.Bus == nil || d.Store == nil || d.Secrets.OutboundKey.Len() != 32 || d.Bus.LeaseManager.Timings().Settle != FastLeases.Settle {
		t.Fatal("deps helper")
	}
	Logger(t).Warn("redacted", "token", "SECRET-VALUE")
}
