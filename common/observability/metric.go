package observability

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"

	"github.com/songquanpeng/one-api/common/config"
)

const metricMeterName = "github.com/songquanpeng/one-api"

// 全局指标句柄
var (
	// Meter 是 one-api 的全局 meter。
	Meter metric.Meter

	// HttpRequestsTotal 记录 HTTP 请求数（按 method/route/status 维度）。
	HttpRequestsTotal metric.Int64Counter
	// HttpRequestDuration 记录 HTTP 请求耗时直方图（秒）。
	HttpRequestDuration metric.Float64Histogram
	// RelayRequestsTotal 记录 relay 转发请求数（按 provider/channel 维度）。
	RelayRequestsTotal metric.Int64Counter
	// RelayTokensTotal 记录 relay 消耗的 token 数。
	RelayTokensTotal metric.Int64Counter
	// ChannelSuccessRate 记录 channel 健康度（0~1）。
	ChannelSuccessRate metric.Float64Gauge
)

func initMeterProvider(ctx context.Context) (*sdkmetric.MeterProvider, error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(config.OtelServiceName),
			semconv.ServiceVersion(serviceVersion),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("create resource: %w", err)
	}

	exporter, err := otlpmetricgrpc.New(ctx,
		otlpmetricgrpc.WithEndpoint(config.OtelEndpoint),
		otlpmetricgrpc.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("create metric exporter: %w", err)
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter)),
	)

	otel.SetMeterProvider(mp)
	Meter = mp.Meter(metricMeterName)

	initMetrics()

	return mp, nil
}

// initMetrics 注册所有全局指标。
func initMetrics() {
	HttpRequestsTotal, _ = Meter.Int64Counter(
		"oneapi.http.requests",
		metric.WithDescription("Total number of HTTP requests"),
	)
	HttpRequestDuration, _ = Meter.Float64Histogram(
		"oneapi.http.duration",
		metric.WithDescription("HTTP request duration in seconds"),
		metric.WithUnit("s"),
	)
	RelayRequestsTotal, _ = Meter.Int64Counter(
		"oneapi.relay.requests",
		metric.WithDescription("Total number of relay requests"),
	)
	RelayTokensTotal, _ = Meter.Int64Counter(
		"oneapi.relay.tokens",
		metric.WithDescription("Total number of tokens consumed by relay"),
		metric.WithUnit("tokens"),
	)
	ChannelSuccessRate, _ = Meter.Float64Gauge(
		"oneapi.channel.success_rate",
		metric.WithDescription("Channel success rate"),
	)
}
