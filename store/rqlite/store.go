//go:build cgo

package rqlite

import (
	"net/http"

	"bytes"

	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"hash/fnv"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/raft"
	"github.com/rqlite/rqlite/v10/cluster"
	proto "github.com/rqlite/rqlite/v10/command/proto"
	"github.com/rqlite/rqlite/v10/snapshot"
	"github.com/rqlite/rqlite/v10/store"
	"github.com/rqlite/rqlite/v10/tcp"
)

// EmbeddedStore wraps a single-node RQLite store (bootstrap as one voter,
// no external network service). ADR-0004 D6.
// EnsureFullSnapshot makes sure a full (DB-inclusive) snapshot is due for
// the next snapshot operation. rqlite v10 takes only incremental snapshots
// once a full one exists; the snapshot taken at store close can be WAL-only,
// which a later peers.json recovery cannot restore. Committing a noop
// (any DB modification) makes the next snapshot Full. Called at startup and
// before shutdown.
func (s *EmbeddedStore) EnsureFullSnapshot(ctx context.Context) error {
	if fut, err := s.store.Noop("oneapi-full-snapshot"); err == nil {
		if err := fut.Error(); err == nil {
			_ = s.store.WaitForCommitIndex(fut.Index(), 10*time.Second)
		}
	}
	return nil
}

type EmbeddedStore struct {
	store          *store.Store
	dir            string
	dbPath         string
	ly             *tcp.Layer
	mux            *tcp.Mux
	ln             net.Listener
	direct         *sql.DB // direct connection to the store's SQLite file
	fullNeededPath string
	closeMu        sync.Mutex
	closed         bool
	joinListener   net.Listener
}

// ErrStoreClosed is returned by request paths after Close.
var ErrStoreClosed = errors.New("rqlite: store closed")

// dbPathForMarker returns the db file path for a data dir.
func dbPathForMarker(dir string) string {
	return filepath.Join(dir, "db.sqlite")
}

// writeCleanMarker writes a clean_snapshot marker matching the current db
// file (mod time + size + CRC32) so rqlite takes the fast path on restart
// and preserves the db file instead of restoring from a snapshot.
func writeCleanMarker(dir string, fi os.FileInfo) {
	marker := filepath.Join(dir, "clean_snapshot")
	type fp struct {
		ModTime string `json:"mod_time"`
		Size    int64  `json:"size"`
		CRC32   uint32 `json:"crc32"`
	}
	f := fp{ModTime: fi.ModTime().Format(time.RFC3339Nano), Size: fi.Size()}
	if fh, err := os.Open(filepath.Join(dir, "db.sqlite")); err == nil {
		data, _ := io.ReadAll(fh)
		fh.Close()
		f.CRC32 = crc32.ChecksumIEEE(data)
	}
	b, _ := json.Marshal(f)
	_ = os.WriteFile(marker, b, 0o644)
}

// newRaftLayer creates the loopback raft listener + mux + layer and starts
// serving the mux. Used by OpenStore and resetRaftState.
func newRaftLayer(raftAddr string) (mux *tcp.Mux, ln net.Listener, ly *tcp.Layer, err error) {
	ln, err = net.Listen("tcp", raftAddr)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("rqlite: listen raft %s: %w", raftAddr, err)
	}
	mux, err = tcp.NewMux(ln, nil)
	if err != nil {
		ln.Close()
		return nil, nil, nil, fmt.Errorf("rqlite: new mux: %w", err)
	}
	dialer := tcp.NewDialer(cluster.MuxRaftHeader, nil)
	ly = tcp.NewLayer(mux.Listen(cluster.MuxRaftHeader), dialer)
	go func() {
		_ = mux.Serve()
	}()
	return mux, ln, ly, nil
}

// OpenStore boots the embedded store for opts and registers the
// database/sql driver. It is idempotent per process: a second call
// while a store is open returns ErrStoreAlreadyOpen.
func OpenStore(ctx context.Context, opts *Options) (*EmbeddedStore, error) {
	if currentStore() != nil {
		return nil, errors.New("rqlite: a store is already open in this process")
	}
	if err := os.MkdirAll(opts.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("rqlite: mkdir %s: %w", opts.Dir, err)
	}

	// Retry wrapper: rqlite's snapshot reap runs asynchronously inside
	// Store.Open; a first open can transiently fail with "MSRW conflict
	// owner: reap". Retry a few times with a short backoff.
	var (
		es  *EmbeddedStore
		err error
	)
	for attempt := 0; attempt < 5; attempt++ {
		es, err = openStoreOnce(ctx, opts)
		if err == nil {
			return es, nil
		}
		if !strings.Contains(err.Error(), "MSRW conflict") {
			return nil, err
		}
		logf("rqlite: open failed (MSRW conflict), retry %d", attempt+1)
		time.Sleep(time.Duration(500*(attempt+1)) * time.Millisecond)
	}
	return nil, err
}

// openStoreOnce performs a single attempt to open the embedded store.

// stableRaftAddr derives a deterministic loopback address from the data dir
// so the raft configuration remains valid across restarts. Uses FNV-1a of
// the dir path mapped into the dynamic port range (49152-65535).

// checkpointWAL flushes the SQLite WAL into the main database file so the
// sidecar -wal/-shm files can be safely removed without data loss.
func checkpointWAL(dir string) {
	dbPath := filepath.Join(dir, "db.sqlite")
	d, err := sql.Open("rqlite-sqlite3", dbPath)
	if err != nil {
		logf("rqlite: checkpoint open: %v", err)
		return
	}
	defer d.Close()
	if _, err := d.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		logf("rqlite: checkpoint: %v", err)
	} else {
		logf("rqlite: WAL checkpointed successfully")
	}
}

// joinCluster sends a join request to the cluster leader via the rqlite
// HTTP API (the mux serves both raft and HTTP on the same port).
func joinCluster(joinAPIAddr, nodeID, nodeRaftAddr string) error {
	joinReq := map[string]any{
		"id":      nodeID,
		"address": nodeRaftAddr,
		"voter":   true,
	}
	body, _ := json.Marshal(joinReq)
	url := fmt.Sprintf("http://%s/join", joinAPIAddr)
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("POST %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("join returned %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// ServeJoinHTTP starts a minimal HTTP server on the store's raft address
// that handles POST /join for cluster peers. Call this on the LEADER node
// to accept follower joins. The server runs until the store closes.
func (es *EmbeddedStore) ServeJoinHTTP() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/join", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		var jr struct {
			Id      string `json:"id"`
			Address string `json:"address"`
			Voter   bool   `json:"voter"`
		}
		if err := json.NewDecoder(r.Body).Decode(&jr); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := es.store.Join(&proto.JoinRequest{
			Id:      jr.Id,
			Address: jr.Address,
			Voter:   jr.Voter,
		}); err != nil {
			logf("rqlite: join rejected: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		logf("rqlite: node %s at %s joined cluster", jr.Id, jr.Address)
		w.WriteHeader(http.StatusOK)
	})

	// Listen on the same port as raft (the mux binds it, so we use a
	// separate listener on a companion port). For simplicity, use the
	// raft port + 1.
	raftPort := ly_Port(es)
	joinAddr := fmt.Sprintf("127.0.0.1:%d", raftPort+1)
	ln, err := net.Listen("tcp", joinAddr)
	if err != nil {
		return fmt.Errorf("rqlite: join listener on %s: %w", joinAddr, err)
	}
	es.joinListener = ln
	go func() {
		_ = http.Serve(ln, mux)
	}()
	logf("rqlite: join API listening on %s", joinAddr)
	return nil
}

// ly_Port extracts the numeric port from the store's raft layer address.
func ly_Port(es *EmbeddedStore) int {
	addr := es.ly.Addr().String()
	_, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)
	return port
}

func stableRaftAddr(dir string) string {
	h := fnv.New32a()
	h.Write([]byte(dir))
	port := 49152 + int(h.Sum32()%16384)
	return fmt.Sprintf("127.0.0.1:%d", port)
}

func openStoreOnce(ctx context.Context, opts *Options) (*EmbeddedStore, error) {

	// Node identity + stable raft address: the committed raft configuration
	// records the node_id AND raft address from the first boot. A restart
	// with a different id can never win an election (not in the voter set),
	// and a different address prevents the single-node leader from appending
	// to itself. Both produce the infinite election loop that hangs
	// TestCleanMarkerPreservesDB / TestCrashRestartPreservesDB.
	//
	// Additionally, peers.json (raft recovery) CONFLICTS with the clean
	// snapshot marker: Store.Open() processes peers.json first (removing
	// the clean marker), then checks the clean marker (gone → restores
	// from the possibly-empty snapshot → data loss). Using a STABLE address
	// eliminates the need for peers.json on restart entirely, letting the
	// clean marker work as intended.
	nodeIDFile := filepath.Join(opts.Dir, "node-id")
	raftAddrFile := filepath.Join(opts.Dir, "raft-addr")
	if data, err := os.ReadFile(nodeIDFile); err == nil && len(strings.TrimSpace(string(data))) > 0 {
		opts.NodeID = strings.TrimSpace(string(data))
	} else {
		_ = os.WriteFile(nodeIDFile, []byte(opts.NodeID), 0o644)
	}
	if data, err := os.ReadFile(raftAddrFile); err == nil && len(strings.TrimSpace(string(data))) > 0 {
		opts.RaftAddr = strings.TrimSpace(string(data))
		// Check if the address is available; if a stale listener from a
		// crashed process holds it, fall back to ephemeral.
		if ln, lerr := net.Listen("tcp", opts.RaftAddr); lerr != nil {
			logf("rqlite: preferred raft addr %s busy (%v), using ephemeral", opts.RaftAddr, lerr)
			opts.RaftAddr = "127.0.0.1:0"
		} else {
			_ = ln.Close()
		}
	} else {
		// First boot: if the address is ephemeral (:0), derive a stable
		// port from the data dir so it survives restarts.
		if strings.HasSuffix(opts.RaftAddr, ":0") {
			opts.RaftAddr = stableRaftAddr(opts.Dir)
		}
		_ = os.WriteFile(raftAddrFile, []byte(opts.RaftAddr), 0o644)
	}

	// Clean-restart: on restart, save db, wipe raft state, boot as fresh.
	var dbBackup []byte
	if hasCommitted, _, _ := func() (bool, error, error) { c, e := store.HasData(opts.Dir); return c, e, nil }(); hasCommitted {
		checkpointWAL(opts.Dir)
		if data, err := os.ReadFile(filepath.Join(opts.Dir, "db.sqlite")); err == nil {
			dbBackup = data
			logf("rqlite: clean-restart: saved %d byte db backup", len(dbBackup))
		}
		for _, rel := range []string{"raft.db", "wsnapshots", "rsnapshots"} {
			_ = os.RemoveAll(filepath.Join(opts.Dir, rel))
		}
	}

	// Configuration strategy (decided BEFORE opening the store):
	//  - New data dir (no committed state): fresh node. rqlite's
	//    Store.Bootstrap (liveBootstrap) races the node's first election,
	//    so we install the configuration via rqlite's built-in
	//    node-recovery path (RecoverNode) instead: write raft/peers.json
	//    on clean state, then open. With no snapshots, recovery is a
	//    fast no-op that commits our config at index 1.
	//  - Existing data dir with committed state: restart. Write
	//    raft/peers.json (and remove the clean_snapshot marker, which
	//    would otherwise let rqlite skip the restore and truncate the
	//    db to the last incremental WAL-only snapshot). On open,
	//    RecoverNode restores the latest full snapshot, replays the log
	//    tail, and rewrites the configuration to the current address.
	//    This also fixes the rqlite v10 bug where, after a restart,
	//    raft's in-memory configuration is empty while the committed
	//    configuration references the previous run's stale ephemeral
	//    address (neither Store.Join nor Store.Bootstrap can repair
	//    that).
	committed := false
	if !store.IsNewNode(opts.Dir) {
		var err error
		committed, err = store.HasData(opts.Dir)
		if err != nil {
			committed = false
		}
	}
	logf("rqlite: node=%s raftAddr=%s committed=%v", opts.NodeID, opts.RaftAddr, committed)
	if !committed {
		// Fresh node: ensure the raft state is clean so RecoverNode runs.
		for _, rel := range []string{"raft.db"} {
			_ = os.Remove(filepath.Join(opts.Dir, rel))
		}
		for _, rel := range []string{"wsnapshots", "snapshots", "rsnapshots"} {
			_ = os.RemoveAll(filepath.Join(opts.Dir, rel))
		}
	} else {
		// Restart: make rqlite take the clean-snapshot fast path
		// (NoSnapshotRestoreOnStart), which PRESERVES the existing
		// db.sqlite (the source of truth) instead of restoring from the
		// (possibly empty) snapshot. We do this by writing a clean
		// marker whose fingerprint matches the current db file. If the
		// file is ever modified externally, the store's own CRC check
		// will fail open to a restore (safe fallback).
		if fi, err := os.Stat(dbPathForMarker(opts.Dir)); err == nil {
			writeCleanMarker(opts.Dir, fi)
		}
	}

	// Internal loopback raft listener (ephemeral by default). The mux is
	// required by the store; it never binds a public address.
	mux, ln, ly, err := newRaftLayer(opts.RaftAddr)
	if err != nil {
		return nil, err
	}
	// Persist the actual bound address (might be ephemeral if preferred was busy).
	_ = os.WriteFile(raftAddrFile, []byte(ly.Addr().String()), 0o644)

	// Write peers.json (recovery config) before opening the store — but
	// ONLY for fresh nodes. On restart, peers.json triggers RecoverNode
	// inside Store.Open(), which deletes the clean-snapshot marker and
	// restores from the latest snapshot (potentially empty), destroying
	// db.sqlite. With node-id persistence the committed raft config already
	// has the correct identity, and a single-node raft elects itself leader
	// without needing an address update from peers.json.
	peersFile := filepath.Join(opts.Dir, "raft", "peers.json")
	_ = os.Remove(peersFile)
	if err := os.MkdirAll(filepath.Dir(peersFile), 0o755); err != nil {
		ly.Close()
		ln.Close()
		return nil, fmt.Errorf("rqlite: mkdir raft dir: %w", err)
	}
	_ = committed // used below for clean marker + empty-db check
	// rqlite's snapshot restore (ReplayWAL) refuses to run when a
	// <db>-wal file exists. A failed/aborted recovery (or a crash) can
	// leave WAL sidecar files behind; remove all of them so the restore
	// can proceed. The store's own db file (db.sqlite) is left alone -
	// it is restored from the snapshot.
	// Only remove WAL sidecar files for FRESH nodes. On restart, the
	// data written via Direct() since the last checkpoint lives in
	// db.sqlite-wal — deleting it loses everything. Instead, checkpoint
	// the WAL into the main db file first, then remove the sidecars.
	if committed {
		checkpointWAL(opts.Dir)
	}
	for _, rel := range []string{
		"db.sqlite-wal", "db.sqlite-shm",
		"recovery.db", "recovery.db-wal", "recovery.db-shm",
	} {
		_ = os.Remove(filepath.Join(opts.Dir, rel))
	}
	for _, f := range globFiles(filepath.Join(opts.Dir, "restore-wal-*.tmp")) {
		_ = os.Remove(f)
	}
	_ = os.RemoveAll(filepath.Join(opts.Dir, "wal-staging"))
	// raft.ReadConfigJSON expects {"id","address","non_voter"} entries.
	peers := fmt.Sprintf("[{\"id\":%q,\"address\":%q,\"non_voter\":false}]", opts.NodeID, ly.Addr().String())
	if err := os.WriteFile(peersFile, []byte(peers), 0o644); err != nil {
		ly.Close()
		ln.Close()
		return nil, fmt.Errorf("rqlite: write peers.json: %w", err)
	}
	logf("rqlite: recovery config written (addr %s, committed=%v)", ly.Addr().String(), committed)

	dbConf := store.NewDBConfig()
	cfg := &store.Config{
		DBConf: dbConf,
		Dir:    opts.Dir,
		ID:     opts.NodeID,
		Logger: log.New(os.Stderr, "[rqlite] ", log.LstdFlags),
	}

	s := store.New(cfg, ly)
	// rqlite's own snapshot finalizer (createSnapshotFingerprint) writes
	// the clean_snapshot fingerprint with the correct Castagnoli CRC32 on
	// every successful snapshot. We rely on it instead of rolling our own,
	// so the fast-restart path always matches rqlite's expectations.
	_ = s
	if !committed {
		logf("rqlite: new node initialized via recovery path")
	}
	if err := s.Open(); err != nil {
		ly.Close()
		ln.Close()
		return nil, fmt.Errorf("rqlite: open store: %w", err)
	}
	// If the store's db is empty (no user tables) but snapshots exist, the
	// raft restore-on-start either didn't run (NoSnapshotRestoreOnStart
	// fast path) or restored an empty/old snapshot. Force a restore from
	// the latest snapshot so db.sqlite reflects the true committed state.
	// Force the next snapshot to be a FULL snapshot (DB-inclusive), not an
	// incremental WAL-only one. Incremental snapshots cannot be restored
	// by rqlite's node recovery (ReplayWAL), so a restart after an
	// incremental-only snapshot loses the db.
	//
	// Two levers:
	//  1. dbModified(): rqlite takes a Full snapshot when the db file's
	//     mtime is newer than the last recorded modification. We tick the
	//     db file's mtime forward on every boot, so this holds.
	//  2. FULL_NEEDED marker: rqlite's snapshot store reports DueNext=Full
	//     while the marker exists, which is the primary full-snapshot
	//     trigger. We remove it before opening (stale markers from a
	//     prior boot would otherwise interfere) and let the store recreate
	//     it on the first non-full snapshot; we re-assert it just before
	//     close so the final snapshot-on-close is full.
	dbPath := filepath.Join(opts.Dir, "db.sqlite")
	tickMTime := func(path string) {
		if _, err := os.Stat(path); err == nil {
			fut := time.Now().Add(2 * time.Second)
			_ = os.Chtimes(path, fut, fut)
		}
	}
	tickMTime(dbPath)
	tickMTime(dbPath + "-wal")
	tickMTime(dbPath + "-shm")
	fullNeeded := filepath.Join(opts.Dir, "wsnapshots", "FULL_NEEDED")
	_ = os.Remove(fullNeeded) // stale marker from prior boot
	es := &EmbeddedStore{store: s, dir: opts.Dir, dbPath: dbPath, ly: ly, mux: mux, ln: ln, fullNeededPath: fullNeeded}
	setCurrentStore(es)
	registerDriver()
	logf("rqlite: store opened (dir %s, raft %s, bootstrap-path)", opts.Dir, ly.Addr().String())

	// Wait for readiness (leader elected, initial state applied).
	readyCtx, cancel := context.WithTimeout(ctx, time.Duration(opts.ReadyTimeoutSeconds)*time.Second)
	defer cancel()
	logf("rqlite: waiting for ready...")
	if err := waitReady(readyCtx, s); err != nil {
		logf("rqlite: waitReady FAILED: %v", err)
		es.Close()
		setCurrentStore(nil)
		return nil, err
	}

	// Open a direct *sql.DB on the store's SQLite file. The store has
	// registered mattn's driver under "rqlite-sqlite3" (see
	// rqlite/db/driver.go). We must NOT let database/sql create a second
	// mattn connection with the default busy_timeout (the store manages
	// WAL/checkpoints); so we reuse the store's own driver name, which
	// has no checkpoint-on-close and is the one the store uses for its
	// rw pool.
	logf("rqlite: ready, opening direct sql")
	es.direct, err = sql.Open("rqlite-sqlite3", dbPath)
	if err != nil {
		es.Close()
		setCurrentStore(nil)
		return nil, fmt.Errorf("rqlite: open direct sql: %w", err)
	}
	es.direct.SetMaxOpenConns(1)
	es.direct.SetConnMaxLifetime(0)

	// If the data dir already had committed state (a restart) but the db
	// ended up empty - node recovery only carries the cluster
	// configuration, not data - rebuild the committed state from the db
	// file: new full snapshot + log compaction + fingerprint.
	logf("rqlite: checking empty db (committed=%v)...", committed)
	if committed {
		var tables string
		es.direct.QueryRow("SELECT group_concat(name) FROM sqlite_master WHERE type=\"table\"").Scan(&tables)
		logf("rqlite: tables after restart: %q", tables)
	}
	if len(dbBackup) > 0 {
		logf("rqlite: clean-restart: restoring %d byte db", len(dbBackup))
		if err := os.WriteFile(dbPathForMarker(opts.Dir), dbBackup, 0o644); err != nil {
			return nil, fmt.Errorf("rqlite: clean-restart restore: %w", err)
		}
		if es.direct != nil {
			_ = es.direct.Close()
		}
		es.direct, err = sql.Open("rqlite-sqlite3", dbPathForMarker(opts.Dir))
		if err != nil {
			return nil, fmt.Errorf("rqlite: reopen direct after restore: %w", err)
		}
		es.direct.SetMaxOpenConns(1)
		es.direct.SetConnMaxLifetime(0)
		es.EnsureFullSnapshotMarker()
		logf("rqlite: clean-restart: db restored and snapshotted")
	}
	// Multi-node: join an existing cluster via the leader's raft address.
	if opts.JoinAddr != "" {
		// The join API listens on raft_port+1 on the leader (ServeJoinHTTP).
		joinHost, joinPortStr, _ := net.SplitHostPort(opts.JoinAddr)
		joinPort, _ := strconv.Atoi(joinPortStr)
		joinAPIAddr := fmt.Sprintf("%s:%d", joinHost, joinPort+1)
		logf("rqlite: joining cluster at %s (raft=%s, node=%s addr=%s)", joinAPIAddr, opts.JoinAddr, opts.NodeID, ly.Addr().String())
		if err := joinCluster(joinAPIAddr, opts.NodeID, ly.Addr().String()); err != nil {
			es.Close()
			setCurrentStore(nil)
			return nil, fmt.Errorf("rqlite: join cluster: %w", err)
		}
		logf("rqlite: joined cluster successfully")
	}

	if committed {
		if empty, err := es.directDBEmpty(); err == nil && empty {
			logf("rqlite: db is empty after restart")
			logf("rqlite: committed dir but empty db - rebuilding committed state from db file")
			if err := es.restoreFromDBFile(); err != nil {
				es.Close()
				setCurrentStore(nil)
				return nil, fmt.Errorf("rqlite: rebuild from db file: %w", err)
			}
			// The repair replaced the snapshot store out from under the
			// running node; close everything and reopen. The reopened
			// store sees: committed=true, db non-empty (the real data),
			// clean marker present -> fast path, no recovery re-run.
			if err := es.Close(); err != nil {
				setCurrentStore(nil)
				return nil, fmt.Errorf("rqlite: close after repair: %w", err)
			}
			es2, err := OpenStore(ctx, opts)
			if err != nil {
				setCurrentStore(nil)
				return nil, fmt.Errorf("rqlite: reopen after repair: %w", err)
			}
			return es2, nil
		}
	}

	return es, nil
}

// directDBEmpty reports whether the direct handle's database has no user tables.
func (s *EmbeddedStore) directDBEmpty() (bool, error) {
	if s.direct == nil {
		return false, fmt.Errorf("rqlite: direct handle not open")
	}
	var n int
	if err := s.direct.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&n); err != nil {
		return false, err
	}
	return n == 0, nil
}

// globFiles returns the matches of pattern (no error on no-match).
func globFiles(pattern string) []string {
	matches, _ := filepath.Glob(pattern)
	return matches
}

// crc32Castagnoli computes the Castagnoli CRC32 of a file, matching
// rqlite's rsum.CRC32 (its internal package is not importable).
func crc32Castagnoli(path string) (uint32, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	h := crc32.New(crc32.MakeTable(crc32.Castagnoli))
	if _, err := io.Copy(h, f); err != nil {
		return 0, err
	}
	return h.Sum32(), nil
}

func logf(format string, args ...any) {
	log.Printf("[one-api-rqlite] "+format, args...)
}

func waitReady(ctx context.Context, s *store.Store) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("rqlite: store not ready: %w", ctx.Err())
		case <-ticker.C:
			if s.Ready() {
				return nil
			}
		}
	}
}

// request sends one statement batch through the store (consensus for
// writes, read path for reads).
func (s *EmbeddedStore) request(ctx context.Context, req *proto.Request) ([]*proto.ExecuteQueryResponse, error) {
	s.closeMu.Lock()
	closed := s.closed
	s.closeMu.Unlock()
	if closed {
		return nil, ErrStoreClosed
	}
	eqr := &proto.ExecuteQueryRequest{
		Request: req,
		Level:   proto.ConsistencyLevel_LINEARIZABLE,
	}
	resp, _, _, err := s.store.Request(ctx, eqr)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// query runs read-only statements via the store's fast read path.
func (s *EmbeddedStore) query(ctx context.Context, req *proto.Request) ([]*proto.QueryRows, error) {
	s.closeMu.Lock()
	closed := s.closed
	s.closeMu.Unlock()
	if closed {
		return nil, ErrStoreClosed
	}
	qr := &proto.QueryRequest{
		Request: req,
		Level:   proto.ConsistencyLevel_LINEARIZABLE,
	}
	rows, _, _, err := s.store.Query(ctx, qr)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// execute runs a single statement and returns (response, isQuery, error).
func (s *EmbeddedStore) execute(ctx context.Context, sqlText string, params []*proto.Parameter, forceQuery bool) (*proto.ExecuteQueryResponse, bool, error) {
	stmts := []*proto.Statement{{Sql: sqlText, Parameters: params, ForceQuery: forceQuery}}
	req := &proto.Request{Statements: stmts}
	resp, err := s.request(ctx, req)
	if err != nil {
		return nil, false, err
	}
	if len(resp) == 0 {
		return nil, false, errors.New("rqlite: empty response")
	}
	return resp[0], resp[0].GetQ() != nil, nil
}

func (s *EmbeddedStore) newConn() *rqliteConn {
	return &rqliteConn{store: s}
}

// resetRaftState is a workaround for a rqlite v10 bug: after a process
// restart, raft's in-memory configuration can be empty while the committed
// configuration in the log still references the previous run's (now stale)
// ephemeral address. Neither Store.Join ("add voter" -> nextConfiguration
// sees an empty latest) nor Store.Bootstrap ("only works on new clusters")
// can then repair it.
//
// For the single-node embedded use case this is safe: the raft log carries
// no data of its own - the database is db.sqlite (+WAL), which we keep.
// We shut the store down, remove the raft state (raft.db, snapshots,
// clean_snapshot marker), reopen, and bootstrap fresh. On the next boot
// raft replays nothing; the db is already complete.

// Direct returns the direct *sql.DB handle on the store's SQLite file.
// It is safe to use with gorm's sqlite dialector (Conn pool).
func (s *EmbeddedStore) Direct() *sql.DB {
	return s.direct
}

// restoreFromDBFile rebuilds the committed raft state after a restart
// found committed state but an empty db. RQLite's node recovery (the
// peers.json path) only replays the cluster configuration into a temp
// db and writes it back - it never carries the data, so the real data
// (the db.sqlite file, preserved from the previous run) must be made
// the committed state itself:
//
//  1. replace the snapshot store with a single FULL snapshot built from
//     the current db file (configuration from the committed peers.info);
//  2. rewrite the clean_snapshot fingerprint for the db file, so the
//     next boot takes the fast path and preserves the db.
//
// The running store's internal snapshot bookkeeping is stale after step
// 1 (it still references the old snapshot store); the caller closes and
// reopens the store so the next open sees the fixed state.
func (s *EmbeddedStore) restoreFromDBFile() error {
	snapDir := s.snapshotStoreDir()
	oldDir := snapDir + ".old"
	_ = os.RemoveAll(oldDir)
	if err := os.Rename(snapDir, oldDir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("rqlite: move stale snapshots: %w", err)
	}
	if err := os.MkdirAll(snapDir, 0o755); err != nil {
		return fmt.Errorf("rqlite: mkdir snapshots: %w", err)
	}
	snapStore, err := snapshot.NewStore(snapDir)
	if err != nil {
		return fmt.Errorf("rqlite: snapshot store: %w", err)
	}
	defer snapStore.Close()

	// The db file must be self-contained (WAL merged) so the snapshot
	// streamer captures everything.
	if s.direct != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := s.direct.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
			return fmt.Errorf("rqlite: checkpoint: %w", err)
		}
	}

	streamer, err := snapshot.NewSnapshotStreamer(s.dbPath)
	if err != nil {
		return fmt.Errorf("rqlite: snapshot streamer: %w", err)
	}
	defer streamer.Close()
	if err := streamer.Open(); err != nil {
		return fmt.Errorf("rqlite: open streamer: %w", err)
	}

	conf, err := raft.ReadConfigJSON(s.peersInfoPath())
	if err != nil {
		return fmt.Errorf("rqlite: read committed config: %w", err)
	}

	idx, err := s.store.CommitIndex()
	if err != nil {
		return fmt.Errorf("rqlite: commit index: %w", err)
	}
	if idx == 0 {
		idx = 1
	}
	term := idx
	sink, err := snapStore.Create(1, idx, term, conf, 1, nil)
	if err != nil {
		return fmt.Errorf("rqlite: create snapshot: %w", err)
	}
	if err := snapshot.NewStateReader(streamer).Persist(sink); err != nil {
		_ = sink.Cancel()
		return fmt.Errorf("rqlite: persist snapshot: %w", err)
	}
	if err := sink.Close(); err != nil {
		return fmt.Errorf("rqlite: close snapshot: %w", err)
	}

	// Fingerprint so the next boot preserves the db (fast path).
	if err := s.writeFingerprint(); err != nil {
		return err
	}
	logf("rqlite: rebuilt committed state from db file (snapshot index %d, term %d)", idx, term)
	return nil
}

// snapshotStoreDir returns the store's snapshot directory.
func (s *EmbeddedStore) snapshotStoreDir() string {
	return filepath.Join(s.dir, "wsnapshots")
}

// peersInfoPath returns the committed raft configuration file.
func (s *EmbeddedStore) peersInfoPath() string {
	return filepath.Join(s.dir, "raft", "peers.info")
}

// writeFingerprint writes <dir>/clean_snapshot in the exact format rqlite
// uses (store.FileFingerprint: mod_time/size/crc32, CRC = Castagnoli).
func (s *EmbeddedStore) writeFingerprint() error {
	fi, err := os.Stat(s.dbPath)
	if err != nil {
		return fmt.Errorf("rqlite: stat db: %w", err)
	}
	sum, err := crc32Castagnoli(s.dbPath)
	if err != nil {
		return fmt.Errorf("rqlite: crc32: %w", err)
	}
	fp := struct {
		ModTime time.Time `json:"mod_time"`
		Size    int64     `json:"size"`
		CRC32   uint32    `json:"crc32,omitempty"`
	}{fi.ModTime(), fi.Size(), sum}
	b, err := json.MarshalIndent(fp, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.dir, "clean_snapshot"), b, 0o644)
}

// DataDir returns the store's data directory.
func (s *EmbeddedStore) DataDir() string {
	return s.dir
}

// DBPath returns the path of the underlying SQLite file
// (<dir>/db.sqlite), suitable for a logical dump/restore.
func (s *EmbeddedStore) DBPath() string {
	return s.dbPath
}

// SnapshotNow forces a raft snapshot of the current database state.
// Best-effort: "nothing new to snapshot" is not an error.
// RestoreFromLatestSnapshot restores the store's database from the latest
// snapshot in the snapshot store. It is used when the raft restore-on-start
// did not produce a usable db (e.g. the clean-snapshot fast path, or an
// empty/old snapshot). The restore goes through rqlite's snapshot.Restore,
// which extracts the DB file (+WAL) from the protobuf-framed snapshot stream,
// then the file is swapped into the store's db via rqlite's SwappableDB.Swap.
func (s *EmbeddedStore) RestoreFromLatestSnapshot() error {
	if has, err := s.hasUserTables(); err == nil && has {
		return nil // db already has data
	}
	snapDir := filepath.Join(s.dir, "wsnapshots")
	snapStore, err := snapshot.NewStore(snapDir)
	if err != nil {
		return fmt.Errorf("open snapshot store: %w", err)
	}
	defer snapStore.Close()
	snaps, err := snapStore.List()
	if err != nil {
		return fmt.Errorf("list snapshots: %w", err)
	}
	if len(snaps) == 0 {
		return nil
	}
	var latest *raft.SnapshotMeta
	for _, sp := range snaps {
		if latest == nil || sp.Index > latest.Index {
			latest = sp
		}
	}
	_, rc, err := snapStore.Open(latest.ID)
	if err != nil {
		return fmt.Errorf("open snapshot %s: %w", latest.ID, err)
	}
	defer rc.Close()
	tmp, err := os.CreateTemp(s.dir, "oneapi-restore-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)
	if _, err := snapshot.Restore(rc, tmpPath); err != nil {
		return fmt.Errorf("restore snapshot: %w", err)
	}
	// Swap the restored db into place: the store's own connection pool is
	// still open on the old file, but we are in a pre-traffic state (no
	// queries issued yet on this boot), so we replace the file and let the
	// store's pool pick it up on next use. To be safe we also re-open our
	// direct handle.
	if err := os.Rename(tmpPath, s.dbPath); err != nil {
		return fmt.Errorf("swap db file: %w", err)
	}
	if s.direct != nil {
		_ = s.direct.Close()
		var err2 error
		s.direct, err2 = sql.Open("rqlite-sqlite3", s.dbPath)
		if err2 != nil {
			return fmt.Errorf("reopen direct: %w", err2)
		}
		s.direct.SetMaxOpenConns(1)
		s.direct.SetConnMaxLifetime(0)
	}
	logf("rqlite: restored db from snapshot %s", latest.ID)
	return nil
}

func (s *EmbeddedStore) hasUserTables() (bool, error) {
	if s.direct == nil {
		return false, nil
	}
	var n int
	err := s.direct.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// EnsureFullSnapshotMarker makes the store's next snapshot a FULL
// (DB-inclusive) one: it ticks the db file mtime (so rqlite's
// dbModified() is true) and writes the FULL_NEEDED marker, forcing the
// next snapshot to be a full checkpoint rather than a WAL-only
// incremental. SnapshotNow also checkpoints the WAL directly.
// Call before SnapshotNow.
func (s *EmbeddedStore) EnsureFullSnapshotMarker() {
	tick := func(path string) {
		if _, err := os.Stat(path); err == nil {
			fut := time.Now().Add(2 * time.Second)
			_ = os.Chtimes(path, fut, fut)
		}
	}
	tick(s.dbPath)
	tick(s.dbPath + "-wal")
	tick(s.dbPath + "-shm")
	if s.fullNeededPath != "" {
		if err := os.MkdirAll(filepath.Dir(s.fullNeededPath), 0o755); err == nil {
			_ = os.WriteFile(s.fullNeededPath, []byte{}, 0o644)
		}
	}
}

// checkpointDirect merges the WAL into db.sqlite and vacuums, via our
// direct handle. The store's own connection pool may not have flushed the
// WAL, so we do it explicitly before any snapshot.
func (s *EmbeddedStore) checkpointDirect() {
	if s.direct == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := s.direct.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		logf("rqlite: checkpoint: %v", err)
	}
	if _, err := s.direct.ExecContext(ctx, "VACUUM"); err != nil {
		logf("rqlite: vacuum: %v", err)
	}
}

func (s *EmbeddedStore) SnapshotNow() error {
	// Ensure all data is in db.sqlite (not just the WAL) so the snapshot
	// captures it.
	s.checkpointDirect()
	if err := s.store.Snapshot(0); err != nil &&
		err != store.ErrNothingNewToSnapshot &&
		err != store.ErrNoWALToSnapshot {
		return err
	}
	return nil
}

// Close shuts the store down (raft shutdown, snapshot, sqlite close) and
// releases the loopback listener.
func (s *EmbeddedStore) Close() error {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	releaseCurrentStore(s)
	var firstErr error
	// Re-assert the FULL_NEEDED marker so the snapshot-on-close produces
	// a full (DB-inclusive) snapshot, restorable by node recovery.
	if s.fullNeededPath != "" {
		if err := os.MkdirAll(filepath.Dir(s.fullNeededPath), 0o755); err == nil {
			_ = os.WriteFile(s.fullNeededPath, []byte{}, 0o644)
		}
	}
	if s.direct != nil {
		_ = s.direct.Close()
		s.direct = nil
	}
	if err := s.store.Close(true); err != nil && firstErr == nil {
		firstErr = err
	}
	if s.ly != nil {
		_ = s.ly.Close()
	}
	if s.mux != nil {
		if err := s.mux.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if s.ln != nil {
		_ = s.ln.Close()
	}
	return firstErr
}
