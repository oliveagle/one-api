//go:build cgo

package rqlite

import (
	"context"
	"database/sql"
	"fmt"
	"log"
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

// OpenSQL returns a *sql.DB over the store's direct SQLite handle (see
// EmbeddedStore.Direct). The direct handle is the only SQL surface that
// works with GORM: our database/sql driver (DriverName) goes through
// Store.Request, which has a known rqlite v10 bug where pure reads can
// return empty results.
func OpenSQL() (*sql.DB, error) {
	es := currentStore()
	if es == nil {
		return nil, ErrNoStore
	}
	direct := es.Direct()
	if direct == nil {
		return nil, fmt.Errorf("rqlite: direct handle not ready")
	}
	return direct, nil
}

// OpenGorm opens a GORM handle over the embedded store.
//
// GORM (via database/sql) requires a driver.Conn, but the embedded store
// exposes its database only through rqlite's proto API (Store.Request),
// and rqlite's mattn-based connection pool is internal to the store.
// Therefore OpenGorm returns a *gorm.DB whose SQL access goes through the
// store's direct rw connection pool (rqlite v10 db.DB), bypassing
// database/sql. This keeps GORM's schema/migration/CRUD working while
// data is actually stored in the RQLite-managed SQLite file.
func OpenGorm() (*gorm.DB, error) {
	es := currentStore()
	if es == nil {
		return nil, ErrNoStore
	}
	direct := es.Direct()
	if direct == nil {
		return nil, fmt.Errorf("rqlite: no direct handle (store not ready)")
	}
	// Route ALL GORM operations through the rqliteConn driver (database/sql
	// → rqliteConn), which already implements read-write separation:
	//   reads  (SELECT/PRAGMA/EXPLAIN/WITH) → store.query()    (fast read pool)
	//   writes (everything else)             → store.request()  (raft consensus → replicates)
	//
	// The previous `Conn: direct` binding bypassed raft consensus entirely,
	// so writes stayed local and never replicated to cluster followers.
	// Removing `Conn` makes GORM call sql.Open(DriverName, "") which creates
	// rqliteConn instances via driverImpl.Open().
	db, err := gorm.Open(sqlite.Dialector{
		DriverName: DriverName,
	}, &gorm.Config{
		PrepareStmt: true,
	})
	if err != nil {
		// On a raft follower, GORM's initialization may attempt DDL or
		// PRAGMA writes that fail with "not leader". The schema arrives
		// via replication; return the handle anyway so reads work.
		if es := currentStore(); es != nil && !es.IsLeader() {
			log.Printf("[rqlite] follower: gorm init error ignored (schema arrives via replication): %v", err)
			return db, nil // db may be partially initialized but usable for reads
		}
		return nil, err
	}
	return db, nil
}

// StartEmbedded opens the process-wide embedded RQLite store for dir and
// returns it. Used by main.go when RQLITE_DIR is set, so the binary can
// serve a RQLite-backed deployment without SQL_DSN changes.
func StartEmbedded(dir, nodeID string) (*EmbeddedStore, error) {
	opts := &Options{Dir: dir, NodeID: "oneapi", RaftAddr: "127.0.0.1:0", ReadyTimeoutSeconds: 30}
	if nodeID != "" {
		opts.NodeID = nodeID
	}
	return OpenStore(context.Background(), opts)
}

// registerDriver registers the database/sql driver exactly once.
func registerDriver() {
	registerOnce.Do(func() {
		sql.Register(DriverName, driverImpl{})
	})
}

// setCurrentStore installs the process-wide embedded store used by the
// database/sql driver. It is called by OpenStore after bootstrap.
func setCurrentStore(s *EmbeddedStore) {
	currentMu.Lock()
	defer currentMu.Unlock()
	current = s
}

// releaseCurrentStore clears the process-wide embedded store. Called by
// EmbeddedStore.Close so the slot can be reused (tests, future multi-store).
func releaseCurrentStore(s *EmbeddedStore) {
	currentMu.Lock()
	defer currentMu.Unlock()
	if current == s {
		current = nil
	}
}

func currentStore() *EmbeddedStore {
	currentMu.RLock()
	defer currentMu.RUnlock()
	return current
}

// IsActive reports whether a process-wide embedded RQLite store is
// currently running (i.e. StartEmbedded has been called and hasn't been
// closed yet).
func IsActive() bool {
	return currentStore() != nil
}

// IsLeader reports whether the process-wide embedded store is the current
// raft leader. Followers should defer writes (schema migration etc.) to
// the leader; data arrives via replication.
func IsLeader() bool {
	es := currentStore()
	return es != nil && es.IsLeader()
}
