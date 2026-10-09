package keys_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/truvity/sluis/audit/keys"
	skeys "github.com/truvity/sluis/storage/keys"
	localkeys "github.com/truvity/sluis/storage/keys/local"
	transitkeys "github.com/truvity/sluis/storage/keys/transit"
	"github.com/truvity/sluis/storage/openbao/openbaotest"
)

// port opens a storage port over the local backend with every audit purpose.
func port(t *testing.T) *skeys.Keys {
	t.Helper()
	b, err := localkeys.New(localkeys.RandomRoot())
	if err != nil {
		t.Fatal(err)
	}
	set, err := skeys.Open(skeys.Config{Adapter: "local", Keys: map[skeys.Purpose]skeys.Entry{
		skeys.Seal: {Key: "audit-seal"}, skeys.Pseudonym: {Key: "audit-pseudonym"}, skeys.Conceal: {Key: "audit-conceal"},
	}}, skeys.Options{Backend: b, Instance: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func TestAPortPseudonymIsKeyedPerTenantAndPurpose(t *testing.T) {
	ctx := context.Background()
	p, err := keys.NewPortProvider(port(t))
	if err != nil || p == nil {
		t.Fatalf("provider = %v, %v", p, err)
	}
	a, err := p.Pseudonym(ctx, "acme", "security", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if !keys.IsPseudonym(a) || strings.Contains(a, "alice") {
		t.Fatalf("pseudonym = %q", a)
	}
	again, _ := p.Pseudonym(ctx, "acme", "security", "alice")
	other, _ := p.Pseudonym(ctx, "acme", "billing", "alice")
	tenant, _ := p.Pseudonym(ctx, "globex", "security", "alice")
	if a != again {
		t.Error("the same person got two pseudonyms in one profile")
	}
	if a == other || a == tenant || other == tenant {
		t.Error("one person is linkable across profiles or tenants")
	}
	if got, err := p.Pseudonym(ctx, "acme", "security", ""); got != "" || err != nil {
		t.Errorf("empty identifier = %q, %v", got, err)
	}
	if _, err := p.Pseudonym(ctx, "a/b", "security", "alice"); err == nil {
		t.Error("a tenant with a slash was accepted: it could name two scopes")
	}
}

func TestAPortSealsAnIdentityToItsTenantAndPurpose(t *testing.T) {
	ctx := context.Background()
	p, _ := keys.NewPortProvider(port(t))
	sealed, err := p.Seal(ctx, "acme", "security", []byte("alice@example.test"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Open(ctx, "acme", "security", sealed)
	if err != nil || string(got) != "alice@example.test" {
		t.Fatalf("open = %q, %v", got, err)
	}
	for _, c := range [][2]string{{"globex", "security"}, {"acme", "billing"}} {
		if _, err := p.Open(ctx, c[0], keys.Purpose(c[1]), sealed); !errors.Is(err, skeys.ErrDecrypt) {
			t.Errorf("%v: err = %v, want the sealed identifier closed to another tenant or purpose", c, err)
		}
	}
}

func TestAPortDestroysATenantsPseudonyms(t *testing.T) {
	ctx := context.Background()
	p, _ := keys.NewPortProvider(port(t))
	if _, err := p.Pseudonym(ctx, "acme", "security", "alice"); err != nil {
		t.Fatal(err)
	}
	other, _ := p.Pseudonym(ctx, "globex", "security", "alice")
	sealed, err := p.Seal(ctx, "acme", "security", []byte("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Destroy(ctx, "acme", "security"); err != nil {
		t.Fatalf("destroy = %v", err)
	}
	if err := p.Destroy(ctx, "acme", "security"); err != nil {
		t.Fatalf("a second destroy = %v", err)
	}
	if _, err := p.Pseudonym(ctx, "acme", "security", "alice"); !errors.Is(err, keys.ErrDestroyed) {
		t.Fatalf("pseudonym after destroy = %v, want ErrDestroyed", err)
	}
	if _, err := p.Open(ctx, "acme", "security", sealed); !errors.Is(err, keys.ErrDestroyed) {
		t.Fatalf("open after destroy = %v, want ErrDestroyed", err)
	}
	if _, err := p.Seal(ctx, "acme", "security", []byte("bob")); !errors.Is(err, keys.ErrDestroyed) {
		t.Fatalf("seal after destroy = %v, want ErrDestroyed", err)
	}
	// Another profile of the same tenant, and another tenant, are untouched.
	if _, err := p.Pseudonym(ctx, "acme", "billing", "alice"); err != nil {
		t.Fatalf("another purpose: %v", err)
	}
	if got, err := p.Pseudonym(ctx, "globex", "security", "alice"); err != nil || got != other {
		t.Fatalf("another tenant: %q, %v", got, err)
	}
}

func TestAPortWithoutPseudonymOrConcealIsNoProvider(t *testing.T) {
	b, _ := localkeys.New(localkeys.RandomRoot())
	set, err := skeys.Open(skeys.Config{Adapter: "local", Keys: map[skeys.Purpose]skeys.Entry{skeys.Seal: {Key: "audit-seal"}}},
		skeys.Options{Backend: b, Instance: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if p, err := keys.NewPortProvider(set); p != nil || err != nil {
		t.Fatalf("provider = %v, %v: a deployment without pseudonyms has none", p, err)
	}
}

func TestAPortSealKeySignsWhatVerifyChecks(t *testing.T) {
	ctx := context.Background()
	s, err := keys.NewPortSigner(port(t), "local:audit-seal")
	if err != nil {
		t.Fatal(err)
	}
	msg := []byte("a seal's signing input")
	sig, err := s.Sign(ctx, msg)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := s.PublicKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := keys.Verify(pub, msg, sig); err != nil {
		t.Fatalf("a signature of the port's seal key does not verify: %v", err)
	}
	if err := keys.Verify(pub, []byte("another"), sig); err == nil {
		t.Fatal("the signature verified another message")
	}
	if s.KeyID() != "local:audit-seal" {
		t.Errorf("key id = %q", s.KeyID())
	}
	if _, err := keys.NewPortSigner(port(t), "x"); err != nil {
		t.Fatal(err)
	}
}

// On transit the pseudonym key is a key per tenant: destroying the tenant makes
// its sealed identifiers unreadable (the engine refuses its key), and no conceal
// key is needed.
func TestAPortOnTransitErasesATenantsSealedIdentities(t *testing.T) {
	ctx := context.Background()
	f := openbaotest.NewFakeTransit(t)
	set, err := skeys.Open(skeys.Config{Adapter: "transit", Keys: map[skeys.Purpose]skeys.Entry{
		skeys.Pseudonym: {Key: "audit"},
	}}, skeys.Options{Backend: transitkeys.New(f.Client(t)), Instance: "test"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := keys.NewPortProvider(set)
	if err != nil || p == nil {
		t.Fatalf("provider %v, %v", p, err)
	}
	pa, err := p.Pseudonym(ctx, "acme@eu.1", "security", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if pb, _ := p.Pseudonym(ctx, "acme", "security", "alice"); pa == pb {
		t.Fatal("two tenants share a pseudonym")
	}
	sealed, err := p.Seal(ctx, "acme@eu.1", "security", []byte("alice"))
	if err != nil {
		t.Fatal(err)
	}
	other, _ := p.Seal(ctx, "acme", "security", []byte("alice"))
	// Pseudonymising and sealing, the writer's calls, never decrypt.
	for _, r := range f.Requests() {
		if strings.HasPrefix(r, "POST decrypt/") {
			t.Fatalf("the writer's calls reached %s", r)
		}
	}
	if got, err := p.Open(ctx, "acme@eu.1", "security", sealed); err != nil || string(got) != "alice" {
		t.Fatalf("open = %q, %v", got, err)
	}
	if err := p.Destroy(ctx, "acme@eu.1", "security"); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"pseudonym": func() error { _, e := p.Pseudonym(ctx, "acme@eu.1", "security", "alice"); return e }(),
		"open":      func() error { _, e := p.Open(ctx, "acme@eu.1", "security", sealed); return e }(),
		"seal":      func() error { _, e := p.Seal(ctx, "acme@eu.1", "security", []byte("x")); return e }(),
	} {
		if !errors.Is(err, keys.ErrDestroyed) {
			t.Errorf("%s after destroy = %v, want ErrDestroyed", name, err)
		}
	}
	if got, err := p.Open(ctx, "acme", "security", other); err != nil || string(got) != "alice" {
		t.Fatalf("another tenant: %q, %v", got, err)
	}
}
