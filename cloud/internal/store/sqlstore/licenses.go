package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/store"
)

func scanLicense(sc interface{ Scan(...any) error }) (*store.License, error) {
	var l store.License
	var created, revoked sql.NullInt64
	if err := sc.Scan(&l.KeyHMAC, &l.TenantID, &created, &revoked); err != nil {
		return nil, mapErr(err)
	}
	l.CreatedAt, l.RevokedAt = fromUS(created), fromUS(revoked)
	return &l, nil
}

const licenseCols = `key_hmac, tenant_id, created_at, revoked_at`

func (s *Store) LookupLicense(ctx context.Context, keyHMAC []byte) (*store.License, error) {
	return scanLicense(s.queryRow(ctx, s.r, `SELECT `+licenseCols+` FROM licenses WHERE key_hmac = ?`, keyHMAC))
}

func (s *Store) EnsureLicenseTenant(ctx context.Context, keyHMAC []byte, newTenant *store.Tenant, at time.Time) (*store.Tenant, *store.License, bool, error) {
	var (
		t       *store.Tenant
		l       *store.License
		created bool
	)
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var err error
		l, err = scanLicense(s.queryRow(ctx, tx, `SELECT `+licenseCols+` FROM licenses WHERE key_hmac = ?`+s.forUpdate(), keyHMAC))
		switch {
		case err == nil:
			t, err = scanTenant(s.queryRow(ctx, tx, `SELECT `+tenantCols+` FROM tenants WHERE id = ?`, l.TenantID))
			return err
		case !errors.Is(err, store.ErrNotFound):
			return err
		}
		if err := s.insertTenant(ctx, tx, newTenant); err != nil {
			return err
		}
		l = &store.License{KeyHMAC: keyHMAC, TenantID: newTenant.ID, CreatedAt: orNow(at)}
		if _, err := s.exec(ctx, tx, `INSERT INTO licenses (key_hmac, tenant_id, created_at) VALUES (?, ?, ?)`,
			keyHMAC, newTenant.ID, l.CreatedAt.UnixMicro()); err != nil {
			return err
		}
		t, created = newTenant, true
		return nil
	})
	if errors.Is(err, store.ErrConflict) {
		// Either a concurrent first use of the key won the race (return its tenant) or the new
		// tenant's slug is taken (ErrConflict: retry with another slug).
		l, lerr := s.LookupLicense(ctx, keyHMAC)
		if lerr != nil {
			return nil, nil, false, err
		}
		t, err = s.GetTenant(ctx, l.TenantID)
		return t, l, false, err
	}
	if err != nil {
		return nil, nil, false, err
	}
	return t, l, created, nil
}

func (s *Store) CreateLicense(ctx context.Context, tenantID string, keyHMAC []byte, at time.Time) error {
	_, err := s.exec(ctx, s.w, `INSERT INTO licenses (key_hmac, tenant_id, created_at) VALUES (?, ?, ?)`,
		keyHMAC, tenantID, orNow(at).UnixMicro())
	return err
}

func (s *Store) ListLicenses(ctx context.Context, tenantID string) ([]store.License, error) {
	rows, err := s.query(ctx, s.r, `SELECT `+licenseCols+` FROM licenses WHERE tenant_id = ? ORDER BY created_at DESC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.License
	for rows.Next() {
		l, err := scanLicense(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, rows.Err()
}

func (s *Store) RevokeLicense(ctx context.Context, tenantID string, keyHMAC []byte, at time.Time) error {
	return s.updateOne(ctx, `UPDATE licenses SET revoked_at = ? WHERE key_hmac = ? AND tenant_id = ?`,
		orNow(at).UnixMicro(), keyHMAC, tenantID)
}
