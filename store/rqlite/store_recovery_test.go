//go:build cgo

package rqlite

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// newStoreForDir opens the process-wide embedded store for dir and returns
// it; the test must Close it before any other boot.
func newStoreForDir(t *testing.T, dir, nodeID string) *EmbeddedStore {
	t.Helper()
	dsn := "rqlite://" + dir
	if nodeID != "" {
		dsn += "?node_id=" + nodeID
	}
	opts, err := ParseDSN(dsn)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	es, err := OpenStore(ctx, opts)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return es
}

func seedStore(t *testing.T, dir string) {
	t.Helper()
	es := newStoreForDir(t, dir, "seed")
	if _, err := es.Direct().Exec("CREATE TABLE IF NOT EXISTS t (id INTEGER PRIMARY KEY, v TEXT)"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := es.Direct().Exec("INSERT INTO t (v) VALUES ('marker')"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := es.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func sha256OfFile(t *testing.T, path string) string {
	t.Helper()
	b, err := osReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum)
}

// TestCleanMarkerPreservesDB pins the restart guarantee: after a clean
// close (which writes a full-snapshot marker), the next boot must preserve
// the existing db.sqlite instead of restoring the latest snapshot. A
// regression here is the "empty db after restart" data-loss bug.
func TestCleanMarkerPreservesDB(t *testing.T) {
	dir := t.TempDir() + "/rqlite"
	seedStore(t, dir)

	dbPath := filepath.Join(dir, "db.sqlite")
	sumBefore := sha256OfFile(t, dbPath)
	if sumBefore == "" {
		t.Fatal("db file missing after seeding")
	}

	// Boot again: the clean marker from the previous close must be honored.
	es := newStoreForDir(t, dir, "restart")
	defer es.Close()

	var v string
	if err := es.Direct().QueryRow("SELECT v FROM t WHERE id = 1").Scan(&v); err != nil {
		t.Fatalf("read back: %v (db was likely clobbered by snapshot restore; file before=%s)", err, sumBefore)
	}
	if v != "marker" {
		t.Fatalf("row lost: got %q", v)
	}
	sumAfter := sha256OfFile(t, dbPath)
	t.Logf("db file after clean restart: hash=%s (before=%s)", sumAfter, sumBefore)
}

// TestCrashRestartPreservesDB pins the crash-restart guarantee: an unclean
// shutdown (process killed) must not lose the db on the next boot.
func TestCrashRestartPreservesDB(t *testing.T) {
	dir := t.TempDir() + "/rqlite"
	seedStore(t, dir)
	es := newStoreForDir(t, dir, "crash")
	if _, err := es.Direct().Exec("INSERT INTO t (id, v) VALUES (2, 'crash-row')"); err != nil {
		t.Fatalf("insert before crash: %v", err)
	}
	// Simulate kill -9: drop the direct handle and forget the store
	// (no Close, so no checkpoint/finalizer runs).
	_ = es.Direct().Close()
	es.closeMu.Lock()
	es.closed = true
	es.closeMu.Unlock()
	releaseCurrentStore(es)

	es2 := newStoreForDir(t, dir, "after-crash")
	defer es2.Close()
	var n int
	if err := es2.Direct().QueryRow("SELECT count(*) FROM t").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Fatalf("rows after crash-restart: got %d, want 2", n)
	}
}
