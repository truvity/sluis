package lambdaapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/truvity/sluis/internal/port/invoke"
)

// Handler is one function's entry point: the platform's event in, a result out.
type Handler interface {
	Handle(ctx context.Context, payload json.RawMessage) (any, error)
}

// The event kinds of a controller function. Both run the same pass; they
// differ in who asked.
const (
	// KindTick is what an EventBridge Scheduler schedule sends, one per target.
	KindTick = "tick"
	// KindRun is what the http function sends when somebody asks for a pass now
	// (internal/port/invoke).
	KindRun = invoke.KindRun
)

// The outcomes of a controller invocation.
const (
	// OutcomeRan is a pass that ran to its end.
	OutcomeRan = "ran"
	// OutcomeUnknown is a run-now of a target this controller does not run: the
	// notification was sent to every controller because nobody knew which kind
	// the target is, and the other one ends here, cleanly.
	OutcomeUnknown = "unknown"
	// OutcomeContended is a target another invocation holds the lease of: the
	// pass is its, and this one ends cleanly. It is not a failure, so the
	// platform neither retries it nor counts it as an error.
	OutcomeContended = "contended"
)

// Result is what a controller invocation returns.
type Result struct {
	Kind    string `json:"kind"`
	Target  string `json:"target"`
	Outcome string `json:"outcome"`
}

// Pass is one assembled controller, for one invocation.
type Pass interface {
	// Pass runs the target's pass under its lease; false, with no error, means
	// another runner holds it.
	Pass(ctx context.Context, target string, unsafeLocal bool) (ran bool, err error)
	// Close flushes what the pass recorded.
	Close() error
}

// Controller runs one pass of one target per invocation.
//
// The controller is assembled per invocation and closed at its end, as
// `sluis tick` does: the audit emitter delivers asynchronously, and a function
// that is frozen between invocations would hold its queue for hours. What is
// per process stays per process: the configuration, read and validated at cold
// start, and the secrets resolved into the environment.
type Controller struct {
	// Name is the controller's, in log lines.
	Name string
	// Open assembles the controller.
	Open func(ctx context.Context) (Pass, error)
	Log  *slog.Logger
	// Unknown says an error is the controller not running that target.
	Unknown func(error) bool
}

// Handle implements [Handler]: payload is {"kind":"tick"|"run","target":"<id>"}.
func (c *Controller) Handle(ctx context.Context, payload json.RawMessage) (any, error) {
	event, err := ParseEvent(payload)
	if err != nil {
		return nil, err
	}
	log := c.Log
	if log == nil {
		log = slog.Default()
	}
	pass, err := c.Open(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", c.Name, err)
	}
	// The leases are in DynamoDB, shared by every invocation, so a pass may run
	// without the operator's opt-in a one-shot command needs for a lease held in
	// its own memory: Pass refuses a State that is not shared.
	ran, err := pass.Pass(ctx, event.Target, false)
	if closeErr := pass.Close(); closeErr != nil {
		log.WarnContext(ctx, "the audit emitter could not be closed cleanly; what its queue held is dropped", "error", closeErr)
	}
	if err != nil && event.Kind == KindRun && c.Unknown != nil && c.Unknown(err) {
		log.InfoContext(ctx, "a run-now for a target this controller does not run", "controller", c.Name, "target", oneLine(event.Target))
		return Result{Kind: event.Kind, Target: event.Target, Outcome: OutcomeUnknown}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %s of %q: %w", c.Name, event.Kind, oneLine(event.Target), err)
	}
	outcome := OutcomeRan
	if !ran {
		outcome = OutcomeContended
	}
	log.InfoContext(ctx, "invocation done", "controller", c.Name, "kind", event.Kind, "outcome", outcome)
	return Result{Kind: event.Kind, Target: event.Target, Outcome: outcome}, nil
}

var targetPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@-]{0,127}$`)

// ParseEvent reads and checks a controller function's event.
func ParseEvent(payload json.RawMessage) (invoke.Event, error) {
	var event invoke.Event
	if err := json.Unmarshal(payload, &event); err != nil {
		return event, fmt.Errorf("the event is not {\"kind\":...,\"target\":...}: %w", err)
	}
	switch {
	case event.Kind != KindTick && event.Kind != KindRun:
		return event, fmt.Errorf("the event's kind is %q: it is %q (a schedule) or %q (run now)", event.Kind, KindTick, KindRun)
	case !targetPattern.MatchString(event.Target):
		// The target reaches a lease key, a report name and a credential's file
		// name, so what the event may carry is what a login or a key is made of.
		return event, errors.New("the event's target is empty, too long or has a character a GitHub login or a Slack key has not")
	}
	// The pattern above already refuses all of these; the explicit forms are the
	// ones a code scanner recognises as cleaning a value that reaches a log line
	// and a file name further down.
	if strings.Contains(event.Target, "..") || strings.ContainsAny(event.Target, "/\\") {
		return event, errors.New("the event's target is a path")
	}
	event.Target = strings.ReplaceAll(strings.ReplaceAll(event.Target, "\n", ""), "\r", "")
	return event, nil
}

// oneLine is a value made safe to put in a log line: a line break in it would
// start another line.
func oneLine(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\r", " ")
}

// KindExports is the event an EventBridge Scheduler schedule sends the `http`
// function to keep the exports (the copies of secrets under /sluis/export/...)
// current: {"kind":"exports"}. There is no loop to do it on Lambda.
const KindExports = "exports"

// ExportsResult is what an exports invocation returns.
type ExportsResult struct {
	Kind string `json:"kind"`
	// Outcome is "ran", "none" (no export is declared) or "failed".
	Outcome   string `json:"outcome"`
	Exports   int    `json:"exports"`
	Done      int    `json:"done"`
	Contended int    `json:"contended"`
	Failed    int    `json:"failed"`
}

// KindRefresh is the event an EventBridge Scheduler schedule sends the `http`
// function to take a new snapshot of every connected workspace's directory:
// {"kind":"refresh"}. The Kubernetes server does it on a ticker; there is no
// loop on Lambda, and a snapshot that is not refreshed stops being
// authoritative after the freshness window, which sign-in refuses on.
const KindRefresh = "refresh"

// RefreshResult is what a refresh invocation returns.
type RefreshResult struct {
	Kind       string `json:"kind"`
	Workspaces int    `json:"workspaces"`
	Ran        int    `json:"ran"`
	Contended  int    `json:"contended"`
	Failed     int    `json:"failed"`
}

// scheduled handles an event of the http function that is not a request.
func (h *HTTP) scheduled(ctx context.Context, kind string) (any, error) {
	if kind == KindRefresh {
		return h.refreshDirectory(ctx)
	}
	if kind != KindExports {
		return nil, fmt.Errorf("the event's kind is %q: the http function takes API Gateway events and {\"kind\":%q} or {\"kind\":%q}",
			oneLine(kind), KindExports, KindRefresh)
	}
	if h.exports == nil {
		return nil, errors.New("this function owns no exports")
	}
	defer func() {
		if h.settle != nil {
			h.settle()
		}
	}()
	res, err := h.exports(ctx)
	if err != nil {
		return nil, err
	}
	// A failed copy is an error the schedule sees: the pass is idempotent and
	// the next one retries, but a copy that stays stale should be noticed.
	if res.Failed > 0 {
		return nil, fmt.Errorf("%d of %d exports could not be made", res.Failed, res.Exports)
	}
	return res, nil
}

// refreshDirectory runs one directory refresh pass.
func (h *HTTP) refreshDirectory(ctx context.Context) (any, error) {
	if h.refresh == nil {
		return nil, errors.New("this function has no directory to refresh")
	}
	defer func() {
		if h.settle != nil {
			h.settle()
		}
	}()
	res, err := h.refresh(ctx)
	if err != nil {
		return nil, err
	}
	// A workspace that could not be read is an error the schedule sees: the
	// snapshot it has is untouched and the next pass retries, but a directory
	// that stays unread loses its authority and should be noticed.
	if res.Failed > 0 {
		return nil, fmt.Errorf("%d of %d workspaces could not be refreshed", res.Failed, res.Workspaces)
	}
	return res, nil
}
