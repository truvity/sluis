package issuer

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"sync"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/truvity/sluis/internal/kmsfake"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/internal/rails"
)

const (
	testRotate     = 24 * time.Hour
	testPrepublish = 15 * time.Minute
	testRetain     = time.Hour + KeyOverlapSkew
)

// The rings are built on the real clock (NewKeyRings takes none) and then moved
// to the test clock, so the test clock starts at the real time.
var wrapT0 = time.Now().UTC().Truncate(time.Second)

type wrapClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *wrapClock) now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *wrapClock) set(t time.Time)         { c.mu.Lock(); defer c.mu.Unlock(); c.t = t }
func (c *wrapClock) advance(d time.Duration) { c.set(c.now().Add(d)) }

// replica is one process: its own KeyRings and WrappedSigning over the shared
// state, KMS and lease store.
type replica struct {
	ws    *WrappedSigning
	rings *KeyRings
}

type wrapEnv struct {
	kms    *kmsfake.KMS
	state  *MemoryState
	leases *memory.Store
	clock  *wrapClock
	algs   []jose.SignatureAlgorithm
	deny   bool // the lease is always held by someone else
}

func newWrapEnv(algs ...jose.SignatureAlgorithm) *wrapEnv {
	if len(algs) == 0 {
		algs = []jose.SignatureAlgorithm{jose.ES384}
	}
	clock := &wrapClock{t: wrapT0}
	state := NewMemoryState()
	state.SetClock(clock.now)
	return &wrapEnv{kms: kmsfake.New(), state: state, leases: memory.New(), clock: clock, algs: algs}
}

func (e *wrapEnv) config() WrappedConfig {
	return WrappedConfig{
		KeyID: "alias/sluis-wrapped", Algorithms: e.algs,
		RotateEvery: testRotate, Prepublish: testPrepublish, Retain: testRetain, Interval: 30 * time.Second,
	}
}

func (e *wrapEnv) start(t *testing.T) *replica {
	t.Helper()
	leases := &rails.Leases{State: e.leases, Holder: rails.NewHolder()}
	lease := func(ctx context.Context, alg jose.SignatureAlgorithm, fn func(context.Context) error) (bool, error) {
		if e.deny {
			return false, nil
		}
		var inner error
		ran, err := leases.Do(ctx, "signing-keygen", string(alg), func(c context.Context) { inner = fn(c) })
		if err != nil {
			return false, err
		}
		return ran, inner
	}
	seed := make([]byte, 32)
	_, _ = rand.Read(seed)
	ws, err := NewWrappedSigning(e.config(), e.kms, seed, lease, nil)
	if err != nil {
		t.Fatal(err)
	}
	ws.SetClock(e.clock.now)
	primary, more, err := ws.Bootstrap(context.Background(), e.state)
	if err != nil {
		t.Fatal(err)
	}
	rings, err := NewKeyRings(primary, more, e.state, ws.ringConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rings.rings {
		r.SetClock(e.clock.now)
	}
	rings.UseWrapped(ws)
	return &replica{ws: ws, rings: rings}
}

func (r *replica) maintain() { r.rings.Maintain(context.Background()) }

func (r *replica) published(alg jose.SignatureAlgorithm) []string {
	var out []string
	for _, k := range r.rings.rings[alg].Published() {
		out = append(out, k.ID())
	}
	return out
}

func contains(ids []string, id string) bool {
	for _, i := range ids {
		if i == id {
			return true
		}
	}
	return false
}

// signAndVerify signs with the ring's active key and verifies the token with
// the PUBLISHED key of its kid, as a relying party does.
func signAndVerify(t *testing.T, r *replica, alg jose.SignatureAlgorithm) string {
	t.Helper()
	active := r.rings.Active(alg)
	if active == nil {
		t.Fatalf("%s: no active key", alg)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: alg, Key: active.Key()},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", active.ID()))
	if err != nil {
		t.Fatal(err)
	}
	jws, err := signer.Sign([]byte(`{"sub":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	token, _ := jws.CompactSerialize()
	parsed, err := jose.ParseSigned(token, []jose.SignatureAlgorithm{alg})
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.Signatures[0].Header.KeyID; got != active.ID() {
		t.Fatalf("kid %q, want %q", got, active.ID())
	}
	for _, k := range r.rings.Published() {
		if k.ID() == active.ID() {
			if _, err := parsed.Verify(k.Key()); err != nil {
				t.Fatalf("%s token does not verify with the published key: %v", alg, err)
			}
			return active.ID()
		}
	}
	t.Fatalf("%s: the active key %s is not published", alg, active.ID())
	return ""
}

func TestWrappedFirstStartGeneratesActiveKeysForEveryAlgorithm(t *testing.T) {
	env := newWrapEnv(jose.ES384, jose.RS256)
	r := env.start(t)
	if env.kms.Generated != 2 {
		t.Fatalf("generated %d key pairs, want one per algorithm", env.kms.Generated)
	}
	if got := r.rings.Default(); got != jose.ES384 {
		t.Errorf("default %s", got)
	}
	for _, alg := range env.algs {
		signAndVerify(t, r, alg)
		if n := len(r.published(alg)); n != 1 {
			t.Errorf("%s: %d keys published", alg, n)
		}
	}
	// The contexts name purpose, algorithm and kid, and nothing else.
	for _, c := range env.kms.Contexts {
		if len(c) != 3 || c["purpose"] != WrapPurpose || c["alg"] == "" || c["kid"] == "" {
			t.Errorf("encryption context %v", c)
		}
	}
	// The JWKS carries public keys only, of the right shape.
	for _, k := range r.rings.Published() {
		if _, ok := k.Key().(interface{ Public() any }); ok {
			t.Errorf("%s publishes a private key", k.ID())
		}
	}
}

func TestWrappedNoPlaintextPrivateKeyIsStored(t *testing.T) {
	env := newWrapEnv(jose.ES384, jose.RS256)
	r := env.start(t)
	// Rotate once, so entries of two generations exist.
	env.clock.advance(testRotate)
	r.maintain()
	if env.kms.Generated != 4 {
		t.Fatalf("generated %d", env.kms.Generated)
	}
	env.state.mu.Lock()
	defer env.state.mu.Unlock()
	checked := 0
	for key, v := range env.state.values {
		for _, plain := range env.kms.Plaintexts {
			// The key is DER; the state holds JSON, so base64 is how it would appear.
			for _, needle := range [][]byte{plain, []byte(base64.StdEncoding.EncodeToString(plain)),
				[]byte(base64.RawURLEncoding.EncodeToString(plain)), plain[len(plain)-40:]} {
				if bytes.Contains(v.value, needle) {
					t.Fatalf("%s holds plaintext private key material", key)
				}
			}
		}
		if strings.Contains(string(v.value), "PRIVATE KEY") {
			t.Fatalf("%s holds a PEM private key", key)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("the state holds nothing: the check proves nothing")
	}
}

func TestWrappedRotationFollowsTheSchedule(t *testing.T) {
	env := newWrapEnv(jose.ES384, jose.RS256)
	r := env.start(t)
	first := map[jose.SignatureAlgorithm]string{}
	for _, alg := range env.algs {
		first[alg] = signAndVerify(t, r, alg)
	}

	// Before rotateEvery: nothing new, however often it runs.
	env.clock.advance(testRotate - time.Minute)
	r.maintain()
	if env.kms.Generated != 2 {
		t.Fatalf("a key was generated early: %d", env.kms.Generated)
	}

	// At rotateEvery: a new key per algorithm, published at once, not yet signing.
	env.clock.advance(time.Minute)
	r.maintain()
	if env.kms.Generated != 4 {
		t.Fatalf("generated %d, want 4 after one period", env.kms.Generated)
	}
	for _, alg := range env.algs {
		if n := len(r.published(alg)); n != 2 {
			t.Fatalf("%s: %d keys published, want the old and the new", alg, n)
		}
		if got := r.rings.Active(alg).ID(); got != first[alg] {
			t.Errorf("%s: signs with %s before the pre-publish ended", alg, got)
		}
	}

	// Just short of the pre-publish: still the old key.
	env.clock.advance(testPrepublish - time.Minute)
	r.maintain()
	for _, alg := range env.algs {
		if got := r.rings.Active(alg).ID(); got != first[alg] {
			t.Errorf("%s: switched %s early", alg, got)
		}
	}
	// After it: the new key signs, and the old one is still published.
	env.clock.advance(time.Minute)
	r.maintain()
	second := map[jose.SignatureAlgorithm]string{}
	for _, alg := range env.algs {
		second[alg] = signAndVerify(t, r, alg)
		if second[alg] == first[alg] {
			t.Errorf("%s: still signing with the first key", alg)
		}
		if !contains(r.published(alg), first[alg]) {
			t.Errorf("%s: the replaced key left the JWKS at once", alg)
		}
	}

	// The replaced key is kept for the retention, then dropped.
	env.clock.advance(testRetain - time.Minute)
	r.maintain()
	for _, alg := range env.algs {
		if !contains(r.published(alg), first[alg]) {
			t.Errorf("%s: dropped before the retention ended", alg)
		}
	}
	env.clock.advance(2 * time.Minute)
	r.maintain()
	for _, alg := range env.algs {
		if contains(r.published(alg), first[alg]) {
			t.Errorf("%s: still published after the retention", alg)
		}
		if !contains(r.published(alg), second[alg]) {
			t.Errorf("%s: the signing key vanished", alg)
		}
	}
}

func TestWrappedAnotherReplicaLearnsAndUnwrapsTheRotatedKey(t *testing.T) {
	env := newWrapEnv(jose.ES384)
	a := env.start(t)
	b := env.start(t)
	first := signAndVerify(t, b, jose.ES384)
	if env.kms.Generated != 1 {
		t.Fatalf("two cold starts generated %d keys", env.kms.Generated)
	}

	env.clock.advance(testRotate)
	a.maintain() // A generates
	b.maintain() // B learns; nothing to generate
	if env.kms.Generated != 2 {
		t.Fatalf("generated %d, want 2", env.kms.Generated)
	}
	if n := len(b.published(jose.ES384)); n != 2 {
		t.Fatalf("B publishes %d keys, want the new one too", n)
	}
	decrypts := env.kms.Decrypted
	env.clock.advance(testPrepublish)
	b.maintain()
	got := signAndVerify(t, b, jose.ES384)
	if got == first {
		t.Fatal("B did not move to the rotated key")
	}
	if env.kms.Decrypted != decrypts+1 {
		t.Errorf("B unwrapped the key with %d calls, want 1", env.kms.Decrypted-decrypts)
	}
	_ = a
}

func TestWrappedALostLeaseRaceIsHarmless(t *testing.T) {
	env := newWrapEnv(jose.ES384)
	a, b := env.start(t), env.start(t)
	env.clock.advance(testRotate)

	// Both are due at once, and run together: the lease serialises them and the
	// loser re-reads before it generates.
	var wg sync.WaitGroup
	for _, r := range []*replica{a, b, a, b} {
		wg.Add(1)
		go func() { defer wg.Done(); r.maintain() }()
	}
	wg.Wait()
	if env.kms.Generated > 3 {
		t.Fatalf("generated %d keys for one rotation", env.kms.Generated)
	}
	// Whatever interleaving: both replicas converge on one signing key that
	// verifies, and the old one is still published.
	env.clock.advance(testPrepublish + time.Minute)
	a.maintain()
	b.maintain()
	ida, idb := signAndVerify(t, a, jose.ES384), signAndVerify(t, b, jose.ES384)
	if ida != idb {
		t.Errorf("the replicas sign with different keys: %s and %s", ida, idb)
	}

	// A lease another runner holds: nothing is generated, and signing goes on.
	env2 := newWrapEnv(jose.ES384)
	r := env2.start(t)
	env2.deny = true
	env2.clock.advance(testRotate + time.Hour)
	r.maintain()
	if env2.kms.Generated != 1 {
		t.Errorf("generated without the lease: %d", env2.kms.Generated)
	}
	signAndVerify(t, r, jose.ES384)
}

func TestWrappedRefusesAContextMismatch(t *testing.T) {
	env := newWrapEnv(jose.ES384)
	r := env.start(t)
	ring := r.rings.rings[jose.ES384]
	var e *ringEntry
	for _, x := range ring.entries {
		e = x
	}
	if _, err := r.ws.unwrap(context.Background(), jose.ES384, e.ID, e.JWK.Key, e.Wrapped); err != nil {
		t.Fatalf("the right context: %v", err)
	}
	for name, try := range map[string]func() error{
		"another kid": func() error {
			_, err := r.ws.unwrap(context.Background(), jose.ES384, "other-kid", e.JWK.Key, e.Wrapped)
			return err
		},
		"another algorithm": func() error {
			_, err := r.ws.unwrap(context.Background(), jose.RS256, e.ID, e.JWK.Key, e.Wrapped)
			return err
		},
		"a blob KMS never made": func() error {
			_, err := r.ws.unwrap(context.Background(), jose.ES384, e.ID, e.JWK.Key, []byte("not a ciphertext"))
			return err
		},
	} {
		if err := try(); err == nil {
			t.Errorf("%s: unwrapped", name)
		}
	}

	// A key moved under another kid in the state fails to start rather than sign.
	other := newWrapEnv(jose.ES384)
	other.start(t)
	for k, v := range env.state.values {
		if strings.Contains(k, "issuer:keyring:entry:ES384:") {
			other.state.values[strings.Replace(k, e.ID, "moved", 1)] = memoryValue{value: []byte(strings.ReplaceAll(string(v.value), e.ID, "moved")), expires: v.expires}
		}
	}
	// (Different KMS: the blob is unknown there, which must refuse too.)
	ws2, err := NewWrappedSigning(other.config(), other.kms, make([]byte, 32), func(context.Context, jose.SignatureAlgorithm, func(context.Context) error) (bool, error) {
		return false, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws2.unwrap(context.Background(), jose.ES384, "moved", e.JWK.Key, e.Wrapped); err == nil {
		t.Error("a blob from another KMS unwrapped")
	}
}

func TestWrappedUnwrapRefusesAPublicKeyThatIsNotItsPair(t *testing.T) {
	env := newWrapEnv(jose.ES384)
	r := env.start(t)
	stranger := newWrapEnv(jose.ES384).start(t)
	var mine, theirs *ringEntry
	for _, x := range r.rings.rings[jose.ES384].entries {
		mine = x
	}
	for _, x := range stranger.rings.rings[jose.ES384].entries {
		theirs = x
	}
	if _, err := r.ws.unwrap(context.Background(), jose.ES384, mine.ID, theirs.JWK.Key, mine.Wrapped); err == nil ||
		!strings.Contains(err.Error(), "not the pair") {
		t.Errorf("a published key that is not the pair: %v", err)
	}
}

func TestWrappedMissingPermissionsAreNamed(t *testing.T) {
	env := newWrapEnv(jose.ES384)
	env.kms.DenyGenerate = true
	ws, _ := NewWrappedSigning(env.config(), env.kms, make([]byte, 32), func(ctx context.Context, _ jose.SignatureAlgorithm, fn func(context.Context) error) (bool, error) {
		return true, fn(ctx)
	}, nil)
	_, _, err := ws.Bootstrap(context.Background(), env.state)
	if err == nil || !strings.Contains(err.Error(), "kms:GenerateDataKeyPairWithoutPlaintext") {
		t.Errorf("a denied generate: %v", err)
	}

	env = newWrapEnv(jose.ES384)
	env.start(t)
	env.kms.DenyDecrypt = true
	ws, _ = NewWrappedSigning(env.config(), env.kms, make([]byte, 32), func(ctx context.Context, _ jose.SignatureAlgorithm, fn func(context.Context) error) (bool, error) {
		return true, fn(ctx)
	}, nil)
	_, _, err = ws.Bootstrap(context.Background(), env.state)
	if err == nil || !strings.Contains(err.Error(), "kms:Decrypt") {
		t.Errorf("a denied decrypt must stop the start, not mint a second key: %v", err)
	}
	if env.kms.Generated != 1 {
		t.Errorf("a second key was generated: %d", env.kms.Generated)
	}
}

func TestWrappedCoexistsWithKeysOfOtherSources(t *testing.T) {
	env := newWrapEnv(jose.ES384)
	// A key another source recorded: public half only, long active, no private
	// key this process can use (a file or KMS key of the previous deployment).
	foreign, err := NewSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	old := NewKeyRing(jose.ES384, env.state, KeyRingConfig{ActivationDelay: testPrepublish, Overlap: testRetain}, nil)
	old.SetClock(env.clock.now)
	if err := old.Observe(context.Background(), foreign); err != nil {
		t.Fatal(err)
	}
	env.clock.advance(time.Hour)

	r := env.start(t)
	// The foreign key cannot sign here, so the wrapped key is active at once
	// (waiting behind a key nobody can sign with would leave no signer), and
	// the foreign key stays in the JWKS for tokens it already signed.
	id := signAndVerify(t, r, jose.ES384)
	if id == foreign.ID() {
		t.Fatal("signed with a key it holds no private half of")
	}
	if !contains(r.published(jose.ES384), foreign.ID()) {
		t.Error("the other source's key left the JWKS")
	}
	// It retires on the normal schedule.
	env.clock.advance(testRetain + time.Minute)
	r.maintain()
	if contains(r.published(jose.ES384), foreign.ID()) {
		t.Error("the other source's key was never retired")
	}
}

func TestWrappedConfigValidation(t *testing.T) {
	ok := WrappedConfig{KeyID: "k", Algorithms: []jose.SignatureAlgorithm{jose.ES384, jose.RS256},
		RotateEvery: testRotate, Prepublish: testPrepublish, Retain: testRetain}
	if err := ok.Validate(DefaultTokenLifetime); err != nil {
		t.Fatalf("the defaults: %v", err)
	}
	for name, tc := range map[string]struct {
		mutate func(*WrappedConfig)
		want   string
	}{
		"no key":             {func(c *WrappedConfig) { c.KeyID = "" }, "keyId"},
		"no algorithms":      {func(c *WrappedConfig) { c.Algorithms = nil }, "at least one"},
		"EdDSA":              {func(c *WrappedConfig) { c.Algorithms = []jose.SignatureAlgorithm{jose.EdDSA} }, "EdDSA is not supported"},
		"ES256":              {func(c *WrappedConfig) { c.Algorithms = []jose.SignatureAlgorithm{jose.ES256} }, "not supported"},
		"twice":              {func(c *WrappedConfig) { c.Algorithms = []jose.SignatureAlgorithm{jose.RS256, jose.RS256} }, "twice"},
		"retain too short":   {func(c *WrappedConfig) { c.Retain = DefaultTokenLifetime }, "retain"},
		"rotate<=prepublish": {func(c *WrappedConfig) { c.RotateEvery = testPrepublish }, "longer than prepublish"},
		"rotate too long":    {func(c *WrappedConfig) { c.RotateEvery = 30 * 24 * time.Hour }, "at most"},
		"zero":               {func(c *WrappedConfig) { c.Prepublish = 0 }, "positive"},
	} {
		c := ok
		tc.mutate(&c)
		if err := c.Validate(DefaultTokenLifetime); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
	if err := (WrappedConfig{}).Validate(0); err == nil {
		t.Error("an empty configuration is valid")
	}
}
