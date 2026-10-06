package issuer_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/internal/port/porttest/grantcost"
)

// ssoClientAdds is the number of times the sign-in's set of clients was
// added to since the counter was last reset.
func ssoClientAdds(h *grantcost.Harness) int {
	n := 0
	for _, op := range h.Counter.Ops() {
		if op.Port == grantcost.PortIndex && op.Call == "add" && op.Kind == "sso-clients" {
			n++
		}
	}
	return n
}

// TestInvolveHappensOnceAtSignIn: a grant that carries a session records its
// client among the sign-in's clients once, at sign-in; refreshes skip it; a
// session recorded before the flag existed is recorded once more at its next
// refresh and then carries the flag.
func TestInvolveHappensOnceAtSignIn(t *testing.T) {
	t.Parallel()
	s := memory.New()
	involveScenario(t, s.Set().State, func(t *testing.T) *grantcost.Harness {
		return grantcost.New(t, grantcost.Env{Set: s.Set(), Advance: s.Advance})
	})
}

// involveScenario runs the check over one State; internal/port/dynamodb
// holds the same check over its fake.
func involveScenario(t *testing.T, state port.State, newHarness func(t *testing.T) *grantcost.Harness) {
	t.Helper()
	ctx := context.Background()
	h := newHarness(t)

	h.Counter.Reset()
	first := h.Grant()
	if n := ssoClientAdds(h); n != 1 {
		t.Errorf("a sign-in and its redemption recorded the client %d times among the sign-in's, want 1", n)
	}
	sessions, err := h.Issuer.Sessions().List(ctx, issuer.Query{Identity: grantcost.Person})
	if err != nil || len(sessions) != 1 {
		t.Fatalf("List = %d sessions, %v; want 1", len(sessions), err)
	}
	if !sessions[0].Involved {
		t.Error("the session opened under a sign-in does not say its client is recorded")
	}
	if sessions[0].SSO == "" {
		t.Fatal("the session carries no sign-in, so this test proves nothing")
	}
	involved, err := h.Issuer.SSO().Involved(ctx, sessions[0].SSO)
	if err != nil || len(involved) != 1 || involved[0] != grantcost.ClientID {
		t.Errorf("the sign-in's clients = %v, %v; want [%s]", involved, err, grantcost.ClientID)
	}

	h.Counter.Reset()
	second := h.Refresh(first.Refresh)
	third := h.Refresh(second.Refresh)
	if second.Status != http.StatusOK || third.Status != http.StatusOK {
		t.Fatalf("refreshes: %d, %d", second.Status, third.Status)
	}
	if n := ssoClientAdds(h); n != 0 {
		t.Errorf("two refreshes recorded the client %d times, want 0", n)
	}

	// A session recorded before the flag existed.
	key := "issuer:session:" + sessions[0].ID
	record, err := state.Get(ctx, key)
	if err != nil {
		t.Fatalf("read the session record: %v", err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(record.Value, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "involved")
	raw, _ := json.Marshal(fields)
	if _, err = state.Update(ctx, key, raw, 12*time.Hour, record.Revision); err != nil {
		t.Fatalf("write the pre-change record: %v", err)
	}

	h.Counter.Reset()
	fourth := h.Refresh(third.Refresh)
	if fourth.Status != http.StatusOK {
		t.Fatalf("refresh of a pre-change session: %d %q", fourth.Status, fourth.Error)
	}
	if n := ssoClientAdds(h); n != 1 {
		t.Errorf("the first refresh of a pre-change session recorded the client %d times, want 1", n)
	}
	after, _, _ := h.Issuer.Sessions().ByID(ctx, sessions[0].ID)
	if !after.Involved {
		t.Error("the refresh did not set the flag")
	}

	h.Counter.Reset()
	if fifth := h.Refresh(fourth.Refresh); fifth.Status != http.StatusOK {
		t.Fatalf("refresh: %d", fifth.Status)
	}
	if n := ssoClientAdds(h); n != 0 {
		t.Errorf("the refresh after that recorded the client %d times, want 0", n)
	}
}
