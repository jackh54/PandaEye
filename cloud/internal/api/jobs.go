package api

import (
	"context"
	"errors"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/app"
	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	"github.com/jackh54/PandaEye/cloud/internal/stream"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// runMetering holds the metering lease and meters every minute (ARCHITECTURE 5.8).
func (s *Service) runMetering(ctx context.Context) error {
	return s.d.Bus.RunSingleton(ctx, stream.LeaseMetering, func(ctx context.Context, l *stream.Lease) error {
		t := time.NewTicker(s.meterEvery)
		defer t.Stop()
		for {
			if l.Valid() {
				if err := s.meterOnce(ctx, s.now()); err != nil && ctx.Err() == nil {
					s.log.Warn("metering failed", "err", err)
				}
			}
			select {
			case <-ctx.Done():
				return nil
			case <-t.C:
			}
		}
	})
}

// onlinePlayers sums the online players of one tenant's live connections: per network_id the
// proxies' count when a proxy (Velocity) is connected, else the sum of the backends; servers
// without a network id count on their own.
func onlinePlayers(conns []stream.Connection) int {
	type network struct{ proxies, backends int }
	nets := map[string]*network{}
	total := 0
	for _, c := range conns {
		n := int(c.Info.OnlinePlayers)
		if c.Info.NetworkID == "" {
			total += n
			continue
		}
		nw := nets[c.Info.NetworkID]
		if nw == nil {
			nw = &network{proxies: -1}
			nets[c.Info.NetworkID] = nw
		}
		if c.Info.Platform == pb.Platform_PLATFORM_VELOCITY {
			nw.proxies = max(nw.proxies, 0) + n
		} else {
			nw.backends += n
		}
	}
	for _, nw := range nets {
		if nw.proxies >= 0 {
			total += nw.proxies
		} else {
			total += nw.backends
		}
	}
	return total
}

// meterOnce records each tenant's current online players as usage_daily = max(existing, value)
// and keeps the free tier's over_limit_since: set when today's or yesterday's peak exceeds 30,
// cleared when neither does or the plan is not free.
func (s *Service) meterOnce(ctx context.Context, now time.Time) error {
	conns, err := s.d.Bus.ListConnections(ctx, "")
	if err != nil {
		return err
	}
	byTenant := map[string][]stream.Connection{}
	for _, c := range conns {
		byTenant[c.TenantID] = append(byTenant[c.TenantID], c)
	}
	ids, err := s.d.Store.ListTenantIDs(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		tc, connected := byTenant[id]
		if connected {
			if err := s.d.Store.RecordPeakPlayers(ctx, id, now, onlinePlayers(tc)); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		plan, overSince, err := s.tenantPlan(ctx, id)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !connected && overSince.IsZero() {
			continue
		}
		over := false
		if plan == store.PlanFree {
			days, err := s.d.Store.ListUsage(ctx, id, now.AddDate(0, 0, -1))
			if err != nil {
				errs = append(errs, err)
				continue
			}
			for _, d := range days {
				if d.PeakPlayers > freeTierLimit {
					over = true
				}
			}
		}
		var since time.Time
		switch {
		case over && overSince.IsZero():
			since = now
		case !over && !overSince.IsZero():
		default:
			continue
		}
		if err := s.d.Store.SetOverLimitSince(ctx, id, since); err != nil {
			errs = append(errs, err)
			continue
		}
		s.log.Info("free tier limit state changed", "tenant_id", id, "over", over)
		if err := app.PublishTenant(ctx, s.d.Store, s.d.Bus, id); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// tenantPlan reads plan and over-limit state from the PE_TENANTS mirror, else the database.
func (s *Service) tenantPlan(ctx context.Context, tenantID string) (string, time.Time, error) {
	s.tenantsMu.RLock()
	w := s.tenants
	s.tenantsMu.RUnlock()
	if w != nil {
		if st, ok := w.Get(tenantID); ok {
			var since time.Time
			if st.OverLimitSince != 0 {
				since = time.UnixMilli(st.OverLimitSince).UTC()
			}
			return st.Plan, since, nil
		}
	}
	t, err := s.d.Store.GetTenant(ctx, tenantID)
	if err != nil {
		return "", time.Time{}, err
	}
	return t.Plan, t.OverLimitSince, nil
}

// runJanitor holds the janitor lease and applies retention once a day (on acquisition, then every
// 24 h).
func (s *Service) runJanitor(ctx context.Context) error {
	return s.d.Bus.RunSingleton(ctx, stream.LeaseJanitor, func(ctx context.Context, l *stream.Lease) error {
		t := time.NewTicker(s.janitorEvery)
		defer t.Stop()
		for {
			if l.Valid() {
				if err := s.janitorOnce(ctx, s.now()); err != nil && ctx.Err() == nil {
					s.log.Warn("janitor failed", "err", err)
				}
			}
			select {
			case <-ctx.Done():
				return nil
			case <-t.C:
			}
		}
	})
}

// janitorOnce applies store.DefaultRetention to every tenant, re-publishes the erasure tombstones
// of the last 8 days (in case PE_ERASURES lost them) and deletes older tombstones.
func (s *Service) janitorOnce(ctx context.Context, now time.Time) error {
	ids, err := s.d.Store.ListTenantIDs(ctx)
	if err != nil {
		return err
	}
	var errs []error
	totals := map[string]int64{}
	tombstoneCutoff := now.Add(-stream.ErasureTombstoneAge)
	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		counts, err := store.Purge(ctx, s.d.Store, id, now, store.DefaultRetention)
		for k, n := range counts {
			totals[k] += n
		}
		if err != nil {
			errs = append(errs, err)
		}
		es, err := s.d.Store.ListErasures(ctx, id, tombstoneCutoff)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, e := range es {
			if err := s.ensureTombstone(ctx, id, e); err != nil {
				errs = append(errs, err)
			}
		}
	}
	n, err := s.d.Bus.PurgeErasureTombstones(ctx, tombstoneCutoff)
	if err != nil {
		errs = append(errs, err)
	}
	s.log.Info("janitor finished", "tenants", len(ids), "deleted", totals, "tombstones_deleted", n)
	return errors.Join(errs...)
}

func (s *Service) ensureTombstone(ctx context.Context, tenantID string, e store.Erasure) error {
	player, err := identity.ParseCanonicalUUID(e.PlayerUUID)
	if err != nil {
		return nil
	}
	k, err := stream.ErasureKey(tenantID, player)
	if err != nil {
		return nil
	}
	if _, err := s.d.Bus.Erasures.Get(ctx, k); err == nil {
		return nil
	} else if !errors.Is(err, stream.ErrKeyNotFound) {
		return err
	}
	return s.d.Bus.PutErasure(ctx, tenantID, player, e.RequestedAt)
}
