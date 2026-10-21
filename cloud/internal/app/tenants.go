package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackh54/PandaEye/cloud/internal/store"
	"github.com/jackh54/PandaEye/cloud/internal/stream"
)

// TenantSettings converts a stored tenant and its check configuration into the KV PE_TENANTS value.
func TenantSettings(t *store.Tenant, checks []store.TenantCheck) stream.TenantSettings {
	s := stream.TenantSettings{
		TenantID: t.ID, Leniency: t.Leniency, Autoban: t.Autoban, Autokick: t.Autokick, Mitigations: t.Mitigations,
		Enforcement: t.Enforcement, ReputationOptIn: t.ReputationOptIn, DiscordWebhookEnc: t.DiscordWebhookEnc,
		DiscordMinAction: t.DiscordMinAction, Plan: t.Plan, MaxServers: t.MaxServers, UpdatedUnixMs: t.UpdatedAt.UnixMilli(),
	}
	if !t.OverLimitSince.IsZero() {
		s.OverLimitSince = t.OverLimitSince.UnixMilli()
	}
	if len(checks) > 0 {
		s.Checks = make(map[string]stream.CheckSettings, len(checks))
		for _, c := range checks {
			s.Checks[c.CheckID] = stream.CheckSettings{Enabled: c.Enabled, Overrides: c.Overrides}
		}
	}
	return s
}

func loadTenantSettings(ctx context.Context, st store.Store, tenantID string) (stream.TenantSettings, error) {
	t, err := st.GetTenant(ctx, tenantID)
	if err != nil {
		return stream.TenantSettings{}, err
	}
	checks, err := st.ListTenantChecks(ctx, tenantID)
	if err != nil {
		return stream.TenantSettings{}, err
	}
	return TenantSettings(t, checks), nil
}

// PublishTenant writes a tenant's current settings from the database to KV PE_TENANTS; the API calls
// it after every settings or check change.
func PublishTenant(ctx context.Context, st store.Store, bus *stream.Bus, tenantID string) error {
	s, err := loadTenantSettings(ctx, st, tenantID)
	if err != nil {
		return err
	}
	_, err = bus.PutTenant(ctx, s)
	return err
}

// SyncTenants creates the PE_TENANTS entry of every tenant that has none: the bucket is a cache of the
// tenants table and is rebuilt at start when entries are missing. Existing entries are never
// overwritten (they may be newer than what this process reads).
func SyncTenants(ctx context.Context, st store.Store, bus *stream.Bus) (int, error) {
	have := map[string]bool{}
	lister, err := bus.Tenants.ListKeys(ctx)
	if err != nil {
		return 0, err
	}
	for k := range lister.Keys() { // the channel closes when the listing is complete
		have[k] = true
	}
	ids, err := st.ListTenantIDs(ctx)
	if err != nil {
		return 0, err
	}
	created := 0
	for _, id := range ids {
		if have[id] {
			continue
		}
		s, err := loadTenantSettings(ctx, st, id)
		if err != nil {
			return created, fmt.Errorf("tenant %s: %w", id, err)
		}
		data, err := marshalSettings(s)
		if err != nil {
			return created, err
		}
		if _, err := bus.Tenants.Create(ctx, id, data); err == nil {
			created++
		} else if !errors.Is(err, stream.ErrKeyExists) {
			return created, err
		}
	}
	return created, nil
}

func marshalSettings(s stream.TenantSettings) ([]byte, error) { return json.Marshal(s) }
