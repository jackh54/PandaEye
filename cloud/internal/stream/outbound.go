package stream

import (
	"errors"

	"github.com/jackh54/PandaEye/cloud/internal/identity"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
	"google.golang.org/protobuf/proto"
)

// ErrBadSignature is returned for SignedOutbound messages whose mac does not verify.
var ErrBadSignature = errors.New("stream: outbound signature mismatch")

// EncodeOutbound serialises out as a SignedOutbound: payload = Outbound bytes, mac =
// HMAC-SHA256(PE_OUTBOUND_KEY, payload).
func EncodeOutbound(key []byte, out *pb.Outbound) ([]byte, error) {
	payload, err := proto.Marshal(out)
	if err != nil {
		return nil, err
	}
	return proto.Marshal(&pb.SignedOutbound{Payload: payload, Mac: identity.SignOutbound(key, payload)})
}

// DecodeOutbound verifies and parses a SignedOutbound. The gateway must still check that the
// tenant and server ids match the connection it is delivering to (OutboundFor does both).
func DecodeOutbound(key, data []byte) (*pb.Outbound, error) {
	var so pb.SignedOutbound
	if err := proto.Unmarshal(data, &so); err != nil {
		return nil, err
	}
	if !identity.VerifyOutbound(key, so.Payload, so.Mac) {
		return nil, ErrBadSignature
	}
	var out pb.Outbound
	if err := proto.Unmarshal(so.Payload, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// OutboundFor decodes a SignedOutbound and rejects it unless it is addressed to (tenantID, serverID).
func OutboundFor(key, data []byte, tenantID, serverID string) (*pb.Outbound, error) {
	out, err := DecodeOutbound(key, data)
	if err != nil {
		return nil, err
	}
	if out.TenantId != tenantID || out.ServerId != serverID {
		return nil, ErrBadSignature
	}
	return out, nil
}

// PublishOutbound signs out and publishes it on core NATS to pe.out.<tenant>.<server>.
func (b *Bus) PublishOutbound(key []byte, out *pb.Outbound) error {
	subj, err := OutSubject(out.TenantId, out.ServerId)
	if err != nil {
		return err
	}
	data, err := EncodeOutbound(key, out)
	if err != nil {
		return err
	}
	return b.NC.Publish(subj, data)
}
