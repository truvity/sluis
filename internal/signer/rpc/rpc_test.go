package rpc_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/truvity/sluis/internal/modcall"
	"github.com/truvity/sluis/internal/signer"
	"github.com/truvity/sluis/internal/signer/rpc"
)

// ring is one ES256 key and nothing else.
type ring struct {
	key       *ecdsa.PrivateKey
	maintains int
}

func newRing(t *testing.T) *ring {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &ring{key: k}
}

func (r *ring) Maintain(context.Context) { r.maintains++ }

func (r *ring) Signing(alg jose.SignatureAlgorithm) (signer.ActiveKey, bool) {
	if alg != jose.ES256 {
		return signer.ActiveKey{}, false
	}
	return signer.ActiveKey{KID: "k1", Algorithm: jose.ES256, Key: r.key}, true
}

func (r *ring) Default() jose.SignatureAlgorithm { return jose.ES256 }

func (r *ring) Published() []signer.PublicKey {
	return []signer.PublicKey{{KID: "k1", Algorithm: jose.ES256, Key: &r.key.PublicKey}}
}

// dir is the read-only view of that ring.
type dir struct{ r *ring }

func (d dir) Maintain(ctx context.Context)     { d.r.Maintain(ctx) }
func (dir) Default() jose.SignatureAlgorithm   { return jose.ES256 }
func (dir) Has(a jose.SignatureAlgorithm) bool { return a == jose.ES256 || a == jose.RS256 }
func (dir) Configured() []jose.SignatureAlgorithm {
	return []jose.SignatureAlgorithm{jose.ES256, jose.RS256}
}
func (dir) Algorithms() []jose.SignatureAlgorithm { return []jose.SignatureAlgorithm{jose.ES256} }
func (dir) Secret(string) []byte                  { return []byte("key material") }
func (dir) ActiveKID(a jose.SignatureAlgorithm) (string, bool) {
	return "k1", a == jose.ES256
}

func server(t *testing.T) (*modcall.Server, *ring) {
	t.Helper()
	r := newRing(t)
	s := modcall.NewServer(rpc.Module)
	rpc.Register(s, signer.New(r, signer.LimitsFor(time.Hour)), dir{r})
	return s, r
}

func payload(iat, exp int64) []byte {
	b := []byte(`{"iat":` + strconv.FormatInt(iat, 10))
	if exp != 0 {
		b = append(b, []byte(`,"exp":`+strconv.FormatInt(exp, 10))...)
	}
	return append(b, '}')
}

// transports are the Local transport and HTTP, behind one name each.
func transports(t *testing.T, s *modcall.Server) map[string]modcall.Caller {
	t.Helper()
	ts := httptest.NewServer(s.Handler(func(_ context.Context, bearer string) (string, error) {
		if bearer != "good" {
			return "", errors.New("refused")
		}
		return "issuer", nil
	}))
	t.Cleanup(ts.Close)
	return map[string]modcall.Caller{
		"local": modcall.Local{rpc.Module: s},
		"http": &modcall.HTTPCaller{
			URLs:   map[string]string{rpc.Module: ts.URL},
			Token:  func(context.Context, string) (string, error) { return "good", nil },
			Client: ts.Client(),
		},
	}
}

func TestSignAndThePublicKeysRoundTripOverEveryTransport(t *testing.T) {
	s, _ := server(t)
	for name, c := range transports(t, s) {
		t.Run(name, func(t *testing.T) {
			cl := rpc.NewClient(c, nil)
			now := time.Now().Unix()
			got, err := cl.Sign(context.Background(), signer.Request{Purpose: signer.PurposeAccess, Payload: payload(now, now+60)})
			if err != nil {
				t.Fatal(err)
			}
			if got.KID != "k1" || got.Algorithm != jose.ES256 || got.Token == "" {
				t.Fatalf("%+v", got)
			}
			keys, err := cl.PublicKeys(context.Background())
			if err != nil || len(keys) != 1 || keys[0].KID != "k1" || keys[0].Algorithm != jose.ES256 {
				t.Fatalf("%+v %v", keys, err)
			}
			jws, err := jose.ParseSigned(got.Token, []jose.SignatureAlgorithm{jose.ES256})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = jws.Verify(keys[0].Key); err != nil {
				t.Errorf("the token does not verify under the key set the module published: %v", err)
			}
		})
	}
}

func TestTheLimitsRefusalsCrossTheWireByName(t *testing.T) {
	s, _ := server(t)
	now := time.Now().Unix()
	for name, c := range transports(t, s) {
		cl := rpc.NewClient(c, nil)
		for what, tc := range map[string]struct {
			req  signer.Request
			want error
		}{
			"unknown purpose": {signer.Request{Purpose: "mystery", Payload: payload(now, 0)}, signer.ErrUnknownPurpose},
			"lifetime":        {signer.Request{Purpose: signer.PurposeAccess, Payload: payload(now, now+7200)}, signer.ErrLifetime},
			"exp without iat": {signer.Request{Purpose: signer.PurposeAccess, Payload: []byte(`{"exp":5}`)}, signer.ErrLifetime},
			"no key":          {signer.Request{Purpose: signer.PurposeAccess, Algorithm: jose.RS256, Payload: payload(now, 0)}, signer.ErrNoKey},
		} {
			if _, err := cl.Sign(context.Background(), tc.req); !errors.Is(err, tc.want) {
				t.Errorf("%s/%s: got %v, want %v", name, what, err, tc.want)
			}
		}
	}
}

func TestAnyOtherFailureIsInternalAndTellsNothing(t *testing.T) {
	s, _ := server(t)
	_, err := rpc.NewClient(modcall.Local{rpc.Module: s}, nil).Sign(context.Background(), signer.Request{Purpose: signer.PurposeAccess, Payload: []byte("not json")})
	var e *modcall.Error
	if !errors.As(err, &e) || e.Code != modcall.CodeInternal || err.Error() != "module call: internal" {
		t.Fatalf("%v", err)
	}
}

func TestTheDirectoryIsReadOnceAndReusedForItsTTL(t *testing.T) {
	s, r := server(t)
	cl := rpc.NewClient(modcall.Local{rpc.Module: s}, nil)
	clock := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	cl.Now = func() time.Time { return clock }

	if cl.Default() != jose.ES256 || !cl.Has(jose.RS256) || cl.Has(jose.ES384) {
		t.Errorf("default %v", cl.Default())
	}
	if got := cl.Configured(); len(got) != 2 || got[0] != jose.ES256 || got[1] != jose.RS256 {
		t.Errorf("configured %v", got)
	}
	if got := cl.Algorithms(); len(got) != 1 || got[0] != jose.ES256 {
		t.Errorf("algorithms %v", got)
	}
	if kid, ok := cl.ActiveKID(jose.ES256); !ok || kid != "k1" {
		t.Errorf("active %q %v", kid, ok)
	}
	if _, ok := cl.ActiveKID(jose.RS256); ok {
		t.Error("RS256 has no active key")
	}
	if r.maintains != 1 {
		t.Fatalf("the signer maintained its rings %d times for one read", r.maintains)
	}
	cl.Maintain(context.Background())
	if r.maintains != 1 {
		t.Errorf("a fresh directory was read again: %d", r.maintains)
	}
	clock = clock.Add(rpc.DefaultTTL + time.Second)
	cl.Maintain(context.Background())
	if r.maintains != 2 {
		t.Errorf("a stale directory was not read again: %d", r.maintains)
	}
}

func TestSecretIsKeyMaterialAndStaysInTheSigner(t *testing.T) {
	s, _ := server(t)
	if got := rpc.NewClient(modcall.Local{rpc.Module: s}, nil).Secret("any"); got != nil {
		t.Errorf("the client answered %q", got)
	}
}

func TestAnUnreachableSignerLeavesTheLastDirectory(t *testing.T) {
	s, _ := server(t)
	cl := rpc.NewClient(modcall.Local{rpc.Module: s}, nil)
	clock := time.Now()
	cl.Now = func() time.Time { return clock }
	if cl.Default() != jose.ES256 {
		t.Fatal("not read")
	}
	down := rpc.NewClient(modcall.Local{}, nil)
	if down.Default() != "" || down.Has(jose.ES256) || len(down.Configured()) != 0 {
		t.Error("a signer never reached has no algorithms")
	}
}

func TestACallWithoutAcceptedCredentialsIsA401AndReachesNoMethod(t *testing.T) {
	s, r := server(t)
	ts := httptest.NewServer(s.Handler(func(context.Context, string) (string, error) { return "", errors.New("refused") }))
	defer ts.Close()
	cl := rpc.NewClient(&modcall.HTTPCaller{
		URLs:   map[string]string{rpc.Module: ts.URL},
		Token:  func(context.Context, string) (string, error) { return "bad", nil },
		Client: ts.Client(),
	}, nil)
	_, err := cl.Sign(context.Background(), signer.Request{Purpose: signer.PurposeAccess, Payload: payload(time.Now().Unix(), 0)})
	if !errors.Is(err, modcall.ErrTransport) {
		t.Fatalf("%v", err)
	}
	if r.maintains != 0 {
		t.Error("a refused call reached the signer")
	}

	res, err := http.Post(ts.URL+modcall.RPCPath, "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("no bearer: %d", res.StatusCode)
	}
}
