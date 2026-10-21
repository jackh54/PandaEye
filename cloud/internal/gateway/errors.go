package gateway

import (
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

// fatal is an Error frame that ends the connection.
func fatal(code pb.Error_Code, msg string, retryAfterMs uint32) *pb.Error {
	return &pb.Error{Code: code, Message: msg, Fatal: true, RetryAfterMs: retryAfterMs}
}

func errorFrame(e *pb.Error) *pb.ServerFrame { return &pb.ServerFrame{Kind: &pb.ServerFrame_Error{Error: e}} }
