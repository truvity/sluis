package logtest_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"testing/slogtest"

	"github.com/truvity/sluis/storage/logtest"
)

// result rebuilds, from what the handler captured, the map slogtest checks:
// built-in keys at the top level and groups as nested maps.
func result(h *logtest.Handler) func(t *testing.T) map[string]any {
	return func(t *testing.T) map[string]any {
		t.Helper()
		recs := h.Records()
		if len(recs) == 0 {
			t.Fatal("no record captured")
		}
		last := recs[len(recs)-1]
		m := map[string]any{}
		if !last.Time.IsZero() {
			m[slog.TimeKey] = last.Time
		}
		m[slog.LevelKey] = last.Level
		m[slog.MessageKey] = last.Message
		for _, a := range last.Attrs {
			parts := strings.Split(a.Key, ".")
			cur := m
			for _, p := range parts[:len(parts)-1] {
				next, ok := cur[p].(map[string]any)
				if !ok {
					next = map[string]any{}
					cur[p] = next
				}
				cur = next
			}
			cur[parts[len(parts)-1]] = a.Value.Any()
		}
		return m
	}
}

func TestHandlerHonoursTheSlogContract(t *testing.T) {
	h := logtest.New(nil)
	slogtest.Run(t, func(*testing.T) slog.Handler { return h }, result(h))
}

func TestFindAndAttr(t *testing.T) {
	log, h := logtest.Logger()
	log.With(slog.String("k", "v")).WithGroup("g").InfoContext(context.Background(), "hello", slog.Int("n", 3), slog.Group("in", slog.String("x", "y")))
	r := h.MustFind(t, "hello")
	if r.String("k") != "v" || r.String("g.n") != "3" || r.String("g.in.x") != "y" {
		t.Fatalf("attrs = %v", r.Attrs)
	}
	if _, ok := r.Attr("missing"); ok {
		t.Fatal("found a missing attribute")
	}
	if h.Count("hello") != 1 || len(h.Messages()) != 1 {
		t.Fatal("count")
	}
}
