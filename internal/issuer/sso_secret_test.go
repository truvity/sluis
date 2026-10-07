package issuer_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	accessissuerv1 "github.com/truvity/sluis/gen/accessissuer/v1"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/audit/audittest"
	"github.com/truvity/sluis/internal/demo"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/policy"
)

// The browser's cookie is a secret of its own, never the sign-in's id. The
// id is shown to operators, filed under every session the sign-in opens and
// carried in the listing; if it also signed somebody in, anybody who could
// read a console page could be that person. These tests hold the
// separation from the outside: through HTTP, through the session service,
// and through the keys the store ends up holding.

const (
	ssoPointerPrefix = "issuer:sso-cookie:"
	ssoRecordPrefix  = "issuer:sso:"
	ssoEmail         = "ada@north.example"
	ssoBob           = "bob@north.example"
)

// ssoHash is the pointer's hash as the issuer defines it, written out here
// on purpose: a change to the derivation must be a change to this test.
func ssoHash(secret string) string {
	sum := sha256.Sum256([]byte("sluis issuer sso-cookie v1" + "\x00" + secret))

	return hex.EncodeToString(sum[:])
}

// recState is a State that remembers every key, value and member written
// through it, the lifetime each key was given, and how many reads and writes
// it served.
type recState struct {
	issuer.State

	mu     sync.Mutex
	reads  int
	writes int
	ttl    map[string]time.Duration
	seen   []string

	// failGet makes every read fail; failSet makes the writes it names fail.
	failGet bool
	// failGetKey makes the reads of the keys it names fail.
	failGetKey func(key string) bool
	failSet    func(key string) bool
}

var errStoreDown = errors.New("the store is down")

func (r *recState) setFailures(get bool, set func(string) bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.failGet, r.failSet = get, set
}

func (r *recState) setReadFailure(keys func(string) bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.failGetKey = keys
}

func (r *recState) failing(get bool, key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	return (get && (r.failGet || (r.failGetKey != nil && r.failGetKey(key)))) || (!get && r.failSet != nil && r.failSet(key))
}

func newRecState() *recState {
	return &recState{State: issuer.NewMemoryState(), ttl: map[string]time.Duration{}}
}

func (r *recState) note(read bool, ttl time.Duration, strs ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if read {
		r.reads++

		return
	}

	r.writes++
	r.seen = append(r.seen, strs...)

	if ttl > 0 && len(strs) > 0 {
		r.ttl[strs[0]] = ttl
	}
}

func (r *recState) Get(ctx context.Context, key string) ([]byte, bool, error) {
	if r.failing(true, key) {
		return nil, false, errStoreDown
	}

	r.note(true, 0)

	return r.State.Get(ctx, key)
}

func (r *recState) Members(ctx context.Context, key string) ([]string, error) {
	r.note(true, 0)

	return r.State.Members(ctx, key)
}

func (r *recState) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if r.failing(false, key) {
		return errStoreDown
	}

	r.note(false, ttl, key, string(value))

	return r.State.Set(ctx, key, value, ttl)
}

func (r *recState) SetIfAbsent(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	r.note(false, ttl, key, string(value))

	return r.State.SetIfAbsent(ctx, key, value, ttl)
}

func (r *recState) Delete(ctx context.Context, key string) error {
	r.note(false, 0, key)

	return r.State.Delete(ctx, key)
}

func (r *recState) Add(ctx context.Context, key, member string, ttl time.Duration) error {
	r.note(false, ttl, key, member)

	return r.State.Add(ctx, key, member, ttl)
}

func (r *recState) Remove(ctx context.Context, key, member string) error {
	r.note(false, 0, key, member)

	return r.State.Remove(ctx, key, member)
}

// counts reads the counters, and resets them.
func (r *recState) counts() (reads, writes int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	reads, writes = r.reads, r.writes
	r.reads, r.writes = 0, 0

	return reads, writes
}

func (r *recState) everything() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]string(nil), r.seen...)
}

// signInIDs are the ids of the sign-ins written so far.
func (r *recState) signInIDs() []string {
	var ids []string

	for _, s := range r.everything() {
		if id, ok := strings.CutPrefix(s, ssoRecordPrefix); ok && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}

	return ids
}

// lockedBuffer is a log sink that parallel tests may share.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.buf.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.buf.String()
}

// whoProvider is the directory's sign-in, whoever the test says is at the
// keyboard.
type whoProvider struct {
	mu    sync.Mutex
	email string
}

func (*whoProvider) Kind() string { return "google" }

func (*whoProvider) URL(state string) (string, error) {
	return "https://idp.example/authorize?state=" + url.QueryEscape(state), nil
}

func (p *whoProvider) Identify(context.Context, string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.email, nil
}

// as makes the next sign-in this person's.
func (g *ssoRig) as(email string) {
	g.who.mu.Lock()
	defer g.who.mu.Unlock()

	g.who.email = email
}

// ssoRig is the issuer with its sign-in pages, over a recording State, a
// directory the test can change its mind about, and a log it can read.
type ssoRig struct {
	server *httptest.Server
	iss    *issuer.Issuer
	state  *recState
	dir    *fakeDirectory
	logs   *lockedBuffer
	trail  *audittest.Recorder
	who    *whoProvider

	mu        sync.Mutex
	announced []issuer.Session
}

func (g *ssoRig) told() []issuer.Session {
	g.mu.Lock()
	defer g.mu.Unlock()

	return append([]issuer.Session(nil), g.announced...)
}

func newSSORig(t *testing.T, cfg issuer.Config) *ssoRig {
	t.Helper()

	declared, err := policy.Parse([]byte(demo.Policy))
	if err != nil {
		t.Fatalf("parse the policy: %v", err)
	}

	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatalf("policy set: %v", err)
	}

	dir := &fakeDirectory{standing: map[string]issuer.Standing{
		ssoEmail: {Found: true, Authoritative: true, Groups: []string{"engineering@north.example"}},
	}}
	state := newRecState()
	logs := &lockedBuffer{}
	trail := audittest.New(t)
	who := &whoProvider{email: ssoEmail}
	rig := &ssoRig{state: state, dir: dir, logs: logs, trail: trail, who: who}
	dir.standing[ssoBob] = issuer.Standing{Found: true, Authoritative: true, Groups: []string{"engineering@north.example"}}

	cfg.URL, cfg.AllowInsecure = "http://issuer.example", true
	iss := issuer.New(cfg, set, dir, state)
	iss.UseAudit(trail)

	storage, err := issuer.NewStorage(iss, fakeVerifier{}, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("storage: %v", err)
	}

	handler, err := handlerWithSignIn(iss, storage, issuer.SignInDeps{
		Providers:    []issuer.SignIn{who},
		State:        access.NewStateCodec([]byte("a-test-key-for-signing-state"), 0),
		ConsoleMount: "/console",
		Log:          slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Announce: func(_ context.Context, sessions []issuer.Session) {
			rig.mu.Lock()
			defer rig.mu.Unlock()

			rig.announced = append(rig.announced, sessions...)
		},
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	rig.server, rig.iss = server, iss

	return rig
}

// signedInBrowser is a browser that has signed in once, with the id of its
// sign-in.
func (g *ssoRig) signedInBrowser(t *testing.T) (b *browser, id string) {
	t.Helper()

	before := len(g.state.signInIDs())
	b = newBrowser(t, g.server)
	b.signIn()

	ids := g.state.signInIDs()
	if len(ids) != before+1 {
		t.Fatalf("signing in wrote %d new sign-in records, want 1", len(ids)-before)
	}

	return b, ids[len(ids)-1]
}

// proves reports whether the browser's current cookie signs it in at
// /authorize, without going to the provider.
func proves(b *browser) bool {
	b.t.Helper()

	return !strings.Contains(b.authorize(""), "/login/google/start")
}

// service is the session contract over this rig's stores.
func (g *ssoRig) service() *issuer.SessionsService {
	return issuer.NewSessionsServiceForCookieTest(g.iss.Sessions(), g.iss.SSO(), verifier(),
		func(context.Context, string) ([]string, error) { return nil, nil })
}

// 1. After sign-in the cookie value is not the sign-in id, is at least 43
// base64url characters, and differs on every sign-in.
func TestTheSignInCookieIsASecretNotTheSignInID(t *testing.T) {
	t.Parallel()

	rig := newSSORig(t, issuer.Config{})
	urlSafe := regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	seen := map[string]bool{}

	for range 3 {
		b, id := rig.signedInBrowser(t)
		cookie := b.cookies[issuer.SSOCookieName]

		if cookie == "" {
			t.Fatal("signing in set no cookie")
		}

		if cookie == id {
			t.Errorf("the cookie is the sign-in id %q", id)
		}

		if len(cookie) < 43 || !urlSafe.MatchString(cookie) {
			t.Errorf("cookie %q is not at least 43 base64url characters without padding", cookie)
		}

		if seen[cookie] {
			t.Errorf("cookie %q was issued twice", cookie)
		}

		seen[cookie] = true
	}
}

// 2. A request that carries the sign-in id as its cookie is not signed in,
// on every path that reads the cookie. Each path is also run with the real
// cookie, so a path that refused everybody would not pass.
func TestTheSignInIDAsACookieSignsNobodyIn(t *testing.T) {
	t.Parallel()

	rig := newSSORig(t, issuer.Config{})
	owner, id := rig.signedInBrowser(t)

	forged := newBrowser(t, rig.server)
	forged.cookies[issuer.SSOCookieName] = id

	t.Run("silent authorize", func(t *testing.T) {
		if !proves(owner) {
			t.Error("the real cookie did not complete /authorize silently")
		}

		if proves(forged) {
			t.Error("the sign-in id completed /authorize silently")
		}
	})

	t.Run("account", func(t *testing.T) {
		_, to, _ := owner.do(http.MethodGet, "/account")
		if to != "/console/#/people/"+ssoEmail {
			t.Errorf("the real cookie was sent to %q", to)
		}

		_, to, _ = forged.do(http.MethodGet, "/account")
		if to != "/console/" {
			t.Errorf("the sign-in id was sent to %q, want the console root a stranger gets", to)
		}
	})

	t.Run("session service", func(t *testing.T) {
		call := func(cookie string) error {
			req := connect.NewRequest(&accessissuerv1.ListSessionsRequest{Identity: ssoEmail})
			req.Header().Set("Cookie", issuer.SSOCookieName+"="+cookie)
			_, err := rig.service().ListSessions(context.Background(), req)

			return err
		}

		if err := call(owner.cookies[issuer.SSOCookieName]); err != nil {
			t.Errorf("the real cookie was refused: %v", err)
		}

		err := call(id)
		if connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Errorf("the sign-in id as a cookie: %v, want unauthenticated", err)
		}
	})

	// A sign-out that is given only the id has nothing to end: it must not
	// end the sign-in the id names, by either door.
	for _, door := range []struct{ name, method, path string }{
		{"logout", http.MethodGet, "/logout"},
		{"logout post", http.MethodPost, "/logout"},
		{"end_session", http.MethodGet, "/end_session"},
	} {
		t.Run(door.name+" with only the id", func(t *testing.T) {
			t.Parallel()

			own := newSSORig(t, issuer.Config{})
			owner, ownID := own.signedInBrowser(t)

			stranger := newBrowser(t, own.server)
			stranger.cookies[issuer.SSOCookieName] = ownID
			stranger.do(door.method, door.path)

			if _, live, err := own.iss.SSO().Get(context.Background(), ownID); err != nil || !live {
				t.Errorf("%s with only the id ended the sign-in: live=%v err=%v", door.path, live, err)
			}

			if !proves(owner) {
				t.Errorf("%s with only the id signed the real browser out", door.path)
			}
		})
	}
}

// 3. The store never holds the secret, as a key, a value or a set member;
// the pointer is keyed by its hash, its value is the sign-in id, and its
// lifetime is the record's.
func TestTheStoreNeverHoldsTheSecret(t *testing.T) {
	t.Parallel()

	rig := newSSORig(t, issuer.Config{})
	b, id := rig.signedInBrowser(t)
	secret := b.cookies[issuer.SSOCookieName]

	for _, s := range rig.state.everything() {
		if strings.Contains(s, secret) {
			t.Errorf("the store was handed the secret: %q", s)
		}
	}

	pointer := ssoPointerPrefix + ssoHash(secret)

	raw, found, err := rig.state.State.Get(context.Background(), pointer)
	if err != nil || !found {
		t.Fatalf("no pointer at %s: found=%v err=%v", pointer, found, err)
	}

	if string(raw) != id {
		t.Errorf("the pointer holds %q, want the sign-in id %q", raw, id)
	}

	rig.state.mu.Lock()
	defer rig.state.mu.Unlock()

	if got, want := rig.state.ttl[pointer], rig.state.ttl[ssoRecordPrefix+id]; got == 0 || got != want {
		t.Errorf("the pointer lives %v, the record %v: they must be the same", got, want)
	}
}

// 4. The pointer is gone after every way a sign-in ends.
func TestEveryEndOfASignInRemovesThePointer(t *testing.T) {
	t.Parallel()

	gone := func(t *testing.T, rig *ssoRig, secret string) {
		t.Helper()

		_, found, err := rig.state.State.Get(context.Background(), ssoPointerPrefix+ssoHash(secret))
		if err != nil {
			t.Fatal(err)
		}

		if found {
			t.Error("the cookie's pointer outlived the sign-in")
		}
	}

	ada := "ada@north.example|"

	ends := []struct {
		name string
		cfg  issuer.Config
		// past makes the sign-in older than the absolute limit.
		past bool
		end  func(t *testing.T, rig *ssoRig, b *browser, id string)
	}{
		{name: "logout", end: func(_ *testing.T, _ *ssoRig, b *browser, _ string) {
			b.do(http.MethodGet, "/logout")
		}},
		{name: "end_session", end: func(_ *testing.T, _ *ssoRig, b *browser, _ string) {
			b.do(http.MethodGet, "/end_session")
		}},
		{name: "revoke one browser by id", end: func(t *testing.T, rig *ssoRig, _ *browser, id string) {
			t.Helper()

			if ended, err := revoke(t, rig.service(), ada,
				&accessissuerv1.RevokeSessionsRequest{Identity: ssoEmail, Sso: id}); err != nil {
				t.Fatalf("revoke: %v (ended %d)", err, ended)
			}
		}},
		{name: "sign out everywhere", end: func(t *testing.T, rig *ssoRig, _ *browser, _ string) {
			t.Helper()

			if _, err := revoke(t, rig.service(), ada,
				&accessissuerv1.RevokeSessionsRequest{Identity: ssoEmail}); err != nil {
				t.Fatalf("revoke: %v", err)
			}
		}},
		{name: "absolute limit in silent sign-in", cfg: issuer.Config{AbsoluteLifetime: time.Hour}, past: true,
			end: func(_ *testing.T, _ *ssoRig, b *browser, _ string) { b.authorize("") }},
		{name: "unadmitted identity in silent sign-in", end: func(_ *testing.T, rig *ssoRig, b *browser, _ string) {
			rig.dir.standing[ssoEmail] = issuer.Standing{Found: false, Authoritative: true}
			b.authorize("")
		}},
	}

	for _, tc := range ends {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rig := newSSORig(t, tc.cfg)
			if tc.past {
				old := time.Now().Add(-2 * time.Hour)
				rig.iss.SSO().SetClock(func() time.Time { return old })
			}

			b, id := rig.signedInBrowser(t)
			secret := b.cookies[issuer.SSOCookieName]

			if tc.past {
				rig.iss.SSO().SetClock(time.Now)
			}

			if _, found, _ := rig.state.State.Get(context.Background(), ssoPointerPrefix+ssoHash(secret)); !found {
				t.Fatal("no pointer to begin with")
			}

			tc.end(t, rig, b, id)

			gone(t, rig, secret)

			if _, live, _ := rig.iss.SSO().Get(context.Background(), id); live {
				t.Error("the sign-in itself is still live")
			}
		})
	}
}

// 5. A record written before the cookie had a secret carries no cookie_hash:
// it is listed and can be ended, and no cookie resolves to it.
func TestAPreUpgradeRecordIsListedAndEndedAndNoCookieResolvesToIt(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	state := issuer.NewMemoryState()
	sso := issuer.NewSSO(state, time.Hour)

	old := issuer.SSOSession{
		ID: "old-sign-in", Identity: ssoEmail, How: "google",
		AuthTime: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}

	raw, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(raw), "cookie_hash") {
		t.Fatal("the pre-upgrade record already names a cookie hash")
	}

	if err = state.Set(ctx, "issuer:sso:"+old.ID, raw, time.Hour); err != nil {
		t.Fatal(err)
	}

	for _, set := range []string{"issuer:sso", "issuer:sso-of:" + ssoEmail} {
		if err = state.Add(ctx, set, old.ID, time.Hour); err != nil {
			t.Fatal(err)
		}
	}

	listed, err := sso.List(ctx, "")
	if err != nil || len(listed) != 1 || listed[0].ID != old.ID {
		t.Fatalf("the old record is not listed: %v, %v", listed, err)
	}

	// The cookie it used to have was its id; and no other string finds it.
	for _, cookie := range []string{old.ID, "", ssoHash(old.ID), "A" + strings.Repeat("a", 42)} {
		if _, live, err := sso.Resolve(ctx, cookie); err != nil || live {
			t.Errorf("cookie %q resolved to the old record: live=%v err=%v", cookie, live, err)
		}
	}

	if err = sso.End(ctx, old.ID); err != nil {
		t.Fatalf("end: %v", err)
	}

	if _, live, _ := sso.Get(ctx, old.ID); live {
		t.Error("the old record could not be ended")
	}

	if listed, _ = sso.List(ctx, ""); len(listed) != 0 {
		t.Errorf("the ended record is still listed: %v", listed)
	}
}

// 6. A pointer whose record names another hash, or has no record, does not
// resolve: the record must name this cookie too.
func TestAPointerThatDoesNotMatchItsRecordDoesNotResolve(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	state := issuer.NewMemoryState()
	sso := issuer.NewSSO(state, time.Hour)

	mine, mySecret, err := sso.Begin(ctx, ssoEmail, "google")
	if err != nil {
		t.Fatal(err)
	}

	other, otherSecret, err := sso.Begin(ctx, "eli@south.example", "google")
	if err != nil {
		t.Fatal(err)
	}

	if _, live, _ := sso.Resolve(ctx, mySecret); !live {
		t.Fatal("a fresh cookie did not resolve")
	}

	t.Run("record names another hash", func(t *testing.T) {
		// My pointer, aimed at the other person's sign-in.
		if err := state.Set(ctx, ssoPointerPrefix+ssoHash(mySecret), []byte(other.ID), time.Hour); err != nil {
			t.Fatal(err)
		}

		if session, live, err := sso.Resolve(ctx, mySecret); err != nil || live {
			t.Errorf("resolved to %+v (live=%v err=%v) through a pointer the record does not name", session, live, err)
		}

		// The other person's own cookie is unaffected.
		if session, live, _ := sso.Resolve(ctx, otherSecret); !live || session.ID != other.ID {
			t.Error("the other cookie stopped resolving")
		}
	})

	t.Run("no record", func(t *testing.T) {
		if err := state.Set(ctx, ssoPointerPrefix+ssoHash(mySecret), []byte("no-such-sign-in"), time.Hour); err != nil {
			t.Fatal(err)
		}

		if _, live, err := sso.Resolve(ctx, mySecret); err != nil || live {
			t.Errorf("resolved through a pointer with no record: live=%v err=%v", live, err)
		}
	})

	t.Run("record ended under the pointer", func(t *testing.T) {
		if err := state.Set(ctx, ssoPointerPrefix+ssoHash(mySecret), []byte(mine.ID), time.Hour); err != nil {
			t.Fatal(err)
		}

		if _, live, _ := sso.Resolve(ctx, mySecret); !live {
			t.Fatal("restoring the pointer did not restore the cookie")
		}

		if err := state.Delete(ctx, "issuer:sso:"+mine.ID); err != nil {
			t.Fatal(err)
		}

		if _, live, err := sso.Resolve(ctx, mySecret); err != nil || live {
			t.Errorf("resolved with the record deleted: live=%v err=%v", live, err)
		}
	})
}

// 7. What each operation costs the store, so a change that adds a read to
// every page load, or a write to every sign-out, shows up here.
func TestWhatTheSignInStoreCosts(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	fresh := func() (*issuer.SSO, *recState) {
		st := newRecState()

		return issuer.NewSSO(st, time.Hour), st
	}

	check := func(t *testing.T, st *recState, what string, wantReads, wantWrites int) {
		t.Helper()

		if reads, writes := st.counts(); reads != wantReads || writes != wantWrites {
			t.Errorf("%s: %d reads / %d writes, want %d / %d", what, reads, writes, wantReads, wantWrites)
		}
	}

	t.Run("sign-in", func(t *testing.T) {
		t.Parallel()

		sso, st := fresh()
		if _, _, err := sso.Begin(ctx, ssoEmail, "google"); err != nil {
			t.Fatal(err)
		}

		check(t, st, "sign-in", 0, 4)
	})

	t.Run("silent sign-in", func(t *testing.T) {
		t.Parallel()

		sso, st := fresh()
		_, secret, _ := sso.Begin(ctx, ssoEmail, "google")
		st.counts()

		if _, live, err := sso.Resolve(ctx, secret); err != nil || !live {
			t.Fatalf("resolve: live=%v err=%v", live, err)
		}

		check(t, st, "silent sign-in", 2, 0)
	})

	t.Run("sign-out", func(t *testing.T) {
		t.Parallel()

		sso, st := fresh()
		_, secret, _ := sso.Begin(ctx, ssoEmail, "google")
		st.counts()

		// What /logout does to the sign-in: resolve the cookie, end the id.
		session, live, err := sso.Resolve(ctx, secret)
		if err != nil || !live {
			t.Fatalf("resolve: live=%v err=%v", live, err)
		}

		if err = sso.End(ctx, session.ID); err != nil {
			t.Fatal(err)
		}

		check(t, st, "sign-out", 4, 4)
	})

	t.Run("end by id", func(t *testing.T) {
		t.Parallel()

		sso, st := fresh()
		session, _, _ := sso.Begin(ctx, ssoEmail, "google")
		st.counts()

		if err := sso.End(ctx, session.ID); err != nil {
			t.Fatal(err)
		}

		check(t, st, "end by id", 2, 4)
	})

	t.Run("end for an identity", func(t *testing.T) {
		t.Parallel()

		// One read for the set, then per sign-in 2 reads (the record, the
		// clients set) / 4 writes, when no client was involved.
		for _, n := range []int{1, 3} {
			sso, st := fresh()

			for range n {
				if _, _, err := sso.Begin(ctx, ssoEmail, "google"); err != nil {
					t.Fatal(err)
				}
			}

			st.counts()

			if ended, err := sso.EndFor(ctx, ssoEmail); err != nil || ended != n {
				t.Fatalf("EndFor ended %d: %v", ended, err)
			}

			check(t, st, "EndFor", 1+2*n, 4*n)
		}
	})
}

// 9. The cookie keeps the names browsers already hold.
func TestTheCookieNamesAreUnchanged(t *testing.T) {
	t.Parallel()

	sso := issuer.NewSSO(newRecState(), time.Hour)

	if got := sso.Cookie("v", true).Name; got != "__Host-access_issuer_sso" {
		t.Errorf("secure cookie name = %q", got)
	}

	if got := sso.Cookie("v", false).Name; got != "access_issuer_sso" {
		t.Errorf("plain cookie name = %q", got)
	}

	if issuer.SSOCookieName != "access_issuer_sso" {
		t.Errorf("SSOCookieName = %q", issuer.SSOCookieName)
	}

	// And it is what a real sign-in sets.
	rig := newSSORig(t, issuer.Config{})
	b, _ := rig.signedInBrowser(t)

	if b.cookies["access_issuer_sso"] == "" {
		t.Errorf("a sign-in set %v, want the plain name over plain HTTP", b.cookies)
	}
}

// 10. Neither the secret nor its hash reaches a log, through a sign-in, a
// silent sign-in, the refusals and the ends.
func TestLogsNeverHoldTheSecretOrItsHash(t *testing.T) {
	// Not parallel: the default logger is process-wide, and it is where
	// the paths without a logger of their own write.
	rig := newSSORig(t, issuer.Config{})

	var global lockedBuffer

	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&global, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	b, id := rig.signedInBrowser(t)
	secret := b.cookies[issuer.SSOCookieName]

	proves(b) // silent sign-in

	forged := newBrowser(t, rig.server)
	forged.cookies[issuer.SSOCookieName] = id
	proves(forged)
	forged.do(http.MethodGet, "/logout")

	b2, _ := rig.signedInBrowser(t)
	secret2 := b2.cookies[issuer.SSOCookieName]
	b2.do(http.MethodGet, "/logout")

	// An unadmitted identity logs a warning of its own.
	rig.dir.standing[ssoEmail] = issuer.Standing{Found: false, Authoritative: true}
	proves(b)

	logs := rig.logs.String() + global.String()
	if logs == "" {
		t.Fatal("captured no logs at all: the capture is not wired")
	}

	for _, leak := range []string{secret, ssoHash(secret), secret2, ssoHash(secret2)} {
		if strings.Contains(logs, leak) {
			t.Errorf("a log line holds %q", leak)
		}
	}
}

// Signing out everywhere tells every client once: the one holding a refresh
// token by its session, the one holding none by what the sign-in knows.
func TestSignOutEverywhereTellsEveryClient(t *testing.T) {
	t.Parallel()

	rig := newSSORig(t, issuer.Config{})
	_, id := rig.signedInBrowser(t)

	if err := rig.iss.SSO().Involve(context.Background(), id, "openid-only"); err != nil {
		t.Fatal(err)
	}

	// A client that holds a refresh token, and is also filed as involved, as
	// a real sign-in files it.
	if _, err := rig.iss.Sessions().Record(context.Background(), issuer.Opened{
		Identity: ssoEmail, ClientID: "refreshing", How: issuer.HowCode, Token: "t-refreshing", SSO: id,
	}); err != nil {
		t.Fatal(err)
	}

	if err := rig.iss.SSO().Involve(context.Background(), id, "refreshing"); err != nil {
		t.Fatal(err)
	}

	var told []issuer.Session

	service := rig.service().WithAnnounceForTest(func(_ context.Context, sessions []issuer.Session) {
		told = append(told, sessions...)
	})

	if _, err := revoke(t, service, "ada@north.example|",
		&accessissuerv1.RevokeSessionsRequest{Identity: ssoEmail}); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	count := map[string]int{}
	for _, one := range told {
		count[one.ClientID]++

		if one.Identity != ssoEmail || one.SSO != id {
			t.Errorf("told %+v, want identity %s and sign-in %s", one, ssoEmail, id)
		}
	}

	if len(told) != 2 || count["openid-only"] != 1 || count["refreshing"] != 1 {
		t.Errorf("told %+v, want one token each for openid-only and refreshing", told)
	}

	for _, one := range told {
		if one.ClientID == "refreshing" && one.ID == "" {
			t.Error("the client holding a session was told without its session id")
		}
	}
}
