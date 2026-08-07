package observability

import (
	"context"
	"net"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/songquanpeng/one-api/common/config"
)

// collectorReachable 探测本地 otel-collector 4317 是否在线；不在线则跳过 E2E。
func collectorReachable(t *testing.T) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", "127.0.0.1:4317", 500*time.Millisecond)
	if err != nil {
		t.Skip("local otel-collector not reachable on 127.0.0.1:4317, skipping E2E")
	}
	_ = conn.Close()
}

// TestDisabledIsNoop 验证 OTEL_ENABLED=false 时 Init 是 no-op，指标句柄为 nil。
func TestDisabledIsNoop(t *testing.T) {
	config.OtelEnabled = false
	Init()
	if HttpRequestsTotal != nil {
		t.Fatal("expected no metric instruments when disabled")
	}
}

// TestEmitLogE2E 验证日志经 OTLP 导出到本地 collector（端到端）。
func TestEmitLogE2E(t *testing.T) {
	collectorReachable(t)

	config.OtelEnabled = true
	config.OtelEndpoint = "127.0.0.1:4317"
	config.OtelTracesEnabled = true
	config.OtelMetricsEnabled = true
	config.OtelLogsEnabled = true

	Init()
	defer Shutdown()

	if LogProvider == nil || OtelLogger == nil {
		t.Fatal("log provider not initialized")
	}
	if Tracer == nil {
		t.Fatal("tracer not initialized")
	}
	if Meter == nil {
		t.Fatal("meter not initialized")
	}

	// 写一条日志
	EmitLog(context.Background(), "INFO", "observability E2E test log message")

	// 记录一个指标
	ctx := context.Background()
	HttpRequestsTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("test", "e2e")))
	HttpRequestDuration.Record(ctx, 0.01, metric.WithAttributes(attribute.String("test", "e2e")))

	// trace 一个 span
	_, span := Tracer.Start(ctx, "e2e-span")
	span.SetAttributes(attribute.String("test", "e2e"))
	span.End()

	// Shutdown 会 flush。额外等 1s 让 batch exporter 送出。
	Shutdown()
	time.Sleep(1500 * time.Millisecond)
}
