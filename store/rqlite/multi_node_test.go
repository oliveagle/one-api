//go:build cgo

package rqlite

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestJoinCluster_WellFormedRequest pins the join protocol: the request
// body sent to the leader's /join endpoint carries the node's id, raft
// address, and voter flag.
func TestJoinCluster_WellFormedRequest(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/join" {
			t.Errorf("path = %q, want /join", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := joinCluster(srv.Listener.Addr().String(), "test-node", "127.0.0.1:9999"); err != nil {
		t.Fatalf("joinCluster: %v", err)
	}
	if gotBody["id"] != "test-node" {
		t.Errorf("id = %v, want test-node", gotBody["id"])
	}
	if gotBody["address"] != "127.0.0.1:9999" {
		t.Errorf("address = %v, want 127.0.0.1:9999", gotBody["address"])
	}
	if gotBody["voter"] != true {
		t.Errorf("voter = %v, want true", gotBody["voter"])
	}
}

// TestJoinCluster_LeaderRejects pins the error path: a non-200 response
// from the leader surfaces as an error.
func TestJoinCluster_LeaderRejects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("not leader"))
	}))
	defer srv.Close()

	err := joinCluster(srv.Listener.Addr().String(), "test", "127.0.0.1:1")
	if err == nil {
		t.Fatal("expected error when leader returns 403")
	}
}

// TestJoinCluster_UnreachableLeader pins the network error path.
func TestJoinCluster_UnreachableLeader(t *testing.T) {
	// Port 1 is always bound by the system; connecting should fail.
	err := joinCluster("127.0.0.1:1", "test", "127.0.0.1:1")
	if err == nil {
		t.Fatal("expected error when leader is unreachable")
	}
}

// TestMultiNodeDSN pins the DSN parsing for multi-node parameters.
func TestMultiNodeDSN(t *testing.T) {
	opts, err := ParseDSN("rqlite:///data?node_id=ws01&raft_addr=0.0.0.0:15320&join_addr=192.168.2.100:15320")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if opts.NodeID != "ws01" {
		t.Errorf("NodeID = %q, want ws01", opts.NodeID)
	}
	if opts.RaftAddr != "0.0.0.0:15320" {
		t.Errorf("RaftAddr = %q, want 0.0.0.0:15320", opts.RaftAddr)
	}
	if opts.JoinAddr != "192.168.2.100:15320" {
		t.Errorf("JoinAddr = %q, want 192.168.2.100:15320", opts.JoinAddr)
	}
	// No join_addr → single-node
	opts2, _ := ParseDSN("rqlite:///data")
	if opts2.JoinAddr != "" {
		t.Errorf("JoinAddr = %q, want empty for single-node", opts2.JoinAddr)
	}
}

// TestStableRaftAddr_Deterministic pins that the same dir always derives
// the same raft address (required for restart identity persistence).
func TestStableRaftAddr_Deterministic(t *testing.T) {
	dir := "/some/path/rqlite"
	a1 := stableRaftAddr(dir)
	a2 := stableRaftAddr(dir)
	if a1 != a2 {
		t.Fatalf("stableRaftAddr(%q) = %q and %q, want deterministic", dir, a1, a2)
	}
	// Different dirs → different addresses (avoid port conflicts)
	b := stableRaftAddr("/other/path")
	if a1 == b {
		t.Fatalf("different dirs produced same address %q", a1)
	}
}

// ctx helper for tests that need a context.
func testCtx() context.Context {
	ctx, _ := context.WithTimeout(context.Background(), 30*time.Second)
	return ctx
}
