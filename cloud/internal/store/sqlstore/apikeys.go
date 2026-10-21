package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/store"
)

const apiKeyCols = `id, tenant_id, name, key_hash, scopes, created_at, last_used_at, revoked_at`

func scanAPIKey(sc interface{ Scan(...any) error }) (*store.APIKey, error) {
	var k store.APIKey
	var scopes string
	var created, used, revoked sql.NullInt64
	if err := sc.Scan(&k.ID, &k.TenantID, &k.Name, &k.KeyHash, &scopes, &created, &used, &revoked); err != nil {
		return nil, mapErr(err)
	}
	if err := json.Unmarshal([]byte(scopes), &k.Scopes); err != nil {
		return nil, err
	}
	k.CreatedAt, k.LastUsedAt, k.RevokedAt = fromUS(created), fromUS(used), fromUS(revoked)
	return &k, nil
}

func (s *Store) CreateAPIKey(ctx context.Context, k *store.APIKey) error {
	k.CreatedAt = orNow(k.CreatedAt)
	if len(k.Scopes) == 0 {
		k.Scopes = []string{store.ScopeRead}
	}
	_, err := s.exec(ctx, s.w, `INSERT INTO api_keys (`+apiKeyCols+`) VALUES (`+placeholders(8)+`)`,
		k.ID, k.TenantID, k.Name, k.KeyHash, jsonText(k.Scopes, "[]"), k.CreatedAt.UnixMicro(), us(k.LastUsedAt), us(k.RevokedAt))
	return err
}

func (s *Store) VerifyAPIKey(ctx context.Context, id string, secret identity.Secret, at time.Time) (*store.APIKey, error) {
	k, err := scanAPIKey(s.queryRow(ctx, s.r, `SELECT `+apiKeyCols+` FROM api_keys WHERE id = ?`, id))
	if err != nil {
		return nil, store.ErrNotFound
	}
	if !identity.CheckSecret(k.KeyHash, secret.Reveal()) || !k.RevokedAt.IsZero() {
		return nil, store.ErrNotFound
	}
	at = orNow(at)
	if at.Sub(k.LastUsedAt) >= time.Minute {
		if _, err := s.exec(ctx, s.w, `UPDATE api_keys SET last_used_at = ? WHERE id = ? AND tenant_id = ?`, at.UnixMicro(), k.ID, k.TenantID); err != nil {
			return nil, err
		}
		k.LastUsedAt = at
	}
	return k, nil
}

func (s *Store) ListAPIKeys(ctx context.Context, tenantID string) ([]store.APIKey, error) {
	rows, err := s.query(ctx, s.r, `SELECT `+apiKeyCols+` FROM api_keys WHERE tenant_id = ? ORDER BY created_at DESC, id DESC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.APIKey
	for rows.Next() {
		k, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *k)
	}
	return out, rows.Err()
}

func (s *Store) RevokeAPIKey(ctx context.Context, tenantID, id string, at time.Time) error {
	return s.updateOne(ctx, `UPDATE api_keys SET revoked_at = COALESCE(revoked_at, ?) WHERE id = ? AND tenant_id = ?`,
		orNow(at).UnixMicro(), id, tenantID)
}
