package issuer_test

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/demo"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/internal/signer"
	"github.com/truvity/sluis/policy"
)

// hintServer is the demonstration issuer for one person, under a signing key
// of the test's choosing: two of them with the same key and issuer URL verify
// each other's tokens, which is how a test holds an ID token of somebody who
// is not signed in to the server it asks.
func hintServer(t *testing.T, email string, key *signer.SigningKey) *httptest.Server {
	t.Helper()

	declared, err := policy.Parse([]byte(demo.Policy))
	if err != nil {
		t.Fatal(err)
	}

	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatal(err)
	}

	dir := &fakeDirectory{standing: map[string]issuer.Standing{
		email: {Found: true, Authoritative: true, Groups: []string{"engineering@north.example"}},
	}}
	iss := issuer.New(issuer.Config{URL: "http://issuer.example", AllowInsecure: true}, set, dir, issuer.NewMemoryState())

	storage, err := issuer.NewTestStorage(iss, fakeVerifier{}, nil, key, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	h, err := handlerWithSignIn(iss, storage, issuer.SignInDeps{
		Providers: []issuer.SignIn{oneProvider{email: email}},
		State:     access.NewStateCodec([]byte("a-test-key-for-signing-state"), 0),
	})
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(h)
	t.Cleanup(server.Close)

	return server
}

// OpenID Connect Core 3.1.2.1: an `id_token_hint` names the person the request
// is for. A browser signed in as someone else does not answer it: `prompt=none`
// is `login_required`; otherwise the person signs in, and if they are still not
// the one named the request is refused, never completed as them.
func TestAnIDTokenHintOfAnotherPersonIsNotAnsweredByThisSignIn(t *testing.T) {
	t.Parallel()
	key := mustSoftKey(t, false)

	adaServer := hintServer(t, "ada@north.example", key)
	ada := newBrowser(t, adaServer)
	ada.signIn()
	hint, _ := redeem(t, ada, ada.authorize(""))["id_token"].(string)

	if hint == "" {
		t.Fatal("no id_token to use as a hint")
	}

	withHint := "&id_token_hint=" + url.QueryEscape(hint)

	// The person the hint names, in their own browser: answered silently.
	if where := ada.authorize(withHint); !strings.Contains(where, "/authorize/callback") {
		t.Errorf("the hinted person's own session went to %q, want a silent completion", where)
	}

	graceServer := hintServer(t, "grace@north.example", key)
	grace := newBrowser(t, graceServer)
	grace.signIn()

	// Without a hint the same browser is answered silently, so what follows is
	// the hint's doing.
	if where := grace.authorize(""); !strings.Contains(where, "/authorize/callback") {
		t.Fatalf("control: grace's own request went to %q, want a silent completion", where)
	}

	// prompt=none: no UI, and not the person named.
	where := grace.authorize(withHint + "&prompt=none")

	to, err := url.Parse(where)
	if err != nil || to.Host != "localhost:8000" || to.Query().Get("error") != "login_required" || to.Query().Has("code") {
		t.Errorf("prompt=none with another person's hint went to %q, want login_required at the client", where)
	}

	// Otherwise: sign in again.
	where = grace.authorize(withHint)
	if !strings.Contains(where, "/login/google/start") {
		t.Fatalf("another person's hint went to %q, want a fresh sign-in", where)
	}

	_, toProvider, _ := grace.do("GET", where)
	state := toProvider[strings.Index(toProvider, "state=")+len("state="):]

	status, back, body := grace.do("GET", "/login/google/callback?code=x&state="+state)
	to, err = url.Parse(back)

	if status != 302 || err != nil || to.Host != "localhost:8000" ||
		to.Query().Get("error") != "login_required" || to.Query().Has("code") {
		t.Errorf("signing in as somebody else than the hint named: %d %q %s, want login_required and no code", status, back, body)
	}
}
