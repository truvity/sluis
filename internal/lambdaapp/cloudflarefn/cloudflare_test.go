package cloudflarefn

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/deploycheck"
	"github.com/truvity/sluis/internal/lambdaapp"
)

func noCheck(context.Context) (deploycheck.Report, error) { return deploycheck.Report{}, nil }

// The rotation schedule sends {"kind":"cloudflare"} to this function. In
// v1.76.0-rc.3 the handler never took the event ("this function has no
// cloudflare section"), so no preset rotated and every 1h credential expired.
func TestTheFunctionAnswersTheRotationSchedule(t *testing.T) {
	ticks := 0
	tick := func(context.Context) (int, string, error) {
		ticks++
		return 0, "archive=rotated", nil
	}
	out, err := handler(nil, tick, noCheck, slog.New(slog.DiscardHandler)).
		Handle(context.Background(), json.RawMessage(`{"kind":"cloudflare"}`))
	if err != nil {
		t.Fatalf("the cloudflare event was refused: %v", err)
	}
	if ticks != 1 {
		t.Fatalf("the presets were ticked %d times, want 1", ticks)
	}
	res, ok := out.(lambdaapp.CloudflareResult)
	if !ok || res.Presets != "archive=rotated" || res.Failed != 0 {
		t.Fatalf("result = %#v, want archive=rotated with no failure", out)
	}
}

// A preset that did not rotate is an error the schedule sees (App.TickAll joins
// the failures, each naming its preset).
func TestAFailedPresetFailsTheEvent(t *testing.T) {
	tick := func(context.Context) (int, string, error) {
		return 1, "archive=failed cache=kept", errors.New("preset archive: cloudflare: 403")
	}
	_, err := handler(nil, tick, noCheck, slog.New(slog.DiscardHandler)).
		Handle(context.Background(), json.RawMessage(`{"kind":"cloudflare"}`))
	if err == nil || !strings.Contains(err.Error(), "preset archive") {
		t.Fatalf("err = %v, want the failed preset named", err)
	}
}
