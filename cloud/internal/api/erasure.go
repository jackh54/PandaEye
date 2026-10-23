package api

import (
	"context"
	"net/http"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/identity"
)

// ErasureSink deletes a player's analytics rows (ClickHouse: ALTER TABLE ... DELETE WHERE
// tenant_id = ? AND player = ? on every table, ARCHITECTURE 5.13; feature-window files: a journal entry,
// the rows are removed in the background); player is a canonical UUID. The telemetry sink
// (telemetry.ErasureSink, a no-op when telemetry is disabled) implements it.
type ErasureSink interface {
	ErasePlayer(ctx context.Context, tenantID, player string) error
}

const erasureSinkTimeout = 30 * time.Second

// handleErasePlayer runs the erasure job (5.13): tombstone in KV PE_ERASURES first (workers and the
// verdict service drop late data), then the SQL rows (EraseSubject; active bans keep a minimal
// record), then analytics. It is idempotent; a failed analytics step leaves the job incomplete so a
// retry finishes it.
func (s *Service) handleErasePlayer(w http.ResponseWriter, r *http.Request, rc *reqCtx) error {
	uuid, err := parsePlayer(r.PathValue("uuid"))
	if err != nil {
		return err
	}
	ctx := r.Context()
	tenantID := rc.tenant()
	player, _ := identity.ParseCanonicalUUID(uuid)
	if err := s.d.Store.BeginErasure(ctx, tenantID, uuid, rc.now); err != nil {
		return err
	}
	if err := s.d.Bus.PutErasure(ctx, tenantID, player, rc.now); err != nil {
		return unavailable("the erasure tombstone could not be published; retry")
	}
	counts, err := s.d.Store.EraseSubject(ctx, tenantID, uuid)
	if err != nil {
		return err
	}
	if s.erasure != nil {
		ectx, cancel := context.WithTimeout(ctx, erasureSinkTimeout)
		err := s.erasure.ErasePlayer(ectx, tenantID, uuid)
		cancel()
		if err != nil {
			s.log.Error("analytics erasure failed", "err", err)
			s.auditRC(r, rc, "player.erase", uuid, map[string]any{"counts": counts, "complete": false})
			return unavailable("player data was erased from the database but analytics deletion failed; retry")
		}
	}
	if err := s.d.Store.CompleteErasure(ctx, tenantID, uuid, counts, rc.now); err != nil {
		return err
	}
	s.auditRC(r, rc, "player.erase", uuid, map[string]any{"counts": counts, "complete": true})
	writeJSON(w, http.StatusOK, map[string]any{"counts": counts})
	return nil
}
