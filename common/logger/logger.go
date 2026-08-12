package logger

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/common/env"
	"github.com/songquanpeng/one-api/common/helper"
	"github.com/songquanpeng/one-api/common/observability"
)

type loggerLevel string

const (
	loggerDEBUG loggerLevel = "DEBUG"
	loggerINFO  loggerLevel = "INFO"
	loggerWarn  loggerLevel = "WARN"
	loggerError loggerLevel = "ERROR"
	loggerFatal loggerLevel = "FATAL"
)

// default rotation parameters. These are tuned for a single-instance low-volume
// service: keep ~7 daily files, rotate when they reach 100MB each, compress old
// files. Override via env vars (LOG_MAX_SIZE_MB, LOG_MAX_AGE_DAYS,
// LOG_MAX_BACKUPS, LOG_COMPRESS).
const (
	defaultLogMaxSizeMB  = 100
	defaultLogMaxBackups = 7
	defaultLogMaxAgeDays = 30
	defaultLogCompress   = true
)

// setupLogOnce ensures the global writers are wired exactly once per process.
var setupLogOnce sync.Once

// activeFileWriter is the shared lumberjack-rotating writer used by both the
// gin global writer and our internal helpers. Keeping a single instance means
// every log line benefits from the same rotation policy.
var activeFileWriter io.Writer

// SetupLogger configures the global log writers. By default logs are written
// to a rotating file under LogDir (default ./logs) AND to stdout/stderr. This
// is the canonical behavior - no configuration is needed for files to be
// produced and rotated. Set LOG_TO_STDOUT_ONLY=1 to disable file output and
// keep logs going to the original stdout/stderr stream only.
func SetupLogger() {
	setupLogOnce.Do(func() {
		// Pick the directory we want logs in. LogDir is set by common.Init
		// from --log-dir / LOG_DIR. If somehow empty, default to ./logs.
		dir := LogDir
		if dir == "" {
			dir = "./logs"
			LogDir = dir
		}
		// Make sure the directory exists. MkdirAll is safe to call multiple
		// times and tolerates the directory already being present.
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Printf("failed to create log dir %s: %v - falling back to stdout", dir, err)
			// Leave gin writers pointed at stdout/stderr; nothing more to do.
			return
		}

		// Build a lumberjack rotator. Compress old files to keep disk usage
		// bounded, rotate by size, and keep a fixed window of backups.
		rotator := &lumberjack.Logger{
			Filename:   filepath.Join(dir, "oneapi.log"),
			MaxSize:    env.Int("LOG_MAX_SIZE_MB", defaultLogMaxSizeMB),   // megabytes per file
			MaxBackups: env.Int("LOG_MAX_BACKUPS", defaultLogMaxBackups),  // number of rotated files to keep
			MaxAge:     env.Int("LOG_MAX_AGE_DAYS", defaultLogMaxAgeDays), // days to keep rotated files
			Compress:   env.Bool("LOG_COMPRESS", defaultLogCompress),      // gzip rotated files
			LocalTime:  true,
		}
		activeFileWriter = rotator

		// Decide whether stdout is still wanted alongside the file. The
		// historical behavior (when --log-dir was set) was to keep stdout,
		// so we preserve that. LOG_TO_STDOUT_ONLY=1 reverts to the
		// pre-file behavior (stdout only) for users who explicitly want it.
		toStdout := env.String("LOG_TO_STDOUT_ONLY", "")
		if toStdout == "1" || strings.EqualFold(toStdout, "true") {
			// File disabled by request. Keep gin defaults (stdout/stderr).
			activeFileWriter = nil
			return
		}

		// Wire gin writers to a MultiWriter of stdout + file for info, and
		// stderr + file for errors. This way log lines still appear in
		// `docker logs` / `journalctl` style setups while ALSO landing in a
		// rotated file.
		gin.DefaultWriter = io.MultiWriter(os.Stdout, rotator)
		gin.DefaultErrorWriter = io.MultiWriter(os.Stderr, rotator)
	})
}

// fileWriter returns the active rotating file writer, or nil if file output
// is disabled. Tests can rely on this to assert on disk content without
// depending on gin internals.
func fileWriter() io.Writer {
	return activeFileWriter
}

func SysLog(s string) {
	logHelper(nil, loggerINFO, s)
}

func SysLogf(format string, a ...any) {
	logHelper(nil, loggerINFO, fmt.Sprintf(format, a...))
}

func SysWarn(s string) {
	logHelper(nil, loggerWarn, s)
}

func SysWarnf(format string, a ...any) {
	logHelper(nil, loggerWarn, fmt.Sprintf(format, a...))
}

func SysError(s string) {
	logHelper(nil, loggerError, s)
}

func SysErrorf(format string, a ...any) {
	logHelper(nil, loggerError, fmt.Sprintf(format, a...))
}

func Debug(ctx context.Context, msg string) {
	if !config.DebugEnabled {
		return
	}
	logHelper(ctx, loggerDEBUG, msg)
}

func Info(ctx context.Context, msg string) {
	logHelper(ctx, loggerINFO, msg)
}

func Warn(ctx context.Context, msg string) {
	logHelper(ctx, loggerWarn, msg)
}

func Error(ctx context.Context, msg string) {
	logHelper(ctx, loggerError, msg)
}

func Debugf(ctx context.Context, format string, a ...any) {
	if !config.DebugEnabled {
		return
	}
	logHelper(ctx, loggerDEBUG, fmt.Sprintf(format, a...))
}

func Infof(ctx context.Context, format string, a ...any) {
	logHelper(ctx, loggerINFO, fmt.Sprintf(format, a...))
}

func Warnf(ctx context.Context, format string, a ...any) {
	logHelper(ctx, loggerWarn, fmt.Sprintf(format, a...))
}

func Errorf(ctx context.Context, format string, a ...any) {
	logHelper(ctx, loggerError, fmt.Sprintf(format, a...))
}

func FatalLog(s string) {
	logHelper(nil, loggerFatal, s)
}

func FatalLogf(format string, a ...any) {
	logHelper(nil, loggerFatal, fmt.Sprintf(format, a...))
}

func logHelper(ctx context.Context, level loggerLevel, msg string) {
	writer := gin.DefaultErrorWriter
	if level == loggerINFO {
		writer = gin.DefaultWriter
	}
	var requestId string
	if ctx != nil {
		rawRequestId := helper.GetRequestID(ctx)
		if rawRequestId != "" {
			requestId = fmt.Sprintf(" | %s", rawRequestId)
		}
	}
	lineInfo, funcName := getLineInfo()
	now := time.Now()
	_, _ = fmt.Fprintf(writer, "[%s] %v%s%s %s%s \n", level, now.Format("2006/01/02 - 15:04:05"), requestId, lineInfo, funcName, msg)
	// Ensure SetupLogger() has run so an operator starting with a fresh
	// process (or running just logHelper without going through main) still
	// gets rotation configured.
	SetupLogger()
	// 同步导出到 OTel log exporter（OTEL_ENABLED 时才有效，内部自带 nil 检查）
	observability.EmitLog(ctx, string(level), msg)
	if level == loggerFatal {
		os.Exit(1)
	}
}

func getLineInfo() (string, string) {
	funcName := "[unknown] "
	pc, file, line, ok := runtime.Caller(3)
	if ok {
		if fn := runtime.FuncForPC(pc); fn != nil {
			parts := strings.Split(fn.Name(), ".")
			funcName = "[" + parts[len(parts)-1] + "] "
		}
	} else {
		file = "unknown"
		line = 0
	}
	parts := strings.Split(file, "one-api/")
	if len(parts) > 1 {
		file = parts[1]
	}
	return fmt.Sprintf(" | %s:%d", file, line), funcName
}
