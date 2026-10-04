package lambdaapp_test

import (
	"context"
	"encoding/json"
	"errors"
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
