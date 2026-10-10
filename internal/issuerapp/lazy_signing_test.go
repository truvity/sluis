package issuerapp_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/kms"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/issuerapp"
	"github.com/truvity/sluis/internal/secrets"
	"github.com/truvity/sluis/internal/store"
)

// stateSecretReads is a secrets source that counts the reads of one name (the
// state secret's), slowly enough that requests which arrive together overlap,
// and that can be made to fail its first reads.
type stateSecretReads struct {
	inner secrets.Source
	name  string
	n     atomic.Int32
	fail  atomic.Int32
}

func (s *stateSecretReads) Get(ctx context.Context, name string) (string, error) {
	if name == s.name {
		s.n.Add(1)
		time.Sleep(20 * time.Millisecond)
		if s.fail.Add(-1) >= 0 {
			return "", errors.New("the secrets store is unavailable")
		}
	}
	return s.inner.Get(ctx, name)
}

func (s *stateSecretReads) Describe(name string) string { return s.inner.Describe(name) }

// kmsCalls counts every call to the key service of the wrapped ring.
type kmsCalls struct {
	*fakeKMS
	calls atomic.Int32
}

func (k *kmsCalls) Encrypt(ctx context.Context, key string, pt []byte, ec map[string]string) ([]byte, error) {
	k.calls.Add(1)
	return k.fakeKMS.Encrypt(ctx, key, pt, ec)
}

func (k *kmsCalls) Decrypt(ctx context.Context, key string, ct []byte, ec map[string]string) ([]byte, error) {
	k.calls.Add(1)
	return k.fakeKMS.Decrypt(ctx, key, ct, ec)
}

func lazily(c *issuerapp.Config) { c.LazySigningKeys = true }

// lazyWrapped assembles a KMS-wrapped issuer that opens its keys on first use.
func lazyWrapped(t *testing.T) (*issuerapp.App, *stateSecretReads, *kmsCalls) {
	t.Helper()
	fake := &kmsCalls{fakeKMS: newFakeKMS(t)}
	var reads *stateSecretReads
	change := wrappedConfig(t, &config.SigningKeyKMSWrapped{})
	probe := &config.Serve{}
	change(probe)
	reads = &stateSecretReads{inner: testSecrets, name: probe.SigningKey.KMSWrapped.StateSecret}
	app := bootConfigured(t, issuerapp.Deps{Directory: stubHub(t, true, false), Keys: fake, Stores: &store.Stores{Secrets: reads}}, lazily,
		func(f *config.Serve) {
			change(f)
		})
	return app, reads, fake
}

func mint(t *testing.T, app *issuerapp.App) {
	t.Helper()
	if _, _, err := app.MintFor(context.Background(), "platform@north.example", "console", time.Minute); err != nil {
		t.Errorf("MintFor: %v", err)
	}
}

func discoveryAlgs(t *testing.T, app *issuerapp.App) []string {
	t.Helper()
	code, body := get(t, app.Handler(), "/.well-known/openid-configuration")
	if code != 200 {
		t.Fatalf("discovery = %d %s", code, body)
	}
	var doc struct {
		Algs []string `json:"id_token_signing_alg_values_supported"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	slices.Sort(doc.Algs)
	return doc.Algs
}

// Assembling a KMS-wrapped issuer reads no state secret and makes no KMS call,
// and neither does the discovery document, which lists the configured
// algorithms. The first signing reads the secret once and opens the keys.
func TestLazyWrappedKeysOpenAtTheFirstSigning(t *testing.T) {
	app, reads, fake := lazyWrapped(t)
	if reads.n.Load() != 0 || fake.calls.Load() != 0 {
		t.Fatalf("assembling read the secret %d times and called KMS %d times", reads.n.Load(), fake.calls.Load())
	}
	if algs := discoveryAlgs(t, app); !slices.Equal(algs, []string{"ES384", "RS256"}) {
		t.Errorf("discovery algorithms = %v", algs)
	}
	if reads.n.Load() != 0 || fake.calls.Load() != 0 {
		t.Fatalf("discovery read the secret %d times and called KMS %d times", reads.n.Load(), fake.calls.Load())
	}
	mint(t, app)
	if reads.n.Load() != 1 || fake.calls.Load() == 0 {
		t.Fatalf("the first signing read the secret %d times and called KMS %d times, want one read and some calls", reads.n.Load(), fake.calls.Load())
	}
	calls := fake.calls.Load()
	mint(t, app)
	if code, _ := get(t, app.Handler(), "/keys"); code != 200 {
		t.Errorf("/keys = %d", code)
	}
	if reads.n.Load() != 1 || fake.calls.Load() != calls {
		t.Errorf("later requests read the secret %d times in all and made %d KMS calls, want 1 and %d", reads.n.Load(), fake.calls.Load(), calls)
	}
}

// Concurrent first signings share one read of the secret and one set of KMS calls.
func TestConcurrentFirstSigningsOpenTheKeysOnce(t *testing.T) {
	once, _, single := lazyWrapped(t)
	mint(t, once)
	want := single.calls.Load()

	app, reads, fake := lazyWrapped(t)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			mint(t, app)
		}()
	}
	close(start)
	wg.Wait()
	if reads.n.Load() != 1 {
		t.Errorf("16 concurrent first signings read the secret %d times, want 1", reads.n.Load())
	}
	if got := fake.calls.Load(); got != want {
		t.Errorf("16 concurrent first signings made %d KMS calls, one signing makes %d", got, want)
	}
}

// The key set is published from the open rings, so the first request for it
// pays, once; and a failed open is not remembered.
func TestTheFirstKeySetRequestOpensTheKeysAndAFailedOpenIsRetried(t *testing.T) {
	app, reads, _ := lazyWrapped(t)
	reads.fail.Store(1)
	if code, _ := get(t, app.Handler(), "/keys"); code == 200 {
		t.Fatal("/keys answered though the state secret could not be read")
	}
	if code, body := get(t, app.Handler(), "/keys"); code != 200 {
		t.Fatalf("/keys after the secret came back = %d %s", code, body)
	}
	if reads.n.Load() != 2 {
		t.Errorf("reads = %d, want the failed one and the one that worked", reads.n.Load())
	}
	get(t, app.Handler(), "/keys")
	if reads.n.Load() != 2 {
		t.Errorf("a later /keys read the secret again: %d", reads.n.Load())
	}
}

// The deprecated direct KMS mode opens on first use too: its public keys are
// read from KMS with the secret as their seed.
func TestLazyDirectKMSKeysOpenAtTheFirstKeySet(t *testing.T) {
	a, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	var api countingKMSFake
	api.kmsFake = kmsFake{"alias/a": a}
	change := kmsConfig(t, "alias/a")
	probe := &config.Serve{}
	change(probe)
	reads := &stateSecretReads{inner: testSecrets, name: probe.SigningKey.KMS.StateSecret}
	app := bootConfigured(t, issuerapp.Deps{Directory: nobody{}, KMS: &api, Stores: &store.Stores{Secrets: reads}}, lazily, change)
	if discoveryAlgs(t, app); reads.n.Load() != 0 || api.n.Load() != 0 {
		t.Fatalf("assembling and discovery read the secret %d times and called KMS %d times", reads.n.Load(), api.n.Load())
	}
	if code, _ := get(t, app.Handler(), "/keys"); code != 200 || reads.n.Load() != 1 || api.n.Load() != 1 {
		t.Fatalf("/keys = %d after %d reads and %d KMS calls, want 200, 1 and 1", code, reads.n.Load(), api.n.Load())
	}
}

type countingKMSFake struct {
	kmsFake
	n atomic.Int32
}

func (c *countingKMSFake) GetPublicKey(ctx context.Context, in *kms.GetPublicKeyInput, opts ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error) {
	c.n.Add(1)
	return c.kmsFake.GetPublicKey(ctx, in, opts...)
}
