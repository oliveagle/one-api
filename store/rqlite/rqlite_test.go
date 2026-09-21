//go:build cgo

package rqlite

import (
	"context"
	"database/sql"
		"testing"
	"time"
)

func TestParseDSN(t *testing.T) {
	t.Parallel()
	cases := []struct {
		dsn    string
		want   string
		wantID string
		wantErr bool
	}{
		{dsn: "rqlite://./data/rqlite", want: "data/rqlite", wantID: "oneapi"},
		{dsn: "rqlite://data/rqlite?node_id=n1", want: "data/rqlite", wantID: "n1"},
		{dsn: "rqlite:///abs/path?raft_addr=127.0.0.1:4001&ready_timeout=5", want: "/abs/path", wantID: "oneapi"},
		{dsn: "rqlite://", wantErr: true},
		{dsn: "postgres://x", wantErr: true},
		{dsn: "rqlite://dir?ready_timeout=-1", wantErr: true},
	}
	for _, c := range cases {
		opts, err := ParseDSN(c.dsn)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseDSN(%q): expected error, got %v", c.dsn, opts)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseDSN(%q): %v", c.dsn, err)
			continue
		}
		if opts.Dir != c.want {
			t.Errorf("ParseDSN(%q).Dir = %q, want %q", c.dsn, opts.Dir, c.want)
		}
		if c.wantID != "" && opts.NodeID != c.wantID {
			t.Errorf("ParseDSN(%q).NodeID = %q, want %q", c.dsn, opts.NodeID, c.wantID)
		}
	}
}

func TestIsRQLiteDSN(t *testing.T) {
	t.Parallel()
	if !IsRQLiteDSN("rqlite://x") {
		t.Error("rqlite://x should be recognized")
	}
	if IsRQLiteDSN("postgres://x") {
		t.Error("postgres://x should not be recognized")
	}
	if IsRQLiteDSN("") {
		t.Error("empty DSN should not be recognized")
	}
}

// TestEmbeddedStoreLifecycle boots a single-node store, runs a small GORM
// workload through our driver, and shuts it down.
func TestEmbeddedStoreLifecycle(t *testing.T) {
	dir := t.TempDir() + "/rqlite"
	opts, err := ParseDSN("rqlite://" + dir + "?ready_timeout=30")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	es, err := OpenStore(ctx, opts)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer es.Close()

	// Raw SQL through the database/sql driver.
	sqlDB, err := sql.Open(DriverName, dir)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer sqlDB.Close()
	if err := sqlDB.PingContext(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}

	if _, err := sqlDB.ExecContext(ctx, "CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT, quota BIGINT)"); err != nil {
		t.Fatalf("create: %v", err)
	}
	res, err := sqlDB.ExecContext(ctx, "INSERT INTO t (name, quota) VALUES (?, ?)", "alice", 100)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if id, _ := res.LastInsertId(); id != 1 {
		t.Fatalf("last insert id = %d, want 1", id)
	}
	var name string
	var quota int64
	if err := sqlDB.QueryRowContext(ctx, "SELECT name, quota FROM t WHERE id = ?", 1).Scan(&name, &quota); err != nil {
		t.Fatalf("select: %v", err)
	}
	if name != "alice" || quota != 100 {
		t.Fatalf("got (%q, %d), want (alice, 100)", name, quota)
	}

	// RANDOM() must be accepted (RQLite rewrites it on the write path).
	var n int
	if err := sqlDB.QueryRowContext(ctx, "SELECT RANDOM()").Scan(&n); err != nil {
		t.Fatalf("random: %v", err)
	}
	_ = n

	// GORM workload via OpenGorm.
	gormDB, err := OpenGorm()
	if err != nil {
		t.Fatalf("open gorm: %v", err)
	}
	type user struct {
		ID        int   `gorm:"primaryKey"`
		Username  string
		UsedQuota int64
	}
	if err := gormDB.AutoMigrate(&user{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	if err := gormDB.Create(&user{Username: "bob", UsedQuota: 42}).Error; err != nil {
		t.Fatalf("gorm create: %v", err)
	}
	var got user
	if err := gormDB.Where("username = ?", "bob").First(&got).Error; err != nil {
		t.Fatalf("gorm first: %v", err)
	}
	if got.UsedQuota != 42 {
		t.Fatalf("quota = %d, want 42", got.UsedQuota)
	}
}
