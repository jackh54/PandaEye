package gateway

import (
	"context"
	"errors"
	"io"
	"net"
	"syscall"

	"github.com/coder/websocket"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// closeCode maps a fatal Error code to its WebSocket close code (PROTOCOL 10).
func closeCode(c pb.Error_Code) websocket.StatusCode {
	switch c {
	case pb.Error_CODE_BAD_REQUEST:
		return 4000
	case pb.Error_CODE_UNAUTHENTICATED:
		return 4001
	case pb.Error_CODE_LICENSE_LIMIT:
		return 4002
	case pb.Error_CODE_UNSUPPORTED_PROTOCOL:
		return 4003
	case pb.Error_CODE_DUPLICATE_SERVER:
		return 4004
	case pb.Error_CODE_RATE_LIMITED:
		return 4008
	case pb.Error_CODE_SHUTTING_DOWN:
		return websocket.StatusGoingAway
	default:
		return websocket.StatusInternalError
	}
}

// authResult is the metrics label of a fatal handshake error (pandaeye_gateway_auth_total).
func authResult(c pb.Error_Code) string {
	switch c {
	case pb.Error_CODE_UNAUTHENTICATED:
		return "invalid"
	case pb.Error_CODE_LICENSE_LIMIT:
		return "limit"
	case pb.Error_CODE_DUPLICATE_SERVER:
		return "duplicate"
	case pb.Error_CODE_RATE_LIMITED:
		return "rate_limited"
	case pb.Error_CODE_INTERNAL:
		return "unavailable"
	case pb.Error_CODE_SHUTTING_DOWN:
		return "shutting_down"
	default:
		return "bad_request"
	}
}

// closeCause groups close reasons (pandaeye_gateway_connections_closed_total) by what ended the
// connection, for the close log line.
func closeCause(reason string) string {
	switch reason {
	case "client", "client_error", "goodbye":
		return "peer_close"
	case "connection_lost":
		return "read_error"
	case "write_failed":
		return "write_error"
	case "silent":
		return "silent_timeout"
	case "replaced":
		return "replaced"
	case "shutdown", "shutting_down":
		return "shutdown"
	case "invalid", "limit", "duplicate", "rate_limited":
		return "refused"
	case "bad_request", "message_too_big":
		return "protocol_error"
	default: // unavailable, internal
		return "internal_error"
	}
}

// ioErrorKind names a read or write error for the close log line without its text, which carries
// the peer's address.
func ioErrorKind(err error) string {
	var ne net.Error
	switch {
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "eof"
	case errors.Is(err, syscall.ECONNRESET):
		return "reset"
	case errors.Is(err, syscall.EPIPE):
		return "broken_pipe"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return "timeout"
	case errors.Is(err, net.ErrClosed):
		return "closed"
	default:
		return "other"
	}
}

// fatal is an Error frame that ends the connection.
func fatal(code pb.Error_Code, msg string, retryAfterMs uint32) *pb.Error {
	return &pb.Error{Code: code, Message: msg, Fatal: true, RetryAfterMs: retryAfterMs}
}

func errorFrame(e *pb.Error) *pb.ServerFrame {
	return &pb.ServerFrame{Kind: &pb.ServerFrame_Error{Error: e}}
}
