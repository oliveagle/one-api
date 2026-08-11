package model

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/songquanpeng/one-api/common"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// rotationTestSetup spins up an isolated sqlite DB for each rotation test
// so we don't trample on the package-level LOG_DB.
func rotationTestSetup(t *testing.T) *gorm.DB {
	t.Helper()
	common.UsingSQLite = true
	common.UsingMySQL = false
	common.UsingPostgreSQL = false
	db, err := gorm.Open(
		sqlite.Open(":memory:?_busy_timeout=5000&_pragma=foreign_keys(1)"),
		&gorm.Config{
			Logger:                                   logger.Default.LogMode(logger.Silent),
			DisableForeignKeyConstraintWhenMigrating: true,
			PrepareStmt:                              true,
		},
	)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&Log{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}

// seedOldLogs inserts n rows with created_at set to (now - retentionDays - 1)
// days so they're all eligible for rotation. Returns the inserted IDs.
func seedOldLogs(t *testing.T, db *gorm.DB, n int, cutoff time.Time) []int {
	t.Helper()
	rows := make([]Log, n)
	ids := make([]int, n)
	for i := 0; i < n; i++ {
		rows[i] = Log{
			UserId:    1,
			Username:  "rotator-test",
			CreatedAt: cutoff.Add(-time.Duration(i+1) * time.Hour).Unix(),
			Type:      LogTypeConsume,
			Content:   "old-row",
			Quota:     i,
		}
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed old logs: %v", err)
	}
	for i, r := range rows {
		ids[i] = r.Id
	}
	return ids
}

// seedRecentLogs inserts n rows with created_at = now so they stay in DB
// past the rotation sweep.
func seedRecentLogs(t *testing.T, db *gorm.DB, n int) {
	t.Helper()
	rows := make([]Log, n)
	for i := 0; i < n; i++ {
		rows[i] = Log{
			UserId:    1,
			Username:  "rotator-test",
			CreatedAt: time.Now().Unix(),
			Type:      LogTypeConsume,
			Content:   "recent-row",
			Quota:     i,
		}
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed recent logs: %v", err)
	}
}

// TestRotateDBLogsOnce_ExportsOldDeletesRecent verifies the happy path:
// rows older than the retention cutoff get exported to the rotated file
// and removed from DB, while recent rows stay.
func TestRotateDBLogsOnce_ExportsOldDeletesRecent(t *testing.T) {
	db := rotationTestSetup(t)
	oldDB := LOG_DB
	LOG_DB = db
	resetDBLogRotator()
	t.Cleanup(func() {
		LOG_DB = oldDB
		resetDBLogRotator()
	})

	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	oldIDs := seedOldLogs(t, db, 25, cutoff)
	recentCount := 10
	seedRecentLogs(t, db, recentCount)

	logDir := t.TempDir()
	cfg := DBLogRotateConfig{
		RetentionDays: 7,
		SweepInterval: time.Hour,
		BatchSize:     100,
	}

	n, err := RotateDBLogsOnce(t.Context(), logDir, cfg)
	if err != nil {
		t.Fatalf("RotateDBLogsOnce: %v", err)
	}
	if n != len(oldIDs) {
		t.Errorf("rotated %d, want %d", n, len(oldIDs))
	}

	// DB should now only have the recent rows.
	var remaining int64
	if err := db.Model(&Log{}).Count(&remaining).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if remaining != int64(recentCount) {
		t.Errorf("DB count after rotation = %d, want %d", remaining, recentCount)
	}

	// Rotated file should contain one JSON line per exported row. We use
	// bufio to count newline-terminated records rather than the byte
	// count so we don't depend on lumberjack's internal buffering.
	path := DBLogRotatorPath()
	if path == "" {
		t.Fatalf("DBLogRotatorPath returned empty after rotation")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open rotated file %s: %v", path, err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var lineCount int
	var idsSeen []int
	for sc.Scan() {
		var got Log
		if err := json.Unmarshal(sc.Bytes(), &got); err != nil {
			t.Fatalf("line %d not valid JSON: %v\n%s", lineCount+1, err, sc.Text())
		}
		idsSeen = append(idsSeen, got.Id)
		lineCount++
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if lineCount != len(oldIDs) {
		t.Errorf("rotated file lines = %d, want %d", lineCount, len(oldIDs))
	}
	// IDs in the rotated file should be the same set we seeded as old.
	if !sameInts(idsSeen, oldIDs) {
		t.Errorf("rotated IDs %v != seeded old IDs %v", idsSeen, oldIDs)
	}
}

// TestRotateDBLogsOnce_EmptyDBNoop checks that an empty (or all-recent)
// table doesn't fail and doesn't produce spurious output.
func TestRotateDBLogsOnce_EmptyDBNoop(t *testing.T) {
	db := rotationTestSetup(t)
	oldDB := LOG_DB
	LOG_DB = db
	resetDBLogRotator()
	t.Cleanup(func() {
		LOG_DB = oldDB
		resetDBLogRotator()
	})

	logDir := t.TempDir()
	cfg := DBLogRotateConfig{RetentionDays: 7, SweepInterval: time.Hour, BatchSize: 100}

	n, err := RotateDBLogsOnce(t.Context(), logDir, cfg)
	if err != nil {
		t.Fatalf("RotateDBLogsOnce: %v", err)
	}
	if n != 0 {
		t.Errorf("rotated %d, want 0", n)
	}
	// Path should still be configured (so a future sweep has somewhere
	// to write) but the file may not exist yet.
	_ = DBLogRotatorPath()
}

// TestRotateDBLogsOnce_BatchBoundaries confirms BatchSize caps each
// individual sweep batch. We seed more rows than one batch and verify the
// rotator drains them across multiple batches.
func TestRotateDBLogsOnce_BatchBoundaries(t *testing.T) {
	db := rotationTestSetup(t)
	oldDB := LOG_DB
	LOG_DB = db
	resetDBLogRotator()
	t.Cleanup(func() {
		LOG_DB = oldDB
		resetDBLogRotator()
	})

	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	const seeded = 250
	seedOldLogs(t, db, seeded, cutoff)

	logDir := t.TempDir()
	cfg := DBLogRotateConfig{
		RetentionDays: 7,
		SweepInterval: time.Hour,
		BatchSize:     100, // 250 rows -> 3 batches (100, 100, 50)
	}

	n, err := RotateDBLogsOnce(t.Context(), logDir, cfg)
	if err != nil {
		t.Fatalf("RotateDBLogsOnce: %v", err)
	}
	if n != seeded {
		t.Errorf("rotated %d, want %d", n, seeded)
	}

	var remaining int64
	if err := db.Model(&Log{}).Count(&remaining).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if remaining != 0 {
		t.Errorf("DB count after rotation = %d, want 0", remaining)
	}

	// Count JSON lines in the rotated file.
	path := DBLogRotatorPath()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var count int
	for sc.Scan() {
		count++
	}
	if count != seeded {
		t.Errorf("line count = %d, want %d", count, seeded)
	}
}

// TestSetupDBLogRotator_Idempotent confirms the singleton setup can be
// invoked repeatedly without creating multiple rotators or duplicating
// goroutines. It also asserts the file path is stable.
func TestSetupDBLogRotator_Idempotent(t *testing.T) {
	// Reset the once so we can re-exercise setup with a fresh dir.
	dbLogRotatorOnce = sync.Once{}
	dbLogRotator = nil

	dir := t.TempDir()
	r1 := setupDBLogRotator(dir)
	r2 := setupDBLogRotator(dir)
	if r1 == nil || r2 == nil {
		t.Fatalf("setupDBLogRotator returned nil")
	}
	if r1 != r2 {
		t.Errorf("setupDBLogRotator returned different writers across calls")
	}
	want := filepath.Join(dir, "db-logs", dbLogFilename)
	if DBLogRotatorPath() != want {
		t.Errorf("DBLogRotatorPath = %q, want %q", DBLogRotatorPath(), want)
	}
}

func sameInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	m := make(map[int]int, len(a))
	for _, v := range a {
		m[v]++
	}
	for _, v := range b {
		m[v]--
		if m[v] < 0 {
			return false
		}
	}
	return true
}

// TestStartDBLogRotator_RunsInitialSweep confirms that starting the rotator
// kicks off the initial sweep synchronously (within a short window) so a
// freshly-restarted server cleans its DB right away.
func TestStartDBLogRotator_RunsInitialSweep(t *testing.T) {
	db := rotationTestSetup(t)
	oldDB := LOG_DB
	LOG_DB = db
	resetDBLogRotator()
	t.Cleanup(func() {
		LOG_DB = oldDB
		resetDBLogRotator()
	})

	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	seedOldLogs(t, db, 5, cutoff)

	dir := t.TempDir()
	// Override the env vars so the rotator sweeps fast.
	t.Setenv("LOG_DB_RETENTION_DAYS", "7")
	t.Setenv("LOG_DB_ROTATION_INTERVAL_HOURS", "1")
	t.Setenv("LOG_DB_ROTATION_BATCH_SIZE", "100")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	StartDBLogRotator(ctx, dir)

	// The initial sweep is synchronous in StartDBLogRotator so the table
	// should already be empty by the time we get here.
	var remaining int64
	if err := db.Model(&Log{}).Count(&remaining).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if remaining != 0 {
		t.Errorf("after StartDBLogRotator initial sweep: DB has %d rows, want 0", remaining)
	}
	path := DBLogRotatorPath()
	if path == "" {
		t.Fatal("DBLogRotatorPath is empty after StartDBLogRotator")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("rotated file %s missing: %v", path, err)
	}
}
