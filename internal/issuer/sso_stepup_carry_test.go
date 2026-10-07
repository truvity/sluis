package issuer_test

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/issuer"
)

// The same person signing in again in the same browser -- a step-up,
// `prompt=login`, `max_age` -- keeps what they opened, and it stays theirs to
// end: the sessions and the clients of the sign-in it replaced are carried
// over to the new one, so a later sign-out ends them and tells the clients.
// Before, they stayed filed under the old sign-in's id, the sign-out
// revoked what was filed under the new one, and they ran on to their own end.
func TestASignOutAfterAStepUpEndsWhatWasOpenedBeforeIt(t *testing.T) {
	t.Parallel()

	for _, how := range []string{"&prompt=login", "&max_age=0"} {
		t.Run(how, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			rig := newSSORig(t, issuer.Config{})
			b, oldID := rig.signedInBrowser(t)

			rig.open(t, ssoEmail, "argocd", oldID)

			if err := rig.iss.SSO().Involve(ctx, oldID, "argocd"); err != nil {
				t.Fatal(err)
			}

			if err := rig.iss.SSO().Involve(ctx, oldID, "grafana"); err != nil { // openid only
				t.Fatal(err)
			}

			// The same person's session in ANOTHER browser is not this
			// browser's to carry, nor to end.
			rig.open(t, ssoEmail, "kargo", "another-browser")

			if status := stepUp(t, b, how); status != http.StatusFound {
				t.Fatalf("callback: %d", status)
			}

			newID := b.signInID(rig.iss.SSO())
			if newID == "" || newID == oldID {
				t.Fatalf("the step-up's sign-in = %q (was %q)", newID, oldID)
			}

			// Carried, not ended: nothing was revoked and nobody told.
			under, err := rig.iss.Sessions().List(ctx, issuer.Query{Identity: ssoEmail, SSO: newID})
			if err != nil || len(under) != 1 || under[0].ClientID != "argocd" {
				t.Fatalf("sessions under the new sign-in = %+v, %v; want the argocd one", under, err)
			}

			if n := rig.sessionsOf(ssoEmail); n != 2 {
				t.Errorf("%d sessions after the step-up, want both to stay", n)
			}

			if len(rig.told()) != 0 {
				t.Errorf("clients were told of a step-up: %+v", rig.told())
			}

			clients, err := rig.iss.SSO().Involved(ctx, newID)
			if err != nil || !slices.Contains(clients, "argocd") || !slices.Contains(clients, "grafana") {
				t.Errorf("clients of the new sign-in = %v, %v; want argocd and grafana carried over", clients, err)
			}

			// And the person's sign-out reaches them.
			if status, _, _ := b.do(http.MethodGet, "/logout"); status != http.StatusFound {
				t.Fatalf("sign-out: %d", status)
			}

			left, err := rig.iss.Sessions().List(ctx, issuer.Query{Identity: ssoEmail})
			if err != nil || len(left) != 1 || left[0].ClientID != "kargo" {
				t.Errorf("sessions after the sign-out = %+v, %v; want only the other browser's", left, err)
			}

			for _, client := range []string{"argocd", "grafana"} {
				if !rig.announcedTo(client) {
					t.Errorf("%s was not told of the sign-out: %+v", client, rig.told())
				}
			}

			if rig.announcedTo("kargo") {
				t.Error("the other browser's client was told")
			}
		})
	}
}

// stepUp signs the browser in again with the provider, as `how` forces.
func stepUp(t *testing.T, b *browser, how string) int {
	t.Helper()

	where := b.authorize(how)

	_, toProvider, _ := b.do(http.MethodGet, where)

	_, state, found := strings.Cut(toProvider, "state=")
	if !found {
		t.Fatalf("%s went to %q, want the provider", how, toProvider)
	}

	status, _, _ := b.do(http.MethodGet, "/login/google/callback?code=x&state="+state)

	return status
}

// A session racing the move is never brought back: one revoked between the
// listing and the write stays revoked, and one under another sign-in, or
// another person's, is left where it is.
func TestRefileMovesOnlyThatPersonsSessionsUnderThatSignIn(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	rig := newSSORig(t, issuer.Config{})

	rig.open(t, ssoEmail, "argocd", "old")
	rig.open(t, ssoEmail, "kargo", "old")
	rig.open(t, ssoEmail, "vault", "elsewhere")
	rig.open(t, ssoBob, "argocd", "old")

	revoked, err := rig.iss.Sessions().List(ctx, issuer.Query{Identity: ssoEmail, ClientID: "kargo"})
	if err != nil || len(revoked) != 1 {
		t.Fatalf("kargo = %v, %v", revoked, err)
	}

	if _, err = rig.iss.Sessions().RevokeID(ctx, revoked[0].ID); err != nil {
		t.Fatal(err)
	}

	moved, err := rig.iss.Sessions().Refile(ctx, ssoEmail, "old", "new")
	if err != nil || moved != 1 {
		t.Fatalf("Refile = %d, %v; want the one live argocd session", moved, err)
	}

	for _, want := range []struct {
		identity, sso string
		clients       []string
	}{
		{ssoEmail, "new", []string{"argocd"}},
		{ssoEmail, "old", nil},
		{ssoEmail, "elsewhere", []string{"vault"}},
		{ssoBob, "old", []string{"argocd"}},
	} {
		got, err := rig.iss.Sessions().List(ctx, issuer.Query{Identity: want.identity, SSO: want.sso})
		if err != nil {
			t.Fatal(err)
		}

		var clients []string
		for i := range got {
			clients = append(clients, got[i].ClientID)
		}

		if !slices.Equal(clients, want.clients) {
			t.Errorf("%s under %s = %v, want %v", want.identity, want.sso, clients, want.clients)
		}
	}
}
