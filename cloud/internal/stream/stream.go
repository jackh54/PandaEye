// Package stream is PandaEye's event bus on NATS JetStream (ARCHITECTURE 5.4): the embedded
// in-process server for `all`, external connections, provisioning of every stream and KV bucket,
// partition metadata, subject builders, publish helpers that wait for every PubAck, leases with
// self-fencing, consumer epochs, the ordered partition consumer loop, ordered replays and KV helpers.
package stream

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/config"
	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/metrics"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Streams and KV buckets (ARCHITECTURE 5.4).
const (
	StreamEvents   = "PE_EVENTS"   // pe.events.<NN>: StreamBatch
	StreamSignals  = "PE_SIGNALS"  // pe.signals.<NN>: SignalEnvelope
	StreamVerdicts = "PE_VERDICTS" // pe.verdicts.<tenant>: VerdictRecord, pe.acks.<tenant>: AckRecord

	BucketLeases      = "PE_LEASES"      // events.<NN>, signals.<NN>, writer, metering, janitor, members.*
	BucketConnections = "PE_CONNECTIONS" // <tenant>.<server>: ConnectionInfo
	BucketServers     = "PE_SERVERS"     // <tenant>.<server>: ServerRecord
	BucketTenants     = "PE_TENANTS"     // <tenant>: TenantSettings
	BucketMeta        = "PE_META"        // partitions, partition_key
	BucketErasures    = "PE_ERASURES"    // <tenant>.<player hex>: erasure tombstones
)

// MaxPayload is the payload limit the bus needs (an 8 MiB batch).
const MaxPayload = 8 << 20

// Config configures Open. Zero durations take the production values; tests shorten them.
type Config struct {
	URL   string          // external NATS URL(s); empty = embedded, in-process server
	Creds string          // credentials file (external)
	Token identity.Secret // token (external)
	TLSCA string          // CA file (external)
	Name  string          // client connection name

	Dir        string // embedded JetStream storage
	SyncAlways bool   // embedded file store fsyncs every write (PE_NATS_SYNC=always)

	Replicas       int
	Partitions     int
	EventsMaxAge   time.Duration
	EventsMaxBytes int64

	OwnerID        string // stable owner id in lease tokens
	AdoptOwnLeases bool   // `all`: adopt leases still holding this owner id after a restart

	Lease               LeaseTimings
	ConnectionTTL       time.Duration // PE_CONNECTIONS TTL (30 s)
	PublishTimeout      time.Duration // per PubAck (5 s)
	MaxPendingPublishes int           // async publish window (partitions x 20 batches/s x 16 servers)
}

// ConfigFrom maps the process configuration.
func ConfigFrom(c *config.Config) Config {
	return Config{
		URL: c.NATS.URL, Creds: c.NATS.Creds, Token: c.NATS.Token, TLSCA: c.NATS.TLSCA,
		Name: "pandaeye-" + c.OwnerID,
		Dir:  c.NATS.Dir, SyncAlways: c.NATS.Sync == config.SyncAlways,
		Replicas: c.NATS.Replicas, Partitions: c.Partitions,
		EventsMaxAge: c.EventsMaxAge, EventsMaxBytes: c.EventsMaxBytes,
		OwnerID: c.OwnerID, AdoptOwnLeases: c.Role == config.RoleAll,
	}
}

func (c Config) withDefaults() Config {
	c.Lease = c.Lease.withDefaults()
	if c.ConnectionTTL <= 0 {
		c.ConnectionTTL = 30 * time.Second
	}
	if c.PublishTimeout <= 0 {
		c.PublishTimeout = 5 * time.Second
	}
	if c.Replicas <= 0 {
		c.Replicas = 1
	}
	if c.Partitions <= 0 {
		c.Partitions = 16
	}
	if c.EventsMaxAge <= 0 {
		c.EventsMaxAge = 24 * time.Hour
	}
	if c.EventsMaxBytes <= 0 {
		c.EventsMaxBytes = 10 << 30
	}
	if c.MaxPendingPublishes <= 0 {
		c.MaxPendingPublishes = c.Partitions * 20 * 16
	}
	if c.OwnerID == "" {
		c.OwnerID = "pandaeye"
	}
	return c
}

// Bus is a connected, provisioned event bus.
type Bus struct {
	NC          *nats.Conn
	JS          jetstream.JetStream
	Partitioner identity.Partitioner

	Leases      jetstream.KeyValue
	Connections jetstream.KeyValue
	Servers     jetstream.KeyValue
	Tenants     jetstream.KeyValue
	Meta        jetstream.KeyValue
	Erasures    jetstream.KeyValue

	// LeaseManager takes and renews this process's leases.
	LeaseManager *LeaseManager

	cfg Config
	srv *server.Server
	log *slog.Logger
	m   *metrics.Metrics
}

// Open connects (starting the embedded server when cfg.URL is empty), provisions every stream and
// bucket and loads or initialises the partition metadata. m may be nil.
func Open(ctx context.Context, cfg Config, log *slog.Logger, m *metrics.Metrics) (*Bus, error) {
	cfg = cfg.withDefaults()
	if m == nil {
		m = metrics.New()
	}
	b := &Bus{cfg: cfg, log: log, m: m}
	if err := b.connect(); err != nil {
		b.Close()
		return nil, err
	}
	if err := b.provision(ctx); err != nil {
		b.Close()
		return nil, err
	}
	key, err := b.ensureMeta(ctx)
	if err != nil {
		b.Close()
		return nil, err
	}
	if b.Partitioner, err = identity.NewPartitioner(key, cfg.Partitions); err != nil {
		b.Close()
		return nil, err
	}
	b.LeaseManager = newLeaseManager(b.Leases, cfg.OwnerID, cfg.AdoptOwnLeases, cfg.Lease, log, m)
	return b, nil
}

// Config returns the effective configuration.
func (b *Bus) Config() Config { return b.cfg }

// Embedded reports whether the bus runs the in-process server.
func (b *Bus) Embedded() bool { return b.srv != nil }

func (b *Bus) connect() error {
	opts := []nats.Option{
		nats.Name(b.cfg.Name),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil {
				b.log.Warn("nats disconnected", "err", err)
			}
		}),
		nats.ReconnectHandler(func(*nats.Conn) { b.log.Info("nats reconnected") }),
		nats.ErrorHandler(func(_ *nats.Conn, sub *nats.Subscription, err error) {
			subj := ""
			if sub != nil {
				subj = sub.Subject
			}
			b.log.Warn("nats async error", "subject", subj, "err", err)
		}),
	}
	url := b.cfg.URL
	if url == "" {
		srv, err := startEmbedded(b.cfg, b.log)
		if err != nil {
			return err
		}
		b.srv = srv
		opts = append(opts, nats.InProcessServer(srv))
	} else {
		if b.cfg.Creds != "" {
			opts = append(opts, nats.UserCredentials(b.cfg.Creds))
		}
		if !b.cfg.Token.IsZero() {
			opts = append(opts, nats.Token(b.cfg.Token.Reveal()))
		}
		if b.cfg.TLSCA != "" {
			opts = append(opts, nats.RootCAs(b.cfg.TLSCA))
		}
	}
	nc, err := nats.Connect(url, opts...)
	if err != nil {
		return fmt.Errorf("stream: connect: %w", err)
	}
	b.NC = nc
	if nc.MaxPayload() < MaxPayload {
		return fmt.Errorf("stream: NATS max_payload is %d bytes, PandaEye needs at least %d", nc.MaxPayload(), MaxPayload)
	}
	b.JS, err = jetstream.New(nc,
		jetstream.WithPublishAsyncMaxPending(b.cfg.MaxPendingPublishes),
		jetstream.WithPublishAsyncTimeout(b.cfg.PublishTimeout))
	if err != nil {
		return fmt.Errorf("stream: jetstream: %w", err)
	}
	return nil
}

func startEmbedded(cfg Config, log *slog.Logger) (*server.Server, error) {
	if cfg.Dir == "" {
		return nil, errors.New("stream: embedded server needs a storage directory")
	}
	srv, err := server.NewServer(&server.Options{
		ServerName: "pandaeye-embedded",
		DontListen: true,
		JetStream:  true,
		StoreDir:   cfg.Dir,
		MaxPayload: MaxPayload,
		SyncAlways: cfg.SyncAlways,
		NoSigs:     true,
	})
	if err != nil {
		return nil, fmt.Errorf("stream: embedded server: %w", err)
	}
	srv.SetLoggerV2(&natsLogger{log: log.With("component", "nats")}, false, false, false)
	srv.Start()
	if !srv.ReadyForConnections(30 * time.Second) {
		srv.Shutdown()
		return nil, errors.New("stream: embedded server did not start")
	}
	return srv, nil
}

// Close drains the connection (flushing pending publishes and acks) and stops the embedded server.
func (b *Bus) Close() error {
	var err error
	if b.NC != nil && !b.NC.IsClosed() {
		done := make(chan struct{})
		b.NC.SetClosedHandler(func(*nats.Conn) { close(done) })
		if derr := b.NC.Drain(); derr != nil {
			b.NC.Close()
			err = derr
		} else {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				b.NC.Close()
			}
		}
	}
	if b.srv != nil {
		b.srv.Shutdown()
		b.srv.WaitForShutdown()
	}
	return err
}

// natsLogger forwards embedded server logs to slog.
type natsLogger struct{ log *slog.Logger }

func (l *natsLogger) Noticef(format string, v ...any) { l.log.Debug(fmt.Sprintf(format, v...)) }
func (l *natsLogger) Warnf(format string, v ...any)   { l.log.Warn(fmt.Sprintf(format, v...)) }
func (l *natsLogger) Fatalf(format string, v ...any)  { l.log.Error(fmt.Sprintf(format, v...)) }
func (l *natsLogger) Errorf(format string, v ...any)  { l.log.Error(fmt.Sprintf(format, v...)) }
func (l *natsLogger) Debugf(format string, v ...any)  { l.log.Debug(fmt.Sprintf(format, v...)) }
func (l *natsLogger) Tracef(format string, v ...any)  {}
