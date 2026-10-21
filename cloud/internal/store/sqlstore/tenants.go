package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/store"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

const tenantCols = `id, name, slug, leniency, autoban, autokick, mitigations, enforcement, reputation_opt_in,
	discord_webhook_enc, discord_min_action, plan, max_servers, license_verified_at, license_valid_until,
	over_limit_since, created_at, updated_at`

func scanTenant(sc interface{ Scan(...any) error }) (*store.Tenant, error) {
	var t store.Tenant
	var minAction int64
	var verified, validUntil, overLimit, created, updated sql.NullInt64
	err := sc.Scan(&t.ID, &t.Name, &t.Slug, &t.Leniency, &t.Autoban, &t.Autokick, &t.Mitigations, &t.Enforcement,
		&t.ReputationOptIn, &t.DiscordWebhookEnc, &minAction, &t.Plan, &t.MaxServers, &verified, &validUntil,
		&overLimit, &created, &updated)
	if err != nil {
		return nil, mapErr(err)
	}
	t.DiscordMinAction = pb.Action(minAction)
	t.LicenseVerifiedAt, t.LicenseValidUntil, t.OverLimitSince = fromUS(verified), fromUS(validUntil), fromUS(overLimit)
	t.CreatedAt, t.UpdatedAt = fromUS(created), fromUS(updated)
	return &t, nil
}

func (s *Store) insertTenant(ctx context.Context, r runner, t *store.Tenant) error {
	t.CreatedAt = orNow(t.CreatedAt)
	if t.UpdatedAt.IsZero() {
		t.UpdatedAt = t.CreatedAt
	}
	_, err := s.exec(ctx, r, `INSERT INTO tenants (`+tenantCols+`) VALUES (`+placeholders(18)+`)`,
		t.ID, t.Name, t.Slug, t.Leniency, t.Autoban, t.Autokick, t.Mitigations, t.Enforcement, t.ReputationOptIn,
		t.DiscordWebhookEnc, int64(t.DiscordMinAction), t.Plan, t.MaxServers, us(t.LicenseVerifiedAt),
		us(t.LicenseValidUntil), us(t.OverLimitSince), t.CreatedAt.UnixMicro(), t.UpdatedAt.UnixMicro())
	return err
}

func (s *Store) CreateTenant(ctx context.Context, t *store.Tenant) error {
	return s.insertTenant(ctx, s.w, t)
}

func (s *Store) GetTenant(ctx context.Context, tenantID string) (*store.Tenant, error) {
	return scanTenant(s.queryRow(ctx, s.r, `SELECT `+tenantCols+` FROM tenants WHERE id = ?`, tenantID))
}

func (s *Store) UpdateTenant(ctx context.Context, tenantID string, p store.TenantPatch, at time.Time) (*store.Tenant, error) {
	set := "updated_at = ?"
	args := []any{orNow(at).UnixMicro()}
	add := func(col string, v any) { set += ", " + col + " = ?"; args = append(args, v) }
	if p.Name != nil {
		add("name", *p.Name)
	}
	if p.Leniency != nil {
		add("leniency", *p.Leniency)
	}
	if p.Autoban != nil {
		add("autoban", *p.Autoban)
	}
	if p.Autokick != nil {
		add("autokick", *p.Autokick)
	}
	if p.Mitigations != nil {
		add("mitigations", *p.Mitigations)
	}
	if p.Enforcement != nil {
		add("enforcement", *p.Enforcement)
	}
	if p.ReputationOptIn != nil {
		add("reputation_opt_in", *p.ReputationOptIn)
	}
	if p.DiscordWebhookEnc != nil {
		add("discord_webhook_enc", *p.DiscordWebhookEnc)
	}
	if p.DiscordMinAction != nil {
		add("discord_min_action", int64(*p.DiscordMinAction))
	}
	if p.Plan != nil {
		add("plan", *p.Plan)
	}
	var t *store.Tenant
	err := s.tx(ctx, func(tx *sql.Tx) error {
		res, err := s.exec(ctx, tx, `UPDATE tenants SET `+set+` WHERE id = ?`, append(args, tenantID)...)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return store.ErrNotFound
		}
		t, err = scanTenant(s.queryRow(ctx, tx, `SELECT `+tenantCols+` FROM tenants WHERE id = ?`, tenantID))
		return err
	})
	return t, err
}

func (s *Store) RecordLicenseCheck(ctx context.Context, tenantID string, maxServers int, verifiedAt, validUntil time.Time) error {
	return s.updateOne(ctx, `UPDATE tenants SET max_servers = ?, license_verified_at = ?, license_valid_until = ? WHERE id = ?`,
		maxServers, us(verifiedAt), us(validUntil), tenantID)
}

func (s *Store) SetOverLimitSince(ctx context.Context, tenantID string, since time.Time) error {
	return s.updateOne(ctx, `UPDATE tenants SET over_limit_since = ? WHERE id = ?`, us(since), tenantID)
}

// updateOne runs an UPDATE and maps "no row" to ErrNotFound.
func (s *Store) updateOne(ctx context.Context, q string, args ...any) error {
	res, err := s.exec(ctx, s.w, q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) ListTenantIDs(ctx context.Context) ([]string, error) {
	rows, err := s.query(ctx, s.r, `SELECT id FROM tenants ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *Store) ListTenantChecks(ctx context.Context, tenantID string) ([]store.TenantCheck, error) {
	rows, err := s.query(ctx, s.r, `SELECT check_id, enabled, overrides FROM tenant_checks WHERE tenant_id = ? ORDER BY check_id`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.TenantCheck
	for rows.Next() {
		c := store.TenantCheck{TenantID: tenantID}
		var overrides string
		if err := rows.Scan(&c.CheckID, &c.Enabled, &overrides); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(overrides), &c.Overrides); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) ReplaceTenantChecks(ctx context.Context, tenantID string, checks []store.TenantCheck) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := s.exec(ctx, tx, `DELETE FROM tenant_checks WHERE tenant_id = ?`, tenantID); err != nil {
			return err
		}
		for _, c := range checks {
			if _, err := s.exec(ctx, tx, `INSERT INTO tenant_checks (tenant_id, check_id, enabled, overrides) VALUES (?, ?, ?, ?)`,
				tenantID, c.CheckID, c.Enabled, jsonText(c.Overrides, "{}")); err != nil {
				return err
			}
		}
		return nil
	})
}

// jsonText marshals v for a JSON column; nil values store empty.
func jsonText(v any, empty string) string {
	b, err := json.Marshal(v)
	if err != nil || string(b) == "null" {
		return empty
	}
	return string(b)
}
