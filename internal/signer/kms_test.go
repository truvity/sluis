package signer_test

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/aws/smithy-go"
	jose "github.com/go-jose/go-jose/v4"
	"github.com/zitadel/oidc/v3/pkg/op"

	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/internal/signer"
)

// fakeKMS signs with a local P-384 key and answers in DER, as KMS does.
type fakeKMS struct {
	rsaKey   *rsa.PrivateKey // set: an RSA key, spec RSA_3072 unless changed
	key      *ecdsa.PrivateKey
	arn      string
	spec     types.KeySpec
	usage    types.KeyUsageType
	pubErr   error
	signErr  error
	signs    int
	lastSign *kms.SignInput
}

func newFakeKMS(t *testing.T) *fakeKMS {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &fakeKMS{key: key, arn: "arn:aws:kms:eu-west-1:111122223333:key/abc",
		spec: types.KeySpecEccNistP384, usage: types.KeyUsageTypeSignVerify}
}

func newFakeRSAKMS(t *testing.T) *fakeKMS {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		t.Fatal(err)
	}
	return &fakeKMS{rsaKey: key, arn: "arn:aws:kms:eu-west-1:111122223333:key/rsa",
		spec: types.KeySpecRsa3072, usage: types.KeyUsageTypeSignVerify}
}

func (f *fakeKMS) GetPublicKey(_ context.Context, _ *kms.GetPublicKeyInput, _ ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error) {
	if f.pubErr != nil {
		return nil, f.pubErr
	}
	if f.rsaKey != nil {
		der, err := x509.MarshalPKIXPublicKey(&f.rsaKey.PublicKey)
		if err != nil {
			return nil, err
		}
		return &kms.GetPublicKeyOutput{
			KeyId: aws.String(f.arn), KeySpec: f.spec, KeyUsage: f.usage, PublicKey: der,
			SigningAlgorithms: []types.SigningAlgorithmSpec{types.SigningAlgorithmSpecRsassaPkcs1V15Sha256},
		}, nil
	}
	der, err := x509.MarshalPKIXPublicKey(&f.key.PublicKey)
	if err != nil {
		return nil, err
	}
	return &kms.GetPublicKeyOutput{
		KeyId: aws.String(f.arn), KeySpec: f.spec, KeyUsage: f.usage, PublicKey: der,
		SigningAlgorithms: []types.SigningAlgorithmSpec{types.SigningAlgorithmSpecEcdsaSha384},
	}, nil
}

func (f *fakeKMS) Sign(_ context.Context, in *kms.SignInput, _ ...func(*kms.Options)) (*kms.SignOutput, error) {
	f.signs++
	f.lastSign = in
	if f.signErr != nil {
		return nil, f.signErr
	}
	if f.rsaKey != nil {
		if in.SigningAlgorithm != types.SigningAlgorithmSpecRsassaPkcs1V15Sha256 || len(in.Message) != 32 {
			return nil, errors.New("fake: not an RS256 digest request")
		}
		sig, err := rsa.SignPKCS1v15(rand.Reader, f.rsaKey, crypto.SHA256, in.Message)
		return &kms.SignOutput{Signature: sig}, err
	}
	der, err := ecdsa.SignASN1(rand.Reader, f.key, in.Message)
	if err != nil {
		return nil, err
	}
	return &kms.SignOutput{Signature: der}, nil
}

var kmsSeed = []byte(strings.Repeat("s", 32))

// A token signed through KMS verifies against the public key the ring would
// publish, and KMS was asked for a digest, not the message.
func TestAKMSKeySignsATokenThatVerifies(t *testing.T) {
	t.Parallel()
	fake := newFakeKMS(t)
	key, err := signer.KMSSigningKey(context.Background(), fake, "alias/sluis-signing", kmsSeed)
	if err != nil {
		t.Fatalf("KMSSigningKey: %v", err)
	}
	if key.SignatureAlgorithm() != jose.ES384 {
		t.Fatalf("algorithm %s", key.SignatureAlgorithm())
	}

	// Signed the way the library signs: a JSONWebKey around the key.
	signer, err := op.SignerFromKey(key)
	if err != nil {
		t.Fatalf("SignerFromKey: %v", err)
	}
	for range 20 { // r and s are of varying length in DER; exercise the padding
		signed, err := signer.Sign([]byte(`{"sub":"x"}`))
		if err != nil {
			t.Fatalf("Sign: %v", err)
		}
		compact, err := signed.CompactSerialize()
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := jose.ParseSigned(compact, []jose.SignatureAlgorithm{jose.ES384})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parsed.Verify(&fake.key.PublicKey); err != nil {
			t.Fatalf("verify: %v", err)
		}
		if got := len(parsed.Signatures[0].Signature); got != 96 {
			t.Fatalf("signature is %d bytes, want 96", got)
		}
	}
	in := fake.lastSign
	if in.MessageType != types.MessageTypeDigest || in.SigningAlgorithm != types.SigningAlgorithmSpecEcdsaSha384 ||
		len(in.Message) != sha512.Size384 {
		t.Fatalf("KMS was asked %+v", in)
	}
	if aws.ToString(in.KeyId) != fake.arn {
		t.Fatalf("signed with %q, want the resolved ARN", aws.ToString(in.KeyId))
	}
}

// The kid is the key's thumbprint, so a file holding the same key has the same id.
func TestAKMSKeyHasTheKidAFileKeyWouldHave(t *testing.T) {
	t.Parallel()
	fake := newFakeKMS(t)
	key, err := signer.KMSSigningKey(context.Background(), fake, "k", kmsSeed)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(fake.key)
	file, err := signer.ParseSigningKey(pemOf("PRIVATE KEY", der))
	if err != nil {
		t.Fatal(err)
	}
	if key.ID() != file.ID() {
		t.Fatalf("kid %s, file kid %s", key.ID(), file.ID())
	}
}

func TestAKMSKeyOfTheWrongShapeIsRefused(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(*fakeKMS){
		"P-256":   func(f *fakeKMS) { f.spec = types.KeySpecEccNistP256 },
		"RSA":     func(f *fakeKMS) { f.spec = types.KeySpecRsa2048 },
		"ENCRYPT": func(f *fakeKMS) { f.usage = types.KeyUsageTypeEncryptDecrypt },
	} {
		fake := newFakeKMS(t)
		mutate(fake)
		if _, err := signer.KMSSigningKey(context.Background(), fake, "k", kmsSeed); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

type apiErr struct{ code string }

func (e apiErr) Error() string                 { return e.code }
func (e apiErr) ErrorCode() string             { return e.code }
func (e apiErr) ErrorMessage() string          { return e.code }
func (e apiErr) ErrorFault() smithy.ErrorFault { return smithy.FaultClient }

func TestAMissingGetPublicKeyGrantIsNamed(t *testing.T) {
	t.Parallel()
	fake := newFakeKMS(t)
	fake.pubErr = apiErr{"AccessDeniedException"}
	_, err := signer.KMSSigningKey(context.Background(), fake, "alias/sluis-signing", kmsSeed)
	if err == nil || !strings.Contains(err.Error(), "kms:GetPublicKey") {
		t.Fatalf("got %v", err)
	}
}

func TestAShortStateSecretIsRefused(t *testing.T) {
	t.Parallel()
	if _, err := signer.KMSSigningKey(context.Background(), newFakeKMS(t), "k", []byte("short")); err == nil {
		t.Fatal("accepted")
	}
}

func TestAKMSFailureSurfacesFromSign(t *testing.T) {
	t.Parallel()
	fake := newFakeKMS(t)
	key, err := signer.KMSSigningKey(context.Background(), fake, "k", kmsSeed)
	if err != nil {
		t.Fatal(err)
	}
	fake.signErr = errors.New("throttled")
	signer, _ := op.SignerFromKey(key)
	if _, err := signer.Sign([]byte("{}")); err == nil {
		t.Fatal("a failed kms:Sign produced a token")
	}
}

// Derive is the seed's, stable across reads of the same key.
func TestAKMSKeyDerivesFromItsStateSecret(t *testing.T) {
	t.Parallel()
	fake := newFakeKMS(t)
	a, _ := signer.KMSSigningKey(context.Background(), fake, "k", kmsSeed)
	b, _ := signer.KMSSigningKey(context.Background(), fake, "k", kmsSeed)
	if string(a.Derive("x")) != string(b.Derive("x")) || string(a.Derive("x")) == string(a.Derive("y")) {
		t.Fatal("derivation is not stable per label")
	}
}

func pemOf(kind string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der})
}

func ringAt(t *testing.T, state issuer.State, clock *settableClock) *signer.KeyRing {
	t.Helper()
	ring := signer.NewKeyRing(jose.ES384, state,
		signer.KeyRingConfig{ActivationDelay: time.Minute, Overlap: 10 * time.Minute}, nil)
	ring.SetClock(clock.now)
	return ring
}

// H1: keys [A,B] listed forever. Once A retires, polling the whole list again
// must not bring it back: A stays retired and B keeps signing.
func TestARetiredKeyStaysRetiredWhileItIsStillListed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	state := issuer.NewMemoryState()
	clock := newSettableClock(time.Now())
	ring := ringAt(t, state, clock)
	fake := newFakeKMS(t)
	a, err := signer.KMSSigningKey(ctx, fake, "a", kmsSeed)
	if err != nil {
		t.Fatal(err)
	}
	fakeB := newFakeKMS(t)
	b, _ := signer.KMSSigningKey(ctx, fakeB, "b", kmsSeed)

	_ = ring.Observe(ctx, a)
	_ = ring.Observe(ctx, b) // new: waits the delay
	clock.set(clock.now().Add(2 * time.Minute))
	_ = ring.Observe(ctx, b)
	if ring.Active().ID() != b.ID() {
		t.Fatal("b should sign after the delay")
	}
	clock.set(clock.now().Add(time.Hour)) // past a's overlap
	for range 5 {
		_ = ring.ObserveKnown(ctx, a)
		_ = ring.Observe(ctx, b)
		clock.set(clock.now().Add(time.Hour))
		if ring.Active().ID() != b.ID() {
			t.Fatal("rotation undid itself")
		}
	}
	assertPublished(t, ring, b.ID())

	// Even a full Observe of a retired key, or a restarted replica, does not
	// bring it back: the retirement is in the shared state.
	_ = ring.Observe(ctx, a)
	restarted := ringAt(t, state, clock)
	_ = restarted.Observe(ctx, a)
	_ = restarted.Observe(ctx, b)
	assertPublished(t, restarted, b.ID())
	if restarted.Active().ID() != b.ID() {
		t.Fatal("a restart re-adopted the retired key")
	}
}

// M1: the store already holds a file key; this replica now reads only KMS keys.
// No KMS key signs before its activation delay: the request fails closed.
func TestAMigrationDoesNotSignWithAKeyBeforeItsDelay(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	state := issuer.NewMemoryState()
	clock := newSettableClock(time.Now())
	file, err := signer.NewSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	_ = ringAt(t, state, clock).Observe(ctx, file) // another replica, still on files

	ring := ringAt(t, state, clock)
	k, _ := signer.KMSSigningKey(ctx, newFakeKMS(t), "k", kmsSeed)
	_ = ring.Observe(ctx, k)
	if got := ring.Active(); got != nil {
		t.Fatalf("signing with %s before the delay", got.ID())
	}
	clock.set(clock.now().Add(2 * time.Minute))
	_ = ring.Observe(ctx, k)
	if got := ring.Active(); got == nil || got.ID() != k.ID() {
		t.Fatal("the KMS key should sign after its delay")
	}
}

// A signature KMS returns that does not verify against the published public
// half is never handed to the library.
func TestASignatureThatDoesNotVerifyIsRefused(t *testing.T) {
	t.Parallel()
	fake := newFakeKMS(t)
	key, err := signer.KMSSigningKey(context.Background(), fake, "k", kmsSeed)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	fake.key = other // KMS now signs with a different key than the one read
	signer, _ := op.SignerFromKey(key)
	if _, err := signer.Sign([]byte("{}")); err == nil {
		t.Fatal("an unverifiable signature became a token")
	}
}

func TestAnEmptyKeyIdIsRefused(t *testing.T) {
	t.Parallel()
	fake := newFakeKMS(t)
	fake.arn = ""
	if _, err := signer.KMSSigningKey(context.Background(), fake, "alias/x", kmsSeed); err == nil {
		t.Fatal("fell back to the alias")
	}
}

// Hardening 1: a replica that starts with [A,B,C] on a state that knows none of
// them records only C; A and B are never adopted, so the oldest cannot sign.
func TestOnlyTheLastListedKeyIsRecordedAsNew(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clock := newSettableClock(time.Now())
	ring := ringAt(t, issuer.NewMemoryState(), clock)
	var keys []*signer.SigningKey
	for _, ref := range []string{"a", "b", "c"} {
		k, err := signer.KMSSigningKey(ctx, newFakeKMS(t), ref, kmsSeed)
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, k)
	}
	_ = ring.ObserveKnown(ctx, keys[0])
	_ = ring.ObserveKnown(ctx, keys[1])
	_ = ring.Observe(ctx, keys[2])
	assertPublished(t, ring, keys[2].ID())
	if ring.Active().ID() != keys[2].ID() {
		t.Fatal("the newest key should sign")
	}
}

// failingGets is a state whose Get fails, as a state outage does.
type failingGets struct{ issuer.State }

func (failingGets) Get(context.Context, string) ([]byte, bool, error) {
	return nil, false, errors.New("state unavailable")
}

// Hardening 2: when the state cannot say whether a key was retired, a non-last
// key is treated as retired; the last is still recorded.
func TestAStateErrorTreatsAnEarlierKeyAsRetired(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ring := ringAt(t, failingGets{issuer.NewMemoryState()}, newSettableClock(time.Now()))
	a, _ := signer.KMSSigningKey(ctx, newFakeKMS(t), "a", kmsSeed)
	b, _ := signer.KMSSigningKey(ctx, newFakeKMS(t), "b", kmsSeed)
	_ = ring.ObserveKnown(ctx, a)
	_ = ring.Observe(ctx, b)
	assertPublished(t, ring, b.ID())
}

func TestAnRSAKMSKeySignsRS256(t *testing.T) {
	t.Parallel()
	fake := newFakeRSAKMS(t)
	key, err := signer.KMSSigningKeyFor(context.Background(), fake, "alias/rs", kmsSeed, jose.RS256)
	if err != nil {
		t.Fatal(err)
	}
	if key.SignatureAlgorithm() != jose.RS256 {
		t.Fatalf("alg %s", key.SignatureAlgorithm())
	}
	signer, err := op.SignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := signer.Sign([]byte(`{"sub":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	compact, _ := signed.CompactSerialize()
	parsed, err := jose.ParseSigned(compact, []jose.SignatureAlgorithm{jose.RS256})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parsed.Verify(&fake.rsaKey.PublicKey); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if fake.lastSign.MessageType != types.MessageTypeDigest || len(fake.lastSign.Message) != 32 {
		t.Fatalf("KMS was asked %+v", fake.lastSign)
	}
	// kid is the RFC 7638 thumbprint of the RSA key.
	jwk := jose.JSONWebKey{Key: &fake.rsaKey.PublicKey}
	tp, _ := jwk.Thumbprint(crypto.SHA256)
	if key.ID() != base64.RawURLEncoding.EncodeToString(tp) {
		t.Fatalf("kid %s is not the thumbprint", key.ID())
	}
}

func TestAKMSKeyOfTheWrongAlgorithmShapeIsRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	// An EC key where RS256 is asked for, and an RSA key where ES384 is.
	if _, err := signer.KMSSigningKeyFor(ctx, newFakeKMS(t), "k", kmsSeed, jose.RS256); err == nil {
		t.Error("an EC key was accepted for RS256")
	}
	if _, err := signer.KMSSigningKeyFor(ctx, newFakeRSAKMS(t), "k", kmsSeed, jose.ES384); err == nil {
		t.Error("an RSA key was accepted for ES384")
	}
	bad := newFakeRSAKMS(t)
	bad.spec = types.KeySpecRsa2048 // reports 2048 while the key is 3072
	if _, err := signer.KMSSigningKeyFor(ctx, bad, "k", kmsSeed, jose.RS256); err == nil {
		t.Error("a spec that disagrees with the key was accepted")
	}
	enc := newFakeRSAKMS(t)
	enc.usage = types.KeyUsageTypeEncryptDecrypt
	if _, err := signer.KMSSigningKeyFor(ctx, enc, "k", kmsSeed, jose.RS256); err == nil {
		t.Error("an ENCRYPT_DECRYPT key was accepted")
	}
}

// A signature KMS returns that does not verify is never handed back (RSA).
func TestAnRSASignatureThatDoesNotVerifyIsRefused(t *testing.T) {
	t.Parallel()
	fake := newFakeRSAKMS(t)
	key, err := signer.KMSSigningKeyFor(context.Background(), fake, "k", kmsSeed, jose.RS256)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := rsa.GenerateKey(rand.Reader, 3072)
	fake.rsaKey = other
	signer, _ := op.SignerFromKey(key)
	if _, err := signer.Sign([]byte("{}")); err == nil {
		t.Fatal("an unverifiable signature became a token")
	}
}

// Rotation is per ring: an RS256 list [A,B] rotates and A stays retired through
// repeated polls, while the ES384 ring beside it, on the same state, keeps its
// own key and schedule.
func TestKMSRingsRotateIndependently(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	state := issuer.NewMemoryState()
	clock := newSettableClock(time.Now())
	cfg := signer.KeyRingConfig{ActivationDelay: time.Minute, Overlap: 10 * time.Minute}
	es := signer.NewKeyRing(jose.ES384, state, cfg, nil)
	rs := signer.NewKeyRing(jose.RS256, state, cfg, nil)
	es.SetClock(clock.now)
	rs.SetClock(clock.now)

	esKey := mustKMSKey(t, newFakeKMS(t), jose.ES384)
	a := mustKMSKey(t, newFakeRSAKMS(t), jose.RS256)
	b := mustKMSKey(t, newFakeRSAKMS(t), jose.RS256)
	_ = es.Observe(ctx, esKey)
	_ = rs.Observe(ctx, a)
	_ = rs.Observe(ctx, b) // appended: waits its delay
	if rs.Active().ID() != a.ID() {
		t.Fatal("the appended RS256 key signs before its activation delay")
	}
	clock.set(clock.now().Add(2 * time.Minute))
	_ = rs.Observe(ctx, b)
	if rs.Active().ID() != b.ID() {
		t.Fatal("the appended RS256 key should sign after its delay")
	}
	for range 4 {
		clock.set(clock.now().Add(time.Hour))
		_ = rs.ObserveKnown(ctx, a)
		_ = rs.Observe(ctx, b)
		_ = es.Observe(ctx, esKey)
	}
	assertPublished(t, rs, b.ID())
	assertPublished(t, es, esKey.ID())
	if es.Active().ID() != esKey.ID() || rs.Active().ID() != b.ID() {
		t.Fatal("a ring lost its signer")
	}
}

func mustKMSKey(t *testing.T, api signer.KMSAPI, alg jose.SignatureAlgorithm) *signer.SigningKey {
	t.Helper()
	k, err := signer.KMSSigningKeyFor(context.Background(), api, "k", kmsSeed, alg)
	if err != nil {
		t.Fatal(err)
	}
	return k
}
