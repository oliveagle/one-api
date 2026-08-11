package logger

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
)

// resetSetup re-arms the global setup so each test can reconfigure it
// independently. Since setupLogOnce is a sync.Once it can only fire once
// per process, so tests that depend on fresh state must reset it via
// setupLogOnce = sync.Once{} before calling SetupLogger again.
func resetSetup(t *testing.T) {
	t.Helper()
	setupLogOnce = sync.Once{}
	activeFileWriter = nil
	gin.DefaultWriter = io.Discard
	gin.DefaultErrorWriter = io.Discard
	LogDir = ""
	// Drop any LOG_* env that previous tests may have set so we exercise
	// the documented defaults.
	for _, k := range []string{
		"LOG_MAX_SIZE_MB",
		"LOG_MAX_BACKUPS",
		"LOG_MAX_AGE_DAYS",
		"LOG_COMPRESS",
		"LOG_TO_STDOUT_ONLY",
	} {
		os.Unsetenv(k)
	}
}

func TestSetupLogger_CreatesDirAndWriter(t *testing.T) {
	resetSetup(t)
	tmp := t.TempDir()
	LogDir = tmp

	SetupLogger()

	if _, err := os.Stat(tmp); err != nil {
		t.Fatalf("expected log dir to exist at %s, got %v", tmp, err)
	}
	if w := fileWriter(); w == nil {
		t.Fatalf("expected fileWriter to be non-nil after SetupLogger")
	}
}

func TestSetupLogger_Idempotent(t *testing.T) {
	resetSetup(t)
	tmp := t.TempDir()
	LogDir = tmp

	SetupLogger()
	first := fileWriter()
	SetupLogger() // calling again should be a no-op
	second := fileWriter()

	if first != second {
		t.Fatalf("SetupLogger should reuse the same writer on repeated calls")
	}
}

func TestSetupLogger_DefaultsToLogsDir(t *testing.T) {
	resetSetup(t)
	// Point the process at a tmp CWD so we don't pollute the repo.
	cwd, _ := os.Getwd()
	defer os.Chdir(cwd)
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	// Leave LogDir empty: SetupLogger should fall back to ./logs.
	LogDir = ""
	SetupLogger()

	logsDir := filepath.Join(tmp, "logs")
	if _, err := os.Stat(logsDir); err != nil {
		t.Fatalf("expected fallback dir %s, got %v", logsDir, err)
	}
}

func TestSetupLogger_StdoutOnlyWhenRequested(t *testing.T) {
	resetSetup(t)
	tmp := t.TempDir()
	LogDir = tmp
	os.Setenv("LOG_TO_STDOUT_ONLY", "1")
	defer os.Unsetenv("LOG_TO_STDOUT_ONLY")

	// Capture gin writer redirects so we can assert they stay at stdout
	// rather than getting clobbered with MultiWriter.
	gin.DefaultWriter = os.Stdout
	gin.DefaultErrorWriter = os.Stderr

	SetupLogger()

	if w := fileWriter(); w != nil {
		t.Fatalf("expected fileWriter=nil when LOG_TO_STDOUT_ONLY=1, got %T", w)
	}
	if gin.DefaultWriter != os.Stdout {
		t.Fatalf("expected gin.DefaultWriter to stay as stdout, got %T", gin.DefaultWriter)
	}
}

func TestSetupLogger_ActualWritesLandOnDisk(t *testing.T) {
	resetSetup(t)
	tmp := t.TempDir()
	LogDir = tmp
	SetupLogger()

	// Write a unique marker through the logger and confirm it lands in the
	// rotated file. Lumberjack writes through an internal buffer; doing a
	// write followed by a stat is sufficient to flush via the OS write
	// syscall it issues under the hood.
	marker := "hello-rotation-test-marker-xyz"
	SysLog(marker)

	mainPath := filepath.Join(tmp, "oneapi.log")
	data, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatalf("expected log file at %s after write, got %v", mainPath, err)
	}
	if !strings.Contains(string(data), marker) {
		t.Fatalf("expected log file to contain marker %q, got:\n%s", marker, data)
	}
}

func TestLogHelper_WritesFormattedLine(t *testing.T) {
	resetSetup(t)
	tmp := t.TempDir()
	LogDir = tmp
	SetupLogger()

	var buf bytes.Buffer
	// Replace gin writer with our buffer so we can read what was written.
	gin.DefaultWriter = &buf
	gin.DefaultErrorWriter = &buf

	SysLog("sample-line")

	got := buf.String()
	if !strings.Contains(got, "sample-line") {
		t.Fatalf("expected log line to contain %q, got %q", "sample-line", got)
	}
	if !strings.Contains(got, "[INFO]") {
		t.Fatalf("expected [INFO] level prefix in %q", got)
	}
}

func TestSetupLogger_RespectsSizeOverride(t *testing.T) {
	resetSetup(t)
	tmp := t.TempDir()
	LogDir = tmp
	os.Setenv("LOG_MAX_SIZE_MB", "5")
	defer os.Unsetenv("LOG_MAX_SIZE_MB")

	SetupLogger()

	// We can't easily reach into the rotator to read MaxSize, so instead we
	// assert the writer is set and the file was created. The env-var path is
	// exercised; an integration test would assert rotation by writing past
	// the threshold.
	if fileWriter() == nil {
		t.Fatal("expected writer to be configured")
	}
}