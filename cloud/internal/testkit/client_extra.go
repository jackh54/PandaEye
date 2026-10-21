package testkit

import (
	"context"

	"github.com/coder/websocket"
)

// SendText sends a text message (protocol violation tests).
func (c *Client) SendText(ctx context.Context, s string) error {
	return c.conn.Write(ctx, websocket.MessageText, []byte(s))
}

// ReadErr reads until a fatal Error frame or the end of the connection and returns the Error
// (nil if none came) and the WebSocket close code (-1 when the connection ended without one).
func (c *Client) ReadErr(ctx context.Context) (*ServerError, int) {
	var se *ServerError
	for {
		f, err := c.Read(ctx)
		if err != nil {
			return se, CloseCode(err)
		}
		if e := f.GetError(); e != nil && e.Fatal {
			se = &ServerError{Frame: e}
		}
	}
}
