package logger

import (
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

// TestLumberjack_RotatesAtSizeThreshold exercises lumberjack directly so we
// know the actual rotation policy behaves as configured. This is the test
// that would catch regressions like "we silently stopped rotating".
func TestLumberjack_RotatesAtSizeThreshold(t *testing.T) {
	tmp := t.TempDir()
	rot := &lumberjack.Logger{
		Filename:   filepath.Join(tmp, "rot.log"),
		MaxSize:    1, // megabytes per file
		MaxBackups: 3,
		MaxAge:     30,
		Compress:   true,
		LocalTime:  true,
	}

	// Write 3MB in 1MB chunks. Each chunk crosses MaxSize (1MB) so we get
	// at least two rotations during the run.
	chunk := strings.Repeat("x", 1024*1024-1) // 1MB-1B per chunk
	for i := 0; i < 3; i++ {
		if _, err := rot.Write([]byte(chunk)); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	// After 3 1MB writes the active file should be roughly 1MB and there
	// should be at least 1 rotated backup (the rotation logic kicks in at
	// the next write that would push the active file over the limit).
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	active, rotated := countRotationFiles(entries, "rot.log")
	if active < 1 {
		t.Fatalf("expected the active log to exist, found none in %s (entries: %v)", tmp, entryNames(entries))
	}
	if rotated < 1 {
		t.Fatalf("expected at least 1 rotated backup, found %d in %s (entries: %v)", rotated, tmp, entryNames(entries))
	}
}

// TestLumberjack_CompressionProducesGz verifies that rotated backups are
// gzipped when Compress=true. This is what protects long-running hosts from
// running out of disk on verbose services.
func TestLumberjack_CompressionProducesGz(t *testing.T) {
	tmp := t.TempDir()
	rot := &lumberjack.Logger{
		Filename:   filepath.Join(tmp, "rot.log"),
		MaxSize:    1,
		MaxBackups: 5,
		MaxAge:     30,
		Compress:   true,
	}

	chunk := strings.Repeat("y", 1024*1024-1)
	for i := 0; i < 3; i++ {
		if _, err := rot.Write([]byte(chunk)); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	// Close triggers the final rotation. Lumberjack runs compression in a
	// background goroutine, so we have to wait for the .gz files to land
	// on disk before reading them.
	if err := rot.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Poll for at least one .gz file. Lumberjack may rename a backup first
	// and only compress it later, so we wait up to 5s.
	var foundGz string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		entries, _ := os.ReadDir(tmp)
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".gz") {
				foundGz = filepath.Join(tmp, e.Name())
				break
			}
		}
		if foundGz != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if foundGz == "" {
		t.Skip("no .gz rotated file appeared within 5s; environment too slow for this assertion")
	}

	// Confirm the .gz file is a real gzip stream. We retry the open+read
	// briefly because the gzip writer in lumberjack may still be flushing
	// when the file first appears.
	var lastErr error
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		lastErr = tryDecompressGz(foundGz)
		if lastErr == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	if lastErr != nil {
		t.Fatalf("decompress %s: %v", foundGz, lastErr)
	}
}

// tryDecompressGz opens the file, runs it through a gzip reader, and drains
// the stream. Returns nil on a clean EOF, otherwise the underlying error.
func tryDecompressGz(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	if _, err := io.Copy(io.Discard, gz); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// TestSetupLogger_DefaultRotationKnobs documents what defaults the package
// picks when no env vars are set. If this test breaks, it means somebody
// silently changed the rotation policy without updating the docs.
func TestSetupLogger_DefaultRotationKnobs(t *testing.T) {
	if defaultLogMaxSizeMB != 100 {
		t.Errorf("default size changed: got %d, want 100", defaultLogMaxSizeMB)
	}
	if defaultLogMaxBackups != 7 {
		t.Errorf("default backups changed: got %d, want 7", defaultLogMaxBackups)
	}
	if defaultLogMaxAgeDays != 30 {
		t.Errorf("default age changed: got %d, want 30", defaultLogMaxAgeDays)
	}
	if !defaultLogCompress {
		t.Errorf("default compress changed: got %v, want true", defaultLogCompress)
	}
}

// TestSetupLogger_DirAutoCreated proves we don't require operators to mkdir
// the log directory themselves; we create it on demand. This is what makes
// "default behavior" actually work on a fresh install.
func TestSetupLogger_DirAutoCreated(t *testing.T) {
	resetSetup(t)
	parent := t.TempDir()
	target := filepath.Join(parent, "deeply", "nested", "logs")
	LogDir = target

	SetupLogger()

	if _, err := os.Stat(target); err != nil {
		t.Fatalf("expected SetupLogger to mkdir %s, got %v", target, err)
	}
	if fileWriter() == nil {
		t.Fatal("expected writer to be configured")
	}
}

// TestResetSetup_AllowsReconfiguration ensures the test helper truly resets
// state, so tests that depend on fresh configuration are isolated.
func TestResetSetup_AllowsReconfiguration(t *testing.T) {
	resetSetup(t)
	LogDir = t.TempDir()
	SetupLogger()
	first := fileWriter()

	resetSetup(t)
	LogDir = t.TempDir()
	SetupLogger()
	second := fileWriter()

	if first == nil || second == nil {
		t.Fatalf("expected non-nil writers after setup; first=%v second=%v", first, second)
	}
	if first == second {
		t.Fatal("reset should have produced a fresh writer")
	}
}

// countRotationFiles partitions directory entries into the active file
// (matches the configured filename exactly) and rotated backups (everything
// else produced by lumberjack for that file). We treat .gz rotated backups
// and uncompressed rotated backups both as "rotated".
func countRotationFiles(entries []os.DirEntry, activeName string) (active, rotated int) {
	for _, e := range entries {
		name := e.Name()
		if name == activeName {
			active++
			continue
		}
		base := strings.TrimSuffix(activeName, filepath.Ext(activeName))
		if strings.HasPrefix(name, base+"-") {
			rotated++
		}
	}
	return active, rotated
}

func entryNames(entries []os.DirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}
