package store

import (
	"context"
	"time"
)

// PlayerStore records players and the names they used.
type PlayerStore interface {
	// UpsertPlayer records that player (canonical UUID) was seen with name on serverID at the given
	// time: creates or updates players and player_names.
	UpsertPlayer(ctx context.Context, tenantID, player, name, serverID string, at time.Time) error
	// GetPlayer returns the player with its names (newest first) or ErrNotFound.
	GetPlayer(ctx context.Context, tenantID, player string) (*Player, error)
	// FindPlayersByName returns players who used a name starting with prefix (case-insensitive),
	// most recently seen first, at most limit (<= 200).
	FindPlayersByName(ctx context.Context, tenantID, prefix string, limit int) ([]Player, error)
}
