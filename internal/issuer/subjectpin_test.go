package issuer_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/audit/audittest"
	"github.com/truvity/sluis/internal/demo"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/policy"
)

// accountProvider signs in one address from the account the test names, and
// remembers the state each callback was identified under.
type accountProvider struct {
	email string
	mu    sync.Mutex
	sub   string
	seen  []string
}

func (*accountProvider) Kind() string { return "google" }

func (*accountProvider) URL(state string) (string, error) {
	return "https://idp.example/authorize?state=" + url.QueryEscape(state), nil
}

func (*accountProvider) Identify(context.Context, string) (string, error) {
	return "", errors.New("the account provider is asked through IdentifyAccount only")
}

func (p *accountProvider) IdentifyAccount(_ context.Context, _, state string) (email, subject string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seen = append(p.seen, state)
	return p.email, p.sub, nil
}

func (p *accountProvider) from(sub string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sub = sub
}

// failingState is a State whose writes fail.
type failingState struct{ issuer.State }

func (failingState) SetIfAbsent(context.Context, string, []byte, time.Duration) (bool, error) {
	return false, errors.New("the store is not answering")
}

func pinnedSignIn(t *testing.T, state issuer.State, provider issuer.SignIn) (*httptest.Server, *audittest.Recorder) {
	t.Helper()
	declared, err := policy.Parse([]byte(demo.Policy))
	if err != nil {
		t.Fatalf("parse the policy: %v", err)
	}
	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatalf("policy set: %v", err)
	}
	iss := issuer.New(issuer.Config{URL: "http://issuer.example", AllowInsecure: true}, set,
		&fakeDirectory{standing: map[string]issuer.Standing{"ada@north.example": live("engineering@north.example")}}, state)
	trail := audittest.New(t)
	iss.UseAudit(trail)
	storage, err := issuer.NewTestStorage(iss, fakeVerifier{}, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	h, err := handlerWithSignIn(iss, storage, issuer.SignInDeps{
		Providers: []issuer.SignIn{provider},
		State:     access.NewStateCodec([]byte("a-test-key-for-signing-state"), 0),
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	return server, trail
}

// The first sign-in with an address pins it to the account at the provider; a
// later one from another account with the same address (an address deleted
// and given to a new account) is refused, and audited; the first account still
// signs in.
func TestAnAddressIsPinnedToTheAccountThatFirstSignedInWithIt(t *testing.T) {
	t.Parallel()
	const email = "ada@north.example"
	state := issuer.NewMemoryState()
	provider := &accountProvider{email: email, sub: "1001"}
	server, trail := pinnedSignIn(t, state, provider)

	if status, body, _ := callback(t, server); status >= 400 {
		t.Fatalf("the first sign-in = %d: %s", status, body)
	}
	if got, ok, err := state.Get(context.Background(), issuer.SubjectPinKey("google", email)); err != nil || !ok || string(got) != "1001" {
		t.Fatalf("the pin = %q, %v, %v; want the first account's subject", got, ok, err)
	}

	provider.from("2002")
	status, body, _ := callback(t, server)
	if status != http.StatusForbidden {
		t.Fatalf("a sign-in from another account with the address = %d, want 403: %s", status, body)
	}
	records := trail.Records()
	last := records[len(records)-1].GetData().AsMap()
	if last["reason_code"] != "subject_changed" {
		t.Errorf("the refusal's reason_code = %v, want subject_changed", last["reason_code"])
	}

	provider.from("1001")
	if status, body, _ := callback(t, server); status >= 400 {
		t.Errorf("the first account again = %d: %s", status, body)
	}

	// An operator who confirms the change deletes the pin, and the next sign-in
	// pins the new account.
	if err := state.Delete(context.Background(), issuer.SubjectPinKey("google", email)); err != nil {
		t.Fatal(err)
	}
	provider.from("2002")
	if status, body, _ := callback(t, server); status >= 400 {
		t.Errorf("the new account after the pin was cleared = %d: %s", status, body)
	}
}

// The provider is handed the callback's own state, which its nonce is derived
// from.
func TestTheProviderIsHandedTheCallbacksState(t *testing.T) {
	t.Parallel()
	provider := &accountProvider{email: "ada@north.example", sub: "1001"}
	server, _ := pinnedSignIn(t, issuer.NewMemoryState(), provider)
	callback(t, server)
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.seen) != 1 || provider.seen[0] == "" {
		t.Fatalf("states seen = %q, want the callback's one", provider.seen)
	}
	if _, err := access.NewStateCodec([]byte("a-test-key-for-signing-state"), 0).VerifyBinding(provider.seen[0]); err != nil {
		t.Errorf("the provider was handed %q, not the signed state: %v", provider.seen[0], err)
	}
}

// A pin that cannot be read or written is a refusal, not a pass.
func TestASignInWhoseAccountCannotBeCheckedIsRefused(t *testing.T) {
	t.Parallel()
	server, trail := pinnedSignIn(t, failingState{issuer.NewMemoryState()}, &accountProvider{email: "ada@north.example", sub: "1001"})
	status, body, _ := callback(t, server)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("callback = %d, want 503: %s", status, body)
	}
	records := trail.Records()
	if got := records[len(records)-1].GetData().AsMap()["reason_code"]; got != "subject_unchecked" {
		t.Errorf("reason_code = %v, want subject_unchecked", got)
	}
}

// A provider that names no account (Identify only) pins nothing.
func TestAProviderWithoutAccountsPinsNothing(t *testing.T) {
	t.Parallel()
	state := issuer.NewMemoryState()
	server, _ := pinnedSignIn(t, state, oneProvider{email: "ada@north.example"})
	if status, body, _ := callback(t, server); status >= 400 {
		t.Fatalf("callback = %d: %s", status, body)
	}
	if _, ok, _ := state.Get(context.Background(), issuer.SubjectPinKey("google", "ada@north.example")); ok {
		t.Error("a provider without an account id pinned the address")
	}
}
