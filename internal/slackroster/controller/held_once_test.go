package controller_test

import (
	"context"
	"slices"
	"testing"
)

// A hold is audited once per transition into held, however many processes
// pass over it: every Lambda invocation is a cold start whose only memory is
// the report the last one wrote.
func TestAHoldIsAuditedOnceAcrossColdStartingTicks(t *testing.T) {
	r := newRig(t)
	r.person("ann@acme.example", []string{"g-all"}, "acme")
	r.person("new@acme.example", []string{"g-all"}, "") // no Slack account: held

	for range 4 {
		if _, _, err := r.fresh("acme").RunTarget(context.Background(), "acme"); err != nil {
			t.Fatal(err)
		}
	}
	if got := r.actions("roster.slack_action.held"); got != 1 {
		t.Errorf("the hold was audited %d times across cold ticks (%v), want once", got, r.audit.Actions())
	}
	if rec := r.reports.workspace(t, "acme").Recorded; len(rec) == 0 {
		t.Error("the report does not carry what was audited, so the next cold start cannot seed from it")
	}
}

// A reason that carries counts changes whenever the count does; the hold is
// the same hold, so it is not audited again. Free text is logged, not audited.
func TestAHoldWhoseReasonCountsMoveIsNotAuditedAgain(t *testing.T) {
	r := newRig(t)
	var keep, leaving []string
	for _, n := range []string{"a", "b"} {
		keep = append(keep, r.person(n+"@acme.example", []string{"g-eng"}, "acme"))
	}
	for _, n := range []string{"w", "x", "y", "z"} {
		leaving = append(leaving, r.person(n+"@acme.example", nil, "acme"))
	}
	ch := r.strictChannel(append(slices.Clone(keep), leaving...)...)
	held := func() int { return r.actions("roster.slack_action.held") }

	r.pass("acme")
	first := held()
	if first == 0 {
		t.Fatalf("the breaker's hold was not audited: %v", r.audit.Actions())
	}
	// Another leaver turns up: "4 of 7" becomes "5 of 8".
	extra := r.person("v@acme.example", nil, "acme")
	r.fake.Channels[ch.ID].Members = append(r.fake.Channels[ch.ID].Members, extra)
	r.pass("acme")
	r.fresh("acme").Pass(context.Background())
	if held() != first {
		t.Errorf("the same hold was audited again when its counts moved: %d then %d", first, held())
	}
}
