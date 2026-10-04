package consoleauth_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/truvity/sluis/internal/consoleauth"
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

func TestTheTokenIsScopedToTheAudienceAndCachedUntilNearExpiry(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	api := &fakeSTS{expiry: now.Add(10 * time.Minute)}
	src, err := consoleauth.NewAWSWith(api, "https://sluis.example/aws", func() time.Time { return now })
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
	if len(in.Audience) != 1 || in.Audience[0] != "https://sluis.example/aws" || aws.ToString(in.SigningAlgorithm) != "ES384" {
		t.Errorf("request %+v", in)
	}
	now = now.Add(8*time.Minute + 30*time.Second) // inside the two minutes before expiry
	if tok, _ := src.Token(context.Background()); tok != "token-2" || len(api.calls) != 2 {
		t.Errorf("near expiry: %q after %d calls, want a fresh token", tok, len(api.calls))
	}
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
