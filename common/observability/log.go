package observability

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	otellog "go.opentelemetry.io/otel/log"
	otellogsdk "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"

	"github.com/songquanpeng/one-api/common/config"
)

var (
	// LogProvider 是 OTLP log provider。
	LogProvider *otellogsdk.LoggerProvider
	// OtelLogger 是 bridge 的 OTel logger，供 logger 包使用。
	OtelLogger otellog.Logger
)

func initLogProvider(ctx context.Context) (*otellogsdk.LoggerProvider, error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(config.OtelServiceName),
			semconv.ServiceVersion(serviceVersion),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("create resource: %w", err)
	}

	exporter, err := otlploggrpc.New(ctx, otlploggrpc.WithEndpoint(config.OtelEndpoint), otlploggrpc.WithInsecure())
	if err != nil {
		return nil, fmt.Errorf("create log exporter: %w", err)
	}

	lp := otellogsdk.NewLoggerProvider(
		otellogsdk.WithResource(res),
		otellogsdk.WithProcessor(
			otellogsdk.NewBatchProcessor(exporter),
		),
	)

	LogProvider = lp
	OtelLogger = lp.Logger(
		"github.com/songquanpeng/one-api",
		otellog.WithInstrumentationVersion(serviceVersion),
	)

	return lp, nil
}

// EmitLog 向 OTel log exporter 写入一条 log record。
// 供 common/logger 包在每次日志写入时调用。
func EmitLog(ctx context.Context, level, msg string) {
	if OtelLogger == nil || LogProvider == nil {
		return
	}

	rec := otellog.Record{}
	rec.SetTimestamp(time.Now())

	// 映射 log level
	switch level {
	case "DEBUG":
		rec.SetSeverity(otellog.SeverityDebug)
		rec.SetSeverityText("DEBUG")
	case "INFO":
		rec.SetSeverity(otellog.SeverityInfo)
		rec.SetSeverityText("INFO")
	case "WARN":
		rec.SetSeverity(otellog.SeverityWarn)
		rec.SetSeverityText("WARN")
	case "ERROR":
		rec.SetSeverity(otellog.SeverityError)
		rec.SetSeverityText("ERROR")
	case "FATAL":
		rec.SetSeverity(otellog.SeverityFatal)
		rec.SetSeverityText("FATAL")
	default:
		rec.SetSeverity(otellog.SeverityInfo)
		rec.SetSeverityText(level)
	}

	rec.SetBody(otellog.StringValue(msg))

	OtelLogger.Emit(ctx, rec)
}
