//go:build sluis_test

package sessiond_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	configv2alpha1 "github.com/truvity/sluis/v2/gen/go/sluis/config/v2alpha1"
	sessionv2alpha1 "github.com/truvity/sluis/v2/gen/go/sluis/session/v2alpha1"
)

// The tables of kits/conformance/token/client-vectors.json that session is held to: how a client's declaration decides what
// an authorization request needs ("authorize"), which redirect_uri it may present ("redirects"), and what the token endpoint
// asks of the verifier ("token"). actors/store reads the same file for the loader ("config"), so the two sides cannot drift.

type clientSetup struct {
	Kind       string `json:"kind"`
	Profile    string `json:"profile"`
	Pkce       string `json:"pkce"`
	Secret     bool   `json:"secret"`
	Registered string `json:"registered"`
	Presented  string `json:"presented"`
}

type clientVectors struct {
	Authorize struct {
		Setups  map[string]clientSetup `json:"setups"`
		Vectors []struct {
			Name      string   `json:"name"`
			Setups    []string `json:"setups"`
			Challenge string   `json:"challenge"`
			Method    string   `json:"method"`
			Nonce     string   `json:"nonce"`
			Outcome   string   `json:"outcome"`
			Reason    string   `json:"reason"`
		} `json:"vectors"`
	} `json:"authorize"`
	Redirects struct {
		Vectors []struct {
			Name        string   `json:"name"`
			Kinds       []string `json:"kinds"`
			Profile     string   `json:"profile"`
			Environment string   `json:"environment"`
			Registered  []string `json:"registered"`
			Presented   string   `json:"presented"`
			Match       bool     `json:"match"`
		} `json:"vectors"`
	} `json:"redirects"`
	Token struct {
		Vectors []struct {
			Name     string `json:"name"`
			Setup    string `json:"setup"`
			Mode     string `json:"mode"`
			Verifier string `json:"verifier"`
			Outcome  string `json:"outcome"`
		} `json:"vectors"`
	} `json:"token"`
}

func loadClientVectors(t *testing.T) clientVectors {
	t.Helper()
	raw, err := os.ReadFile("../../../../kits/conformance/token/client-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v clientVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// vectorVerifier is the code_verifier of the PKCE pair the vectors use; anotherVerifier is a valid one of a different pair.
const (
	vectorVerifier  = "client-verifier-0123456789abcdefghijklmnopqrstuvwxyz"
	anotherVerifier = "another-verifier-0123456789abcdefghijklmnopqrstuvwxyz"
)

// challengeOf makes the code_challenge a vector names: valid is the S256 of the verifier.
func challengeOf(t *testing.T, token string) string {
	t.Helper()
	valid := s256(vectorVerifier)
	switch token {
	case "valid":
		return valid
	case "absent":
		return ""
	case "short":
		return valid[:42]
	case "long":
		return valid + "A"
	case "padded":
		return valid[:42] + "="
	case "outside":
		return valid[:42] + "+"
	}
	t.Fatalf("a challenge this test does not know: %q", token)
	return ""
}

// methodOf makes the code_challenge_method a vector names.
func methodOf(method string) string {
	if method == "absent" {
		return ""
	}
	return method
}

// nonceOfToken makes the nonce a vector names.
func nonceOfToken(t *testing.T, token string) string {
	t.Helper()
	switch token {
	case "valid":
		return nonceValue
	case "absent":
		return ""
	case "seven":
		return "1234567"
	case "eight":
		return "12345678"
	case "spaces":
		return strings.Repeat(" ", 60)
	}
	t.Fatalf("a nonce this test does not know: %q", token)
	return ""
}

// installationClient makes the client a setup describes, as the loader would have stored it.
func (s clientSetup) installationClient(id string, redirects ...string) *configv2alpha1.InstallationClient {
	c := &configv2alpha1.InstallationClient{
		Id: id, Kind: s.Kind, Profile: s.Profile, Pkce: s.Pkce, Redirects: redirects, Requires: []string{testGroup},
	}
	if s.Secret {
		c.Secret = credential(id)
	}
	return c
}

// vectorEnv is an installation with a client for each setup, named by the setup, and the secret of those that have one.
func vectorEnv(t *testing.T, setups map[string]clientSetup, environment string) *env {
	t.Helper()
	e := newEnv(t, func(o *options) {
		if environment != "" {
			o.boundary.Environment = environment
		}
		for id, s := range setups {
			o.extra = append(o.extra, s.installationClient(id, s.Registered))
		}
	})
	for id, s := range setups {
		if s.Secret {
			e.store.put("installation-client-secret", "", id, &configv2alpha1.InstallationClientSecret{Client: id, Secret: []byte(confSecret)})
		}
	}
	return e
}

// authorizeRequest is a request of the client with the PKCE part and the nonce the vector names.
func authorizeRequest(t *testing.T, id, redirect, challenge, method, nonce string) *sessionv2alpha1.AuthorizeRequest {
	t.Helper()
	return &sessionv2alpha1.AuthorizeRequest{
		ResponseType: "code", ClientId: id, RedirectUri: redirect, Scope: "openid", State: "client-state-1", Nonce: nonceOfToken(t, nonce),
		CodeChallenge: challengeOf(t, challenge), CodeChallengeMethod: methodOf(method),
	}
}

// What an authorization request needs for each way of declaring a client: PKCE with S256 for everybody, the nonce in place of it
// for a confidential client that says so and for nobody else, a sent challenge or method checked whatever the setting says, and
// no sign-in at all for an exchange client. A refusal goes to the client's redirect with the error and leaves the closed reason
// in the audit record, and the upstream is never called for it.
//
// verifies: REQ-AUTHN-002, REQ-AUTHN-026, REQ-AUTHN-104, REQ-AUTHN-105, REQ-AUTHN-109
func TestAnAuthorizationRequestNeedsWhatTheSharedVectorsSayForEachClient(t *testing.T) {
	v := loadClientVectors(t)
	if len(v.Authorize.Setups) < 6 || len(v.Authorize.Vectors) < 15 {
		t.Fatalf("the vectors are shorter than they were: %d setups, %d vectors", len(v.Authorize.Setups), len(v.Authorize.Vectors))
	}
	e := vectorEnv(t, v.Authorize.Setups, "")
	for _, c := range v.Authorize.Vectors {
		for _, name := range c.Setups {
			setup, ok := v.Authorize.Setups[name]
			if !ok {
				t.Fatalf("%s: no setup named %s", c.Name, name)
			}
			t.Run(c.Name+"/"+name, func(t *testing.T) {
				before, refusals := len(e.upstream.begins), len(reasonsOf(e))
				r := authorizeRequest(t, name, setup.Presented, c.Challenge, c.Method, c.Nonce)
				began, errCode, _, _ := answered(e, r)
				switch c.Outcome {
				case "begins":
					if !began {
						t.Fatalf("refused with %s (%v)", errCode, reasonsOf(e))
					}
					return
				case "page":
					if began || errCode != "page" {
						t.Fatalf("began=%v answer=%q, want the error page", began, errCode)
					}
				default:
					if began || errCode != c.Outcome {
						t.Fatalf("began=%v error=%q, want %s", began, errCode, c.Outcome)
					}
				}
				if len(e.upstream.begins) != before {
					t.Error("the upstream was called for a refused request")
				}
				rs := reasonsOf(e)
				if len(rs) == refusals {
					t.Fatal("no audit record names the refusal")
				}
				if c.Reason != "" && rs[len(rs)-1] != c.Reason {
					t.Errorf("reason = %s, want %s", rs[len(rs)-1], c.Reason)
				}
			})
		}
	}
}

// Which redirect_uri a client may present: the exact string, the free port of a loopback IP literal registered without one, and
// outside development no plain http but a native app's on its own host. A redirect that does not match is the fixed error page
// and never a redirect to the presented address, and the upstream is never called.
//
// verifies: REQ-AUTHN-003, REQ-AUTHN-004
func TestARedirectIsOneTheClientRegisteredAsTheSharedVectorsSay(t *testing.T) {
	v := loadClientVectors(t)
	if len(v.Redirects.Vectors) < 25 {
		t.Fatalf("the vectors are shorter than they were: %d", len(v.Redirects.Vectors))
	}
	// One client for each row and kind, so that a row sees only what it registered; one installation for each environment.
	clientOf := func(row int, kind string) string { return fmt.Sprintf("redirect-%d-%s", row, kind) }
	environmentOf := func(row int) string {
		if e := v.Redirects.Vectors[row].Environment; e != "" {
			return e
		}
		return "development"
	}
	envs := map[string]*env{}
	for _, environment := range []string{"development", "production"} {
		envs[environment] = newEnv(t, func(o *options) {
			o.boundary.Environment = environment
			for i, c := range v.Redirects.Vectors {
				if environmentOf(i) != environment {
					continue
				}
				for _, kind := range c.Kinds {
					s := clientSetup{Kind: kind, Profile: c.Profile, Secret: kind == "confidential"}
					o.extra = append(o.extra, s.installationClient(clientOf(i, kind), c.Registered...))
				}
			}
		})
	}
	for i, c := range v.Redirects.Vectors {
		for _, kind := range c.Kinds {
			t.Run(fmt.Sprintf("%s/%s", c.Name, kind), func(t *testing.T) {
				e := envs[environmentOf(i)]
				before := len(e.upstream.begins)
				p := newAuthorize()
				p.req.ClientId, p.req.RedirectUri = clientOf(i, kind), c.Presented
				r := e.authorize(p.req)
				if c.Match {
					if got := location(t, r); got.Host != "idp.example.test" {
						t.Fatalf("redirected to %s, want the upstream", got)
					}
					return
				}
				assertErrorPage(t, r)
				if len(e.upstream.begins) != before {
					t.Error("the upstream was called")
				}
			})
		}
	}
}

// The token endpoint holds a code to how it was bound: the verifier that matches the challenge, none for a code that the nonce
// holds, and a verifier for a code issued without a challenge is refused whatever the client says (RFC 9700 section 4.8.2). A
// refusal spends nothing, so the right request that follows it still redeems the code.
//
// verifies: REQ-AUTHN-002, REQ-AUTHN-106, REQ-AUTHN-107
func TestACodeIsRedeemedAsTheSharedVectorsSay(t *testing.T) {
	v := loadClientVectors(t)
	if len(v.Token.Vectors) < 8 {
		t.Fatalf("the vectors are shorter than they were: %d", len(v.Token.Vectors))
	}
	for _, c := range v.Token.Vectors {
		t.Run(c.Name, func(t *testing.T) {
			setup, ok := v.Authorize.Setups[c.Setup]
			if !ok {
				t.Fatalf("no setup named %s", c.Setup)
			}
			e := vectorEnv(t, v.Authorize.Setups, "")
			challenge, method := "valid", "S256"
			if c.Mode == "nonce" {
				challenge, method = "absent", "absent"
			}
			began, errCode, up, cookie := answered(e, authorizeRequest(t, c.Setup, setup.Presented, challenge, method, "valid"))
			if !began {
				t.Fatalf("the code was not issued: %s", errCode)
			}
			code := finish(e, up, cookie)
			request := func(verifier string) *sessionv2alpha1.TokenRequest {
				r := &sessionv2alpha1.TokenRequest{GrantType: "authorization_code", Code: code, RedirectUri: setup.Presented, ClientId: c.Setup, CodeVerifier: verifier}
				if setup.Secret {
					return withBasic(r, c.Setup, confSecret)
				}
				return r
			}
			verifier := map[string]string{
				"right": vectorVerifier, "wrong": anotherVerifier, "plain": s256(vectorVerifier), "none": "", "any": vectorVerifier,
			}[c.Verifier]
			r := e.token(request(verifier))
			if c.Outcome == "ok" {
				if r.GetStatus() != http.StatusOK {
					t.Fatalf("status %d: %s", r.GetStatus(), r.GetBody())
				}
				return
			}
			assertOAuth(t, r, http.StatusBadRequest, c.Outcome)
			// A refusal spends nothing.
			right := vectorVerifier
			if c.Mode == "nonce" {
				right = ""
			}
			if r := e.token(request(right)); r.GetStatus() != http.StatusOK {
				t.Fatalf("the refusal spent the code: %d %s", r.GetStatus(), r.GetBody())
			}
		})
	}
}
