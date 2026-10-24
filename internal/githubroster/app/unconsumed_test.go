package app

import (
	"context"
	"log/slog"
	"testing"

	"github.com/truvity/sluis/storage/logtest"
)

// The function assembles a controller for every invocation, so the warning
// that internal groups are declared and not consumed is said once per process
// for a set of groups, with the same words and the groups, and said again for
// a different set.
func TestTheUnconsumedGroupsWarningIsSaidOncePerSet(t *testing.T) {
	ctx := context.Background()
	log, records := logtest.Logger()

	first := []string{"once-test:alpha:one", "once-test:alpha:two"}
	for range 5 {
		warnUnconsumedOnce(ctx, log, first)
	}
	if got := records.Count("internal groups are declared but nothing consumes them"); got != 1 {
		t.Fatalf("%d warnings for five calls with one set, want 1", got)
	}
	warning := records.MustFind(t, "internal groups are declared but nothing consumes them")
	if warning.Level != slog.LevelWarn {
		t.Errorf("level = %s, want WARN", warning.Level)
	}
	if got, ok := warning.Attr("groups"); !ok || got.String() != "[once-test:alpha:one once-test:alpha:two]" {
		t.Errorf("groups = %v, want the two names", got)
	}

	// Another set is news; a set with nothing in it is not.
	warnUnconsumedOnce(ctx, log, []string{"once-test:alpha:one"})
	warnUnconsumedOnce(ctx, log, []string{"once-test:alpha:one"})
	warnUnconsumedOnce(ctx, log, nil)
	if got := records.Count("internal groups are declared but nothing consumes them"); got != 2 {
		t.Errorf("%d warnings after a second set, want 2", got)
	}
}
