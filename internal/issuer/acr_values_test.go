package issuer_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/issuer"
)

// A client that asks with `acr_values` for every class discovery lists gets
// one of them back (OpenID Connect Core 3.1.2.1), and never a class the
// sign-in did not meet. This is the conformance suite's
// `oidcc-ensure-request-with-acr-values-succeeds`, which signs in as the
// conformance account: its class was not listed, so the token answered with
// a value the suite had not asked for.

// discoveredACRValues is `acr_values_supported` as the issuer serves it.
func discoveredACRValues(t *testing.T, b *browser) []string {
	t.Helper()
	status, _, body := b.do(http.MethodGet, "/.well-known/openid-configuration")
	if status != http.StatusOK {
		t.Fatalf("discovery = %d %s", status, body)
	}
	var doc struct {
		ACRValues []string `json:"acr_values_supported"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("discovery is not JSON: %v", err)
	}
	return doc.ACRValues
}

// openChooserAsking is openChooser with `acr_values` on the request.
func openChooserAsking(t *testing.T, b *browser, client string, acrValues []string) string {
	t.Helper()
	query := url.Values{
		"client_id": {client}, "redirect_uri": {"http://localhost:8000/callback"}, "response_type": {"code"},
		"scope": {"openid"}, "state": {"pw-test"}, "code_challenge_method": {"S256"},
		"code_challenge": {challengeOf(pkceVerifier)}, "acr_values": {strings.Join(acrValues, " ")},
	}
	status, where, body := b.do(http.MethodGet, "/authorize?"+query.Encode())
	if status != http.StatusFound {
		t.Fatalf("/authorize: %d %s", status, body)
	}
	status, _, page := b.do(http.MethodGet, where)
	if status != http.StatusOK {
		t.Fatalf("the chooser: %d %s", status, page)
	}
	return page
}

func TestDiscoveryListsTheConformanceClassOnlyWhileTheAccountIsOn(t *testing.T) {
	t.Parallel()

	for name, on := range map[string]bool{"on": true, "off": false} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w := newPasswordWorld(t, pwOptions{conformance: on})
			listed := discoveredACRValues(t, newBrowser(t, w.server))

			for _, v := range []string{issuer.ACRDirectory, issuer.ACRRecovery} {
				if !slices.Contains(listed, v) {
					t.Errorf("acr_values_supported = %v, lacks %s", listed, v)
				}
			}
			if slices.Contains(listed, issuer.ACRConformance) != on {
				t.Errorf("acr_values_supported = %v with the conformance account %s", listed, name)
			}
		})
	}
}

func TestTheSuitesACRRequestGetsAClassItAskedFor(t *testing.T) {
	t.Parallel()
	w := newPasswordWorld(t, pwOptions{conformance: true})

	for _, tc := range []struct {
		account, client, password, want string
	}{
		{"conformance", "harness-rp", conformancePassword, issuer.ACRConformance},
		{"recovery", "app", recoveryPassword, issuer.ACRRecovery},
	} {
		t.Run(tc.account, func(t *testing.T) {
			t.Parallel()
			b := newBrowser(t, w.server)
			requested := discoveredACRValues(t, b)
			page := openChooserAsking(t, b, tc.client, requested)

			status, done := signInWith(t, b, stateOf(t, page), tc.account, tc.password)
			if status != http.StatusOK {
				t.Fatalf("sign-in = %d %v", status, done)
			}
			acr := payloadOf(t, tokensFrom(t, b, tc.client, done)["id_token"].(string))["acr"]
			if acr != tc.want || !slices.Contains(requested, tc.want) {
				t.Errorf("acr = %v, want %s, one of the requested %v", acr, tc.want, requested)
			}
		})
	}
}

// A class the sign-in did not meet is not claimed because it was asked for:
// the token says how the person was proved, and a relying party that needs
// the class it asked for refuses the token.
func TestARequestedClassTheSignInDidNotMeetIsNotClaimed(t *testing.T) {
	t.Parallel()
	w := newPasswordWorld(t, pwOptions{conformance: true})

	for _, tc := range []struct {
		account, client, password, want string
	}{
		{"conformance", "harness-rp", conformancePassword, issuer.ACRConformance},
		{"recovery", "app", recoveryPassword, issuer.ACRRecovery},
	} {
		t.Run(tc.account, func(t *testing.T) {
			t.Parallel()
			b := newBrowser(t, w.server)
			page := openChooserAsking(t, b, tc.client, []string{issuer.ACRDirectory})

			status, done := signInWith(t, b, stateOf(t, page), tc.account, tc.password)
			if status != http.StatusOK {
				t.Fatalf("sign-in = %d %v", status, done)
			}
			acr := payloadOf(t, tokensFrom(t, b, tc.client, done)["id_token"].(string))["acr"]
			if acr != tc.want {
				t.Errorf("acr = %v, want %s: the directory did not prove this sign-in", acr, tc.want)
			}
		})
	}
}
