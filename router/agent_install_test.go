package router

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/common/config"
)

// setupAgentInstallTest creates a gin engine with ONLY the agent install
// router wired up so each test is hermetic and does not depend on the rest
// of one-api's middleware stack.
func setupAgentInstallTest(t *testing.T, dir string) *gin.Engine {
	t.Helper()
	// Save and restore config so we don't poison other tests.
	oldDir := config.AgentInstallDir
	config.AgentInstallDir = dir
	t.Cleanup(func() { config.AgentInstallDir = oldDir })

	gin.SetMode(gin.TestMode)
	r := gin.New()
	if err := SetAgentInstallRouter(r); err != nil {
		t.Fatalf("SetAgentInstallRouter: %v", err)
	}
	return r
}

func TestAgentInstall_ServesExistingFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "install.sh"), []byte("#!/bin/bash\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "MANIFEST"), []byte("test manifest\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := setupAgentInstallTest(t, dir)

	for _, tc := range []struct{ path, want string }{
		{"/agent-install/install.sh", "#!/bin/bash\necho hi\n"},
		{"/agent-install/MANIFEST", "test manifest\n"},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", tc.path, w.Code)
		}
		body, _ := io.ReadAll(w.Body)
		if string(body) != tc.want {
			t.Errorf("%s: body = %q, want %q", tc.path, string(body), tc.want)
		}
	}
}

func TestAgentInstall_MissingFileReturns404(t *testing.T) {
	dir := t.TempDir()
	r := setupAgentInstallTest(t, dir)

	// /agent-install/nonexistent.sh should be handled by the static handler
	// returning 404 (gin-contrib/static's Exists() returns false -> NoRoute).
	req := httptest.NewRequest(http.MethodGet, "/agent-install/nonexistent.sh", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("missing file: status = %d, want 404, body=%q", w.Code, w.Body.String())
	}
}

func TestAgentInstall_DisabledDirIsNotRegistered(t *testing.T) {
	oldDir := config.AgentInstallDir
	config.AgentInstallDir = ""
	t.Cleanup(func() { config.AgentInstallDir = oldDir })

	gin.SetMode(gin.TestMode)
	r := gin.New()
	if err := SetAgentInstallRouter(r); err != nil {
		t.Fatalf("SetAgentInstallRouter: %v", err)
	}

	// With AgentInstallDir empty, /agent-install/* should NOT have any
	// matching route. gin returns 404 for unrouted paths.
	req := httptest.NewRequest(http.MethodGet, "/agent-install/install.sh", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("disabled: status = %d, want 404", w.Code)
	}
}

func TestAgentInstall_NonexistentDirSkipsRegistration(t *testing.T) {
	oldDir := config.AgentInstallDir
	config.AgentInstallDir = "/this/path/does/not/exist/anywhere"
	t.Cleanup(func() { config.AgentInstallDir = oldDir })

	gin.SetMode(gin.TestMode)
	r := gin.New()
	if err := SetAgentInstallRouter(r); err != nil {
		t.Fatalf("SetAgentInstallRouter: %v", err)
	}

	// Non-existent dir should be logged-and-skipped, not panic.
	req := httptest.NewRequest(http.MethodGet, "/agent-install/install.sh", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("missing dir: status = %d, want 404", w.Code)
	}
}

func TestAgentInstall_PathTraversalBlocked(t *testing.T) {
	dir := t.TempDir()
	// Place a "secret" file OUTSIDE the agent-install dir.
	secret := filepath.Join(filepath.Dir(dir), "secret.txt")
	if err := os.WriteFile(secret, []byte("SENSITIVE"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(secret) })

	r := setupAgentInstallTest(t, dir)

	// Try to escape via ../. The static handler should refuse.
	for _, p := range []string{
		"/agent-install/../secret.txt",
		"/agent-install/%2e%2e/secret.txt",
	} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		body, _ := io.ReadAll(w.Body)
		if strings.Contains(string(body), "SENSITIVE") {
			t.Errorf("path traversal succeeded for %q: body=%q", p, body)
		}
	}
}
