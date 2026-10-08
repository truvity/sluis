package lambdaapp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/lambdaapp"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/internal/rails"
)

type fakePass struct {
	ran       bool
	err       error
	targets   []string
	unsafeSet bool
	closed    int
}

func (f *fakePass) Pass(_ context.Context, target string, unsafeLocal bool) (bool, error) {
	f.targets = append(f.targets, target)
	f.unsafeSet = unsafeLocal
	return f.ran, f.err
}

func (f *fakePass) Close() error { f.closed++; return nil }

func handle(t *testing.T, c *lambdaapp.Controller, payload string) (lambdaapp.Result, error) {
	t.Helper()
	out, err := c.Handle(context.Background(), json.RawMessage(payload))
	if err != nil {
		return lambdaapp.Result{}, err
	}
	return out.(lambdaapp.Result), nil
}

func TestATickAndARunEventEachRunOnePassOfTheirTarget(t *testing.T) {
	for _, kind := range []string{"tick", "run"} {
		pass := &fakePass{ran: true}
		c := &lambdaapp.Controller{Name: "github", Open: func(context.Context) (lambdaapp.Pass, error) { return pass, nil }}
		res, err := handle(t, c, `{"kind":"`+kind+`","target":"acme"}`)
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if res != (lambdaapp.Result{Kind: kind, Target: "acme", Outcome: "ran"}) {
			t.Errorf("%s: %+v", kind, res)
		}
		if len(pass.targets) != 1 || pass.targets[0] != "acme" || pass.unsafeSet {
			t.Errorf("%s: the pass was %v unsafe=%v", kind, pass.targets, pass.unsafeSet)
		}
		if pass.closed != 1 {
			t.Errorf("%s: the controller was closed %d times: its audit queue is flushed by closing", kind, pass.closed)
		}
	}
}

func TestABadEventIsRefusedBeforeAnythingIsAssembled(t *testing.T) {
	c := &lambdaapp.Controller{Open: func(context.Context) (lambdaapp.Pass, error) {
		t.Error("a controller was assembled for a bad event")
		return nil, errors.New("no")
	}}
	for _, payload := range []string{`{}`, `{"kind":"tick"}`, `{"kind":"reboot","target":"x"}`, `not json`, `{"version":"2.0"}`} {
		if _, err := handle(t, c, payload); err == nil {
			t.Errorf("%s was accepted", payload)
		}
	}
}

func TestAFailedPassIsAnErrorTheSchedulerSees(t *testing.T) {
	c := &lambdaapp.Controller{Name: "slack", Open: func(context.Context) (lambdaapp.Pass, error) {
		return &fakePass{err: errors.New("slack said no")}, nil
	}}
	if _, err := handle(t, c, `{"kind":"tick","target":"T1"}`); err == nil {
		t.Error("a failed pass returned success")
	}
}

func TestARunOfATargetThisControllerDoesNotRunEndsCleanly(t *testing.T) {
	errUnknown := errors.New("not a target")
	c := &lambdaapp.Controller{
		Name: "slack", Unknown: func(err error) bool { return errors.Is(err, errUnknown) },
		Open: func(context.Context) (lambdaapp.Pass, error) { return &fakePass{err: errUnknown}, nil },
	}
	res, err := handle(t, c, `{"kind":"run","target":"acme"}`)
	if err != nil || res.Outcome != lambdaapp.OutcomeUnknown {
		t.Errorf("run: %+v, %v", res, err)
	}
	// A schedule naming a target nobody runs is a mistake to see, not a hint.
	if _, err = handle(t, c, `{"kind":"tick","target":"acme"}`); err == nil {
		t.Error("a tick of an unknown target succeeded")
	}
}

// leasePass is a controller pass over the real lease code (internal/rails) and
// a State two invocations share, as DynamoDB is.
type leasePass struct {
	leases  *rails.Leases
	started chan struct{}
	release chan struct{}
}

func (p *leasePass) Pass(ctx context.Context, target string, _ bool) (bool, error) {
	return p.leases.Do(ctx, "org", target, func(context.Context) {
		if p.started != nil {
			close(p.started)
			<-p.release
		}
	})
}

func (p *leasePass) Close() error { return nil }

func TestASecondInvocationForTheSameTargetIsContendedAndEndsCleanly(t *testing.T) {
	state := memory.New().Set().State
	newLeases := func() *rails.Leases { return &rails.Leases{State: state, Holder: rails.NewHolder(), TTL: time.Minute} }
	first := &leasePass{leases: newLeases(), started: make(chan struct{}), release: make(chan struct{})}
	second := &leasePass{leases: newLeases()}
	controller := func(p *leasePass) *lambdaapp.Controller {
		return &lambdaapp.Controller{Name: "github", Open: func(context.Context) (lambdaapp.Pass, error) { return p, nil }}
	}

	var wg sync.WaitGroup
	var firstRes lambdaapp.Result
	var firstErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		firstRes, firstErr = handle(t, controller(first), `{"kind":"tick","target":"acme"}`)
	}()
	<-first.started // the first invocation holds acme's lease

	res, err := handle(t, controller(second), `{"kind":"run","target":"acme"}`)
	if err != nil {
		t.Fatalf("a contended invocation failed: %v: the platform would retry it", err)
	}
	if res.Outcome != lambdaapp.OutcomeContended {
		t.Errorf("outcome %q, want contended", res.Outcome)
	}
	// Another target is not blocked by it.
	if res, err = handle(t, controller(second), `{"kind":"run","target":"other"}`); err != nil || res.Outcome != lambdaapp.OutcomeRan {
		t.Errorf("another target: %+v, %v", res, err)
	}
	close(first.release)
	wg.Wait()
	if firstErr != nil || firstRes.Outcome != lambdaapp.OutcomeRan {
		t.Errorf("the first invocation: %+v, %v", firstRes, firstErr)
	}
}

func TestATargetMustLookLikeALoginOrAKey(t *testing.T) {
	for _, target := range []string{"acme", "github:links", "T01ABC", "my-org.io", "a@b"} {
		if _, err := lambdaapp.ParseEvent(json.RawMessage(`{"kind":"tick","target":"` + target + `"}`)); err != nil {
			t.Errorf("%q: %v", target, err)
		}
	}
	for _, target := range []string{`../etc`, `a/b`, `x\ny`, ` a`, `-a`, strings.Repeat("a", 129)} {
		raw, _ := json.Marshal(map[string]string{"kind": "tick", "target": target})
		if _, err := lambdaapp.ParseEvent(raw); err == nil {
			t.Errorf("%q was accepted", target)
		}
	}
}

func TestARefreshEventRunsOneDirectoryPassAndAFailedWorkspaceIsAnError(t *testing.T) {
	calls := 0
	res := lambdaapp.RefreshResult{Kind: "refresh", Workspaces: 2, Ran: 2}
	run := func(context.Context) (lambdaapp.RefreshResult, error) { calls++; return res, nil }
	h := lambdaapp.NewHTTP(http.NotFoundHandler(), nil, nil).WithRefresh(run)
	out, err := h.Handle(context.Background(), json.RawMessage(`{"kind":"refresh"}`))
	if err != nil || calls != 1 || out.(lambdaapp.RefreshResult).Ran != 2 {
		t.Fatalf("%v, %d runs, %+v", err, calls, out)
	}
	res = lambdaapp.RefreshResult{Workspaces: 2, Ran: 1, Failed: 1}
	if _, err = h.Handle(context.Background(), json.RawMessage(`{"kind":"refresh"}`)); err == nil {
		t.Error("an unread workspace was reported as success")
	}
	none := lambdaapp.NewHTTP(http.NotFoundHandler(), nil, nil)
	if _, err = none.Handle(context.Background(), json.RawMessage(`{"kind":"refresh"}`)); err == nil {
		t.Error("a function with no directory ran a refresh")
	}
}

// The one function sends a tick or a run to the controller the policy says the
// target belongs to, an API Gateway event to the handler, and a run for a
// target nobody declares ends cleanly while a schedule's tick for one fails.
func TestOneFunctionDispatchesAnEventToTheControllerOfItsTarget(t *testing.T) {
	gh, sl := &fakePass{ran: true}, &fakePass{ran: true}
	controller := func(name string, p *fakePass) *lambdaapp.Controller {
		return &lambdaapp.Controller{Name: name, Open: func(context.Context) (lambdaapp.Pass, error) { return p, nil }}
	}
	kindOf := func(target string) string {
		switch target {
		case "acme", "github:links":
			return "github"
		case "T0SLACK":
			return "slack"
		}
		return ""
	}
	h := lambdaapp.NewHTTP(http.NotFoundHandler(), nil, nil).
		WithControllers(kindOf, map[string]*lambdaapp.Controller{"github": controller("github", gh), "slack": controller("slack", sl)})
	for _, c := range []struct {
		event string
		pass  *fakePass
		other *fakePass
	}{
		{`{"kind":"tick","target":"acme"}`, gh, sl},
		{`{"kind":"run","target":"github:links"}`, gh, sl},
		{`{"kind":"tick","target":"T0SLACK"}`, sl, gh},
	} {
		gh.targets, sl.targets = nil, nil
		out, err := h.Handle(context.Background(), json.RawMessage(c.event))
		if err != nil || out.(lambdaapp.Result).Outcome != "ran" {
			t.Fatalf("%s: %v %+v", c.event, err, out)
		}
		if len(c.pass.targets) != 1 || len(c.other.targets) != 0 {
			t.Errorf("%s: ran %v on its controller and %v on the other", c.event, c.pass.targets, c.other.targets)
		}
	}
	out, err := h.Handle(context.Background(), json.RawMessage(`{"kind":"run","target":"nobody"}`))
	if err != nil || out.(lambdaapp.Result).Outcome != lambdaapp.OutcomeUnknown {
		t.Errorf("a run-now of an undeclared target: %v %+v", err, out)
	}
	if _, err = h.Handle(context.Background(), json.RawMessage(`{"kind":"tick","target":"nobody"}`)); err == nil {
		t.Error("a schedule's tick of an undeclared target was reported as success")
	}
	// A function that runs no controller says so, the same way.
	none := lambdaapp.NewHTTP(http.NotFoundHandler(), nil, nil)
	if _, err = none.Handle(context.Background(), json.RawMessage(`{"kind":"tick","target":"acme"}`)); err == nil {
		t.Error("a function with no controllers ran a tick")
	}
}

type closeFails struct{ fakePass }

func (closeFails) Close() error { return errors.New("closed\nlevel=ERROR msg=forged\r") }

func TestALogLineBuiltFromAnErrorWithLineBreaksIsOneRecord(t *testing.T) {
	var out bytes.Buffer
	c := &lambdaapp.Controller{
		Name: "slack",
		Open: func(context.Context) (lambdaapp.Pass, error) { return &closeFails{fakePass{ran: true}}, nil },
		Log:  slog.New(slog.NewTextHandler(&out, nil)),
	}
	if _, err := handle(t, c, `{"kind":"tick","target":"acme"}`); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 2 || strings.ContainsAny(out.String(), "\r") {
		t.Fatalf("want the warning and the done line, one record each, got %q", out.String())
	}
	if !strings.Contains(lines[0], "closedlevel=ERROR") || !strings.Contains(lines[1], "invocation done") {
		t.Errorf("unexpected records: %q", lines)
	}
}

// The exports event is retired: the function takes it for an unknown kind.
func TestTheRetiredExportsEventIsRefused(t *testing.T) {
	h := lambdaapp.NewHTTP(http.NotFoundHandler(), nil, nil)
	if _, err := h.Handle(context.Background(), json.RawMessage(`{"kind":"exports"}`)); err == nil {
		t.Error("the retired exports event was accepted")
	}
}
