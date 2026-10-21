// Package testkit has test helpers for every PandaEye package: a fake plugin client that speaks the
// wire protocol over a real WebSocket, event builders, and ready-to-use stores, buses and Deps.
package testkit

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/coder/websocket"
	"github.com/jackh54/PandaEye/cloud/internal/codec"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// ServerError is a fatal Error frame answered to a Hello.
type ServerError struct{ Frame *pb.Error }

func (e *ServerError) Error() string {
	return fmt.Sprintf("server error %v: %s (retry after %d ms)", e.Frame.GetCode(), e.Frame.GetMessage(), e.Frame.GetRetryAfterMs())
}

// Client is a fake plugin connection.
type Client struct {
	conn    *websocket.Conn
	dec     *codec.Decoder
	Welcome *pb.Welcome // set by a successful Hello
}

// Dial opens a WebSocket to url (ws://host/v1/connect). header may be nil.
func Dial(ctx context.Context, url string, header http.Header) (*Client, error) {
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(codec.MaxDecompressed + 1)
	dec, err := codec.NewDecoder(codec.Limits{})
	if err != nil {
		conn.CloseNow()
		return nil, err
	}
	return &Client{conn: conn, dec: dec}, nil
}

// Hello sends h (codec 0x00) and reads the answer: the Welcome, or a *ServerError for a fatal Error.
func (c *Client) Hello(ctx context.Context, h *pb.Hello) (*pb.Welcome, error) {
	if err := c.Send(ctx, &pb.ClientFrame{Kind: &pb.ClientFrame_Hello{Hello: h}}, codec.Raw); err != nil {
		return nil, err
	}
	f, err := c.Read(ctx)
	if err != nil {
		return nil, err
	}
	switch k := f.Kind.(type) {
	case *pb.ServerFrame_Welcome:
		c.Welcome = k.Welcome
		return k.Welcome, nil
	case *pb.ServerFrame_Error:
		return nil, &ServerError{Frame: k.Error}
	default:
		return nil, fmt.Errorf("testkit: expected welcome, got %T", f.Kind)
	}
}

// Send encodes f with the given codec (codec.Raw, codec.Zstd or codec.Deflate) and sends it.
func (c *Client) Send(ctx context.Context, f *pb.ClientFrame, codecByte byte) error {
	msg, err := codec.EncodeClientFrame(f, codecByte)
	if err != nil {
		return err
	}
	return c.SendRaw(ctx, msg)
}

// SendRaw sends msg as one binary message (malformed-input tests).
func (c *Client) SendRaw(ctx context.Context, msg []byte) error {
	return c.conn.Write(ctx, websocket.MessageBinary, msg)
}

// SendBatch sends a Batch.
func (c *Client) SendBatch(ctx context.Context, b *pb.Batch, codecByte byte) error {
	return c.Send(ctx, &pb.ClientFrame{Kind: &pb.ClientFrame_Batch{Batch: b}}, codecByte)
}

// Ping sends a heartbeat.
func (c *Client) Ping(ctx context.Context, p *pb.Ping) error {
	return c.Send(ctx, &pb.ClientFrame{Kind: &pb.ClientFrame_Ping{Ping: p}}, codec.Raw)
}

// AckVerdict answers a Verdict.
func (c *Client) AckVerdict(ctx context.Context, verdictID string, executed bool, note string) error {
	return c.Send(ctx, &pb.ClientFrame{Kind: &pb.ClientFrame_VerdictAck{VerdictAck: &pb.VerdictAck{VerdictId: verdictID, Executed: executed, Note: note}}}, codec.Raw)
}

// Goodbye announces a clean disconnect.
func (c *Client) Goodbye(ctx context.Context, reason string) error {
	return c.Send(ctx, &pb.ClientFrame{Kind: &pb.ClientFrame_Goodbye{Goodbye: &pb.Goodbye{Reason: reason}}}, codec.Raw)
}

// Read returns the next ServerFrame.
func (c *Client) Read(ctx context.Context) (*pb.ServerFrame, error) {
	typ, msg, err := c.conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	if typ != websocket.MessageBinary {
		return nil, errors.New("testkit: server sent a text message")
	}
	return c.dec.DecodeServerFrame(msg)
}

// ReadUntil reads frames until match returns true and returns that frame.
func (c *Client) ReadUntil(ctx context.Context, match func(*pb.ServerFrame) bool) (*pb.ServerFrame, error) {
	for {
		f, err := c.Read(ctx)
		if err != nil {
			return nil, err
		}
		if match(f) {
			return f, nil
		}
	}
}

// WaitBatchAck reads until a BatchAck covering seq arrives.
func (c *Client) WaitBatchAck(ctx context.Context, seq uint64) error {
	_, err := c.ReadUntil(ctx, func(f *pb.ServerFrame) bool { return f.GetBatchAck() != nil && f.GetBatchAck().Seq >= seq })
	return err
}

// ReadVerdict reads until a Verdict arrives.
func (c *Client) ReadVerdict(ctx context.Context) (*pb.Verdict, error) {
	f, err := c.ReadUntil(ctx, func(f *pb.ServerFrame) bool { return f.GetVerdict() != nil })
	if err != nil {
		return nil, err
	}
	return f.GetVerdict(), nil
}

// Close closes the connection normally.
func (c *Client) Close() error {
	defer c.dec.Close()
	return c.conn.Close(websocket.StatusNormalClosure, "")
}

// CloseNow drops the connection without a close handshake (crash).
func (c *Client) CloseNow() error {
	defer c.dec.Close()
	return c.conn.CloseNow()
}

// CloseCode returns the WebSocket close code carried by a Read error (e.g. 4001), or -1.
func CloseCode(err error) int { return int(websocket.CloseStatus(err)) }
