package issuer_test

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/zitadel/oidc/v3/pkg/oidc"

	"github.com/truvity/sluis/internal/demo"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/policy"
)

// A silent sign-in reads the browser's sign-in and then completes the
// request; a sign-out in another tab can land in between, or anywhere before
// the relying party redeems the code. The sign-out ends the sessions filed
// under the sign-in and then the sign-in itself, so the code must open
// nothing afterwards: a session filed under a sign-in that no longer exists
// outlives the sign-out the person just made, and no later sign-out reaches
// it. The same for a client that asked for `openid` alone, which holds no
// session but would be handed an ID token it can open a session of its own
// with, under a sign-in that will never announce its end.
func TestACodeCompletedUnderASignInThatHasSinceEndedOpensNothing(t *testing.T) {
	t.Parallel()

	// "foo" is no scope the client may ask for, so the library drops it
	// and the grant carries none at all: no session, and no claims for
	// the ID token, so nothing past the access token reads the sign-in.
	for _, scope := range []string{"openid offline_access", "openid", "foo"} {
		t.Run(scope, func(t *testing.T) {
			t.Parallel()

			rig := newSSORig(t, issuer.Config{})
			b, _ := rig.signedInBrowser(t)
			over := map[string]string{"scope": scope}

			// The path itself works: a code completed under a sign-in that
			// stands is redeemed.
			first := b.authorizeWith(over, "")
			if strings.Contains(first, "/login/") {
				t.Fatalf("the silent sign-in went to %q", first)
			}

			if tokens := redeem(t, b, first); tokens["id_token"] == nil {
				t.Fatalf("the first redemption issued no ID token: %v", tokens)
			}

			opened := rig.sessionsOf(ssoEmail)

			// A second request completes silently under the same sign-in...
			sentTo := b.authorizeWith(over, "")
			if strings.Contains(sentTo, "/login/") {
				t.Fatalf("the second silent sign-in went to %q", sentTo)
			}

			// ...and the person signs out in another tab before the
			// relying party redeems its code.
			tab := newBrowser(t, rig.server)
			tab.cookies = maps.Clone(b.cookies)

			if status, _, _ := tab.do(http.MethodGet, "/logout"); status != http.StatusFound {
				t.Fatalf("sign-out: %d", status)
			}

			if n := rig.sessionsOf(ssoEmail); n != 0 {
				t.Fatalf("%d sessions survived the sign-out itself", n)
			}

			status, body := redeemAnswer(t, b, sentTo)
			if status != http.StatusBadRequest || body["error"] != "invalid_grant" {
				t.Errorf("redeeming after the sign-out = %d %v, want 400 invalid_grant", status, body["error"])
			}

			if body["id_token"] != nil || body["refresh_token"] != nil {
				t.Error("the redemption after the sign-out issued an ID token or a refresh token")
			}

			if n := rig.sessionsOf(ssoEmail); n != 0 {
				t.Errorf("%d sessions opened under a sign-in that had ended (%d before the sign-out)", n, opened)
			}
		})
	}
}

// A sign-out ends the sign-in BEFORE it revokes and announces. It used to
// end it last, so the sign-in stood through the revocation and every
// Back-Channel Logout -- seconds each -- and a code redeemed in that
// window opened a session after the revocation had listed them, which no
// sign-out could reach any more.
func TestACodeRedeemedWhileASignOutIsAnnouncingOpensNothing(t *testing.T) {
	t.Parallel()

	rig := newSSORig(t, issuer.Config{})
	b, _ := rig.signedInBrowser(t)

	sentTo := b.authorizeWith(map[string]string{"scope": "openid offline_access"}, "")
	if strings.Contains(sentTo, "/login/") {
		t.Fatalf("the silent sign-in went to %q", sentTo)
	}

	var (
		once   sync.Once
		status int
		body   map[string]any
	)

	rig.mu.Lock()
	rig.duringAnnounce = func() {
		once.Do(func() { status, body = redeemAnswer(t, b, sentTo) })
	}
	rig.mu.Unlock()

	tab := newBrowser(t, rig.server)
	tab.cookies = maps.Clone(b.cookies)

	if code, _, _ := tab.do(http.MethodGet, "/logout"); code != http.StatusFound {
		t.Fatalf("sign-out: %d", code)
	}

	if status == 0 {
		t.Fatal("the sign-out announced nothing, so the code was never redeemed during it")
	}

	if status != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Errorf("redeeming while the sign-out announced = %d %v, want 400 invalid_grant", status, body["error"])
	}

	if n := rig.sessionsOf(ssoEmail); n != 0 {
		t.Errorf("%d sessions outlived the sign-out they were opened during", n)
	}
}

// A code redeemed while its sign-in ends is held to the sign-in AFTER its
// session is filed: one that ends between the code's first look and the
// session's write is found ended by the second, and the session goes.
func TestASignInEndingWhileTheSessionIsFiledLeavesNoSession(t *testing.T) {
	t.Parallel()

	rig := newSSORig(t, issuer.Config{})
	b, id := rig.signedInBrowser(t)

	sentTo := b.authorizeWith(map[string]string{"scope": "openid offline_access"}, "")
	if strings.Contains(sentTo, "/login/") {
		t.Fatalf("the silent sign-in went to %q", sentTo)
	}

	var once sync.Once

	rig.state.setOnWrite(func(key string) {
		if strings.HasPrefix(key, "issuer:session:") {
			once.Do(func() {
				if err := rig.iss.SSO().End(context.Background(), id); err != nil {
					t.Errorf("end the sign-in: %v", err)
				}
			})
		}
	})

	status, body := redeemAnswer(t, b, sentTo)
	if status != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Errorf("redeeming as the sign-in ended = %d %v, want 400 invalid_grant", status, body["error"])
	}

	if n := rig.sessionsOf(ssoEmail); n != 0 {
		t.Errorf("%d sessions filed under a sign-in that ended as they were written", n)
	}
}

// Ending a sign-in is not cancelled with the request that asked for it: a
// browser that goes away once the sign-in has ended must not leave what it
// opened running under a sign-in nothing can end any more.
func TestASignOutWhoseBrowserGoesAwayStillEndsItsSessions(t *testing.T) {
	t.Parallel()

	rig := newSSORig(t, issuer.Config{})
	b, id := rig.signedInBrowser(t)
	rig.open(t, ssoEmail, "argocd", id)
	rig.state.honourCancel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rig.state.setOnWrite(func(key string) {
		if key == "issuer:sso:"+id {
			cancel() // the browser goes away as the sign-in ends
		}
	})

	request := httptest.NewRequestWithContext(ctx, http.MethodGet, "/logout", nil)
	request.AddCookie(&http.Cookie{Name: issuer.SSOCookieName, Value: b.cookies[issuer.SSOCookieName]})

	deps := issuer.SignInDeps{
		SSO: rig.iss.SSO(), Issuer: rig.iss,
		Announce: func(_ context.Context, sessions []issuer.Session) {
			rig.mu.Lock()
			defer rig.mu.Unlock()

			rig.announced = append(rig.announced, sessions...)
		},
	}

	if err := issuer.SignOut(deps, httptest.NewRecorder(), request); err != nil {
		t.Fatalf("sign-out: %v", err)
	}

	if ctx.Err() == nil {
		t.Fatal("the request was never cancelled; the test proves nothing")
	}

	if _, live, _ := rig.iss.SSO().Get(context.Background(), id); live {
		t.Error("the sign-in survived")
	}

	if n := rig.sessionsOf(ssoEmail); n != 0 {
		t.Errorf("%d sessions outlived a sign-out whose browser went away", n)
	}

	if !rig.announcedTo("argocd") {
		t.Errorf("the client was not told: %+v", rig.told())
	}
}

// The same for a step-up: the old sign-in ends, and its sessions are still
// moved to the new one when the browser goes away in between.
func TestAStepUpWhoseBrowserGoesAwayStillCarriesItsSessions(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	rig := newSSORig(t, issuer.Config{})
	b, oldID := rig.signedInBrowser(t)
	rig.open(t, ssoEmail, "argocd", oldID)

	next, _, err := rig.iss.SSO().Begin(ctx, ssoEmail, "google")
	if err != nil {
		t.Fatal(err)
	}

	rig.state.honourCancel()

	requestCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	rig.state.setOnWrite(func(key string) {
		if key == "issuer:sso:"+oldID {
			cancel()
		}
	})

	request := httptest.NewRequestWithContext(requestCtx, http.MethodGet, "/login/google/callback", nil)
	request.AddCookie(&http.Cookie{Name: issuer.SSOCookieName, Value: b.cookies[issuer.SSOCookieName]})

	issuer.EndPreviousForTest(issuer.SignInDeps{SSO: rig.iss.SSO(), Issuer: rig.iss}, request,
		issuer.Authenticated{Subject: ssoEmail, SSO: next.ID})

	if requestCtx.Err() == nil {
		t.Fatal("the request was never cancelled; the test proves nothing")
	}

	if _, live, _ := rig.iss.SSO().Get(ctx, oldID); live {
		t.Error("the old sign-in survived")
	}

	under, err := rig.iss.Sessions().List(ctx, issuer.Query{Identity: ssoEmail, SSO: next.ID})
	if err != nil || len(under) != 1 {
		t.Errorf("sessions under the new sign-in = %+v, %v; want the argocd one carried over", under, err)
	}
}

// A code that opens no session and asks for no scope at all -- every scope
// it named was one the client may not ask for -- is held to its sign-in
// before the access token is minted. Nothing after the access token can be
// relied on to read it: with no scope there are no claims for the ID token,
// and the hook that reads the sign-in for an `openid`-only code is not
// called for none.
func TestAnAccessTokenIsNotMintedUnderAnEndedSignIn(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	declared, err := policy.Parse([]byte(demo.Policy))
	if err != nil {
		t.Fatal(err)
	}

	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatal(err)
	}

	shared := issuer.NewMemoryState()
	iss := issuer.New(issuer.Config{URL: "https://issuer.example"}, set, &fakeDirectory{
		standing: map[string]issuer.Standing{
			"ada@north.example": {Found: true, Authoritative: true, Groups: []string{"directory-admins@north.example"}},
		},
	}, shared)

	storage, err := issuer.NewTestStorage(iss, fakeVerifier{}, nil, nil, nil, shared)
	if err != nil {
		t.Fatal(err)
	}

	signIn, _, err := iss.SSO().Begin(ctx, "ada@north.example", "google")
	if err != nil {
		t.Fatal(err)
	}

	for _, ended := range []bool{false, true} {
		id, err := storage.CreateSignedInAuthRequestForTest(ctx, "ada@north.example", "console", signIn.ID)
		if err != nil {
			t.Fatal(err)
		}

		code := "code-" + id
		if ended {
			code += "-ended"

			if err = iss.SSO().End(ctx, signIn.ID); err != nil {
				t.Fatal(err)
			}
		}

		if err = storage.SaveAuthCode(ctx, id, code); err != nil {
			t.Fatal(err)
		}

		found, err := storage.AuthRequestByCode(ctx, code)
		if err != nil {
			t.Fatal(err)
		}

		_, _, err = storage.CreateAccessToken(ctx, found)

		var refused *oidc.Error
		switch {
		case !ended && err != nil:
			t.Fatalf("an access token under a standing sign-in: %v", err)
		case ended && (!errors.As(err, &refused) || refused.ErrorType != oidc.InvalidGrant):
			t.Errorf("an access token under an ended sign-in = %v, want invalid_grant", err)
		}
	}
}
