package rosterapp

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

// consoleWait is how long a controller waits for the console, which is its own
// process, before it passes anyway.
const consoleWait = 2 * time.Minute

// awaitConsole returns once the console at url answers anything at all (a 401
// is an answer: the controller has not presented its token), or after
// [consoleWait], or when ctx ends. A first pass that met a console that was not
// listening yet would fail every target and not try again for an interval; the
// controller's own readiness is already open, so the Service that routes to
// this pod does not wait on it.
func awaitConsole(ctx context.Context, log *slog.Logger, controller, url string) {
	if url == "" {
		return
	}
	deadline := time.Now().Add(consoleWait)
	client := &http.Client{Timeout: 3 * time.Second}
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return
		}
		if resp, err := client.Do(req); err == nil {
			_ = resp.Body.Close()
			return
		}
		if time.Now().After(deadline) {
			log.WarnContext(ctx, "the console did not answer; the controller passes anyway",
				slog.String("controller", controller), slog.String("wait", consoleWait.String()))
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}
