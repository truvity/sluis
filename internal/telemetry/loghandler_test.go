package telemetry_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"go.opentelemetry.io/otel/trace"

	"github.com/truvity/sluis/internal/telemetry"
)

func logLine(ctx context.Context, t *testing.T) map[string]any {
	t.Helper()
	var out bytes.Buffer
	log := slog.New(telemetry.LogHandler(slog.NewJSONHandler(&out, nil))).With(slog.String("component", "test"))
	log.InfoContext(ctx, "something happened")
	var line map[string]any
	if err := json.Unmarshal(out.Bytes(), &line); err != nil {
		t.Fatalf("log line is not JSON: %v: %q", err, out.String())
	}
	return line
}

func TestLogHandlerAddsTraceContext(t *testing.T) {
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{0x4b, 0xf9, 0x2f, 0x35, 0x77, 0xb3, 0x4d, 0xa6, 0xa3, 0xce, 0x92, 0x9d, 0x0e, 0x0e, 0x47, 0x36},
		SpanID:     trace.SpanID{0x00, 0xf0, 0x67, 0xaa, 0x0b, 0xa9, 0x02, 0xb7},
		TraceFlags: trace.FlagsSampled,
	})
	line := logLine(trace.ContextWithSpanContext(context.Background(), sc), t)
	want := map[string]string{
		"trace_id":    "4bf92f3577b34da6a3ce929d0e0e4736",
		"span_id":     "00f067aa0ba902b7",
		"trace_flags": "01",
		"component":   "test",
	}
	for k, v := range want {
		if line[k] != v {
			t.Errorf("%s = %v, want %q", k, line[k], v)
		}
	}
}

func TestLogHandlerLeavesARecordWithoutASpanAlone(t *testing.T) {
	line := logLine(context.Background(), t)
	for _, k := range []string{"trace_id", "span_id", "trace_flags"} {
		if _, ok := line[k]; ok {
			t.Errorf("%s present on a record with no span context", k)
		}
	}
	if line["component"] != "test" {
		t.Errorf("component = %v, want it kept", line["component"])
	}
}
