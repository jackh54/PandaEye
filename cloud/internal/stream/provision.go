package stream

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/nats-io/nats.go/jetstream"
)

// ErasureTombstoneAge is how long erasure tombstones are kept (ARCHITECTURE 5.4): past the 7 d of the
// longest stream workers and verdict services read from, so no late data can arrive afterwards. The
// janitor deletes older ones with PurgeErasureTombstones.
const ErasureTombstoneAge = 8 * 24 * time.Hour

// provision creates or updates every stream and KV bucket of ARCHITECTURE 5.4.
func (b *Bus) provision(ctx context.Context) error {
	r := b.cfg.Replicas
	streams := []jetstream.StreamConfig{
		{
			Name: StreamEvents, Subjects: []string{"pe.events.*"}, Storage: jetstream.FileStorage,
			Retention: jetstream.LimitsPolicy, Discard: jetstream.DiscardOld, MaxAge: b.cfg.EventsMaxAge,
			MaxBytes: b.cfg.EventsMaxBytes, Duplicates: 10 * time.Minute, Replicas: r, MaxMsgSize: MaxPayload,
		},
		{
			Name: StreamSignals, Subjects: []string{"pe.signals.*"}, Storage: jetstream.FileStorage,
			Retention: jetstream.LimitsPolicy, Discard: jetstream.DiscardOld, MaxAge: 7 * 24 * time.Hour,
			Duplicates: 10 * time.Minute, Replicas: r, MaxMsgSize: MaxPayload,
		},
		{
			Name: StreamVerdicts, Subjects: []string{"pe.verdicts.*", "pe.acks.*"}, Storage: jetstream.FileStorage,
			Retention: jetstream.LimitsPolicy, Discard: jetstream.DiscardOld, MaxAge: 30 * 24 * time.Hour,
			Duplicates: time.Hour, Replicas: r, MaxMsgSize: MaxPayload,
		},
	}
	for _, sc := range streams {
		if _, err := b.JS.CreateOrUpdateStream(ctx, sc); err != nil {
			return fmt.Errorf("stream: provision %s: %w", sc.Name, err)
		}
	}
	buckets := []jetstream.KeyValueConfig{
		{Bucket: BucketLeases, TTL: b.cfg.Lease.TTL, History: 1},
		{Bucket: BucketConnections, TTL: b.cfg.ConnectionTTL, History: 1},
		{Bucket: BucketServers, History: 1},
		{Bucket: BucketTenants, History: 1},
		{Bucket: BucketMeta, History: 1},
		{Bucket: BucketErasures, History: 1},
	}
	kvs := make([]jetstream.KeyValue, len(buckets))
	for i, kc := range buckets {
		kc.Storage, kc.Replicas = jetstream.FileStorage, r
		kv, err := b.JS.CreateOrUpdateKeyValue(ctx, kc)
		if err != nil {
			return fmt.Errorf("stream: provision %s: %w", kc.Bucket, err)
		}
		kvs[i] = kv
	}
	b.Leases, b.Connections, b.Servers, b.Tenants, b.Meta, b.Erasures = kvs[0], kvs[1], kvs[2], kvs[3], kvs[4], kvs[5]
	return nil
}

// Meta keys.
const (
	MetaPartitions   = "partitions"
	MetaPartitionKey = "partition_key"
)

// ErrPartitionMismatch is returned when PE_PARTITIONS differs from the count stored in PE_META.
var ErrPartitionMismatch = errors.New("stream: PE_PARTITIONS does not match the partition count stored in KV PE_META")

// ensureMeta stores the partition count and siphash key on first start and verifies them later.
func (b *Bus) ensureMeta(ctx context.Context) (identity.PartitionKey, error) {
	raw, err := createOrGet(ctx, b.Meta, MetaPartitionKey, []byte(identity.NewPartitionKey().Hex()))
	if err != nil {
		return identity.PartitionKey{}, err
	}
	key, err := identity.ParsePartitionKey(string(raw))
	if err != nil {
		return identity.PartitionKey{}, fmt.Errorf("stream: PE_META %s: %w", MetaPartitionKey, err)
	}
	raw, err = createOrGet(ctx, b.Meta, MetaPartitions, []byte(strconv.Itoa(b.cfg.Partitions)))
	if err != nil {
		return identity.PartitionKey{}, err
	}
	if stored, err := strconv.Atoi(string(raw)); err != nil || stored != b.cfg.Partitions {
		return identity.PartitionKey{}, fmt.Errorf("%w (stored %q, configured %d)", ErrPartitionMismatch, raw, b.cfg.Partitions)
	}
	return key, nil
}

// createOrGet creates key with value unless it exists, and returns the stored value.
func createOrGet(ctx context.Context, kv jetstream.KeyValue, key string, value []byte) ([]byte, error) {
	if _, err := kv.Create(ctx, key, value); err == nil {
		return value, nil
	} else if !errors.Is(err, jetstream.ErrKeyExists) {
		return nil, fmt.Errorf("stream: %s/%s: %w", kv.Bucket(), key, err)
	}
	e, err := kv.Get(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("stream: %s/%s: %w", kv.Bucket(), key, err)
	}
	return e.Value(), nil
}
