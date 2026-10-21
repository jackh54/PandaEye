package stream

import (
	"errors"
	"strconv"
	"strings"

	"github.com/jackh54/PandaEye/cloud/internal/identity"
)

// ErrBadToken is returned when a tenant or server id is not a safe subject/key token.
var ErrBadToken = errors.New("stream: invalid tenant or server id")

// EventsSubject returns pe.events.<NN>.
func EventsSubject(partition int) string { return "pe.events." + identity.FormatPartition(partition) }

// SignalsSubject returns pe.signals.<NN>.
func SignalsSubject(partition int) string { return "pe.signals." + identity.FormatPartition(partition) }

// VerdictsSubject returns pe.verdicts.<tenant>.
func VerdictsSubject(tenantID string) (string, error) {
	if !identity.ValidTenantID(tenantID) {
		return "", ErrBadToken
	}
	return "pe.verdicts." + tenantID, nil
}

// AcksSubject returns pe.acks.<tenant>.
func AcksSubject(tenantID string) (string, error) {
	if !identity.ValidTenantID(tenantID) {
		return "", ErrBadToken
	}
	return "pe.acks." + tenantID, nil
}

// OutSubject returns pe.out.<tenant>.<server> (core NATS, SignedOutbound).
func OutSubject(tenantID, serverID string) (string, error) {
	k, err := ServerKey(tenantID, serverID)
	if err != nil {
		return "", err
	}
	return "pe.out." + k, nil
}

// ServerKey returns "<tenant>.<server>", the key of a server in PE_CONNECTIONS and PE_SERVERS.
func ServerKey(tenantID, serverID string) (string, error) {
	if !identity.ValidTenantID(tenantID) || !identity.ValidServerID(serverID) {
		return "", ErrBadToken
	}
	return tenantID + "." + serverID, nil
}

// ParseServerKey splits a ServerKey.
func ParseServerKey(key string) (tenantID, serverID string, err error) {
	t, s, ok := strings.Cut(key, ".")
	if !ok || !identity.ValidTenantID(t) || !identity.ValidServerID(s) {
		return "", "", ErrBadToken
	}
	return t, s, nil
}

// ErasureKey returns "<tenant>.<player hex>", the key of an erasure tombstone.
func ErasureKey(tenantID string, player identity.UUID) (string, error) {
	if !identity.ValidTenantID(tenantID) {
		return "", ErrBadToken
	}
	return tenantID + "." + strings.ReplaceAll(player.String(), "-", ""), nil
}

// PartitionOf returns NN of a pe.events.NN or pe.signals.NN subject.
func PartitionOf(subject string) (int, error) {
	i := strings.LastIndexByte(subject, '.')
	n, err := strconv.Atoi(subject[i+1:])
	if i < 0 || err != nil || n < 0 {
		return 0, errors.New("stream: subject has no partition: " + subject)
	}
	return n, nil
}

// Lease keys in PE_LEASES. Partition leases are "<group>.<NN>".
const (
	GroupEvents  = "events"  // workers: pe.events.<NN>
	GroupSignals = "signals" // verdict services: pe.signals.<NN>

	LeaseWriter   = "writer"   // the store writer (PE_VERDICTS)
	LeaseMetering = "metering" // the api metering loop
	LeaseJanitor  = "janitor"  // the api janitor
)

// PartitionLeaseKey returns "<group>.<NN>".
func PartitionLeaseKey(group string, partition int) string {
	return group + "." + identity.FormatPartition(partition)
}

// EventsMsgID returns the JetStream msg id of a StreamBatch: <tenant>:<server>:<seq>:<NN>.
func EventsMsgID(tenantID, serverID string, batchSeq uint64, partition int) string {
	return tenantID + ":" + serverID + ":" + strconv.FormatUint(batchSeq, 10) + ":" + identity.FormatPartition(partition)
}

// SignalMsgID returns the msg id of a signal:
// <tenant>:<server>:<batch_seq>:<NN>:<event index>:<check>:<emit ordinal>.
func SignalMsgID(tenantID, serverID string, batchSeq uint64, partition, eventIndex int, checkID string, ordinal int) string {
	return EventsMsgID(tenantID, serverID, batchSeq, partition) + ":" + strconv.Itoa(eventIndex) + ":" + checkID + ":" + strconv.Itoa(ordinal)
}

// SessionMarkerMsgID returns the msg id of a session marker:
// <tenant>:<server>:<batch_seq>:<NN>:<event index>:session.
func SessionMarkerMsgID(tenantID, serverID string, batchSeq uint64, partition, eventIndex int) string {
	return EventsMsgID(tenantID, serverID, batchSeq, partition) + ":" + strconv.Itoa(eventIndex) + ":session"
}

// VerdictRecordMsgID returns the msg id of a VerdictRecord: the verdict id.
func VerdictRecordMsgID(verdictID string) string { return verdictID }

// AckRecordMsgID returns the msg id of an AckRecord: ack:<verdict id>.
func AckRecordMsgID(verdictID string) string { return "ack:" + verdictID }
