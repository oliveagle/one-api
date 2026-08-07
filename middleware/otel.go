package middleware

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/songquanpeng/one-api/common/helper"
	"github.com/songquanpeng/one-api/common/observability"
)

// OTelMiddleware 为每个 HTTP 请求创建 span，并记录请求数/耗时指标。
// 仅在 OTEL_ENABLED=true 且对应信号启用时才真正干活；否则是廉价 no-op。
func OTelMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// --- traces: 创建 span ---
		if observability.Tracer == nil {
			c.Next()
			return
		}

		start := time.Now()
		spanCtx, span := observability.Tracer.Start(c.Request.Context(), "HTTP "+c.Request.Method,
			trace.WithAttributes(
				semconv.HTTPRequestMethodKey.String(c.Request.Method),
				semconv.HTTPRouteKey.String(c.FullPath()),
				semconv.URLPathKey.String(c.Request.URL.Path),
				semconv.ServerAddressKey.String(c.Request.Host),
				attribute.String("request.id", helper.GetRequestID(c.Request.Context())),
			),
		)
		defer span.End()

		c.Request = c.Request.WithContext(spanCtx)
		c.Next()

		// --- record result ---
		status := c.Writer.Status()
		span.SetAttributes(
			semconv.HTTPResponseStatusCodeKey.Int(status),
		)
		if status >= 400 {
			span.SetStatus(codes.Error, "HTTP "+itoa(status))
		}

		// --- metrics: 请求计数 + 耗时 ---
		if observability.HttpRequestsTotal != nil {
			observability.HttpRequestsTotal.Add(context.Background(), 1,
				metric.WithAttributes(metricAttributes(c)...),
			)
		}
		if observability.HttpRequestDuration != nil {
			observability.HttpRequestDuration.Record(context.Background(),
				time.Since(start).Seconds(),
				metric.WithAttributes(metricAttributes(c)...),
			)
		}
	}
}

func metricAttributes(c *gin.Context) []attribute.KeyValue {
	return []attribute.KeyValue{
		semconv.HTTPRequestMethodKey.String(c.Request.Method),
		semconv.HTTPRouteKey.String(c.FullPath()),
		semconv.HTTPResponseStatusCodeKey.Int(c.Writer.Status()),
	}
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
