// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"testing"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2/commands"
)

func TestInitTelemetryDisabledByDefault(t *testing.T) {
	ctx := context.Background()
	for _, exporter := range []string{"", "none", "NONE", " none "} {
		t.Setenv("OTEL_TRACES_EXPORTER", exporter)
		tel, err := initTelemetry(ctx, zerolog.Nop())
		if err != nil {
			t.Fatalf("initTelemetry(%q): unexpected error: %v", exporter, err)
		}
		if tel != nil {
			t.Fatalf("initTelemetry(%q): expected nil telemetry, got %#v", exporter, tel)
		}
	}
}

func TestInitTelemetryUnknownExporter(t *testing.T) {
	t.Setenv("OTEL_TRACES_EXPORTER", "jaeger")
	tel, err := initTelemetry(context.Background(), zerolog.Nop())
	if err == nil {
		t.Fatalf("expected an error for an unsupported exporter, got telemetry %#v", tel)
	}
	if tel != nil {
		t.Fatalf("expected nil telemetry on error, got %#v", tel)
	}
}

func TestTelemetryResourceServiceName(t *testing.T) {
	ctx := context.Background()

	t.Setenv("OTEL_SERVICE_NAME", "")
	res := telemetryResource(ctx, zerolog.Nop())
	if got := res.Attributes(); len(got) == 0 {
		t.Fatal("expected at least the service.name attribute")
	} else {
		found := false
		for _, attr := range got {
			if string(attr.Key) == "service.name" && attr.Value.AsString() == telemetryServiceNameDefault {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected service.name=%q in resource attributes, got %v", telemetryServiceNameDefault, got)
		}
	}

	t.Setenv("OTEL_SERVICE_NAME", "my-jmpchat")
	res = telemetryResource(ctx, zerolog.Nop())
	for _, attr := range res.Attributes() {
		if string(attr.Key) == "service.name" && attr.Value.AsString() != "my-jmpchat" {
			t.Fatalf("expected the environment service.name to win, got %q", attr.Value.AsString())
		}
	}
}

func TestCommandSpanHandlerWrapping(t *testing.T) {
	t.Parallel()
	if h := commandSpanHandler(nil); h != nil {
		t.Fatal("nil handler must stay nil")
	}
	if h := commandSpanHandler(&commands.FullHandler{Name: "x"}); h == nil || h.Func != nil {
		t.Fatal("handler without a Func must be returned unchanged")
	}

	handler := &commands.FullHandler{Name: "ping"}
	called := false
	handler.Func = func(ce *commands.Event) { called = true }
	wrapped := commandSpanHandler(handler)
	if wrapped == handler {
		t.Fatal("expected a copy of the handler, not the original")
	}
	if wrapped.Name != "ping" || wrapped.Func == nil {
		t.Fatalf("unexpected wrapped handler: %#v", wrapped)
	}
	if handler.Func == nil {
		t.Fatal("original handler must keep its Func")
	}
	ce := &commands.Event{}
	wrapped.Func(ce)
	if !called {
		t.Fatal("wrapped handler did not call the original function")
	}
	if handler.Func == nil {
		t.Fatal("original handler Func was mutated")
	}
}

func TestTracerOrNoopWithoutTelemetry(t *testing.T) {
	t.Parallel()
	c := &MyConnector{}
	if c.tracerOrNoop() == nil {
		t.Fatal("expected a no-op tracer, got nil")
	}
	ctx, span := c.startSpan(context.Background(), "test.span")
	if span == nil || ctx == nil {
		t.Fatal("startSpan returned nil values")
	}
	span.End()
}

func TestTelemetryShutdownNilSafe(t *testing.T) {
	t.Parallel()
	var tel *telemetry
	tel.shutdown(context.Background())
	(&telemetry{}).shutdown(context.Background())
}
