package writer

import (
	"log/slog"
	"testing"

	"github.com/truvity/sluis/storage/logtest"
)

func logged(missing, kept []string) *logtest.Handler {
	log, h := logtest.Logger()
	reportUnhandled(log, "audit.digest.written", missing, kept)
	return h
}

// A profile only some deployments configure is not a fault when another
// profile the action names keeps its records.
func TestAPartialMatchIsNotAWarning(t *testing.T) {
	got := logged([]string{"evidence"}, []string{"security"})
	if got.CountLevel(slog.LevelWarn) != 0 || got.CountLevel(slog.LevelError) != 0 {
		t.Fatalf("a kept action warned: %q", got.Messages())
	}
	if got.CountLevel(slog.LevelDebug) == 0 || !got.Mentions("evidence") {
		t.Fatalf("the partial match is not a debug line naming what is missing: %q", got.Messages())
	}
}

// When none of the profiles is configured the records are dropped, and the
// warning names the action and the profiles nobody configured.
func TestNoMatchWarnsNamingTheAction(t *testing.T) {
	got := logged([]string{"evidence", "security"}, nil)
	if got.CountLevel(slog.LevelWarn) == 0 || !got.Mentions("audit.digest.written") || !got.Mentions("evidence") {
		t.Fatalf("a dropped action did not warn by name: %q", got.Messages())
	}
}
