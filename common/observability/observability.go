package observability

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/songquanpeng/one-api/common/config"
)

var (
	shutdownOnce  sync.Once
	shutdownFuncs []func(context.Context) error
)

// Init 初始化 OpenTelemetry，必须在 main() 早期调用。
// OTEL_ENABLED=true 时才真正启用，否则是 no-op。
func Init() {
	if !config.OtelEnabled {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. TracerProvider
	if config.OtelTracesEnabled {
		tp, err := initTracerProvider(ctx)
		if err != nil {
			log.Printf("[otel] failed to init tracer provider: %v", err)
		} else {
			shutdownFuncs = append(shutdownFuncs, tp.Shutdown)
			log.Printf("[otel] trace exporter → %s", config.OtelEndpoint)
		}
	}

	// 2. MeterProvider
	if config.OtelMetricsEnabled {
		mp, err := initMeterProvider(ctx)
		if err != nil {
			log.Printf("[otel] failed to init meter provider: %v", err)
		} else {
			shutdownFuncs = append(shutdownFuncs, mp.Shutdown)
			log.Printf("[otel] metric exporter → %s", config.OtelEndpoint)
		}
	}

	// 3. LogProvider (OTLP log bridge)
	if config.OtelLogsEnabled {
		lp, err := initLogProvider(ctx)
		if err != nil {
			log.Printf("[otel] failed to init log provider: %v", err)
		} else {
			shutdownFuncs = append(shutdownFuncs, lp.Shutdown)
			log.Printf("[otel] log exporter → %s", config.OtelEndpoint)
		}
	}

	log.Printf("[otel] all providers initialized")
}

// Shutdown 优雅关闭所有 OTel provider，在 main() defer 中调用。
func Shutdown() {
	shutdownOnce.Do(func() {
		if len(shutdownFuncs) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, fn := range shutdownFuncs {
			if err := fn(ctx); err != nil {
				log.Printf("[otel] shutdown error: %v", err)
			}
		}
	})
}
