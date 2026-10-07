package issuer_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/internal/port/porttest/grantcost"
)

// What a reuse of a spent refresh token costs, over the in-memory adapter:
// the first presentation after the grace window ends the session (two reads,
// the record and its three index sets as four writes) and a repeat finds the
// session gone (the same two reads, no write). The normal refresh keeps the
// budget grantcost.Budgets holds, which TestGrantCostOverTheMemoryAdapter
// runs.
// Not parallel: the OpenID library writes a package variable while a provider
// is built, so two tests building one at once race.
func TestAReuseCostsTwoReadsAndFourWritesThenThreeReadsAndNoWrite(t *testing.T) {
	h := grantcost.New(t, func() grantcost.Env {
		s := memory.New()
		return grantcost.Env{Set: s.Set(), Advance: s.Advance}
	}())

	first := h.Grant()
	rotated := h.Refresh(first.Refresh)
	if rotated.Status != 200 {
		t.Fatalf("refresh: %d %q", rotated.Status, rotated.Error)
	}
	h.Advance(33 * time.Second)

	var reused grantcost.Tokens
	one := h.Measure(func() { reused = h.Refresh(first.Refresh) })
	t.Logf("first reuse: %s", one.Summary())
	t.Logf("first reuse calls:\n%s", one.Breakdown())
	if reused.Status != 400 || reused.Error != "invalid_grant" {
		t.Fatalf("reuse = %d %q, want 400 invalid_grant", reused.Status, reused.Error)
	}
	t.Run("first reuse", func(t *testing.T) {
		if one.Reads > 2 || one.Writes > 4 {
			t.Errorf("the first reuse made %d reads and %d writes, budget 2 and 4", one.Reads, one.Writes)
		}
	})
	if one.Resolutions != 0 {
		t.Errorf("a reuse asked the directory %d times, want none", one.Resolutions)
	}

	var again grantcost.Tokens
	two := h.Measure(func() { again = h.Refresh(first.Refresh) })
	t.Logf("repeated reuse: %s\n%s", two.Summary(), two.Breakdown())
	if again.Status != 400 || again.Error != "invalid_grant" {
		t.Fatalf("repeated reuse = %d %q, want 400 invalid_grant", again.Status, again.Error)
	}
	t.Run("repeated reuse", func(t *testing.T) {
		// The pointer, the record, and the pointer again on the refusal path
		// of a session that is gone, which tells the absolute limit from a
		// plain refusal. No write at all.
		if two.Reads > 3 || two.Writes != 0 {
			t.Errorf("a repeated reuse made %d reads and %d writes, want at most 3 and 0", two.Reads, two.Writes)
		}
	})
	if two.Resolutions != 0 {
		t.Errorf("a repeated reuse asked the directory %d times, want none", two.Resolutions)
	}

	// The successor of the reused token is dead too, at the same price.
	var dead grantcost.Tokens
	three := h.Measure(func() { dead = h.Refresh(rotated.Refresh) })
	if dead.Status == 200 {
		t.Error("the successor of a reused token refreshes")
	}
	if three.Writes != 0 {
		t.Errorf("refusing the successor of an ended session wrote %d times", three.Writes)
	}
}

// postRefresh is a refresh grant as the named client sends it.
func postRefreshAs(t *testing.T, h *grantcost.Harness, client, token string) (int, string) {
	t.Helper()
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {token}, "client_id": {client}}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, h.Server.URL+"/token", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := h.Server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var body struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(response.Body).Decode(&body)
	return response.StatusCode, body.Error
}

// A spent token presented by a client that is not the session's own is
// refused and ends nothing: anybody can send a refresh token under any
// client id, and the session is only the owner's to lose. The session's own
// client presenting the same token ends it.
// Not parallel: the OpenID library writes a package variable while a provider
// is built, so two tests building one at once race.
func TestAReuseUnderAnotherClientEndsNothingAndUnderTheOwnClientEndsTheSession(t *testing.T) {
	h := grantcost.New(t, func() grantcost.Env {
		s := memory.New()
		return grantcost.Env{Set: s.Set(), Advance: s.Advance}
	}())

	first := h.Grant()
	rotated := h.Refresh(first.Refresh)
	if rotated.Status != http.StatusOK {
		t.Fatalf("refresh: %d %q", rotated.Status, rotated.Error)
	}
	h.Advance(33 * time.Second)

	count := func() int {
		got, err := h.Issuer.Sessions().List(t.Context(), issuer.Query{})
		if err != nil {
			t.Fatal(err)
		}
		return len(got)
	}

	for range 2 {
		status, errName := postRefreshAs(t, h, "k8s:devel", first.Refresh)
		if status != http.StatusBadRequest || errName != "invalid_grant" {
			t.Errorf("under another client: %d %q, want 400 invalid_grant", status, errName)
		}
		if count() != 1 {
			t.Fatal("a spent token presented under another client ended the session")
		}
	}
	// The live token of the session still refreshes: nothing was touched.
	live := h.Refresh(rotated.Refresh)
	if live.Status != http.StatusOK {
		t.Fatalf("the live token stopped refreshing after a presentation under another client: %d %q", live.Status, live.Error)
	}
	h.Advance(33 * time.Second)

	// The session's own client, presenting a token spent long ago.
	if reused := h.Refresh(first.Refresh); reused.Status != http.StatusBadRequest || reused.Error != "invalid_grant" {
		t.Fatalf("reuse by the owner: %d %q, want 400 invalid_grant", reused.Status, reused.Error)
	}
	if count() != 0 {
		t.Error("the session is still listed after its own client reused a spent token")
	}
	if dead := h.Refresh(live.Refresh); dead.Status == http.StatusOK {
		t.Error("the successor refreshes after the session was ended")
	}
}
