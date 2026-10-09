package signer_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/truvity/sluis/internal/signer"
)

type ring struct {
	active     map[jose.SignatureAlgorithm]signer.ActiveKey
	def        jose.SignatureAlgorithm
	maintained int
}

func (r *ring) Maintain(context.Context)         { r.maintained++ }
func (r *ring) Default() jose.SignatureAlgorithm { return r.def }
func (r *ring) Published() []signer.PublicKey {
	return []signer.PublicKey{{KID: "a", Algorithm: r.def}}
}
func (r *ring) Active(a jose.SignatureAlgorithm) (signer.ActiveKey, bool) {
	k, ok := r.active[a]
	return k, ok
}

func newRing(t *testing.T) (*ring, *ecdsa.PrivateKey, *rsa.PrivateKey) {
	t.Helper()
	ec, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rs, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return &ring{def: jose.ES384, active: map[jose.SignatureAlgorithm]signer.ActiveKey{
		jose.ES384: {KID: "ec", Algorithm: jose.ES384, Key: ec},
		jose.RS256: {KID: "rs", Algorithm: jose.RS256, Key: rs},
	}}, ec, rs
}

func limits() signer.Limits {
	return signer.Limits{MaxLifetime: map[signer.Purpose]time.Duration{
		signer.PurposeAccess: 10 * time.Minute, signer.PurposeLogout: 0,
	}}
}

func parse(t *testing.T, token string) *jose.JSONWebSignature {
	t.Helper()
	jws, err := jose.ParseSigned(token, []jose.SignatureAlgorithm{jose.ES384, jose.RS256})
	if err != nil {
		t.Fatal(err)
	}
	return jws
}

func TestSignDefaultAndNamedAlgorithm(t *testing.T) {
	r, ec, rs := newRing(t)
	s := signer.New(r, limits())
	payload := []byte(`{"iat":100,"exp":160}`)

	got, err := s.Sign(context.Background(), signer.Request{Purpose: signer.PurposeAccess, Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	if got.KID != "ec" || got.Algorithm != jose.ES384 {
		t.Fatalf("default signed with %s %s", got.KID, got.Algorithm)
	}
	jws := parse(t, got.Token)
	if h := jws.Signatures[0].Header; h.KeyID != "ec" || h.ExtraHeaders[jose.HeaderType] != "JWT" {
		t.Fatalf("header %+v", h)
	}
	if out, err := jws.Verify(&ec.PublicKey); err != nil || string(out) != string(payload) {
		t.Fatalf("verify: %v %s", err, out)
	}

	got, err = s.Sign(context.Background(), signer.Request{Purpose: signer.PurposeAccess, Algorithm: jose.RS256, Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	if got.KID != "rs" || got.Algorithm != jose.RS256 {
		t.Fatalf("named signed with %s %s", got.KID, got.Algorithm)
	}
	if _, err := parse(t, got.Token).Verify(&rs.PublicKey); err != nil {
		t.Fatal(err)
	}
	if r.maintained != 2 {
		t.Fatalf("the ring was maintained %d times for 2 signs", r.maintained)
	}
}

func TestLogoutTypeIsFixedByPurpose(t *testing.T) {
	r, _, _ := newRing(t)
	got, err := signer.New(r, limits()).Sign(context.Background(), signer.Request{Purpose: signer.PurposeLogout, Payload: []byte(`{"iat":100}`)})
	if err != nil {
		t.Fatal(err)
	}
	if typ := parse(t, got.Token).Signatures[0].Header.ExtraHeaders[jose.HeaderType]; typ != "logout+jwt" {
		t.Fatalf("typ %v", typ)
	}
}

func TestLimits(t *testing.T) {
	r, _, _ := newRing(t)
	s := signer.New(r, limits())
	cases := []struct {
		name    string
		purpose signer.Purpose
		payload string
		want    error
	}{
		{"at the maximum", signer.PurposeAccess, `{"iat":100,"exp":700}`, nil},
		{"no exp", signer.PurposeAccess, `{"iat":100}`, nil},
		{"above the maximum", signer.PurposeAccess, `{"iat":100,"exp":701}`, signer.ErrLifetime},
		{"exp without iat", signer.PurposeAccess, `{"exp":700}`, signer.ErrLifetime},
		{"a logout token with an exp", signer.PurposeLogout, `{"iat":100,"exp":101}`, signer.ErrLifetime},
		{"unknown purpose", "refresh", `{"iat":100}`, signer.ErrUnknownPurpose},
		{"empty purpose", "", `{"iat":100}`, signer.ErrUnknownPurpose},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := s.Sign(context.Background(), signer.Request{Purpose: c.purpose, Payload: []byte(c.payload)})
			if !errors.Is(err, c.want) || (c.want == nil) != (err == nil) {
				t.Fatalf("got %v, want %v", err, c.want)
			}
		})
	}
}

func TestAPurposeWithoutALimitIsUnknown(t *testing.T) {
	r, _, _ := newRing(t)
	s := signer.New(r, signer.Limits{MaxLifetime: map[signer.Purpose]time.Duration{signer.PurposeAccess: time.Minute}})
	_, err := s.Sign(context.Background(), signer.Request{Purpose: signer.PurposeLogout, Payload: []byte(`{}`)})
	if !errors.Is(err, signer.ErrUnknownPurpose) {
		t.Fatalf("got %v", err)
	}
}

func TestRefusedRequestDoesNotTouchTheRing(t *testing.T) {
	r, _, _ := newRing(t)
	_, _ = signer.New(r, limits()).Sign(context.Background(), signer.Request{Purpose: "x", Payload: []byte(`{}`)})
	if r.maintained != 0 {
		t.Fatal("a refused request reached the ring")
	}
}

func TestNoKeyAndBadPayload(t *testing.T) {
	r, _, _ := newRing(t)
	s := signer.New(r, limits())
	missing := signer.Request{Purpose: signer.PurposeAccess, Algorithm: jose.ES512, Payload: []byte(`{}`)}
	if _, err := s.Sign(context.Background(), missing); !errors.Is(err, signer.ErrNoKey) {
		t.Fatalf("got %v", err)
	}
	if _, err := s.Sign(context.Background(), signer.Request{Purpose: signer.PurposeAccess, Payload: []byte(`nope`)}); err == nil {
		t.Fatal("a payload that is not JSON was signed")
	}
}

func TestPublicKeys(t *testing.T) {
	r, _, _ := newRing(t)
	keys, err := signer.New(r, limits()).PublicKeys(context.Background())
	if err != nil || len(keys) != 1 || keys[0].KID != "a" || r.maintained != 1 {
		t.Fatalf("%v %v maintained=%d", keys, err, r.maintained)
	}
}
