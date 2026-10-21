package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/app"
	"github.com/jackh54/PandaEye/cloud/internal/store"
)

// Published latency SLO (SLO.md): plugin-measured probe RTT, in-region.
const (
	SLOTargetP95 = 100 * time.Millisecond
	SLOTargetP99 = 250 * time.Millisecond
	statusWindow = time.Hour
	statusTTL    = 10 * time.Second
)

// Status is the public /status document.
type Status struct {
	Region           string    `json:"region"`
	Version          string    `json:"version"`
	UptimeS          int64     `json:"uptime_s"`
	ConnectedServers int       `json:"connected_servers"`
	ProbeRTT         StatusRTT `json:"probe_rtt"`
	SLO              StatusSLO `json:"slo"`
	GeneratedAt      time.Time `json:"generated_at"`
}

// StatusRTT is the plugin-measured probe RTT over the last hour (upper bounds of the histogram
// buckets; zero without samples).
type StatusRTT struct {
	P50Us   int64  `json:"p50_us"`
	P95Us   int64  `json:"p95_us"`
	P99Us   int64  `json:"p99_us"`
	Samples uint64 `json:"samples"`
	WindowS int64  `json:"window_s"`
}

// StatusSLO reports the targets and whether they are met; the verdicts are null without samples (a
// broken pipeline shows "no data", never a good value).
type StatusSLO struct {
	TargetP95Ms int64 `json:"target_p95_ms"`
	TargetP99Ms int64 `json:"target_p99_ms"`
	P95Met      *bool `json:"p95_met"`
	P99Met      *bool `json:"p99_met"`
	Met         *bool `json:"met"`
}

// BuildStatus computes the status document (also usable by the API's /status).
func BuildStatus(ctx context.Context, d *app.Deps) (Status, error) {
	now := time.Now().UTC()
	st := Status{Region: d.Config.Region, Version: d.Version, UptimeS: int64(now.Sub(d.Started).Seconds()), GeneratedAt: now,
		SLO: StatusSLO{TargetP95Ms: SLOTargetP95.Milliseconds(), TargetP99Ms: SLOTargetP99.Milliseconds()}}
	conns, err := d.Bus.ListConnections(ctx, "")
	if err != nil {
		return st, err
	}
	st.ConnectedServers = len(conns)
	sum, err := d.Store.GlobalProbeSummary(ctx, store.ProbeSourcePlugin, now.Add(-statusWindow))
	if err != nil {
		return st, err
	}
	st.ProbeRTT = StatusRTT{P50Us: sum.P50, P95Us: sum.P95, P99Us: sum.P99, Samples: sum.Count, WindowS: int64(statusWindow.Seconds())}
	if sum.Count > 0 {
		p95 := sum.P95 < SLOTargetP95.Microseconds()
		p99 := sum.P99 < SLOTargetP99.Microseconds()
		met := p95 && p99
		st.SLO.P95Met, st.SLO.P99Met, st.SLO.Met = &p95, &p99, &met
	}
	return st, nil
}

// statusCache serves /status from a document at most statusTTL old (stale on errors).
type statusCache struct {
	g   *Service
	mu  sync.Mutex
	at  time.Time
	doc []byte
}

func newStatusCache(g *Service) *statusCache { return &statusCache{g: g} }

func (c *statusCache) get(ctx context.Context) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.doc != nil && time.Since(c.at) < statusTTL {
		return c.doc, nil
	}
	st, err := BuildStatus(ctx, c.g.d)
	if err != nil {
		if c.doc != nil {
			return c.doc, nil
		}
		return nil, err
	}
	doc, err := json.Marshal(st)
	if err != nil {
		return nil, err
	}
	c.doc, c.at = doc, time.Now()
	return doc, nil
}

func (c *statusCache) serve(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	doc, err := c.get(ctx)
	if err != nil {
		c.g.log.Warn("building /status failed", "err", err)
		http.Error(w, "status unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=10")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Write(doc)
}
