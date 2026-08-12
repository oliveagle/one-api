package model

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
	"gorm.io/gorm"

	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/common/env"
	"github.com/songquanpeng/one-api/common/logger"
)

// Default DB-log retention / sweep parameters. We tune these so a default
// install has zero config to do the right thing: anything older than 30 days
// is rotated out of the logs table into a dated file in <log-dir>/db-logs/,
// and the sweep runs every 6 hours. Operators can override via env vars.
const (
	defaultDBLogRetentionDays    = 30
	defaultDBLogSweepIntervalHrs = 6
	defaultDBLogBatchSize        = 1000
	// dbLogFilename is the rotated file stem. lumberjack will append "-<ts>"
	// and ".gz" on rotation, so the active file is always exactly this name.
	dbLogFilename = "db-logs.log"
)

var (
	dbLogRotatorOnce sync.Once
	dbLogRotator     *lumberjack.Logger
	// dbLogRotatorMtx guards concurrent writes to the rotated file. The
	// rotator itself is goroutine-safe but we want to bound concurrent
	// sweeps: only one sweep at a time across the process.
	dbLogRotatorMtx  sync.Mutex
	dbLogRotatorPath string
)

// setupDBLogRotator wires up the lumberjack rotator used for exporting DB
// logs. The active file lives at <log-dir>/db-logs/db-logs.log and inherits
// the same size/count/age policy as the main log, with a sensible default
// of 100MB per file, 30 days age, 30 backups.
func setupDBLogRotator(logDir string) *lumberjack.Logger {
	dbLogRotatorOnce.Do(func() {
		dir := filepath.Join(logDir, "db-logs")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			logger.SysError("failed to create db-logs dir " + dir + ": " + err.Error())
			return
		}
		dbLogRotatorPath = filepath.Join(dir, dbLogFilename)
		dbLogRotator = &lumberjack.Logger{
			Filename:   dbLogRotatorPath,
			MaxSize:    env.Int("LOG_MAX_SIZE_MB", 100),
			MaxBackups: env.Int("LOG_MAX_BACKUPS", 7),
			MaxAge:     env.Int("LOG_MAX_AGE_DAYS", 30),
			Compress:   env.Bool("LOG_COMPRESS", true),
			LocalTime:  true,
		}
	})
	return dbLogRotator
}

// DBLogRotateConfig controls the rotator's behavior. The zero value is a
// valid configuration that runs with the package defaults.
type DBLogRotateConfig struct {
	// RetentionDays is the age cutoff. Anything older is rotated.
	RetentionDays int
	// SweepInterval is how often the rotator wakes up to do work.
	SweepInterval time.Duration
	// BatchSize caps how many rows are exported per DELETE batch. This
	// bounds the size of each write transaction so a giant table doesn't
	// lock the DB for minutes.
	BatchSize int
}

// resolveDBLogRotateConfig reads the config from env vars, falling back to
// the package defaults when env vars are unset or malformed.
func resolveDBLogRotateConfig() DBLogRotateConfig {
	retention := env.Int("LOG_DB_RETENTION_DAYS", defaultDBLogRetentionDays)
	if retention < 1 {
		retention = defaultDBLogRetentionDays
	}
	intervalHrs := env.Int("LOG_DB_ROTATION_INTERVAL_HOURS", defaultDBLogSweepIntervalHrs)
	if intervalHrs < 1 {
		intervalHrs = defaultDBLogSweepIntervalHrs
	}
	batch := env.Int("LOG_DB_ROTATION_BATCH_SIZE", defaultDBLogBatchSize)
	if batch < 1 {
		batch = defaultDBLogBatchSize
	}
	return DBLogRotateConfig{
		RetentionDays: retention,
		SweepInterval: time.Duration(intervalHrs) * time.Hour,
		BatchSize:     batch,
	}
}

// RotateDBLogsOnce runs a single sweep: find rows older than the retention
// cutoff, append them to the rotated file as NDJSON, and DELETE them. It is
// safe to call concurrently; only one sweep runs at a time per process.
//
// We use the master/slave flag to avoid stepping on replica nodes. Slaves
// can still run it (it's read+write on LOG_DB which is per-node anyway), but
// logging loudly if it does is helpful for diagnostics.
func RotateDBLogsOnce(ctx context.Context, logDir string, cfg DBLogRotateConfig) (rotated int, err error) {
	if LOG_DB == nil {
		return 0, fmt.Errorf("LOG_DB not initialized")
	}
	rotator := setupDBLogRotator(logDir)
	if rotator == nil {
		return 0, fmt.Errorf("db-logs rotator not available")
	}
	dbLogRotatorMtx.Lock()
	defer dbLogRotatorMtx.Unlock()

	cutoff := time.Now().Add(-time.Duration(cfg.RetentionDays) * 24 * time.Hour).Unix()

	total := 0
	for {
		if ctx.Err() != nil {
			return total, ctx.Err()
		}
		n, err := rotateBatch(LOG_DB, rotator, cutoff, cfg.BatchSize)
		total += n
		if err != nil {
			return total, err
		}
		if n < cfg.BatchSize {
			break
		}
	}
	return total, nil
}

// rotateBatch exports up to `limit` rows older than `cutoff` to the rotator
// and DELETEs them on success. Returns the number of rows actually rotated.
func rotateBatch(db *gorm.DB, rotator *lumberjack.Logger, cutoff int64, limit int) (int, error) {
	rows, err := fetchOldestBatch(db, cutoff, limit)
	if err != nil {
		return 0, fmt.Errorf("fetch old logs: %w", err)
	}
	if len(rows) == 0 {
		return 0, nil
	}
	if err := writeBatchToRotator(rotator, rows); err != nil {
		return 0, fmt.Errorf("write rotated logs: %w", err)
	}
	ids := make([]int, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.Id)
	}
	if err := db.Where("id IN ?", ids).Delete(&Log{}).Error; err != nil {
		// The export succeeded but the cleanup failed; log loudly so an
		// operator can re-run the rotation manually. We intentionally do
		// not retry the export since it's already on disk.
		return 0, fmt.Errorf("delete exported logs: %w", err)
	}
	return len(rows), nil
}

// fetchOldestBatch returns up to `limit` logs older than cutoff ordered by
// id ascending so we drain in chronological order.
func fetchOldestBatch(db *gorm.DB, cutoff int64, limit int) ([]*Log, error) {
	var rows []*Log
	err := db.Where("created_at < ?", cutoff).
		Order("id asc").
		Limit(limit).
		Find(&rows).Error
	return rows, err
}

// writeBatchToRotator appends one NDJSON record per row to the rotator. We
// use a single batched write to minimize the number of filesystem syscalls.
func writeBatchToRotator(rotator *lumberjack.Logger, rows []*Log) error {
	for _, r := range rows {
		buf, err := json.Marshal(r)
		if err != nil {
			// A single row failing to marshal shouldn't abort the whole
			// batch; skip it and log. Operators will see the gap in the
			// exported file but the row stays in DB for retry.
			logger.SysError("db-log rotation: marshal failed for id=" + itoa(r.Id) + ": " + err.Error())
			continue
		}
		if _, err := rotator.Write(append(buf, '\n')); err != nil {
			return err
		}
	}
	return nil
}

// itoa avoids pulling strconv into the hot path; avoids import cycle.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// StartDBLogRotator schedules the periodic sweep on a background goroutine
// and runs an initial sweep on startup so a freshly-restarted server gets
// the DB cleaned immediately. The goroutine exits when ctx is cancelled.
//
// On non-master nodes the rotator still runs (LOG_DB is per-node so there's
// nothing to coordinate), but we log so operators see it's expected.
func StartDBLogRotator(ctx context.Context, logDir string) {
	if !config.IsMasterNode {
		logger.SysLog("db-log rotator: running on non-master node (LOG_DB is local, rotation still applies)")
	}
	cfg := resolveDBLogRotateConfig()
	logger.SysLogf("db-log rotator: retention=%dd sweep=%s batch=%d",
		cfg.RetentionDays, cfg.SweepInterval, cfg.BatchSize)

	// Initial sweep at startup so a restart doesn't accumulate cruft.
	if n, err := RotateDBLogsOnce(ctx, logDir, cfg); err != nil {
		logger.SysError("db-log rotator initial sweep failed: " + err.Error())
	} else if n > 0 {
		logger.SysLogf("db-log rotator: initial sweep rotated %d rows", n)
	}

	go func() {
		ticker := time.NewTicker(cfg.SweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				n, err := RotateDBLogsOnce(ctx, logDir, cfg)
				if err != nil {
					logger.SysError("db-log rotator sweep failed: " + err.Error())
					continue
				}
				if n > 0 {
					logger.SysLogf("db-log rotator: sweep rotated %d rows", n)
				}
			}
		}
	}()
}

// DBLogRotatorPath returns the active rotated file's path. Used by tests
// and the admin endpoint that exposes the rotated log location.
func DBLogRotatorPath() string {
	return dbLogRotatorPath
}

// resetDBLogRotator clears the singleton so the next call to
// setupDBLogRotator (or anything that uses it) writes to a fresh dir.
// Exposed only via the internal test helper package.
func resetDBLogRotator() {
	dbLogRotatorOnce = sync.Once{}
	dbLogRotator = nil
	dbLogRotatorPath = ""
}
