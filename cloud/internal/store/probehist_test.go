package store

import (
	"math"
	"testing"
)

func TestProbeHistogramBuckets(t *testing.T) {
	if ProbeBucketUpperUs(0) != 100 || ProbeBucketUpperUs(62) != 60_000_000 || ProbeBucketUpperUs(63) != math.MaxInt64 {
		t.Fatalf("bounds %d %d", ProbeBucketUpperUs(0), ProbeBucketUpperUs(62))
	}
	for i := 1; i < ProbeBuckets-1; i++ {
		r := float64(ProbeBucketUpperUs(i)) / float64(ProbeBucketUpperUs(i-1))
		if r < 1.2 || r > 1.3 {
			t.Fatalf("bucket %d ratio %v", i, r)
		}
	}
	var h ProbeHistogram
	for _, us := range []int64{0, 100, 101, 60_000_000, 60_000_001, 1 << 40} {
		h.Observe(us)
	}
	if h[0] != 2 || h[1] != 1 || h[62] != 1 || h[63] != 2 || h.Count() != 6 {
		t.Fatalf("placement %v", h)
	}
}

func TestProbeHistogramQuantiles(t *testing.T) {
	var h ProbeHistogram
	if h.Quantile(0.95) != 0 {
		t.Fatal("empty quantile")
	}
	for i := 1; i <= 1000; i++ {
		h.Observe(int64(i) * 100) // 0.1 ms .. 100 ms uniform
	}
	for _, c := range []struct {
		q    float64
		want int64
	}{{0.5, 50_000}, {0.95, 95_000}, {0.99, 99_000}} {
		got := h.Quantile(c.q)
		if got < c.want || float64(got) > float64(c.want)*1.25 {
			t.Errorf("q%.2f = %d, want an upper bound within 25%% of %d", c.q, got, c.want)
		}
	}
	var other ProbeHistogram
	other.Observe(5_000_000)
	h.Merge(other)
	if h.Count() != 1001 {
		t.Fatal("merge")
	}
	s := Summarize(h, 5_000_000)
	if s.Count != 1001 || s.MaxUs != 5_000_000 || s.P99 > 5_000_000 || s.P50 < 50_000 {
		t.Fatalf("summary %+v", s)
	}
	capped := Summarize(func() ProbeHistogram { var x ProbeHistogram; x.Observe(101); return x }(), 101)
	if capped.P50 != 101 {
		t.Fatalf("quantile must be capped at the max: %d", capped.P50)
	}
	m := MergeProbeStats([]ProbeStat{{Histogram: h, MaxUs: 5_000_000}, {Histogram: other, MaxUs: 6_000_000}})
	if m.Count != 1002 || m.MaxUs != 6_000_000 {
		t.Fatalf("merged %+v", m)
	}
}
