package issuerapp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/demo"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/policy"
)

// The console on the issuer's origin asks the issuer's own question about a
// browser's sign-in: the absolute limit and the directory, the decisions the
// issuer's silent sign-in makes, from the same function. Before, it asked
// only whether the cookie named a live sign-in, and a sign-in past its
// absolute limit, or one whose person the directory had suspended, went on
// opening the console -- the surface where client secrets are rotated and
// sessions revoked -- for the rest of the sign-in's own lifetime.

const consolePerson = "ada@north.example"

// standingDirectory is the directory's answer about one person, which a
// test changes as it goes.
type standingDirectory struct {
	mu       sync.Mutex
	standing issuer.Standing
	err      error
	calls    int
}

func (d *standingDirectory) ResolveUser(context.Context, string) (issuer.Standing, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	return d.standing, d.err
}

func (d *standingDirectory) set(standing issuer.Standing, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.standing, d.err = standing, err
}

func (d *standingDirectory) asked() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

func admitted() issuer.Standing {
	return issuer.Standing{Found: true, Authoritative: true, Groups: []string{"engineering@north.example"}}
}

// consoleUnderTest is the console's reader of the browser's sign-in, as
// issuerapp assembles it, over an issuer with a directory a test controls.
type consoleUnderTest struct {
	iss  *issuer.Issuer
	dir  *standingDirectory
	read func(http.ResponseWriter, *http.Request) (access.Principal, bool)

	mu        sync.Mutex
	announced []issuer.Session
}

func newConsoleUnderTest(t *testing.T, cfg issuer.Config) *consoleUnderTest {
	t.Helper()
	declared, err := policy.Parse([]byte(demo.Policy))
	if err != nil {
		t.Fatal(err)
	}
	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatal(err)
	}
	cfg.URL = "https://access.example"
	c := &consoleUnderTest{dir: &standingDirectory{standing: admitted()}}
	c.iss = issuer.New(cfg, set, c.dir, issuer.NewMemoryState())
	c.read = signedIn(issuer.SignedInReader(c.iss, nil, issuer.SignInDeps{
		Secure: true,
		// What a sign-in that ends tells the clients that held it (OIDC
		// Back-Channel Logout), captured rather than sent.
		Announce: func(_ context.Context, ended []issuer.Session) {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.announced = append(c.announced, ended...)
		},
	}))
	if c.read == nil {
		t.Fatal("no signed-in reader for an issuer with a sign-in store")
	}
	return c
}

// signIn begins a sign-in for identity through a provider, authenticated
// at authTime, and returns its id and the secret the browser's cookie
// carries.
func (c *consoleUnderTest) signIn(t *testing.T, identity string, authTime time.Time) (id, secret string) {
	t.Helper()
	return c.signInBy(t, identity, "google", authTime)
}

// signInBy is [consoleUnderTest.signIn] made by how: a provider's kind, or
// [issuer.RecoveryHow].
func (c *consoleUnderTest) signInBy(t *testing.T, identity, how string, authTime time.Time) (id, secret string) {
	t.Helper()
	c.iss.SSO().SetClock(func() time.Time { return authTime })
	defer c.iss.SSO().SetClock(time.Now)
	session, secret, err := c.iss.SSO().Begin(t.Context(), identity, how)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	return session.ID, secret
}

// ask is one console request carrying the browser's cookie.
func (c *consoleUnderTest) ask(secret string) (access.Principal, bool, *httptest.ResponseRecorder) {
	r := httptest.NewRequest(http.MethodGet, "/console/", nil)
	r.AddCookie(c.iss.SSO().Cookie(secret, true))
	w := httptest.NewRecorder()
	who, ok := c.read(w, r)
	return who, ok, w
}

func (c *consoleUnderTest) live(t *testing.T, id string) bool {
	t.Helper()
	_, live, err := c.iss.SSO().Get(t.Context(), id)
	if err != nil {
		t.Fatalf("get the sign-in: %v", err)
	}
	return live
}

func (c *consoleUnderTest) told() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.announced))
	for i := range c.announced {
		out = append(out, c.announced[i].ClientID)
	}
	return out
}

// cleared reports whether the answer cleared the browser's sign-in cookie.
func cleared(w *httptest.ResponseRecorder) bool {
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == access.CookieNameFor(issuer.SSOCookieName, true) && cookie.MaxAge < 0 {
			return true
		}
	}
	return false
}

// A sign-in past the absolute limit does not open the console, and it is
// ENDED, with the cascade the issuer's silent sign-in runs: the clients
// signed in under it are told.
func TestTheConsoleRefusesASignInPastTheAbsoluteLimitAndEndsIt(t *testing.T) {
	t.Parallel()
	c := newConsoleUnderTest(t, issuer.Config{AbsoluteLifetime: time.Hour})

	// Two hours ago, past the one-hour limit, and well inside the sign-in's
	// own twelve-hour lifetime: what ends it is the limit and nothing else.
	id, secret := c.signIn(t, consolePerson, time.Now().Add(-2*time.Hour))
	// A client signed in under it with `openid` alone, which only this
	// browser's sign-in knows about: told it ended only if the cascade ran.
	if err := c.iss.SSO().Involve(t.Context(), id, "argocd"); err != nil {
		t.Fatalf("involve: %v", err)
	}

	if who, ok, w := c.ask(secret); ok {
		t.Errorf("a sign-in past the absolute limit opened the console as %q", who.Subject)
	} else if !cleared(w) {
		t.Error("the browser's sign-in cookie was not cleared")
	}
	if c.live(t, id) {
		t.Error("the sign-in is still live: the absolute limit should have ended it")
	}
	if told := c.told(); !slices.Contains(told, "argocd") {
		t.Errorf("told %v, want argocd told its sign-in ended (Back-Channel Logout)", told)
	}
	// The limit decides before the directory is asked, as it does for a
	// silent sign-in.
	if n := c.dir.asked(); n != 0 {
		t.Errorf("the directory was asked %d times about a sign-in the limit had ended", n)
	}
}

// A person the directory no longer admits does not open the console, and
// their sign-in is ended, whichever way the directory stopped admitting
// them -- the same rule, from the same resolver and hold window, the
// issuer's silent sign-in applies.
func TestTheConsoleRefusesAPersonTheDirectoryNoLongerAdmits(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		standing issuer.Standing
		err      error
	}{
		"suspended": {standing: issuer.Standing{Found: true, Suspended: true, Authoritative: true}},
		"removed":   {standing: issuer.Standing{Found: false, Authoritative: true}},
		// Nothing held for them, so there is nothing for the hold window
		// to act on.
		"unvouched and never seen":   {standing: issuer.Standing{Found: true, Authoritative: false}},
		"unreachable and never seen": {err: errors.New("the directory did not answer")},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := newConsoleUnderTest(t, issuer.Config{})
			c.dir.set(tc.standing, tc.err)
			id, secret := c.signIn(t, consolePerson, time.Now())

			if who, ok, w := c.ask(secret); ok {
				t.Errorf("a person the directory does not admit opened the console as %q", who.Subject)
			} else if !cleared(w) {
				t.Error("the browser's sign-in cookie was not cleared")
			}
			if c.live(t, id) {
				t.Error("the sign-in is still live: the directory's refusal should have ended it")
			}
			// Refused once, refused for good: the next request has no
			// sign-in to read.
			if _, ok, _ := c.ask(secret); ok {
				t.Error("the ended sign-in's cookie opened the console on the next request")
			}
		})
	}
}

// Somebody the directory admits, within the limit, is signed in to the
// console and nothing is ended.
func TestTheConsoleAdmitsAPersonTheDirectoryAdmitsWithinTheLimit(t *testing.T) {
	t.Parallel()
	c := newConsoleUnderTest(t, issuer.Config{AbsoluteLifetime: time.Hour})
	id, secret := c.signIn(t, consolePerson, time.Now().Add(-30*time.Minute))

	who, ok, w := c.ask(secret)
	if !ok || who.Email != consolePerson || who.Source != access.SourceOIDC {
		t.Fatalf("the console read %+v, %v; want %s signed in", who, ok, consolePerson)
	}
	if cleared(w) {
		t.Error("an admitted sign-in's cookie was cleared")
	}
	if !c.live(t, id) {
		t.Error("an admitted sign-in was ended")
	}
	if n := c.dir.asked(); n != 1 {
		t.Errorf("the directory was asked %d times, want once", n)
	}
}

// A directory that cannot be reached does not end an admitted person's
// sign-in at once: the issuer's hold window answers from the last-known
// standing, exactly as it does for a silent sign-in, and only past it, or
// for someone it holds nothing for (above), does the sign-in end.
func TestTheConsoleHoldsThroughAnUnreachableDirectory(t *testing.T) {
	t.Parallel()
	c := newConsoleUnderTest(t, issuer.Config{})
	id, secret := c.signIn(t, consolePerson, time.Now())

	if _, ok, _ := c.ask(secret); !ok {
		t.Fatal("an admitted person was refused")
	}
	c.dir.set(issuer.Standing{}, errors.New("the directory did not answer"))
	if who, ok, _ := c.ask(secret); !ok || who.Email != consolePerson {
		t.Errorf("inside the hold window the console read %+v, %v; want the person held", who, ok)
	}
	if !c.live(t, id) {
		t.Error("a sign-in was ended inside the hold window")
	}
}

// A recovery sign-in has no directory to ask, as for a silent sign-in: it
// is still held to the absolute limit, and is not refused for having no
// address.
func TestARecoverySignInOpensTheConsoleWithoutTheDirectory(t *testing.T) {
	t.Parallel()
	c := newConsoleUnderTest(t, issuer.Config{AbsoluteLifetime: time.Hour})
	c.dir.set(issuer.Standing{}, errors.New("the directory did not answer"))
	subject := "system:serviceaccount:sluis:recovery"
	_, secret := c.signInBy(t, subject, issuer.RecoveryHow, time.Now())

	who, ok, _ := c.ask(secret)
	if !ok || who.Source != access.SourceRecovery || !strings.HasSuffix(who.Subject, ":recovery") {
		t.Errorf("a recovery sign-in read as %+v, %v; want a recovery principal", who, ok)
	}
	if n := c.dir.asked(); n != 0 {
		t.Errorf("the directory was asked %d times about a recovery sign-in", n)
	}

	_, old := c.signInBy(t, subject, issuer.RecoveryHow, time.Now().Add(-2*time.Hour))
	if _, ok, _ := c.ask(old); ok {
		t.Error("a recovery sign-in past the absolute limit opened the console")
	}
}

// Recovery is known by how the sign-in was made, not by what its subject
// looks like. A sign-in through an identity provider whose subject happens
// to read as a ServiceAccount is a person: the directory is asked, and it
// is never the recovery account, which is the global operator.
func TestAServiceAccountShapedSubjectIsNotRecovery(t *testing.T) {
	t.Parallel()
	subject := "system:serviceaccount:sluis:recovery"

	t.Run("the directory refuses it", func(t *testing.T) {
		t.Parallel()
		c := newConsoleUnderTest(t, issuer.Config{})
		c.dir.set(issuer.Standing{Found: false, Authoritative: true}, nil)
		id, secret := c.signIn(t, subject, time.Now())

		if who, ok, _ := c.ask(secret); ok {
			t.Errorf("a provider's ServiceAccount-shaped subject opened the console as %+v", who)
		}
		if n := c.dir.asked(); n != 1 {
			t.Errorf("the directory was asked %d times, want once", n)
		}
		if c.live(t, id) {
			t.Error("the sign-in the directory refused is still live")
		}
	})

	t.Run("the directory admits it", func(t *testing.T) {
		t.Parallel()
		c := newConsoleUnderTest(t, issuer.Config{})
		_, secret := c.signIn(t, subject, time.Now())

		who, ok, _ := c.ask(secret)
		if !ok {
			t.Fatal("an admitted sign-in was refused")
		}
		if who.Source == access.SourceRecovery || who.ServiceAccount != nil {
			t.Errorf("a provider's sign-in read as recovery: %+v", who)
		}
	})
}
