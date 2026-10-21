package gateway

import (
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/codec"
	"github.com/jackh54/PandaEye/cloud/internal/license"
	"github.com/jackh54/PandaEye/cloud/internal/store"
)

// Options tunes the gateway. Zero values take the production values of ARCHITECTURE 3 and 5.3;
// tests shorten the timings and budgets.
type Options struct {
	Listen string // default PE_GATEWAY_LISTEN

	HelloTimeout      time.Duration // first frame deadline (10 s)
	HeartbeatInterval time.Duration // Welcome.heartbeat_interval_ms (5 s); 3 silent intervals close
	RenewInterval     time.Duration // PE_CONNECTIONS renewal (10 s)
	SweepInterval     time.Duration // pending verdict sweep (10 s)
	ResendAfter       time.Duration // re-send unacked verdicts delivered this long ago; per-connection dedup (15 s)
	DuplicateSilence  time.Duration // a connection silent this long may be replaced by another instance (15 s)
	PublishTimeout    time.Duration // keep retrying a stuck stream publish this long before closing (60 s)
	CloseTimeout      time.Duration // close handshake before the TCP connection is dropped (2 s)
	DrainTimeout      time.Duration // graceful shutdown budget for open connections (4 s)

	MaxBatchEvents   int            // Welcome.max_batch_events (4096)
	LiveEventsPerS   int            // per connection (20 000)
	LiveEventsBurst  int            // (100 000)
	BytesPerS        int            // per connection, wire bytes (2 MiB)
	ReplayEventsPerS int            // per connection, replayed batches (15 000)
	PlanEventsPerS   map[string]int // per tenant by plan (free: 3 000); plans without an entry are unlimited

	ConnsPerIPPerMin      int           // new connections per client IP (30)
	HellosPerTenantPerMin int           // Hellos per tenant (60)
	AuthFailures          int           // licence failures per IPv4 /32 or IPv6 /64 ... (5)
	AuthFailureWindow     time.Duration // ... within this window (10 min) ...
	AuthBlock             time.Duration // ... block the prefix this long (1 h)

	OutboundQueue int // per-connection outbound queue; overflow closes (1 000)

	Verifier license.Verifier // default: the process's shared verifier (license.New)
}

func (o Options) withDefaults() Options {
	dur := func(v *time.Duration, d time.Duration) {
		if *v <= 0 {
			*v = d
		}
	}
	num := func(v *int, d int) {
		if *v <= 0 {
			*v = d
		}
	}
	dur(&o.HelloTimeout, 10*time.Second)
	dur(&o.HeartbeatInterval, 5*time.Second)
	dur(&o.RenewInterval, 10*time.Second)
	dur(&o.SweepInterval, 10*time.Second)
	dur(&o.ResendAfter, store.PendingResend)
	dur(&o.DuplicateSilence, 15*time.Second)
	dur(&o.PublishTimeout, 60*time.Second)
	dur(&o.CloseTimeout, 2*time.Second)
	dur(&o.DrainTimeout, 4*time.Second)
	num(&o.MaxBatchEvents, codec.DefaultMaxBatchEvents)
	num(&o.LiveEventsPerS, 20_000)
	num(&o.LiveEventsBurst, 100_000)
	num(&o.BytesPerS, 2<<20)
	num(&o.ReplayEventsPerS, 15_000)
	if o.PlanEventsPerS == nil {
		o.PlanEventsPerS = map[string]int{store.PlanFree: 3_000}
	}
	num(&o.ConnsPerIPPerMin, 30)
	num(&o.HellosPerTenantPerMin, 60)
	num(&o.AuthFailures, 5)
	dur(&o.AuthFailureWindow, 10*time.Minute)
	dur(&o.AuthBlock, time.Hour)
	num(&o.OutboundQueue, 1000)
	return o
}
