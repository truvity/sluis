package refusal

import (
	"context"
	"log/slog"
)

// LogKey is the attribute a log line carries the request id under.
const LogKey = "request_id"

// LogHandler adds the request id to every record logged with a request's
// context, whoever logs it -- this service's own code or the OpenID
// library's, which logs the parent of every error it answers with. That is
// how a caller's "request 1f3a..." finds the line that says why.
//
// A record that already names the id itself is left as it is.
func LogHandler(next slog.Handler) slog.Handler { return logHandler{next} }

type logHandler struct{ next slog.Handler }

func (h logHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h logHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := ID(ctx); id != "" {
		named := false
		r.Attrs(func(a slog.Attr) bool {
			named = named || a.Key == LogKey
			return !named
		})
		if !named {
			r.AddAttrs(slog.String(LogKey, id))
		}
	}
	return h.next.Handle(ctx, r)
}

func (h logHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return logHandler{h.next.WithAttrs(attrs)}
}

func (h logHandler) WithGroup(name string) slog.Handler { return logHandler{h.next.WithGroup(name)} }
