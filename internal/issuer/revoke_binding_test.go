package issuer_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/issuer"
)

// RFC 7009 section 2.1: the server verifies that the token was issued to the
// client making the request. A public client has no secret, so what it names
// as its own is all the proof there is, and the token it presents is the rest.

func revokeAs(t *testing.T, rig *sluisctlRig, client, tok string) int {
	t.Helper()
	form := url.Values{"token": {tok}, "client_id": {client}}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, rig.server.URL+"/revoke", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := rig.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

func liveSessions(t *testing.T, rig *sluisctlRig, client string) []issuer.Session {
	t.Helper()
	list, err := rig.iss.Sessions().List(t.Context(), issuer.Query{Identity: "ada@north.example", ClientID: client})
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func TestRevokeEndsTheSessionOfTheClientsOwnRefreshToken(t *testing.T) {
	t.Parallel()
	rig := newSluisctlRig(t)
	rig.tokens(t, "local-dev")
	rig.tokens(t, "sluisctl")

	if status := revokeAs(t, rig, "local-dev", "refresh-local-dev"); status != http.StatusOK {
		t.Fatalf("revoke: %d", status)
	}
	if got := liveSessions(t, rig, "local-dev"); len(got) != 0 {
		t.Errorf("the client's own session survived: %d", len(got))
	}
	if got := liveSessions(t, rig, "sluisctl"); len(got) != 1 {
		t.Errorf("another client's session was touched: %d left, want 1", len(got))
	}
}

func TestRevokeLeavesAnotherClientsRefreshTokenAlone(t *testing.T) {
	t.Parallel()
	rig := newSluisctlRig(t)
	rig.tokens(t, "local-dev")

	// Answered as success, like a token that does not exist, and ends nothing.
	if status := revokeAs(t, rig, "sluisctl", "refresh-local-dev"); status != http.StatusOK {
		t.Fatalf("revoke: %d", status)
	}
	if got := liveSessions(t, rig, "local-dev"); len(got) != 1 {
		t.Errorf("another client revoked this client's session: %d left, want 1", len(got))
	}
}

func TestRevokeLeavesAnotherClientsAccessTokenAlone(t *testing.T) {
	t.Parallel()
	rig := newSluisctlRig(t)
	access := rig.access(t, "sluisctl")

	if status := revokeAs(t, rig, "local-dev", access); status != http.StatusOK {
		t.Fatalf("revoke: %d", status)
	}
	if _, ok := rig.storage.CLISubject(t.Context(), access, []string{"sluisctl"}); !ok {
		t.Error("another client removed this client's access token")
	}
	if status := revokeAs(t, rig, "sluisctl", access); status != http.StatusOK {
		t.Fatalf("revoke: %d", status)
	}
	if _, ok := rig.storage.CLISubject(t.Context(), access, []string{"sluisctl"}); ok {
		t.Error("the client could not revoke its own access token")
	}
}

func TestRevokeDoesNotTakeASessionIDForAToken(t *testing.T) {
	t.Parallel()
	rig := newSluisctlRig(t)
	rig.tokens(t, "local-dev")
	sessions := liveSessions(t, rig, "local-dev")
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d", len(sessions))
	}

	// A session id is in the `sid` claim and in a logout token: it names a
	// session, and proves nothing. Not even the session's own client ends it
	// with the id.
	for _, client := range []string{"local-dev", "sluisctl"} {
		if status := revokeAs(t, rig, client, sessions[0].ID); status != http.StatusOK {
			t.Fatalf("revoke as %s: %d", client, status)
		}
		if got := liveSessions(t, rig, "local-dev"); len(got) != 1 {
			t.Errorf("a raw session id ended the session when %s presented it", client)
		}
	}
}
