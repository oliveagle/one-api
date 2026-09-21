//go:build !cgo

package rqlite

import (
	"context"
	"database/sql"
	"sync"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const DriverName = "oneapi-rqlite"

var (
	registerOnce sync.Once
	current      *EmbeddedStore
	currentMu    sync.RWMutex
)

// EmbeddedStore is the no-CGO placeholder type.
type EmbeddedStore struct{}

// DataDir is a no-op on no-CGO builds.
func (s *EmbeddedStore) DataDir() string { return "" }

// DBPath is a no-op on no-CGO builds.
func (s *EmbeddedStore) DBPath() string { return "" }

// Close is a no-op on no-CGO builds.
func (s *EmbeddedStore) Close() error { return nil }

// Direct is a no-op on no-CGO builds.
func (s *EmbeddedStore) Direct() *sql.DB { return nil }

// EnsureFullSnapshotMarker is a no-op on no-CGO builds.
func (s *EmbeddedStore) EnsureFullSnapshotMarker() {}

// SnapshotNow is a no-op on no-CGO builds.
func (s *EmbeddedStore) SnapshotNow() error { return nil }

// RestoreFromLatestSnapshot is a no-op on no-CGO builds.
func (s *EmbeddedStore) RestoreFromLatestSnapshot() error { return nil }

// OpenStore fails fast on no-CGO builds.
func OpenStore(ctx context.Context, opts *Options) (*EmbeddedStore, error) {
	return nil, ErrNoStore
}

// StartEmbedded fails fast on no-CGO builds.
func StartEmbedded(dir, nodeID string) (*EmbeddedStore, error) {
	return nil, ErrNoStore
}

// OpenGorm fails fast on no-CGO builds.
func OpenGorm() (*gorm.DB, error) {
	return nil, ErrNoStore
}

// OpenSQL fails fast on no-CGO builds.
func OpenSQL() (*sql.DB, error) {
	return nil, ErrNoStore
}

// registerDriver is a no-op on no-CGO builds (the driver stub is registered
// by driver_stub.go).
func registerDriver() {}

var _ = sqlite.Dialector{}
var _ = context.Background

// IsActive reports whether a process-wide embedded RQLite store is
// currently running. Always returns false on no-CGO builds.
func IsActive() bool {
	return false
}
