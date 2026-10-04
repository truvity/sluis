package consoleauth_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/truvity/sluis/internal/consoleauth"
	"github.com/truvity/sluis/internal/verify"
)

type fakeSTS struct {
	calls  []*sts.GetWebIdentityTokenInput
	expiry time.Time
	err    error
}

func (f *fakeSTS) GetWebIdentityToken(_ context.Context, in *sts.GetWebIdentityTokenInput, _ ...func(*sts.Options)) (*sts.GetWebIdentityTokenOutput, error) {
	f.calls = append(f.calls, in)
	if f.err != nil {
		return nil, f.err
	}
	n := len(f.calls)
	return &sts.GetWebIdentityTokenOutput{
		WebIdentityToken: aws.String("token-" + string(rune('0'+n))), Expiration: aws.Time(f.expiry),
	}, nil
}

func TestTheTokenIsScopedToTheAudienceAndReusedOnlyWhileItIsYoung(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	api := &fakeSTS{expiry: now.Add(5 * time.Minute)}
	src, err := consoleauth.NewAWSWith(api, "https://sluis.example/console", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		if tok, err := src.Token(context.Background()); err != nil || tok != "token-1" {
			t.Fatalf("%q %v", tok, err)
		}
	}
	if len(api.calls) != 1 {
		t.Fatalf("%d STS calls for five uses of one token", len(api.calls))
	}
	in := api.calls[0]
	if len(in.Audience) != 1 || in.Audience[0] != "https://sluis.example/console" || aws.ToString(in.SigningAlgorithm) != "ES384" ||
		aws.ToInt32(in.DurationSeconds) != 300 {
		t.Errorf("request %+v", in)
	}
	now = now.Add(3*time.Minute + 59*time.Second)
	if tok, _ := src.Token(context.Background()); tok != "token-1" {
		t.Errorf("a token under four minutes old was replaced: %q", tok)
	}
	now = now.Add(2 * time.Second) // four minutes, though the token is valid for another minute
	api.expiry = now.Add(5 * time.Minute)
	if tok, _ := src.Token(context.Background()); tok != "token-2" || len(api.calls) != 2 {
		t.Errorf("at four minutes: %q after %d calls, want a fresh token", tok, len(api.calls))
	}
}

// The issuer refuses a token whose iat is older than its maxAge (five minutes by
// default) whatever its exp says. A warm function that kept its token until
// near the EXPIRY would be refused from minute five: the cache is by age.
func TestAWarmFunctionIsNeverRefusedForAnOldToken(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keys := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key: key.Public(), KeyID: "aws", Algorithm: string(jose.ES384), Use: "sig",
		}}})
	}))
	defer keys.Close()
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	const issuerURL = "https://abc.tokens.sts.global.api.aws"
	verifier := &verify.AWSAccount{
		Account: "111122223333", Name: "prod", Issuer: issuerURL, JWKSURI: keys.URL,
		Audience: "https://sluis.example/console", Client: keys.Client(),
		Now: func() time.Time { return now }, // MaxAge left at its default
	}
	api := &mintingSTS{key: key, issuer: issuerURL, now: func() time.Time { return now }}
	src, err := consoleauth.NewAWSWith(api, "https://sluis.example/console", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	for _, minute := range []int{0, 1, 3, 4, 5, 6, 8, 9, 12} {
		now = time.Date(2026, 10, 5, 12, minute, 0, 0, time.UTC)
		token, err := src.Token(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := verifier.Verify(context.Background(), token, ""); err != nil {
			t.Errorf("at +%dm the verifier refused the token the cache handed out: %v", minute, err)
		}
	}
	if api.calls < 3 {
		t.Errorf("%d tokens minted over twelve minutes: the cache outlived the issuer's maxAge", api.calls)
	}
}

type mintingSTS struct {
	key    *ecdsa.PrivateKey
	issuer string
	now    func() time.Time
	calls  int
}

func (m *mintingSTS) GetWebIdentityToken(_ context.Context, in *sts.GetWebIdentityTokenInput, _ ...func(*sts.Options)) (*sts.GetWebIdentityTokenOutput, error) {
	m.calls++
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES384, Key: m.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "aws"))
	if err != nil {
		return nil, err
	}
	now := m.now()
	raw, err := jwt.Signed(signer).Claims(map[string]any{
		"iss": m.issuer, "sub": "arn:aws:iam::111122223333:role/sluis-github", "aud": in.Audience[0],
		"iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
		"https://sts.amazonaws.com/": map[string]any{"aws_account": "111122223333"},
	}).Serialize()
	if err != nil {
		return nil, err
	}
	return &sts.GetWebIdentityTokenOutput{WebIdentityToken: aws.String(raw), Expiration: aws.Time(now.Add(5 * time.Minute))}, nil
}

func TestAnSTSFailureIsAnErrorAndNoAudienceIsRefused(t *testing.T) {
	src, _ := consoleauth.NewAWSWith(&fakeSTS{err: errors.New("AccessDenied")}, "a", time.Now)
	if _, err := src.Token(context.Background()); err == nil {
		t.Error("a failed STS call returned a token")
	}
	if _, err := consoleauth.NewAWSWith(&fakeSTS{}, " ", time.Now); err == nil {
		t.Error("an empty audience was accepted")
	}
}

func TestTheFileSourceReadsAfreshEveryTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := consoleauth.File(path)
	if tok, _ := src.Token(context.Background()); tok != "one" {
		t.Errorf("%q", tok)
	}
	if err := os.WriteFile(path, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	if tok, _ := src.Token(context.Background()); tok != "two" {
		t.Errorf("%q: the kubelet rotates the file under the pod", tok)
	}
	if _, err := consoleauth.File(path + ".none").Token(context.Background()); err == nil {
		t.Error("a missing file returned a token")
	}
}

func TestTheInterceptorSetsTheBearerAndStopsOnATokenError(t *testing.T) {
	src, _ := consoleauth.NewAWSWith(&fakeSTS{expiry: time.Now().Add(time.Hour)}, "a", time.Now)
	req := connect.NewRequest(&struct{}{})
	next := func(_ context.Context, r connect.AnyRequest) (connect.AnyResponse, error) {
		if got := r.Header().Get("Authorization"); got != "Bearer token-1" {
			t.Errorf("Authorization %q", got)
		}
		return nil, nil
	}
	if _, err := consoleauth.Unary(src)(next)(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	failing, _ := consoleauth.NewAWSWith(&fakeSTS{err: errors.New("denied")}, "a", time.Now)
	called := false
	_, err := consoleauth.Unary(failing)(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		called = true
		return nil, nil
	})(context.Background(), connect.NewRequest(&struct{}{}))
	if err == nil || called {
		t.Errorf("a call went out without a token: err=%v called=%v", err, called)
	}
}
