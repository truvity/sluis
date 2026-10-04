package issuer_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha512"
	"crypto/x509"
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
)

// fakeKMS signs with a local P-384 key and answers in DER, as KMS does.
type fakeKMS struct {
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

func (f *fakeKMS) GetPublicKey(_ context.Context, _ *kms.GetPublicKeyInput, _ ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error) {
	if f.pubErr != nil {
		return nil, f.pubErr
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
	key, err := issuer.KMSSigningKey(context.Background(), fake, "alias/sluis-signing", kmsSeed)
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
	key, err := issuer.KMSSigningKey(context.Background(), fake, "k", kmsSeed)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(fake.key)
	file, err := issuer.ParseSigningKey(pemOf("PRIVATE KEY", der))
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
		if _, err := issuer.KMSSigningKey(context.Background(), fake, "k", kmsSeed); err == nil {
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
	_, err := issuer.KMSSigningKey(context.Background(), fake, "alias/sluis-signing", kmsSeed)
	if err == nil || !strings.Contains(err.Error(), "kms:GetPublicKey") {
		t.Fatalf("got %v", err)
	}
}

func TestAShortStateSecretIsRefused(t *testing.T) {
	t.Parallel()
	if _, err := issuer.KMSSigningKey(context.Background(), newFakeKMS(t), "k", []byte("short")); err == nil {
		t.Fatal("accepted")
	}
}

func TestAKMSFailureSurfacesFromSign(t *testing.T) {
	t.Parallel()
	fake := newFakeKMS(t)
	key, err := issuer.KMSSigningKey(context.Background(), fake, "k", kmsSeed)
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
	a, _ := issuer.KMSSigningKey(context.Background(), fake, "k", kmsSeed)
	b, _ := issuer.KMSSigningKey(context.Background(), fake, "k", kmsSeed)
	if string(a.Derive("x")) != string(b.Derive("x")) || string(a.Derive("x")) == string(a.Derive("y")) {
		t.Fatal("derivation is not stable per label")
	}
}

func pemOf(kind string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der})
}

func ringAt(t *testing.T, state issuer.State, clock *settableClock) *issuer.KeyRing {
	t.Helper()
	ring := issuer.NewKeyRing(jose.ES384, state,
		issuer.KeyRingConfig{ActivationDelay: time.Minute, Overlap: 10 * time.Minute}, nil)
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
	a, err := issuer.KMSSigningKey(ctx, fake, "a", kmsSeed)
	if err != nil {
		t.Fatal(err)
	}
	fakeB := newFakeKMS(t)
	b, _ := issuer.KMSSigningKey(ctx, fakeB, "b", kmsSeed)

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
	file, err := issuer.NewSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	_ = ringAt(t, state, clock).Observe(ctx, file) // another replica, still on files

	ring := ringAt(t, state, clock)
	k, _ := issuer.KMSSigningKey(ctx, newFakeKMS(t), "k", kmsSeed)
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
	key, err := issuer.KMSSigningKey(context.Background(), fake, "k", kmsSeed)
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
	if _, err := issuer.KMSSigningKey(context.Background(), fake, "alias/x", kmsSeed); err == nil {
		t.Fatal("fell back to the alias")
	}
}
