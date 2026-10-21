package store

import (
	"context"
	"time"
)

// ClipStore stores replay clips.
type ClipStore interface {
	// SaveClip inserts c and its subjects (c.PlayerUUID is always a subject). ErrConflict when the
	// id exists.
	SaveClip(ctx context.Context, c *Clip) error
	// GetClip returns the clip with data and subjects, or ErrNotFound.
	GetClip(ctx context.Context, tenantID, id string) (*Clip, error)
	// DeleteClip deletes a clip (ErrNotFound when missing).
	DeleteClip(ctx context.Context, tenantID, id string) error
	// PurgeClips deletes clips created before `before` unless a verdict that references them has an
	// active ban, an open appeal or a label.
	PurgeClips(ctx context.Context, tenantID string, before time.Time) (int64, error)
}
