package store

import (
	"math"
	"sort"
	"time"
)

// Probe histogram layout (ARCHITECTURE 5.3): 64 log-spaced buckets. Bucket i < 63 holds RTTs up to
// ProbeBucketUpperUs(i) (100 us for bucket 0, 60 s for bucket 62, about 24% apart); bucket 63 holds
// RTTs above 60 s.
const (
	ProbeBuckets    = 64
	probeMinUs      = 100
	probeMaxUs      = 60_000_000
	probeLastFinite = ProbeBuckets - 2
)

var probeUpper = func() [ProbeBuckets - 1]int64 {
	var b [ProbeBuckets - 1]int64
	ratio := math.Pow(probeMaxUs/probeMinUs, 1/float64(probeLastFinite))
	for i := range b {
		b[i] = int64(math.Ceil(probeMinUs * math.Pow(ratio, float64(i))))
	}
	b[probeLastFinite] = probeMaxUs
	return b
}()

// ProbeBucketUpperUs returns the upper bound of bucket i in microseconds (math.MaxInt64 for the
// overflow bucket).
func ProbeBucketUpperUs(i int) int64 {
	if i >= len(probeUpper) {
		return math.MaxInt64
	}
	return probeUpper[i]
}

// ProbeHistogram counts probe RTTs per bucket. The zero value is empty; histograms merge by adding.
type ProbeHistogram [ProbeBuckets]uint64

// Observe adds one RTT in microseconds.
func (h *ProbeHistogram) Observe(us int64) {
	i := sort.Search(len(probeUpper), func(i int) bool { return probeUpper[i] >= us })
	h[i]++
}

// Merge adds o to h.
func (h *ProbeHistogram) Merge(o ProbeHistogram) {
	for i := range h {
		h[i] += o[i]
	}
}

// Count returns the number of observations.
func (h *ProbeHistogram) Count() uint64 {
	var n uint64
	for _, c := range h {
		n += c
	}
	return n
}

// Quantile returns an upper bound of the q-quantile (0 < q <= 1): the upper bound of the bucket that
// holds it, so a reported p95 is never better than the truth. 0 when empty; the overflow bucket
// reports 60 s.
func (h *ProbeHistogram) Quantile(q float64) int64 {
	n := h.Count()
	if n == 0 {
		return 0
	}
	rank := uint64(math.Ceil(q * float64(n)))
	if rank < 1 {
		rank = 1
	}
	var cum uint64
	for i, c := range h {
		if cum += c; cum >= rank {
			return min(ProbeBucketUpperUs(i), probeMaxUs)
		}
	}
	return probeMaxUs
}

// Probe sources.
const (
	ProbeSourcePlugin = "plugin" // Ping.last_probe_rtt_us: the SLO
	ProbeSourceCloud  = "cloud"  // gateway receive -> gateway send
)

// ProbeStat is one stored minute of probe RTTs for a server and source.
type ProbeStat struct {
	TenantID  string
	ServerID  string
	Source    string
	Minute    time.Time
	Histogram ProbeHistogram
	MaxUs     int64
}

// ProbeSummary is merged histograms with their percentiles (upper bounds, capped at MaxUs).
type ProbeSummary struct {
	Count         uint64
	P50, P95, P99 int64
	MaxUs         int64
	Histogram     ProbeHistogram
}

// Summarize computes the percentiles of a merged histogram.
func Summarize(h ProbeHistogram, maxUs int64) ProbeSummary {
	s := ProbeSummary{Count: h.Count(), MaxUs: maxUs, Histogram: h}
	if s.Count == 0 {
		return s
	}
	capped := func(v int64) int64 {
		if maxUs > 0 && v > maxUs {
			return maxUs
		}
		return v
	}
	s.P50, s.P95, s.P99 = capped(h.Quantile(0.50)), capped(h.Quantile(0.95)), capped(h.Quantile(0.99))
	return s
}

// MergeProbeStats merges rows into one summary.
func MergeProbeStats(rows []ProbeStat) ProbeSummary {
	var h ProbeHistogram
	var maxUs int64
	for _, r := range rows {
		h.Merge(r.Histogram)
		maxUs = max(maxUs, r.MaxUs)
	}
	return Summarize(h, maxUs)
}
