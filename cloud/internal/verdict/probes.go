package verdict

import (
	"context"
	"sync"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/store"
)

// probeFlushInterval is how often aggregated probe timings are written to probe_stats.
const probeFlushInterval = 5 * time.Second

type probeKey struct {
	tenant, server string
	minute         int64 // unix seconds of the minute
}

type probeBucket struct {
	h     store.ProbeHistogram
	maxUs int64
}

// probeStats aggregates the cloud-side probe latency (gateway receive -> verdict service) per
// (tenant, server, minute) and writes it to probe_stats (source "cloud") in the background, so the
// partition loop never waits on the database.
type probeStats struct {
	mu sync.Mutex
	m  map[probeKey]*probeBucket
}

func newProbeStats() *probeStats { return &probeStats{m: map[probeKey]*probeBucket{}} }

func (ps *probeStats) observe(tenant, server string, at time.Time, rttUs int64) {
	if rttUs < 0 {
		rttUs = 0
	}
	k := probeKey{tenant, server, at.UTC().Truncate(time.Minute).Unix()}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	b := ps.m[k]
	if b == nil {
		b = &probeBucket{}
		ps.m[k] = b
	}
	b.h.Observe(rttUs)
	b.maxUs = max(b.maxUs, rttUs)
}

// flush writes and clears the aggregated buckets. Buckets that fail to write are merged back unless
// they are older than 10 minutes.
func (ps *probeStats) flush(ctx context.Context, st store.ProbeStatsStore) error {
	ps.mu.Lock()
	cur := ps.m
	ps.m = map[probeKey]*probeBucket{}
	ps.mu.Unlock()
	var first error
	for k, b := range cur {
		minute := time.Unix(k.minute, 0).UTC()
		err := st.RecordProbeStats(ctx, k.tenant, k.server, store.ProbeSourceCloud, minute, b.h, b.maxUs)
		if err == nil {
			continue
		}
		if first == nil {
			first = err
		}
		if time.Since(minute) > 10*time.Minute {
			continue
		}
		ps.mu.Lock()
		if nb := ps.m[k]; nb != nil {
			nb.h.Merge(b.h)
			nb.maxUs = max(nb.maxUs, b.maxUs)
		} else {
			ps.m[k] = b
		}
		ps.mu.Unlock()
	}
	return first
}
