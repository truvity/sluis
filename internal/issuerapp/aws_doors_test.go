package issuerapp

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/internal/verify"
	"github.com/truvity/sluis/policy"
)

// An AWS role's token is a proof at TWO doors, token exchange and the console,
// and each has its own audience: a token minted for one is no proof at the
// other, so a credential handed to one consumer is not silently a credential
// for the other.
func TestTheTwoAWSDoorsEachTakeOnlyTheirOwnAudience(t *testing.T) {
	t.Parallel()
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keys := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key: key.Public(), KeyID: "aws", Algorithm: string(jose.ES384), Use: "sig",
		}}})
	}))
	t.Cleanup(keys.Close)
	const issuerURL = "https://abc.tokens.sts.global.api.aws"
	mint := func(audience string) string {
		signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES384, Key: key},
			(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "aws"))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := jwt.Signed(signer).Claims(map[string]any{
			"iss": issuerURL, "sub": "arn:aws:iam::111122223333:role/sluis-github", "aud": audience,
			"exp": time.Now().Add(5 * time.Minute).Unix(), "iat": time.Now().Unix(),
			"https://sts.amazonaws.com/": map[string]any{"aws_account": "111122223333"},
		}).Serialize()
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	federation := verify.AWSFederation{Audience: "sluis-exchange", Accounts: []verify.AWSAccountRow{{
		Account: "111122223333", Name: "prod", Issuer: issuerURL, JWKSURI: keys.URL,
	}}}
	var all, console issuer.Verifiers
	for _, v := range federation.Verifiers(keys.Client()) {
		all = append(all, v)
	}
	for _, v := range consoleAWSVerifiers(federation, "https://sluis.example/console", keys.Client()) {
		console = append(console, v)
	}
	ctx := context.Background()
	if _, err = all.Verify(ctx, mint("sluis-exchange"), ""); err != nil {
		t.Errorf("exchange door refused its own audience: %v", err)
	}
	if _, err = all.Verify(ctx, mint("https://sluis.example/console"), ""); err == nil {
		t.Error("a console-audience token was accepted for token exchange")
	}
	if _, err = console.Verify(ctx, mint("https://sluis.example/console"), ""); err != nil {
		t.Errorf("console door refused its own audience: %v", err)
	}
	if _, err = console.Verify(ctx, mint("sluis-exchange"), ""); err == nil {
		t.Error("an exchange-audience token was accepted as a console bearer")
	}
}

// One value for both would make them one door again: refused at start.
func TestTheSameAudienceForBothAWSDoorsIsRefused(t *testing.T) {
	t.Parallel()
	cfg := Config{aws: config.AWSFederation{Audience: "same", Accounts: []config.AWSAccount{{
		Account: "111122223333", Name: "prod", Issuer: "https://abc.tokens.sts.global.api.aws",
	}}}, consoleAWSAudience: "same", audience: "x"}
	if _, _, err := openVerifiers(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
		t.Error("the same audience for both doors was accepted")
	}
}

func TestBroadAWSMatchersAreNamedAndNeverRefused(t *testing.T) {
	t.Parallel()
	declared, err := policy.Parse([]byte(`
version: 1
groups:
  all:sluis:viewer:
    matchers:
      - aws: { account: "111122223333" }
      - aws: { account: "111122223333", role: "*" }
      - aws: { account: "111122223333", role: sluis-github }
`))
	if err != nil {
		t.Fatal(err)
	}
	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatalf("a broad matcher was refused: %v", err)
	}
	if broad := BroadAWSMatchers(set); len(broad) != 2 {
		t.Errorf("broad = %v, want the two without a role", broad)
	}
	var out bytes.Buffer
	warnBroadAWSMatchers(context.Background(), set, slog.New(slog.NewTextHandler(&out, nil)))
	if !strings.Contains(out.String(), "all:sluis:viewer") || !strings.Contains(out.String(), "level=WARN") {
		t.Errorf("log: %s", out.String())
	}
}
