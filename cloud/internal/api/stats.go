package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
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

// agreementJSON compares the LiteBans cheat bans and wrongful unbans of one period with the verdicts
// (GET /api/v1/stats/litebans; aggregate counts only).
type agreementJSON struct {
	CheatBans int `json:"cheat_bans"` // cheat bans that started in the period, not lifted as wrong
	Players   int `json:"players"`    // their players
	// FlaggedBefore counts those players by the highest action of their verdicts (shadow included) in
	// the lookback before their first such ban; NotFlagged those without one.
	FlaggedBefore map[string]int `json:"flagged_before"`
	NotFlagged    int            `json:"not_flagged"`
	// MedianLeadTimeS is the median time from a flagged player's first verdict to the ban (null without).
	MedianLeadTimeS *int64 `json:"median_lead_time_s"`
	// WouldKickOrBan counts players with a KICK or BAN verdict (shadow included) created in the period,
	// WouldKickOrBanBanned those of them with a cheat ban within the lookback after one.
	WouldKickOrBan       int `json:"would_kick_or_ban"`
	WouldKickOrBanBanned int `json:"would_kick_or_ban_banned"`
	// Overturns counts the wrongful unbans of the period, VerdictsOverturned the verdicts they overturned.
	Overturns          int `json:"overturns"`
	VerdictsOverturned int `json:"verdicts_overturned"`
}

// agreement computes agreementJSON for [now - span, now).
func (s *Service) agreement(ctx context.Context, tenantID string, now time.Time, span time.Duration) (agreementJSON, error) {
	out := agreementJSON{FlaggedBefore: map[string]int{}}
	for a := pb.Action_ACTION_ALERT; a <= pb.Action_ACTION_BAN; a++ {
		out.FlaggedBefore[actionName(a)] = 0
	}
	ag, err := s.d.Store.ModerationAgreement(ctx, tenantID, now.Add(-span), now, s.d.Config.Labels.Lookback)
	if err != nil {
		return out, err
	}
	out.CheatBans = len(ag.Bans)
	seen := map[string]bool{}
	var leads []time.Duration
	for _, b := range ag.Bans { // oldest first: a player counts with their first ban of the period
		if seen[b.PlayerUUID] {
			continue
		}
		seen[b.PlayerUUID] = true
		out.Players++
		if b.TopAction == pb.Action_ACTION_UNSPECIFIED {
			out.NotFlagged++
			continue
		}
		out.FlaggedBefore[actionName(b.TopAction)]++
		leads = append(leads, b.BanStart.Sub(b.FirstVerdict))
	}
	if len(leads) > 0 {
		slices.Sort(leads)
		m := leads[len(leads)/2]
		if len(leads)%2 == 0 {
			m = (leads[len(leads)/2-1] + m) / 2
		}
		sec := int64(m / time.Second)
		out.MedianLeadTimeS = &sec
	}
	out.WouldKickOrBan, out.WouldKickOrBanBanned = ag.WouldAct, ag.WouldActBanned
	out.Overturns, out.VerdictsOverturned = ag.Overturns, ag.VerdictsOverturned
	return out, nil
}

// handleLiteBansStats serves the agreement of LiteBans with the verdicts over the last 7 and 30 days.
func (s *Service) handleLiteBansStats(w http.ResponseWriter, r *http.Request, rc *reqCtx) error {
	week, err := s.agreement(r.Context(), rc.tenant(), rc.now, 7*24*time.Hour)
	if err != nil {
		return err
	}
	month, err := s.agreement(r.Context(), rc.tenant(), rc.now, 30*24*time.Hour)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"lookback_s": int64(s.d.Config.Labels.Lookback / time.Second), "last_7d": week, "last_30d": month})
	return nil
}

// logAgreement logs the LiteBans agreement of every tenant with LiteBans data in the last 30 days, once
// per window, aggregate counts only (the janitor runs it daily).
func (s *Service) logAgreement(ctx context.Context, tenantIDs []string, now time.Time) {
	for _, id := range tenantIDs {
		month, err := s.agreement(ctx, id, now, 30*24*time.Hour)
		if err != nil {
			if ctx.Err() == nil {
				s.log.Warn("litebans agreement failed", "tenant_id", id, "err", err)
			}
			continue
		}
		if month.CheatBans == 0 && month.Overturns == 0 {
			continue
		}
		week, err := s.agreement(ctx, id, now, 7*24*time.Hour)
		if err != nil {
			continue
		}
		for _, w := range []struct {
			name string
			a    agreementJSON
		}{{"7d", week}, {"30d", month}} {
			lead := int64(-1)
			if w.a.MedianLeadTimeS != nil {
				lead = *w.a.MedianLeadTimeS
			}
			s.log.Info("litebans agreement", "tenant_id", id, "window", w.name, "cheat_bans", w.a.CheatBans, "players", w.a.Players,
				"flagged_alert", w.a.FlaggedBefore["alert"], "flagged_mitigate", w.a.FlaggedBefore["mitigate"],
				"flagged_kick", w.a.FlaggedBefore["kick"], "flagged_ban", w.a.FlaggedBefore["ban"], "not_flagged", w.a.NotFlagged,
				"median_lead_time_s", lead, "would_kick_or_ban", w.a.WouldKickOrBan, "would_kick_or_ban_banned", w.a.WouldKickOrBanBanned,
				"overturns", w.a.Overturns, "verdicts_overturned", w.a.VerdictsOverturned)
		}
	}
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
