package issuer_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	connect "connectrpc.com/connect"

	accessissuerv1 "github.com/truvity/sluis/gen/accessissuer/v1"
	"github.com/truvity/sluis/internal/issuer"
)

// preUpgrade files a sign-in the way the release before the cookie secret
// did: no cookie hash, no pointer, and the id was the cookie. A session is
// opened under it so sign-out has something to revoke and announce.
func preUpgrade(t *testing.T, rig *ssoRig, id string) {
	t.Helper()

	ctx := context.Background()
	state := rig.state.State

	raw, err := json.Marshal(issuer.SSOSession{
		ID: id, Identity: ssoEmail, How: "google",
		AuthTime: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}

	if err = state.Set(ctx, ssoRecordPrefix+id, raw, time.Hour); err != nil {
		t.Fatal(err)
	}

	for _, set := range []string{"issuer:sso", "issuer:sso-of:" + ssoEmail} {
		if err = state.Add(ctx, set, id, time.Hour); err != nil {
			t.Fatal(err)
		}
	}

	if _, err = rig.iss.Sessions().Record(ctx, issuer.Opened{
		Identity: ssoEmail, ClientID: "argocd", How: issuer.HowCode, Token: "t-" + id, SSO: id,
	}); err != nil {
		t.Fatal(err)
	}
}

// M1. A browser that holds a cookie from before the upgrade -- the sign-in's
// id -- can still sign itself OUT during the release that introduced the
// secret, and only out.
func TestAPreUpgradeCookieCanSignOutAndNothingElse(t *testing.T) {
	t.Parallel()

	for _, door := range []struct{ name, method, path string }{
		{"logout", http.MethodGet, "/logout"},
		{"logout post", http.MethodPost, "/logout"},
		{"end_session", http.MethodGet, "/end_session"},
	} {
		t.Run(door.name, func(t *testing.T) {
			t.Parallel()

			rig := newSSORig(t, issuer.Config{})
			preUpgrade(t, rig, "old-sign-in")

			b := newBrowser(t, rig.server)
			b.cookies[issuer.SSOCookieName] = "old-sign-in"

			// Before signing out the id signs nobody in, anywhere.
			if proves(b) {
				t.Fatal("a pre-upgrade cookie completed /authorize silently")
			}

			if _, to, _ := b.do(http.MethodGet, "/account"); to != "/console/" {
				t.Errorf("a pre-upgrade cookie was sent to %q by /account", to)
			}

			req := connect.NewRequest(&accessissuerv1.ListSessionsRequest{Identity: ssoEmail})
			req.Header().Set("Cookie", issuer.SSOCookieName+"=old-sign-in")

			if _, err := rig.service().ListSessions(context.Background(), req); connect.CodeOf(err) != connect.CodeUnauthenticated {
				t.Errorf("a pre-upgrade cookie on the session service: %v", err)
			}

			b.cookies[issuer.SSOCookieName] = "old-sign-in" // proves() may have cleared it

			b.do(door.method, door.path)

			if _, live, _ := rig.iss.SSO().Get(context.Background(), "old-sign-in"); live {
				t.Error("signing out with the pre-upgrade cookie left the sign-in")
			}

			left, err := rig.iss.Sessions().List(context.Background(), issuer.Query{Identity: ssoEmail})
			if err != nil || len(left) != 0 {
				t.Errorf("its sessions survived: %v, %v", left, err)
			}

			announced := false

			for _, s := range rig.told() {
				announced = announced || s.ClientID == "argocd"
			}

			if !announced {
				t.Errorf("argocd was not told (back-channel logout): %+v", rig.told())
			}

			if b.cookies[issuer.SSOCookieName] != "" {
				t.Error("the cookie was not cleared")
			}
		})
	}
}

// The fallback is for records with no cookie hash and for no other: a
// current sign-in is never reached by its id, not even to be ended.
func TestThePreUpgradeFallbackNeverAppliesToAHashedRecord(t *testing.T) {
	t.Parallel()

	rig := newSSORig(t, issuer.Config{})
	owner, id := rig.signedInBrowser(t)

	if _, err := rig.iss.Sessions().Record(context.Background(), issuer.Opened{
		Identity: ssoEmail, ClientID: "argocd", How: issuer.HowCode, Token: "t", SSO: id,
	}); err != nil {
		t.Fatal(err)
	}

	stranger := newBrowser(t, rig.server)
	stranger.cookies[issuer.SSOCookieName] = id
	stranger.do(http.MethodGet, "/logout")
	stranger.cookies[issuer.SSOCookieName] = id
	stranger.do(http.MethodGet, "/end_session")

	if _, live, _ := rig.iss.SSO().Get(context.Background(), id); !live {
		t.Error("a current sign-in was ended by its id")
	}

	if left, _ := rig.iss.Sessions().List(context.Background(), issuer.Query{Identity: ssoEmail}); len(left) != 1 {
		t.Errorf("a current sign-in's sessions were revoked by its id: %v", left)
	}

	if len(rig.told()) != 0 {
		t.Errorf("clients were told of a sign-out that was not the owner's: %+v", rig.told())
	}

	if !proves(owner) {
		t.Error("the owner was signed out")
	}
}

// L2. A store that cannot answer must not turn into a sign-out that did not
// happen: an error, the cookie kept, nothing ended.
func TestSignOutWithAnUnreadableStoreEndsNothingAndKeepsTheCookie(t *testing.T) {
	t.Parallel()

	for _, door := range []struct{ name, method, path string }{
		{"logout", http.MethodGet, "/logout"},
		{"logout post", http.MethodPost, "/logout"},
		{"end_session", http.MethodGet, "/end_session"},
	} {
		t.Run(door.name, func(t *testing.T) {
			t.Parallel()

			rig := newSSORig(t, issuer.Config{})
			b, id := rig.signedInBrowser(t)
			cookie := b.cookies[issuer.SSOCookieName]

			if _, err := rig.iss.Sessions().Record(context.Background(), issuer.Opened{
				Identity: ssoEmail, ClientID: "argocd", How: issuer.HowCode, Token: "t", SSO: id,
			}); err != nil {
				t.Fatal(err)
			}

			rig.state.setFailures(true, nil)
			status, _, body := b.do(door.method, door.path)
			rig.state.setFailures(false, nil)

			if status < 500 {
				t.Errorf("%s answered %d (%s) although nothing was ended, want an error", door.path, status, body)
			}

			if b.cookies[issuer.SSOCookieName] != cookie {
				t.Error("the cookie was cleared although nothing was ended")
			}

			if _, live, _ := rig.iss.SSO().Get(context.Background(), id); !live {
				t.Error("the sign-in ended")
			}

			if _, found, _ := rig.state.State.Get(context.Background(), ssoPointerPrefix+ssoHash(cookie)); !found {
				t.Error("the pointer is gone")
			}

			if left, _ := rig.iss.Sessions().List(context.Background(), issuer.Query{Identity: ssoEmail}); len(left) != 1 {
				t.Errorf("sessions were revoked: %v", left)
			}

			if len(rig.told()) != 0 {
				t.Errorf("clients were told of a sign-out that did not happen: %+v", rig.told())
			}

			// And the retry, with the store back, ends it.
			b.do(door.method, door.path)

			if _, live, _ := rig.iss.SSO().Get(context.Background(), id); live {
				t.Error("the retry did not end the sign-in")
			}
		})
	}
}

// reauthenticate signs the browser in again through the provider, as
// `prompt=login` does, and reports the status of the callback.
func reauthenticate(t *testing.T, b *browser) int {
	t.Helper()

	where := b.authorize("&prompt=login")
	if !strings.Contains(where, "/login/google/start") {
		t.Fatalf("prompt=login went to %q, want the provider", where)
	}

	_, toProvider, _ := b.do(http.MethodGet, where)
	state := toProvider[strings.Index(toProvider, "state=")+len("state="):]
	status, _, _ := b.do(http.MethodGet, "/login/google/callback?code=x&state="+state)

	return status
}

// L4. An interactive sign-in from a browser that already holds one ends the
// previous sign-in: the new cookie replaces it in the browser, and what no
// browser holds must not stay valid for anybody who copied it.
func TestANewSignInEndsTheSignInTheBrowserHeld(t *testing.T) {
	t.Parallel()

	rig := newSSORig(t, issuer.Config{})
	b, oldID := rig.signedInBrowser(t)
	oldCookie := b.cookies[issuer.SSOCookieName]

	if status := reauthenticate(t, b); status != http.StatusFound {
		t.Fatalf("callback: %d", status)
	}

	newCookie := b.cookies[issuer.SSOCookieName]
	if newCookie == "" || newCookie == oldCookie {
		t.Fatalf("the browser holds %q after signing in again", newCookie)
	}

	ctx := context.Background()

	if _, found, _ := rig.state.State.Get(ctx, ssoPointerPrefix+ssoHash(oldCookie)); found {
		t.Error("the previous sign-in's pointer survived")
	}

	if _, live, _ := rig.iss.SSO().Get(ctx, oldID); live {
		t.Error("the previous sign-in's record survived")
	}

	if listed, _ := rig.iss.SSO().List(ctx, ssoEmail); len(listed) != 1 {
		t.Errorf("%d sign-ins listed, want only the new one", len(listed))
	}

	// A copy of the old cookie is worth nothing; the new one works.
	copied := newBrowser(t, rig.server)
	copied.cookies[issuer.SSOCookieName] = oldCookie

	if proves(copied) {
		t.Error("the replaced cookie still signs in")
	}

	if !proves(b) {
		t.Error("the new cookie does not sign in")
	}
}

// L4. A sign-in that worked but could not be filed leaves the browser with no
// cookie: the one it came with names a sign-in that may be somebody else's.
func TestAFailedBeginClearsTheCookieTheBrowserCameWith(t *testing.T) {
	t.Parallel()

	rig := newSSORig(t, issuer.Config{})
	b, _ := rig.signedInBrowser(t)

	rig.state.setFailures(false, func(key string) bool { return strings.HasPrefix(key, ssoRecordPrefix) })
	reauthenticate(t, b)
	rig.state.setFailures(false, nil)

	if got := b.cookies[issuer.SSOCookieName]; got != "" {
		t.Errorf("the browser still holds %q after a sign-in that could not be filed", got)
	}

	if proves(b) {
		t.Error("the browser is signed in by a cookie it should have lost")
	}

}
