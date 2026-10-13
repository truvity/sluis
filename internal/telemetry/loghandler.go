package telemetry

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// Keys of the trace context a [LogHandler] adds to a record, in the W3C
// trace-context hex form (32, 16 and 2 hex digits).
const (
	LogTraceID    = "trace_id"
	LogSpanID     = "span_id"
	LogTraceFlags = "trace_flags"
)

// LogHandler wraps next so that every record logged with a context that holds
// a valid span context carries trace_id, span_id and trace_flags. A record
// without one is passed on unchanged. It adds fields to the stdout line only:
// there is no OTel logs SDK and no other sink. Log with the ...Context
// variants (sloglint's context rule enforces it) or the span is not seen.
func LogHandler(next slog.Handler) slog.Handler { return logHandler{next} }

type logHandler struct{ next slog.Handler }

func (h logHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h logHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(
			slog.String(LogTraceID, sc.TraceID().String()),
			slog.String(LogSpanID, sc.SpanID().String()),
			slog.String(LogTraceFlags, sc.TraceFlags().String()),
		)
	}
	return h.next.Handle(ctx, r)
}

func (h logHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return logHandler{h.next.WithAttrs(attrs)}
}

func (h logHandler) WithGroup(name string) slog.Handler {
	return logHandler{h.next.WithGroup(name)}
}
