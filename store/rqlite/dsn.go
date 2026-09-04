// Package rqlite embeds an RQLite store (github.com/rqlite/rqlite/v10,
// vendored via git submodule at submodules/rqlite) into the one-api
// process and exposes it to GORM through a database/sql driver.
//
// See docs/adr/0004-rqlite-embedded-store.md for the design decisions:
//   - D1: DSN grammar  rqlite://[dir]?[params]
//   - D2: our own database/sql driver over Store.Request (rqlite v10
//     exports no driver)
//   - D3: RQLite is SQLite dialect; common.UsingRQLite is OR-ed into
//     the existing SQLite branches
//   - D5: real implementation requires CGO (mattn/go-sqlite3 inside
//     the store); CGO_ENABLED=0 builds get a stub that fails fast
package rqlite

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"strconv"
)

const DSNPrefix = "rqlite://"

// Options are the parsed components of an rqlite:// DSN.
type Options struct {
	// Dir is the data directory (db.sqlite, raft.db, wsnapshots, peers).
	Dir string
	// NodeID is the raft node identifier.
	NodeID string
	// RaftAddr is the raft listen address. For single-node: loopback
	// ephemeral. For multi-node: a routable host:port reachable from
	// cluster peers.
	RaftAddr string
	// JoinAddr is the address of an existing cluster node to join
	// (host:port of that node's RaftAddr). Empty = bootstrap a new
	// single-node cluster.
	JoinAddr string
	// ReadyTimeoutSeconds bounds how long we wait for the store to report
	// ready after bootstrap.
	ReadyTimeoutSeconds int
}

var (
	ErrNotRQLiteDSN = errors.New("not an rqlite:// DSN")
	ErrEmptyDir     = errors.New("rqlite:// DSN requires a non-empty data directory")
)

// IsRQLiteDSN reports whether dsn uses the rqlite:// scheme.
func IsRQLiteDSN(dsn string) bool {
	return len(dsn) >= len(DSNPrefix) && dsn[:len(DSNPrefix)] == DSNPrefix
}

// ParseDSN parses an rqlite://[dir]?[key=value&...] DSN.
//
// The path is the data directory and must be non-empty. Recognized query
// parameters (all optional):
//
//	node_id        raft node id (default: "oneapi")
//	raft_addr      raft listen address (default: "127.0.0.1:0")
//	ready_timeout  seconds to wait for store readiness (default: 30)
func ParseDSN(dsn string) (*Options, error) {
	if !IsRQLiteDSN(dsn) {
		return nil, fmt.Errorf("%w: %q", ErrNotRQLiteDSN, dsn)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return nil, fmt.Errorf("rqlite: parse %q: %w", dsn, err)
	}

	opts := &Options{
		Dir:                 path.Clean(u.Host + u.Path),
		NodeID:              "oneapi",
		RaftAddr:            "127.0.0.1:0",
		ReadyTimeoutSeconds: 30,
	}
	if opts.Dir == "" || opts.Dir == "." {
		return nil, ErrEmptyDir
	}

	q := u.Query()
	if v := q.Get("node_id"); v != "" {
		opts.NodeID = v
	}
	if v := q.Get("raft_addr"); v != "" {
		opts.RaftAddr = v
	}
	if v := q.Get("join_addr"); v != "" {
		opts.JoinAddr = v
	}
	if v := q.Get("ready_timeout"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("rqlite: invalid ready_timeout %q", v)
		}
		opts.ReadyTimeoutSeconds = n
	}
	return opts, nil
}
