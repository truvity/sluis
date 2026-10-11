package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fixtureJWT builds a compact token whose signature part is a marker the
// output must never contain.
func fixtureJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	body, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"ES384"}`)) + "." + enc(body) + "." + enc([]byte("SIGNATURE-MARKER"))
}

func TestShowClaimsYAMLNeverPrintsTheToken(t *testing.T) {
	jwt := fixtureJWT(t, map[string]any{
		"iss": "https://issuer.example", "sub": "alice@example.com", "aud": "openbao",
		"exp": 1800000600, "iat": 1800000000, "groups": []string{"dev", "ops"}, "email": "alice@example.com",
	})
	var out bytes.Buffer
	if err := showClaims(&out, jwt, "yaml"); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"iss: https://issuer.example", "sub: alice@example.com", "aud: openbao",
		"exp: \"2027-01-15T08:10:00Z\"", "iat: \"2027-01-15T08:00:00Z\"", "- dev", "- ops", "claims:", "email: alice@example.com",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
	parts := strings.Split(jwt, ".")
	for _, leak := range []string{jwt, parts[0], parts[1], parts[2], "SIGNATURE-MARKER"} {
		if strings.Contains(got, leak) {
			t.Errorf("output holds part of the token: %q", leak)
		}
	}
}

func TestShowClaimsJSON(t *testing.T) {
	jwt := fixtureJWT(t, map[string]any{"iss": "i", "sub": "s", "aud": []string{"a", "b"}, "exp": 1800000600, "tenant": "x"})
	var out bytes.Buffer
	if err := showClaims(&out, jwt, "json"); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("not json: %v\n%s", err, out.String())
	}
	if got["exp"] != "2027-01-15T08:10:00Z" || got["claims"].(map[string]any)["tenant"] != "x" {
		t.Errorf("claims = %v", got)
	}
	if _, ok := got["iat"]; ok {
		t.Error("an absent iat must stay absent")
	}
	if strings.Contains(out.String(), "SIGNATURE") {
		t.Error("signature printed")
	}
}

func TestShowClaimsRefusesANonJWT(t *testing.T) {
	var out bytes.Buffer
	if err := showClaims(&out, "opaque-token", "yaml"); err == nil || out.Len() != 0 || strings.Contains(err.Error(), "opaque-token") {
		t.Errorf("err = %v, out = %q", err, out.String())
	}
}

// End to end: --show after a normal issue prints claims, not the token.
func TestTokenShowAfterAnIssue(t *testing.T) {
	jwt := fixtureJWT(t, map[string]any{"iss": "i", "sub": "job", "aud": "openbao", "exp": 1800000600})
	out := tokenShowOutput(t, jwt, "--show", "--format", "json")
	if strings.Contains(out, jwt) || !strings.Contains(out, `"sub": "job"`) {
		t.Errorf("out = %s", out)
	}
}

// tokenShowOutput runs `token --audience openbao` in a job whose issuer
// answers with jwt, plus extra flags, and returns stdout.
func tokenShowOutput(t *testing.T, jwt string, extra ...string) string {
	t.Helper()
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"value": "the-job-token"})
	}))
	t.Cleanup(github.Close)
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": jwt, "expires_in": 600})
	}))
	t.Cleanup(issuer.Close)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv(envGitHubTokenURL, github.URL+"/token")
	t.Setenv(envGitHubTokenGrant, "the-grant")
	args := append([]string{"--audience", "openbao", "--issuer", issuer.URL}, extra...)
	return captureStdout(t, func() error { return token(args) })
}
