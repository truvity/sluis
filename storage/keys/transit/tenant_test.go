package transit_test

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/truvity/sluis/storage/keys"
	"github.com/truvity/sluis/storage/keys/transit"
	"github.com/truvity/sluis/storage/openbao/openbaotest"
)

// pseudonymKey opens the pseudonym purpose over a fake engine, the way an
// installation does: the configured name is the prefix of the tenants' keys.
func pseudonymKey(t *testing.T) (*keys.Key, *openbaotest.FakeTransit) {
	t.Helper()
	f := openbaotest.NewFakeTransit(t)
	ks, err := keys.Open(keys.Config{Adapter: "transit", Keys: map[keys.Purpose]keys.Entry{
		keys.Pseudonym: {Key: "audit"},
	}}, keys.Options{Backend: transit.New(f.Client(t)), Instance: "test"})
	if err != nil {
		t.Fatal(err)
	}
	k, err := ks.For(keys.Pseudonym)
	if err != nil {
		t.Fatal(err)
	}
	return k, f
}

// The escaping gives one name per tenant: no two tenants share a key, and
// every name is a valid transit key name whatever the tenant holds.
func TestEscapeTenantNeverCollides(t *testing.T) {
	if got := transit.EscapeTenant("a@Eu.b_c"); got != "a_40_45u_2eb_5fc" {
		t.Fatalf("escape: %q", got)
	}
	// Every string of up to four characters over the characters that matter:
	// a plain letter, its upper case, the escape lead, the separators, the
	// hex digits an escape holds.
	alphabet := []string{"a", "A", "_", ".", "/", "@", "4", "0", "-", "f", "2"}
	seen := map[string]string{}
	var walk func(s string, depth int)
	walk = func(s string, depth int) {
		if s != "" {
			name, err := transit.TenantKeyName("audit", keys.Pseudonym, s)
			if err != nil {
				t.Fatalf("%q: %v", s, err)
			}
			if prev, dup := seen[name]; dup {
				t.Fatalf("%q and %q are both %q", prev, s, name)
			}
			seen[name] = s
		}
		if depth == 4 {
			return
		}
		for _, c := range alphabet {
			walk(s+c, depth+1)
		}
	}
	walk("", 0)
	// A byte outside ASCII is escaped bytewise; a name too long is refused,
	// not cut short.
	if _, err := transit.TenantKeyName("audit", keys.Pseudonym, "ü"); err != nil {
		t.Fatal(err)
	}
	if _, err := transit.TenantKeyName("audit", keys.Pseudonym, strings.Repeat("a", 200)); err == nil {
		t.Fatal("a name over 128 characters was accepted")
	}
	if _, err := transit.TenantKeyName("audit", keys.Pseudonym, ""); err == nil {
		t.Fatal("an empty tenant was accepted")
	}
}

func TestEachTenantHasAKeyOfItsOwn(t *testing.T) {
	k, f := pseudonymKey(t)
	ctx := t.Context()
	a, err := k.MAC(ctx, "security/acme@eu", []byte("alice"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := k.MAC(ctx, "security/acme", []byte("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("two tenants share a pseudonym")
	}
	if again, _ := k.MAC(ctx, "security/acme@eu", []byte("alice")); !bytes.Equal(a, again) {
		t.Fatal("a pseudonym is not stable")
	}
	want := []string{"audit.pseudonym.security_2facme", "audit.pseudonym.security_2facme_40eu"}
	if got := f.Names(); !slices.Equal(got, want) {
		t.Fatalf("keys %v, want %v", got, want)
	}
	// The writer's calls are hmac, encrypt (to create) and encrypt/decrypt:
	// nothing that reaches rotate, config or trim.
	for _, r := range f.Requests() {
		if strings.Contains(r, "/rotate") || strings.Contains(r, "/config") || strings.Contains(r, "/trim") {
			t.Fatalf("a pseudonym call reached %s", r)
		}
	}
	// The writer's path, MAC and EncryptFor, never decrypts: only resolve does.
	if _, err := k.EncryptFor(ctx, "security/acme", []byte("alice")); err != nil {
		t.Fatal(err)
	}
	for _, r := range f.Requests() {
		if strings.HasPrefix(r, "POST decrypt/") {
			t.Fatalf("the writer's calls reached %s", r)
		}
	}
}

func TestDestroyingATenantEndsItAlone(t *testing.T) {
	k, f := pseudonymKey(t)
	ctx := t.Context()
	keepMAC, _ := k.MAC(ctx, "b", []byte("alice"))
	sealedA, err := k.EncryptFor(ctx, "a", []byte("alice"))
	if err != nil {
		t.Fatal(err)
	}
	sealedB, _ := k.EncryptFor(ctx, "b", []byte("alice"))
	if _, err := k.MAC(ctx, "a", []byte("alice")); err != nil {
		t.Fatal(err)
	}
	if got, err := k.DecryptFor(ctx, "a", sealedA); err != nil || string(got) != "alice" {
		t.Fatalf("before: %q, %v", got, err)
	}

	if gone, err := k.Destroyed(ctx, "a"); err != nil || gone {
		t.Fatalf("destroyed before Destroy: %v, %v", gone, err)
	}
	if err := k.Destroy(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if err := k.Destroy(ctx, "a"); err != nil {
		t.Fatalf("a second Destroy: %v", err)
	}
	if gone, err := k.Destroyed(ctx, "a"); err != nil || !gone {
		t.Fatalf("destroyed after Destroy: %v, %v", gone, err)
	}

	if _, err := k.MAC(ctx, "a", []byte("alice")); !errors.Is(err, keys.ErrDestroyed) {
		t.Fatalf("MAC of a destroyed tenant: %v", err)
	}
	if _, err := k.DecryptFor(ctx, "a", sealedA); !errors.Is(err, keys.ErrDestroyed) {
		t.Fatalf("opening a destroyed tenant's identity: %v", err)
	}
	// Re-creating is refused: no sealing, no pseudonym, and no fresh key.
	if _, err := k.EncryptFor(ctx, "a", []byte("alice")); !errors.Is(err, keys.ErrDestroyed) {
		t.Fatalf("sealing for a destroyed tenant: %v", err)
	}
	if _, err := k.MAC(ctx, "a", []byte("alice")); !errors.Is(err, keys.ErrDestroyed) {
		t.Fatalf("MAC again: %v", err)
	}
	if v := f.Latest("audit.pseudonym.a"); v != 2 {
		t.Fatalf("the destroyed key is at version %d, want the one rotation", v)
	}
	if len(f.Names()) != 2 {
		t.Fatalf("keys %v: a key was made for the destroyed tenant", f.Names())
	}

	// B is untouched.
	if got, err := k.MAC(ctx, "b", []byte("alice")); err != nil || !bytes.Equal(got, keepMAC) {
		t.Fatalf("another tenant's pseudonym changed: %v", err)
	}
	if got, err := k.DecryptFor(ctx, "b", sealedB); err != nil || string(got) != "alice" {
		t.Fatalf("another tenant's identity: %q, %v", got, err)
	}
	// An identity sealed for one tenant does not open as another's.
	if _, err := k.DecryptFor(ctx, "b", sealedA); err == nil {
		t.Fatal("a tenant opened another's ciphertext")
	}
}

func TestDestroyBeforeTheTenantIsSeen(t *testing.T) {
	k, f := pseudonymKey(t)
	ctx := t.Context()
	if err := k.Destroy(ctx, "never"); err != nil {
		t.Fatal(err)
	}
	if gone, err := k.Destroyed(ctx, "never"); err != nil || !gone {
		t.Fatalf("destroyed: %v, %v", gone, err)
	}
	if _, err := k.MAC(ctx, "never", []byte("x")); !errors.Is(err, keys.ErrDestroyed) {
		t.Fatalf("MAC: %v", err)
	}
	if _, err := k.EncryptFor(ctx, "never", []byte("x")); !errors.Is(err, keys.ErrDestroyed) {
		t.Fatalf("seal: %v", err)
	}
	if got := f.Names(); len(got) != 1 {
		t.Fatalf("keys %v", got)
	}
	// A tenant that was never seen is not destroyed, and asking makes no key.
	if gone, err := k.Destroyed(ctx, "unseen"); err != nil || gone {
		t.Fatalf("an unseen tenant: %v, %v", gone, err)
	}
	if len(f.Names()) != 1 {
		t.Fatal("Destroyed made a key")
	}
}

// A Destroy that stopped half-way is finished by the next one, and until then
// the first version is already refused once the config step is done.
func TestDestroyFinishesWhatWasBegun(t *testing.T) {
	f := openbaotest.NewFakeTransit(t)
	c := f.Client(t)
	b := transit.New(c)
	ctx := t.Context()
	if _, err := b.MAC(ctx, "audit", keys.Pseudonym, "t", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Request(ctx, "POST", "transit/keys/audit.pseudonym.t/rotate", nil); err != nil {
		t.Fatal(err)
	}
	if gone, _ := b.Destroyed(ctx, "audit", keys.Pseudonym, "t"); gone {
		t.Fatal("a rotation alone is not the tombstone")
	}
	if err := b.DestroyTenant(ctx, "audit", keys.Pseudonym, "t"); err != nil {
		t.Fatal(err)
	}
	if f.Latest("audit.pseudonym.t") != 2 {
		t.Fatal("the finished Destroy rotated again")
	}
	if _, err := b.MAC(ctx, "audit", keys.Pseudonym, "t", []byte("x")); !errors.Is(err, keys.ErrDestroyed) {
		t.Fatalf("MAC: %v", err)
	}
}

// Only the pseudonym purpose has a key per tenant; the others are unchanged.
func TestOtherPurposesHaveNoTenantKeys(t *testing.T) {
	f := openbaotest.NewFakeTransit(t)
	b := transit.New(f.Client(t))
	ctx := t.Context()
	if err := b.DestroyTenant(ctx, "audit", keys.Conceal, "t"); !errors.Is(err, keys.ErrUnsupported) {
		t.Fatalf("Destroy: %v", err)
	}
	if _, err := b.EncryptTenant(ctx, "audit", keys.Conceal, "t", nil, nil); !errors.Is(err, keys.ErrUnsupported) {
		t.Fatalf("EncryptTenant: %v", err)
	}
	if len(f.Names()) != 0 {
		t.Fatalf("keys %v", f.Names())
	}
}
