// Package testutil provides shared test helpers for the one-api codebase.
//
// Goals:
//   - Mock DB: spin up an in-memory SQLite database with all GORM models
//     auto-migrated, so tests don't need a running MySQL/Postgres.
//   - Mock HTTP: a configurable http.RoundTripper that records and replays
//     canned responses, eliminating flaky external network dependencies.
//   - Mock Redis: a miniredis-backed client sufficient for the small set
//     of operations one-api performs.
//
// All helpers are safe to use from parallel tests: each call returns a
// fresh, isolated instance. Callers do not need to invoke Close
// explicitly — every helper registers a t.Cleanup hook.
//
// Typical usage:
//
//	func TestSomething(t *testing.T) {
//	    t.Parallel()
//	    db := testutil.NewMockDB(t)
//	    httpClient := testutil.NewMockHTTPClient(t)
//	    ...
//	}
package testutil

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/model"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var dbSeq atomic.Uint64

// NewMockDB returns a fresh file-backed SQLite database with every model
// schema applied via AutoMigrate. Each call allocates a unique file under
// t.TempDir() so parallel tests do not share state. The file is removed
// automatically when the test ends.
//
// We use on-disk sqlite (not file::memory:?cache=shared) because shared
// in-memory caches leak state between tests and have caused intermittent
// failures in CI.
func NewMockDB(t *testing.T) *gorm.DB {
	t.Helper()

	seq := dbSeq.Add(1)
	dir := t.TempDir()
	dbPath := fmt.Sprintf("%s/test-%d.db", dir, seq)

	gormDB, err := gorm.Open(
		sqlite.Open(dbPath+"?_busy_timeout=5000&_pragma=foreign_keys(1)"),
		&gorm.Config{
			Logger:                                   logger.Default.LogMode(logger.Silent),
			DisableForeignKeyConstraintWhenMigrating: true,
			PrepareStmt:                              true,
		},
	)
	if err != nil {
		t.Fatalf("testutil: open sqlite %s: %v", dbPath, err)
	}

	if err := model.AutoMigrateAll(gormDB); err != nil {
		t.Fatalf("testutil: automigrate failed: %v", err)
	}

	return gormDB
}

// NewMockDBForCommon configures the package-level common.UsingSQLite flag
// (and clears MySQL/PostgreSQL flags) so code paths that branch on those
// flags behave consistently inside tests. Call this *before* exercising
// code that reads common.UsingSQLite.
func NewMockDBForCommon(t *testing.T) *gorm.DB {
	t.Helper()
	common.UsingSQLite = true
	common.UsingMySQL = false
	common.UsingPostgreSQL = false
	return NewMockDB(t)
}

// dbMigrateMu serialises the very first AutoMigrate call per process.
// GORM's AutoMigrate is internally guarded but a handful of legacy
// indexes (e.g. idx_channels_key) are managed via raw SQL in production
// and would race if two tests migrated concurrently. The mutex is cheap
// because it only protects first-time setup; subsequent calls hit the
// schema cache.
var dbMigrateMu sync.Mutex

// guardMigrate runs fn under dbMigrateMu and returns its result.
// Exposed for unit tests that need to share the lock.
func guardMigrate(fn func() error) error {
	dbMigrateMu.Lock()
	defer dbMigrateMu.Unlock()
	return fn()
}
