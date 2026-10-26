// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
	"maunium.net/go/mautrix/bridgev2/commands"
)

// OpenTelemetry tracing support. It is activated only through environment variables, so
// nothing changes for deployments that do not set them:
//
//   - OTEL_TRACES_EXPORTER: "otlp" (gRPC), "otlphttp" (HTTP/protobuf) or "none"/unset (disabled)
//   - OTEL_EXPORTER_OTLP_ENDPOINT / OTEL_EXPORTER_OTLP_TRACES_ENDPOINT: collector address
//   - OTEL_SERVICE_NAME: resource service.name ("mautrix-jmpchat" when unset)
//
// Spans never carry message bodies or command arguments, only command names and counters.

const (
	telemetryTracerName         = "jmpchat"
	telemetryServiceNameDefault = "mautrix-jmpchat"
)

// telemetry holds the optional tracer provider. A nil *telemetry (or one with a nil provider)
// means tracing is disabled and every helper degrades to a no-op tracer.
type telemetry struct {
	enabled bool
	tp      *sdktrace.TracerProvider
	tracer  trace.Tracer
	log     zerolog.Logger
}

// initTelemetry creates the tracer provider when OTEL_TRACES_EXPORTER selects an exporter.
// It returns (nil, nil) when tracing is disabled.
func initTelemetry(ctx context.Context, log zerolog.Logger) (*telemetry, error) {
	exporterName := strings.ToLower(strings.TrimSpace(os.Getenv("OTEL_TRACES_EXPORTER")))
	if exporterName == "" || exporterName == "none" {
		return nil, nil
	}
	var client otlptrace.Client
	switch exporterName {
	case "otlp":
		client = otlptracegrpc.NewClient()
	case "otlphttp":
		client = otlptracehttp.NewClient()
	default:
		return nil, fmt.Errorf("unsupported OTEL_TRACES_EXPORTER %q (use otlp, otlphttp or none)", exporterName)
	}
	exporter, err := otlptrace.New(ctx, client)
	if err != nil {
		return nil, fmt.Errorf("create OTLP trace exporter: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(telemetryResource(ctx, log)),
	)
	// Register globally so library code (mrtc-sip-bridge, hostkit) emits into this provider too.
	otel.SetTracerProvider(tp)
	t := &telemetry{enabled: true, tp: tp, tracer: tp.Tracer(telemetryTracerName), log: log}
	log.Info().Str("exporter", exporterName).Msg("OpenTelemetry tracing enabled")
	return t, nil
}

// telemetryResource builds the SDK resource: defaults from the environment plus a fallback
// service.name. A resource that cannot be built never blocks startup.
func telemetryResource(ctx context.Context, log zerolog.Logger) *resource.Resource {
	base, err := resource.New(ctx, resource.WithFromEnv(), resource.WithTelemetrySDK())
	if err != nil {
		log.Debug().Err(err).Msg("Falling back to an empty OpenTelemetry resource")
		base = resource.Empty()
	}
	serviceName := strings.TrimSpace(os.Getenv("OTEL_SERVICE_NAME"))
	if serviceName == "" {
		serviceName = telemetryServiceNameDefault
	}
	merged, err := resource.Merge(base, resource.NewWithAttributes(semconv.SchemaURL, semconv.ServiceName(serviceName)))
	if err != nil {
		log.Debug().Err(err).Msg("Using the environment OpenTelemetry resource without a service name")
		return base
	}
	return merged
}

// shutdown flushes pending spans. Safe on a nil receiver.
func (t *telemetry) shutdown(ctx context.Context) {
	if t == nil || t.tp == nil {
		return
	}
	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := t.tp.Shutdown(sctx); err != nil {
		t.log.Warn().Err(err).Msg("Failed to flush OpenTelemetry traces")
	}
}

// tracerOrNoop returns the connector tracer, or a no-op tracer when tracing is disabled.
func (c *MyConnector) tracerOrNoop() trace.Tracer {
	if c != nil && c.tel != nil && c.tel.tracer != nil {
		return c.tel.tracer
	}
	return trace.NewNoopTracerProvider().Tracer(telemetryTracerName)
}

// startSpan begins a span with the connector tracer when one is available.
func (c *MyConnector) startSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return c.tracerOrNoop().Start(ctx, name, trace.WithAttributes(attrs...))
}

// commandSpanHandler wraps a command handler so every invocation runs under a span named after
// the command. Arguments and message contents are never recorded.
func commandSpanHandler(h *commands.FullHandler) *commands.FullHandler {
	if h == nil || h.Func == nil || h.Name == "" {
		return h
	}
	wrapped := *h
	orig := h.Func
	spanName := "command." + h.Name
	wrapped.Func = func(ce *commands.Event) {
		if ce == nil {
			orig(ce)
			return
		}
		tracer := trace.NewNoopTracerProvider().Tracer(telemetryTracerName)
		if ce.Bridge != nil {
			if conn, ok := ce.Bridge.Network.(*MyConnector); ok && conn != nil {
				tracer = conn.tracerOrNoop()
			}
		}
		parent := ce.Ctx
		if parent == nil {
			parent = context.Background()
		}
		ctx, span := tracer.Start(parent, spanName)
		defer span.End()
		previous := ce.Ctx
		ce.Ctx = ctx
		defer func() { ce.Ctx = previous }()
		orig(ce)
	}
	return &wrapped
}
