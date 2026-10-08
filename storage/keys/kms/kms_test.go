package kms_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awskms "github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"

	"github.com/truvity/sluis/storage/keys"
	"github.com/truvity/sluis/storage/keys/conformance"
	"github.com/truvity/sluis/storage/keys/kms"
	"github.com/truvity/sluis/storage/state/memory"
)

// EnvURL names a LocalStack (or any KMS endpoint). Unset, the tests that need
// KMS skip and `go test ./...` stays hermetic; hack/keys-conformance.sh sets
// it and refuses a run that skipped.
const EnvURL = "KEYS_KMS_URL"

type memStore struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (s *memStore) Get(_ context.Context, id string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[id]
	return v, ok, nil
}

func (s *memStore) PutIfAbsent(_ context.Context, id string, w []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.m[id]; ok {
		return v, nil
	}
	if s.m == nil {
		s.m = map[string][]byte{}
	}
	s.m[id] = w
	return w, nil
}

func client(t *testing.T) *awskms.Client {
	t.Helper()
	url := os.Getenv(EnvURL)
	if url == "" {
		t.Skipf("%s is not set: no KMS to run against", EnvURL)
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_REGION", "us-east-1")
	cfg, err := awsconfig.LoadDefaultConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return awskms.NewFromConfig(cfg, func(o *awskms.Options) { o.BaseEndpoint = aws.String(url) })
}

func newKey(t *testing.T, c *awskms.Client, spec types.KeySpec, usage types.KeyUsageType) string {
	t.Helper()
	ctx := context.Background()
	out, err := c.CreateKey(ctx, &awskms.CreateKeyInput{KeySpec: spec, KeyUsage: usage})
	if err != nil {
		t.Fatal(err)
	}
	r := make([]byte, 6)
	_, _ = rand.Read(r)
	alias := "alias/keys-" + hex.EncodeToString(r)
	if _, err := c.CreateAlias(ctx, &awskms.CreateAliasInput{AliasName: &alias, TargetKeyId: out.KeyMetadata.KeyId}); err != nil {
		t.Fatal(err)
	}
	return alias
}

func TestConformance(t *testing.T) {
	c := client(t)
	conformance.Run(t, conformance.Subject{
		Backend:        kms.New(c, kms.WithWrappedStore(kms.FromState(memory.New()))),
		Symmetric:      newKey(t, c, types.KeySpecSymmetricDefault, types.KeyUsageTypeEncryptDecrypt),
		OtherSymmetric: newKey(t, c, types.KeySpecSymmetricDefault, types.KeyUsageTypeEncryptDecrypt),
		Signing:        newKey(t, c, types.KeySpecEccNistP384, types.KeyUsageTypeSignVerify),
	})
}

func TestRSASignsRS256(t *testing.T) {
	c := client(t)
	alias := newKey(t, c, types.KeySpecRsa2048, types.KeyUsageTypeSignVerify)
	b := kms.New(c)
	_, alg, err := b.PublicKey(t.Context(), alias)
	if err != nil || alg != "RS256" {
		t.Fatalf("%s, %v", alg, err)
	}
	if _, err := b.Sign(t.Context(), alias, make([]byte, 48)); err == nil {
		t.Fatal("a SHA-384 digest was accepted for RS256")
	}
	if sig, err := b.Sign(t.Context(), alias, make([]byte, 32)); err != nil || len(sig) != 256 {
		t.Fatalf("%d, %v", len(sig), err)
	}
}

func TestSymmetricKeyIsNotASigningKey(t *testing.T) {
	c := client(t)
	alias := newKey(t, c, types.KeySpecSymmetricDefault, types.KeyUsageTypeEncryptDecrypt)
	if _, _, err := kms.New(c).PublicKey(t.Context(), alias); err == nil {
		t.Fatal("a symmetric key has a public key?")
	}
}

// Wrapped HMAC keys: a second process (a new Backend over the same store)
// derives the same pseudonym from the stored wrapped key, concurrent first
// uses converge, and the stored form is bound to its tenant.
func TestMACWrappedKeys(t *testing.T) {
	c := client(t)
	alias := newKey(t, c, types.KeySpecSymmetricDefault, types.KeyUsageTypeEncryptDecrypt)
	store := &memStore{}
	ctx := context.Background()

	var wg sync.WaitGroup
	got := make([][]byte, 8)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b := kms.New(c, kms.WithWrappedStore(store)) // each a "process" of its own
			got[i], _ = b.MAC(ctx, alias, keys.Pseudonym, "tenant-1", []byte("alice"))
		}()
	}
	wg.Wait()
	for _, g := range got {
		if len(g) != 32 || !bytes.Equal(g, got[0]) {
			t.Fatalf("processes disagree: %x vs %x", g, got[0])
		}
	}
	if len(store.m) != 1 {
		t.Fatalf("%d wrapped keys stored for one (purpose, tenant)", len(store.m))
	}

	// Moving tenant-1's wrapped key to tenant-2's slot must not work: the
	// context {purpose, tenant} is part of what KMS checks.
	other := &memStore{m: map[string][]byte{}}
	for _, w := range store.m {
		other.m["mac/pseudonym/"+base64.RawURLEncoding.EncodeToString([]byte("tenant-2"))] = w
	}
	if _, err := kms.New(c, kms.WithWrappedStore(other)).MAC(ctx, alias, keys.Pseudonym, "tenant-2", nil); err == nil {
		t.Fatal("tenant-2 unwrapped tenant-1's key")
	}
}

func TestMACOverStateStore(t *testing.T) {
	c := client(t)
	alias := newKey(t, c, types.KeySpecSymmetricDefault, types.KeyUsageTypeEncryptDecrypt)
	st := memory.New().Child("wrapped")
	a := kms.New(c, kms.WithWrappedStore(kms.FromState(st)))
	b := kms.New(c, kms.WithWrappedStore(kms.FromState(st)))
	m1, err := a.MAC(t.Context(), alias, keys.Pseudonym, "tenant/1", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	m2, err := b.MAC(t.Context(), alias, keys.Pseudonym, "tenant/1", []byte("x"))
	if err != nil || !bytes.Equal(m1, m2) {
		t.Fatalf("%x vs %x, %v", m1, m2, err)
	}
	id := "mac/pseudonym/" + base64.RawURLEncoding.EncodeToString([]byte("tenant/1"))
	if _, err := st.Get(t.Context(), id); err != nil {
		t.Fatalf("the wrapped key is not in the state store: %v", err)
	}
}

// Erasure over the state store: the wrapped key is gone from the store with
// the tombstone in its place, another process refuses too, and a tenant that
// was never used is refused after Destroy rather than minted.
func TestDestroyOverStateStore(t *testing.T) {
	c := client(t)
	alias := newKey(t, c, types.KeySpecSymmetricDefault, types.KeyUsageTypeEncryptDecrypt)
	st := memory.New().Child("wrapped")
	ctx := t.Context()
	a := kms.New(c, kms.WithWrappedStore(kms.FromState(st)))
	b := kms.New(c, kms.WithWrappedStore(kms.FromState(st)))
	if _, err := a.MAC(ctx, alias, keys.Pseudonym, "t1", []byte("x")); err != nil {
		t.Fatal(err)
	}
	id := "mac/pseudonym/" + base64.RawURLEncoding.EncodeToString([]byte("t1"))
	if err := a.DestroyTenant(ctx, alias, keys.Pseudonym, "t1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Get(ctx, id); err == nil {
		t.Fatal("the wrapped key is still in the state store")
	}
	if _, err := st.Get(ctx, "destroyed/"+id); err != nil {
		t.Fatalf("no tombstone: %v", err)
	}
	for _, x := range []*kms.Backend{a, b} {
		if _, err := x.MAC(ctx, alias, keys.Pseudonym, "t1", []byte("x")); !errors.Is(err, keys.ErrDestroyed) {
			t.Fatalf("MAC after Destroy: %v", err)
		}
	}
	if _, err := st.Get(ctx, id); err == nil {
		t.Fatal("MAC of a destroyed tenant created a new wrapped key")
	}
}

// A store that cannot keep a tombstone cannot destroy: not a silent no-op.
func TestDestroyNeedsAnErasableStore(t *testing.T) {
	for name, b := range map[string]*kms.Backend{
		"no store":    kms.New(nil),
		"plain store": kms.New(nil, kms.WithWrappedStore(&memStore{})),
	} {
		if err := b.DestroyTenant(t.Context(), "alias/x", keys.Pseudonym, "t"); !errors.Is(err, keys.ErrUnsupported) {
			t.Errorf("%s: got %v, want ErrUnsupported", name, err)
		}
	}
}

func TestMACNeedsAStore(t *testing.T) {
	b := kms.New(nil)
	if _, err := b.MAC(t.Context(), "alias/x", keys.Pseudonym, "t", nil); err == nil {
		t.Fatal("MAC without a store")
	}
}

func TestValidateName(t *testing.T) {
	b := kms.New(nil)
	for name, want := range map[string]string{
		"arn:aws:kms:eu-west-1:111122223333:alias/x": "ARN",
		"1234abcd-12ab-34cd-56ef-1234567890ab":       "alias",
		"alias/aws/s3":                               "managed",
		"alias/":                                     "followed by",
		"alias/has space":                            "followed by",
		"alias/audit-seal":                           "",
		"alias/team/audit_seal":                      "",
	} {
		err := b.ValidateName(name)
		switch {
		case want == "" && err != nil, want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%q: %v", name, err)
		}
	}
}
