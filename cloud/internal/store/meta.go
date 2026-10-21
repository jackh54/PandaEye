package store

import "context"

// Meta keys.
const (
	MetaLicensePepper = "license_pepper" // generated in `all` mode
	MetaOutboundKey   = "outbound_key"   // generated in `all` mode
)

// MetaStore holds deployment-wide values (table meta). Not tenant-scoped by nature.
type MetaStore interface {
	// GetMeta returns a value or ErrNotFound.
	GetMeta(ctx context.Context, key string) ([]byte, error)
	// PutMetaIfAbsent stores value unless key exists and returns the stored value either way, so
	// concurrent first starts agree on one value.
	PutMetaIfAbsent(ctx context.Context, key string, value []byte) ([]byte, error)
}
