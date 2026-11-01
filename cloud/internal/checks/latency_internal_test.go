package checks

import "testing"

// A keep-alive whose pairing window lost pongs to the full ring is not judged: the pongs left would be
// the late part of the window only (in a released backlog, the ones with the shortest round trips).
func TestPingspoofPartialWindowNotJudged(t *testing.T) {
	d := &latencyDetector{pongs: NewRing[latSample](4), paired: outcomeBits{}}
	for i := range int64(6) { // pongs of pings sent at 0..5 s; the first two fall out of the ring
		sent := i * 1_000_000
		d.pushPong(latSample{atUs: sent + 50_000, sentUs: sent, rttMs: 50})
	}
	ctx := NewCtx(NewView(), DefaultConfig{}, func(Emission) {})
	d.pairKeepAlive(ctx, latSample{atUs: 2_400_000, sentUs: 2_000_000, rttMs: 400}, 2_000_000) // window 0..4 s
	if d.paired.size() != 0 {
		t.Fatalf("judged a keep-alive whose window lost pongs (%d outcomes)", d.paired.size())
	}
	d.pairKeepAlive(ctx, latSample{atUs: 4_400_000, sentUs: 4_000_000, rttMs: 400}, 2_000_000) // window 2..6 s
	if d.paired.size() != 1 || d.paired.count(1) != 1 {
		t.Fatalf("a complete window was not judged as excess (%d outcomes)", d.paired.size())
	}
}
