package store

import (
	"context"
	"time"
)

// ServerStore manages server installs, keyed by (tenant_id, server id).
type ServerStore interface {
	// CreateServer inserts s. ErrConflict when (tenant, id) exists.
	CreateServer(ctx context.Context, s *Server) error
	// GetServer returns the server or ErrNotFound.
	GetServer(ctx context.Context, tenantID, serverID string) (*Server, error)
	// UpdateServerSeen stores the Hello metadata (name, network, platform and versions, instance id)
	// and last_seen_at of an existing server.
	UpdateServerSeen(ctx context.Context, s *Server) error
	// AdvanceServerSeq raises last_batch_seq to seq (never lowers it) and sets last_seen_at.
	AdvanceServerSeq(ctx context.Context, tenantID, serverID string, seq uint64, at time.Time) error
	// ListServers pages through the tenant's servers ordered by creation, newest first.
	ListServers(ctx context.Context, tenantID string, page Page) ([]Server, string, error)
}
