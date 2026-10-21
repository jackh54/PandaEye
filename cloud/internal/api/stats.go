package api

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/gateway"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// SLO (docs/SLO.md): plugin-measured probe RTT p95 < 100 ms in-region.
const (
	sloTargetP95    = 100 * time.Millisecond
	freeTierLimit   = 30
	usageDays       = 90
	statusCacheTime = 30 * time.Second
)

type countWindowJSON struct {
	ByAction   map[string]int64 `json:"by_action"`
	ByCategory map[string]int64 `json:"by_category"`
	Shadow     int64            `json:"shadow"`
}

func (s *Service) countWindow(ctx context.Context, tenantID string, since time.Time) (countWindowJSON, error) {
	out := countWindowJSON{ByAction: map[string]int64{}, ByCategory: map[string]int64{}}
	for a := pb.Action_ACTION_ALERT; a <= pb.Action_ACTION_UNBAN; a++ {
		out.ByAction[actionName(a)] = 0
	}
	for c := pb.Category_CATEGORY_AUTO_CLICKER; c <= pb.Category_CATEGORY_LATENCY_ABUSE; c++ {
		out.ByCategory[categoryName(c)] = 0
	}
	rows, err := s.d.Store.VerdictCounts(ctx, tenantID, since)
	if err != nil {
		return out, err
	}
	for _, r := range rows {
		if n := actionName(r.Action); n != "" {
			out.ByAction[n] += r.Count
		}
		if c := categoryName(r.Category); c != "" && !corrective(r.Action) {
			out.ByCategory[c] += r.Count
		}
	}
	out.Shadow, err = s.d.Store.CountShadowVerdicts(ctx, tenantID, since)
	return out, err
}

func (s *Service) handleOverview(w http.ResponseWriter, r *http.Request, rc *reqCtx) error {
	day, err := s.countWindow(r.Context(), rc.tenant(), rc.now.Add(-24*time.Hour))
	if err != nil {
		return err
	}
	week, err := s.countWindow(r.Context(), rc.tenant(), rc.now.Add(-7*24*time.Hour))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"last_24h": day, "last_7d": week})
	return nil
}

type percentilesJSON struct {
	P50Us int64  `json:"p50_us"`
	P95Us int64  `json:"p95_us"`
	P99Us int64  `json:"p99_us"`
	Count uint64 `json:"count"`
}

type sloJSON struct {
	TargetP95Ms int64 `json:"target_p95_ms"`
	Met         *bool `json:"met"` // null without probe data
}

func sloOf(sum store.ProbeSummary) sloJSON {
	out := sloJSON{TargetP95Ms: sloTargetP95.Milliseconds()}
	if sum.Count > 0 {
		met := sum.P95 < sloTargetP95.Microseconds()
		out.Met = &met
	}
	return out
}

func (s *Service) handleLatency(w http.ResponseWriter, r *http.Request, rc *reqCtx) error {
	ctx := r.Context()
	hour, err := s.d.Store.ProbeSummary(ctx, rc.tenant(), store.ProbeSourcePlugin, rc.now.Add(-time.Hour))
	if err != nil {
		return err
	}
	day, err := s.d.Store.ProbeSummary(ctx, rc.tenant(), store.ProbeSourcePlugin, rc.now.Add(-24*time.Hour))
	if err != nil {
		return err
	}
	p := func(s store.ProbeSummary) percentilesJSON {
		return percentilesJSON{P50Us: s.P50, P95Us: s.P95, P99Us: s.P99, Count: s.Count}
	}
	writeJSON(w, http.StatusOK, map[string]any{"last_1h": p(hour), "last_24h": p(day), "slo": sloOf(hour)})
	return nil
}

type usageDayJSON struct {
	Day         string `json:"day"`
	PeakPlayers int    `json:"peak_players"`
}

func (s *Service) handleUsage(w http.ResponseWriter, r *http.Request, rc *reqCtx) error {
	ctx := r.Context()
	t, err := s.d.Store.GetTenant(ctx, rc.tenant())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return notFound("tenant")
		}
		return err
	}
	days, err := s.d.Store.ListUsage(ctx, rc.tenant(), rc.now.AddDate(0, 0, -(usageDays-1)))
	if err != nil {
		return err
	}
	out := make([]usageDayJSON, 0, len(days))
	for _, d := range days {
		out = append(out, usageDayJSON{Day: d.Day.UTC().Format(time.DateOnly), PeakPlayers: d.PeakPlayers})
	}
	writeJSON(w, http.StatusOK, map[string]any{"plan": t.Plan, "free_tier_limit": freeTierLimit,
		"over_limit_since": nullTime(t.OverLimitSince), "days": out})
	return nil
}

type statusCache struct {
	mu      sync.Mutex
	doc     *gateway.Status
	expires time.Time
}

// handleStatus serves the public status document: exactly the gateway's (gateway.BuildStatus, from
// the store's merged plugin probe histograms of the last hour), cached for 30 s (the last good one
// on errors); uptime is always current.
func (s *Service) handleStatus(w http.ResponseWriter, r *http.Request, rc *reqCtx) error {
	doc, err := s.statusDoc(r.Context(), rc.now)
	if err != nil {
		s.log.Warn("building /status failed", "err", err)
		return unavailable("status unavailable")
	}
	out := *doc
	if !s.d.Started.IsZero() {
		out.UptimeS = int64(rc.now.Sub(s.d.Started) / time.Second)
	}
	w.Header().Set("Cache-Control", "public, max-age=30")
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Service) statusDoc(ctx context.Context, now time.Time) (*gateway.Status, error) {
	c := &s.status
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.doc != nil && now.Before(c.expires) {
		return c.doc, nil
	}
	bctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	doc, err := gateway.BuildStatus(bctx, s.d)
	if err != nil {
		if c.doc != nil {
			return c.doc, nil
		}
		return nil, err
	}
	c.doc, c.expires = &doc, now.Add(statusCacheTime)
	return c.doc, nil
}
