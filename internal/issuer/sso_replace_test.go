package issuer_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/audit/sdk/record"

	accessissuerv1 "github.com/truvity/sluis/gen/accessissuer/v1"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/issuer"
)

// The audit scopes of the two ends no person asked for. Held to the audit
// package's constants, so a rename there is one edit here.
const (
	scopePreUpgrade = audit.ScopePreUpgradeCookie
	scopeReplaced   = audit.ScopeSignInReplaced
)

// open files a session under a sign-in, the way a token grant does.
func (g *ssoRig) open(t *testing.T, identity, client, signIn string) {
	t.Helper()

	if _, err := g.iss.Sessions().Record(context.Background(), issuer.Opened{
		Identity: identity, ClientID: client, How: issuer.HowCode, Token: "t-" + identity + client + signIn, SSO: signIn,
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
}

func (g *ssoRig) sessionsOf(identity string) int {
	list, err := g.iss.Sessions().List(context.Background(), issuer.Query{Identity: identity})
	if err != nil {
		panic(err)
	}

	return len(list)
}

func (g *ssoRig) pointerLive(secret string) bool {
	_, found, _ := g.state.State.Get(context.Background(), ssoPointerPrefix+ssoHash(secret))

	return found
}

func (g *ssoRig) announcedTo(client string) bool {
	told := g.told()
	for i := range told {
		if told[i].ClientID == client {
			return true
		}
	}

	return false
}

func fieldOf(r *record.Record, name string) any {
	return r.GetData().GetFields()[name].AsInterface()
}

// 1. Another person signs in in the same browser: what the first opened is
// ended as a sign-out would end it, the clients are told, the trail says who
// did it to whom, and the second person's cookie works.
func TestASecondPersonInTheSameBrowserEndsTheFirstsSignIn(t *testing.T) {
	t.Parallel()

	rig := newSSORig(t, issuer.Config{})
	b, idA := rig.signedInBrowser(t)
	cookieA := b.cookies[issuer.SSOCookieName]
	ctx := context.Background()

	rig.open(t, ssoEmail, "argocd", idA)
	rig.open(t, ssoEmail, "kargo", idA)

	if err := rig.iss.SSO().Involve(ctx, idA, "grafana"); err != nil { // openid only
		t.Fatal(err)
	}

	rig.as(ssoBob)

	if status := reauthenticate(t, b); status != http.StatusFound {
		t.Fatalf("callback: %d", status)
	}

	if n := rig.sessionsOf(ssoEmail); n != 0 {
		t.Errorf("%d of the first person's sessions survived", n)
	}

	if _, live, _ := rig.iss.SSO().Get(ctx, idA); live {
		t.Error("the first person's sign-in survived")
	}

	if rig.pointerLive(cookieA) {
		t.Error("the first person's pointer survived")
	}

	// The clients set itself is not asserted gone: State.Delete does not
	// reach a set, so [SSO.End] leaves it to expire with the sign-in's
	// lifetime. What matters here is that its members were announced.
	for _, client := range []string{"argocd", "kargo", "grafana"} {
		if !rig.announcedTo(client) {
			t.Errorf("%s was not told: %+v", client, rig.told())
		}
	}

	revoked := rig.trail.Find("roster.session.revoked")
	if len(revoked) != 1 {
		t.Fatalf("%d roster.session.revoked records, want 1: %v", len(revoked), rig.trail.Actions())
	}

	got := revoked[0]
	if got.GetActor().GetKind() != "person" || got.GetActor().GetId() != ssoBob {
		t.Errorf("actor = %v, want %s", got.GetActor(), ssoBob)
	}

	if got.GetSubject().GetKind() != "person" || got.GetSubject().GetId() != ssoEmail {
		t.Errorf("subject = %v, want %s", got.GetSubject(), ssoEmail)
	}

	if fieldOf(got, "scope") != scopeReplaced || fieldOf(got, "ended") != float64(2) {
		t.Errorf("data = %v, want scope %q and 2 ended", got.GetData(), scopeReplaced)
	}

	if n := len(rig.trail.Find("roster.session.ended")); n != 0 {
		t.Errorf("a sign-out was recorded for somebody who did not sign out (%d)", n)
	}

	if _, to, _ := b.do(http.MethodGet, "/account"); to != "/console/#/people/"+ssoBob {
		t.Errorf("the second person's cookie: /account went to %q", to)
	}
}

// 2. The same person signing in again keeps what they opened: the old record
// and pointer go, nobody is told, nothing is recorded.
func TestTheSamePersonSigningInAgainKeepsTheirSessions(t *testing.T) {
	t.Parallel()

	for _, how := range []string{"&prompt=login", "&max_age=0"} {
		t.Run(how, func(t *testing.T) {
			t.Parallel()

			rig := newSSORig(t, issuer.Config{})
			b, oldID := rig.signedInBrowser(t)
			oldCookie := b.cookies[issuer.SSOCookieName]
			rig.open(t, ssoEmail, "argocd", oldID)

			where := b.authorize(how)
			if !strings.Contains(where, "/login/google/start") {
				t.Fatalf("went to %q", where)
			}

			_, toProvider, _ := b.do(http.MethodGet, where)
			state := toProvider[strings.Index(toProvider, "state=")+len("state="):]

			if status, _, _ := b.do(http.MethodGet, "/login/google/callback?code=x&state="+state); status != http.StatusFound {
				t.Fatalf("callback: %d", status)
			}

			if _, live, _ := rig.iss.SSO().Get(context.Background(), oldID); live {
				t.Error("the old record survived")
			}

			if rig.pointerLive(oldCookie) {
				t.Error("the old pointer survived")
			}

			if b.cookies[issuer.SSOCookieName] == oldCookie {
				t.Error("the cookie was not replaced")
			}

			if n := rig.sessionsOf(ssoEmail); n != 1 {
				t.Errorf("%d sessions, want the 1 they opened to stay", n)
			}

			if len(rig.told()) != 0 {
				t.Errorf("clients were told: %+v", rig.told())
			}

			for _, action := range []string{"roster.session.revoked", "roster.session.ended"} {
				if n := len(rig.trail.Find(action)); n != 0 {
					t.Errorf("%d %s records for a person signing in again", n, action)
				}
			}
		})
	}
}

// 3. Console: revoke one browser by a sign-in id.
func TestRevokingOneBrowserByAnIDThatIsGone(t *testing.T) {
	t.Parallel()

	rig := newSSORig(t, issuer.Config{})
	svc := rig.service()
	ada := "ada@north.example|"

	t.Run("replaced sign-in", func(t *testing.T) {
		// The record is gone; the sessions filed under its id are not.
		rig.open(t, ssoEmail, "argocd", "replaced-id")
		rig.open(t, ssoEmail, "kargo", "replaced-id")
		rig.open(t, ssoEmail, "grafana", "another-browser")

		ended, err := revoke(t, svc, ada, &accessissuerv1.RevokeSessionsRequest{Identity: ssoEmail, Sso: "replaced-id"})
		if err != nil || ended != 2 {
			t.Fatalf("ended %d, %v; want the 2 filed under that id", ended, err)
		}

		if n := rig.sessionsOf(ssoEmail); n != 1 {
			t.Errorf("%d sessions left, want only the other browser's", n)
		}
	})

	t.Run("another identity's sign-in", func(t *testing.T) {
		eli, _, err := rig.iss.SSO().Begin(context.Background(), "eli@south.example", "google")
		if err != nil {
			t.Fatal(err)
		}

		rig.open(t, "eli@south.example", "argocd", eli.ID)
		rig.open(t, ssoEmail, "vault", eli.ID)

		before := rig.sessionsOf(ssoEmail) + rig.sessionsOf("eli@south.example")

		ended, err := revoke(t, svc, ada, &accessissuerv1.RevokeSessionsRequest{Identity: ssoEmail, Sso: eli.ID})
		if err != nil || ended != 0 {
			t.Fatalf("ended %d, %v; want 0", ended, err)
		}

		if after := rig.sessionsOf(ssoEmail) + rig.sessionsOf("eli@south.example"); after != before {
			t.Errorf("sessions went from %d to %d", before, after)
		}

		if _, live, _ := rig.iss.SSO().Get(context.Background(), eli.ID); !live {
			t.Error("the other person's sign-in was ended")
		}
	})
}

// recoveryRig is the recovery door over a real issuer, with a storage that
// completes or does not, and a browser that may already hold a sign-in.
type recoveryRig struct {
	mux   http.Handler
	iss   *issuer.Issuer
	state string
}

type completing struct {
	stubPending
	err error
}

func (c completing) Complete(context.Context, string, issuer.Authenticated) error { return c.err }

func newRecoveryRig(t *testing.T, complete error) (*recoveryRig, *ssoRig) {
	t.Helper()

	// The rig's issuer and stores, with its own sign-in routes in front.
	g := newSSORig(t, issuer.Config{})
	codec := access.NewStateCodec(make([]byte, 32), time.Minute)
	state, err := codec.IssueAs(access.Binding{Bind: "req-recovery", Owner: access.RecoveryPurpose})
	if err != nil {
		t.Fatal(err)
	}

	r := &recoveryRig{iss: g.iss, state: state}
	mux := http.NewServeMux()
	issuer.SignInRoutes(mux, issuer.SignInDeps{
		Recovery: acceptingRecovery{}, Storage: completing{err: complete}, State: codec,
		SSO: g.iss.SSO(), Issuer: g.iss, Log: slog.New(slog.DiscardHandler),
		Return: func(context.Context, string) string { return "/done" },
		Announce: func(_ context.Context, sessions []issuer.Session) {
			g.mu.Lock()
			defer g.mu.Unlock()

			g.announced = append(g.announced, sessions...)
		},
	})
	r.mux = mux

	return r, g
}

// recover posts the recovery form, with the cookie the browser holds.
func (r *recoveryRig) recover(cookie string) *httptest.ResponseRecorder {
	form := url.Values{"state": {r.state}, "proof": {"a-good-token"}}
	request := httptest.NewRequest(http.MethodPost, "/login/recovery", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// The browser the form was served to holds its state as the recovery
	// cookie ([issuer.SignInRoutes]' chooser sets it).
	request.AddCookie(&http.Cookie{Name: access.RecoveryCookieName, Value: r.state})

	if cookie != "" {
		request.AddCookie(&http.Cookie{Name: issuer.SSOCookieName, Value: cookie})
	}

	response := httptest.NewRecorder()
	r.mux.ServeHTTP(response, request)

	return response
}

func ssoCookieSet(response *httptest.ResponseRecorder) (value string, set bool) {
	for _, c := range response.Result().Cookies() {
		if c.Name == issuer.SSOCookieName {
			return c.Value, true
		}
	}

	return "", false
}

const recoverySubject = "cluster:k8s:ops:recovery"

// 4. A recovery refused for want of its record leaves the browser's earlier
// sign-in alone.
func TestARefusedRecoveryLeavesTheEarlierSignInAlone(t *testing.T) {
	t.Parallel()

	r, g := newRecoveryRig(t, fmt.Errorf("%w: S3 refused the put", issuer.ErrUnaudited))
	ctx := context.Background()

	earlier, secret, err := g.iss.SSO().Begin(ctx, ssoEmail, "google")
	if err != nil {
		t.Fatal(err)
	}

	g.open(t, ssoEmail, "argocd", earlier.ID)

	response := r.recover(secret)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("recovery = %d", response.Code)
	}

	if value, set := ssoCookieSet(response); set {
		t.Errorf("the refused recovery set or cleared the SSO cookie (%q)", value)
	}

	if _, live, _ := g.iss.SSO().Resolve(ctx, secret); !live {
		t.Error("the earlier cookie stopped resolving")
	}

	if got, live, _ := g.iss.SSO().Get(ctx, earlier.ID); !live || !got.AuthTime.Equal(earlier.AuthTime) || got.ID != earlier.ID {
		t.Errorf("the earlier record changed: %+v live=%v", got, live)
	}

	open, err := g.iss.SSO().List(ctx, "")
	if err != nil || len(open) != 1 || open[0].ID != earlier.ID {
		t.Errorf("sign-ins = %v, %v; want only the earlier one (the new one ended)", open, err)
	}

	if g.sessionsOf(ssoEmail) != 1 || len(g.told()) != 0 {
		t.Error("the earlier sign-in's sessions were touched")
	}
}

// 5. A recovery that succeeds replaces the earlier sign-in.
func TestASuccessfulRecoveryReplacesTheEarlierSignIn(t *testing.T) {
	t.Parallel()

	t.Run("a different identity", func(t *testing.T) {
		t.Parallel()

		r, g := newRecoveryRig(t, nil)
		ctx := context.Background()
		earlier, secret, _ := g.iss.SSO().Begin(ctx, ssoEmail, "google")
		g.open(t, ssoEmail, "argocd", earlier.ID)

		response := r.recover(secret)
		if response.Code >= 400 {
			t.Fatalf("recovery = %d %s", response.Code, response.Body.String())
		}

		fresh, set := ssoCookieSet(response)
		if !set || fresh == "" || fresh == secret {
			t.Fatalf("the new cookie = %q, set=%v", fresh, set)
		}

		if session, live, _ := g.iss.SSO().Resolve(ctx, fresh); !live || session.Identity != recoverySubject {
			t.Errorf("the new cookie resolves to %+v live=%v", session, live)
		}

		if _, live, _ := g.iss.SSO().Get(ctx, earlier.ID); live || g.pointerLive(secret) {
			t.Error("the earlier sign-in survived")
		}

		if g.sessionsOf(ssoEmail) != 0 || !g.announcedTo("argocd") {
			t.Errorf("the cascade did not run: %d sessions, told %+v", g.sessionsOf(ssoEmail), g.told())
		}
	})

	t.Run("the same identity", func(t *testing.T) {
		t.Parallel()

		r, g := newRecoveryRig(t, nil)
		ctx := context.Background()
		earlier, secret, _ := g.iss.SSO().Begin(ctx, recoverySubject, "recovery")
		g.open(t, recoverySubject, "argocd", earlier.ID)

		response := r.recover(secret)

		fresh, set := ssoCookieSet(response)
		if !set || fresh == "" || fresh == secret {
			t.Fatalf("the new cookie = %q, set=%v", fresh, set)
		}

		if _, live, _ := g.iss.SSO().Get(ctx, earlier.ID); live || g.pointerLive(secret) {
			t.Error("the earlier sign-in survived")
		}

		if g.sessionsOf(recoverySubject) != 1 || len(g.told()) != 0 {
			t.Error("the same person's sessions were ended")
		}
	})
}

// 6. A provider callback whose request cannot be completed still hands over
// the cookie and ends the sign-in the browser held: the person did
// authenticate.
func TestACallbackThatCannotCompleteStillReplacesTheSignIn(t *testing.T) {
	t.Parallel()

	for name, fail := range map[string]error{
		"not waiting":  errors.New("the request is not waiting"),
		"not entitled": fmt.Errorf("%w: nothing for you", issuer.ErrNotEntitled),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			g := newSSORig(t, issuer.Config{})
			ctx := context.Background()
			earlier, secret, _ := g.iss.SSO().Begin(ctx, ssoEmail, "google")
			g.open(t, ssoEmail, "argocd", earlier.ID)

			codec := access.NewStateCodec(make([]byte, 32), time.Minute)
			state, err := codec.Issue("req-1")
			if err != nil {
				t.Fatal(err)
			}

			g.as(ssoBob)

			mux := http.NewServeMux()
			issuer.SignInRoutes(mux, issuer.SignInDeps{
				Providers: []issuer.SignIn{g.who}, Storage: completing{err: fail}, State: codec,
				SSO: g.iss.SSO(), Issuer: g.iss, Log: slog.New(slog.DiscardHandler),
				Return: func(context.Context, string) string { return "/done" },
				Announce: func(_ context.Context, sessions []issuer.Session) {
					g.mu.Lock()
					defer g.mu.Unlock()

					g.announced = append(g.announced, sessions...)
				},
			})

			request := httptest.NewRequest(http.MethodGet, "/login/google/callback?code=x&state="+url.QueryEscape(state), nil)
			request.AddCookie(access.LoginCookie(state, false, time.Minute))
			request.AddCookie(&http.Cookie{Name: issuer.SSOCookieName, Value: secret})
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)

			fresh, set := ssoCookieSet(response)
			if !set || fresh == "" || fresh == secret {
				t.Fatalf("answered %d; the new cookie = %q, set=%v", response.Code, fresh, set)
			}

			if session, live, _ := g.iss.SSO().Resolve(ctx, fresh); !live || session.Identity != ssoBob {
				t.Errorf("the new cookie resolves to %+v live=%v", session, live)
			}

			if _, live, _ := g.iss.SSO().Get(ctx, earlier.ID); live || g.pointerLive(secret) {
				t.Error("the previous sign-in survived")
			}

			if g.sessionsOf(ssoEmail) != 0 || !g.announcedTo("argocd") {
				t.Error("the previous person's sign-in was not ended with its cascade")
			}
		})
	}
}

// 7. Signing out with a pre-upgrade cookie is recorded as what it is: an
// anonymous presenter revoking the person's sessions, never the person
// signing out.
func TestAPreUpgradeSignOutIsAuditedAsAnonymous(t *testing.T) {
	t.Parallel()

	rig := newSSORig(t, issuer.Config{})
	preUpgrade(t, rig, "old-sign-in")

	b := newBrowser(t, rig.server)
	b.cookies[issuer.SSOCookieName] = "old-sign-in"
	b.do(http.MethodGet, "/logout")

	revoked := rig.trail.Find("roster.session.revoked")
	if len(revoked) != 1 {
		t.Fatalf("%d roster.session.revoked records, want 1: %v", len(revoked), rig.trail.Actions())
	}

	got := revoked[0]
	if got.GetActor().GetKind() != "anonymous" {
		t.Errorf("actor = %v, want anonymous", got.GetActor())
	}

	if got.GetSubject().GetKind() != "person" || got.GetSubject().GetId() != ssoEmail {
		t.Errorf("subject = %v, want the person", got.GetSubject())
	}

	if fieldOf(got, "scope") != scopePreUpgrade || fieldOf(got, "ended") != float64(1) {
		t.Errorf("data = %v, want scope %q and 1 ended", got.GetData(), scopePreUpgrade)
	}

	for _, ended := range rig.trail.Find("roster.session.ended") {
		if ended.GetActor().GetId() == ssoEmail || ended.GetSubject().GetId() == ssoEmail {
			t.Errorf("the person is recorded as having signed out: %v", ended)
		}
	}
}

// 8. /end_session when the library succeeded and the store cannot be read:
// a 503 with no redirect, and the sign-out-failed answer.
func TestEndSessionWithAnUnreadableStoreAnswers503WithoutARedirect(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ accept, want string }{
		{"text/html", "nothing was ended"},
		{"application/json", `"temporarily_unavailable"`},
	} {
		t.Run(tc.accept, func(t *testing.T) {
			t.Parallel()

			rig := newSSORig(t, issuer.Config{})
			b, id := rig.signedInBrowser(t)

			// Only the cookie's pointer is unreadable: the library's own
			// reads succeed, so it has answered by the time sign-out asks.
			rig.state.setReadFailure(func(key string) bool { return strings.HasPrefix(key, ssoPointerPrefix) })
			status, location, body := endSessionRequest(t, b, "", tc.accept)
			rig.state.setReadFailure(nil)

			if status != http.StatusServiceUnavailable {
				t.Fatalf("status = %d (%s), want 503", status, body)
			}

			if location != "" {
				t.Errorf("Location = %q on a 503", location)
			}

			if !strings.Contains(body, tc.want) {
				t.Errorf("body = %q, want it to hold %q", body, tc.want)
			}

			if _, live, _ := rig.iss.SSO().Get(context.Background(), id); !live {
				t.Error("the sign-in ended")
			}
		})
	}
}

// 9. A normal sign-out is still recorded as the person signing out.
func TestANormalSignOutIsAuditedAsThePerson(t *testing.T) {
	t.Parallel()

	rig := newSSORig(t, issuer.Config{})
	b, id := rig.signedInBrowser(t)
	rig.open(t, ssoEmail, "argocd", id)
	b.do(http.MethodGet, "/logout")

	ended := rig.trail.Find("roster.session.ended")
	if len(ended) != 1 {
		t.Fatalf("%d roster.session.ended records: %v", len(ended), rig.trail.Actions())
	}

	if a := ended[0].GetActor(); a.GetKind() != "person" || a.GetId() != ssoEmail {
		t.Errorf("actor = %v, want the person", a)
	}

	if fieldOf(ended[0], "ended") != float64(1) {
		t.Errorf("data = %v", ended[0].GetData())
	}

	if n := len(rig.trail.Find("roster.session.revoked")); n != 0 {
		t.Errorf("%d revoked records for a person's own sign-out", n)
	}

}
