package identity

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"

	"github.com/dchest/siphash"
)

// PartitionKey is the 128-bit siphash key stored in KV PE_META (`partition_key`). It keeps players from
// choosing UUIDs that pile onto one partition.
type PartitionKey [16]byte

// NewPartitionKey returns a random key (first start of a deployment).
func NewPartitionKey() PartitionKey { return PartitionKey(RandomKey(16)) }

// ParsePartitionKey parses the hex form stored in PE_META.
func ParsePartitionKey(s string) (PartitionKey, error) {
	var k PartitionKey
	if len(s) != 2*len(k) {
		return k, errors.New("identity: partition key must be 32 hex characters")
	}
	if _, err := hex.Decode(k[:], []byte(s)); err != nil {
		return PartitionKey{}, fmt.Errorf("identity: partition key: %w", err)
	}
	return k, nil
}

// Hex returns the form stored in PE_META.
func (k PartitionKey) Hex() string { return hex.EncodeToString(k[:]) }

// Hash returns siphash-2-4 of data under k (k0, k1 = the key's two little-endian 64-bit halves).
func (k PartitionKey) Hash(data []byte) uint64 {
	return siphash.Hash(binary.LittleEndian.Uint64(k[0:8]), binary.LittleEndian.Uint64(k[8:16]), data)
}

// Partitioner maps players, servers and probes to partitions 0..n-1.
type Partitioner struct {
	key PartitionKey
	n   int
}

// NewPartitioner returns a partitioner for n partitions.
func NewPartitioner(key PartitionKey, n int) (Partitioner, error) {
	if n < 1 {
		return Partitioner{}, errors.New("identity: partition count must be >= 1")
	}
	return Partitioner{key: key, n: n}, nil
}

// Count returns the number of partitions.
func (p Partitioner) Count() int { return p.n }

// ForUUID returns siphash24(key, 16 uuid bytes) % n.
func (p Partitioner) ForUUID(u UUID) int { return int(p.key.Hash(u[:]) % uint64(p.n)) }

// ForPlayer returns the partition of a player given in wire form (16 bytes).
func (p Partitioner) ForPlayer(player []byte) (int, error) {
	u, err := UUIDFromBytes(player)
	if err != nil {
		return 0, err
	}
	return p.ForUUID(u), nil
}

// ForServer returns the partition of server-level events: siphash of the server id's 16 bytes.
func (p Partitioner) ForServer(serverID string) (int, error) {
	u, err := ServerIDBytes(serverID)
	if err != nil {
		return 0, err
	}
	return p.ForUUID(u), nil
}

// ForProbe returns the round-robin partition of a probe (probe_id % n), so every partition owner is
// measured.
func (p Partitioner) ForProbe(probeID uint64) int { return int(probeID % uint64(p.n)) }

// Valid reports whether partition is in 0..n-1 (for explicitly chosen partitions).
func (p Partitioner) Valid(partition int) bool { return partition >= 0 && partition < p.n }

// FormatPartition returns the subject/key form of a partition number: at least two digits ("07").
func FormatPartition(partition int) string {
	if partition < 10 && partition >= 0 {
		return "0" + strconv.Itoa(partition)
	}
	return strconv.Itoa(partition)
}
