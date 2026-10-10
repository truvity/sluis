package issuerapp_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/internal/issuerapp"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/internal/secretstore/secretrec"
)

// The Google client is read when a browser is first sent to Google, not as the
// issuer is assembled; it is reused within the time to live, and read again by
// the request after it.
func TestTheSignInClientIsReadAtTheFirstSignIn(t *testing.T) {
	ctx := context.Background()
	rec := secretrec.New()
	v5 := secretstore.FromStoreV5(rec, "")
	if _, err := v5.OIDC().SignInClientID("google").Put(ctx, []byte("client-id.example"), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := v5.OIDC().SignInClientSecret("google").Put(ctx, []byte("client-secret-value"), ""); err != nil {
		t.Fatal(err)
	}
	rec.Reset()
	providers, err := issuerapp.OpenSignInForTest(ctx, secretstore.NewSourceV5(v5, nil), "https://issuer.example", "google")
	if err != nil || len(providers) != 1 {
		t.Fatalf("openSignIn = %v, %v", providers, err)
	}
	if got := rec.Addresses("get"); len(got) != 0 {
		t.Fatalf("opening read %v", got)
	}
	now := time.Unix(1_000_000, 0)
	issuerapp.SetSignInClockForTest(providers[0], func() time.Time { return now })
	url := func() string {
		t.Helper()
		u, err := providers[0].(issuer.ContextSignIn).URLContext(ctx, "state-1")
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	if u := url(); !strings.Contains(u, "client_id=client-id.example") {
		t.Errorf("the sign-in address does not carry the client: %s", u)
	}
	want := []string{"internal/oidc/signin/google/client-id", "internal/oidc/signin/google/client-secret"}
	got := rec.Addresses("get")
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("the first sign-in read %v, want %v", got, want)
	}
	url()
	if n := len(rec.Addresses("get")); n != 2 {
		t.Errorf("a sign-in within the TTL read again: %d reads in all", n)
	}
	// Rotated, then asked after the TTL.
	_, rev, err := v5.OIDC().SignInClientID("google").Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = v5.OIDC().SignInClientID("google").Put(ctx, []byte("rotated.example"), rev); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	if u := url(); !strings.Contains(u, "client_id=rotated.example") {
		t.Errorf("a sign-in after the TTL served the old client: %s", u)
	}
}

func TestAMissingSignInClientFailsTheSignInAndNamesTheAddress(t *testing.T) {
	v5 := secretstore.FromStoreV5(secretrec.New(), "")
	providers, err := issuerapp.OpenSignInForTest(context.Background(), secretstore.NewSourceV5(v5, nil), "https://issuer.example", "google")
	if err != nil {
		t.Fatal(err)
	}
	_, err = providers[0].(issuer.ContextSignIn).URLContext(context.Background(), "s")
	if err == nil || !strings.Contains(err.Error(), "internal/oidc/signin/google/client-id") {
		t.Errorf("a missing client = %v, want a refusal that names the address", err)
	}
}

// memoryIssuerState is an issuer.State over a map, counting what the
// fingerprint check does to it.
type memoryIssuerState struct {
	issuer.State
	mu     sync.Mutex
	values map[string][]byte
	ops    int
}

func (m *memoryIssuerState) Get(_ context.Context, key string) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ops++
	v, ok := m.values[key]
	return v, ok, nil
}

func (m *memoryIssuerState) SetIfAbsent(_ context.Context, key string, value []byte, _ time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ops++
	if _, ok := m.values[key]; ok {
		return false, nil
	}
	m.values[key] = value
	return true, nil
}

func (m *memoryIssuerState) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ops
}

type fixedSignIn struct{ urls int }

func (*fixedSignIn) Kind() string                                     { return "fixed" }
func (*fixedSignIn) URL(string) (string, error)                       { return "https://idp.example", nil }
func (*fixedSignIn) Identify(context.Context, string) (string, error) { return "a@b.example", nil }
func (f *fixedSignIn) URLContext(context.Context, string) (string, error) {
	f.urls++
	return "https://idp.example", nil
}

// The state secret's fingerprint is checked at the first sign-in, under its
// lease, reused within the time to live and checked again after it; a replica
// holding another secret is refused at its sign-ins, not at its start.
func TestTheStateSecretIsCheckedAtTheFirstSignIn(t *testing.T) {
	ctx := context.Background()
	state := &memoryIssuerState{values: map[string][]byte{}}
	leaseState := memory.New().Set().State
	seed := []byte("0123456789abcdef0123456789abcdef-a")
	other := []byte("0123456789abcdef0123456789abcdef-b")
	inner := &fixedSignIn{}
	gated, clock := issuerapp.GateSignInsForTest([]issuer.SignIn{inner}, state, seed, leaseState)
	now := time.Unix(1_000_000, 0)
	clock(func() time.Time { return now })
	if state.count() != 0 {
		t.Fatalf("assembling touched the state %d times", state.count())
	}
	sign := gated[0].(issuer.ContextSignIn)
	if _, err := sign.URLContext(ctx, "s"); err != nil {
		t.Fatal(err)
	}
	first := state.count()
	if first == 0 || len(state.values) != 1 {
		t.Fatalf("the first sign-in did not record the fingerprint: %d ops, %d values", first, len(state.values))
	}
	if _, err := sign.Identify(ctx, "code"); err != nil || state.count() != first {
		t.Errorf("a sign-in within the TTL checked again: %v, %d ops", err, state.count())
	}
	now = now.Add(time.Hour)
	if _, err := sign.URLContext(ctx, "s"); err != nil || state.count() <= first {
		t.Errorf("a sign-in after the TTL did not check again: %v, %d ops", err, state.count())
	}

	// Another replica, holding another secret, is refused at its sign-in. A
	// lease held elsewhere does not let it through.
	held := &memoryIssuerState{values: state.values}
	refused, clock2 := issuerapp.GateSignInsForTest([]issuer.SignIn{&fixedSignIn{}}, held, other, leaseState)
	clock2(func() time.Time { return now })
	_, err := refused[0].(issuer.ContextSignIn).URLContext(ctx, "s")
	if err == nil || !strings.Contains(err.Error(), "fingerprint mismatch") {
		t.Errorf("another secret = %v, want a fingerprint mismatch", err)
	}
	if _, err = refused[0].Identify(ctx, "code"); err == nil || errors.Is(err, port.ErrNotFound) {
		t.Errorf("a refused check was remembered as passed: %v", err)
	}
}
