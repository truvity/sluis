package issuer_test

import (
	"net/http"
	"net/url"
	"runtime/debug"
	"testing"
)

// An ID token names its client by `aud` and `azp` (OpenID Connect Core 2)
// and carries no `client_id`, which is an access token claim (RFC 9068
// 2.2). The OpenID library sets `client_id` on both; sluis builds a copy
// with the ID token's line removed (third_party/zitadel-oidc), so this test
// fails when the copy stops being the library the issuer is built with.
func TestAnIDTokenNamesItsClientByAzpAndCarriesNoClientID(t *testing.T) {
	t.Parallel()
	server, _, _, _ := newMultiAlgServer(t)

	b := newBrowser(t, server)
	b.signIn()

	// One assertion set for both grants: the code exchange and the refresh
	// mint their ID token by the same library call, and their access token
	// by another that must keep `client_id`.
	check := func(grant string, tokens map[string]any) {
		t.Helper()

		raw, _ := tokens["id_token"].(string)
		if raw == "" {
			t.Fatalf("%s: no id_token in %v", grant, tokens)
		}
		id := payloadOf(t, raw)
		if id["azp"] != "local-dev" {
			t.Errorf("%s: ID token azp = %v, want local-dev", grant, id["azp"])
		}
		if !audContains(id["aud"], "local-dev") {
			t.Errorf("%s: ID token aud = %v, want local-dev among it", grant, id["aud"])
		}
		if value, carried := id["client_id"]; carried {
			t.Errorf("%s: ID token carries client_id = %v; an ID token names its client by azp "+
				"(is third_party/zitadel-oidc still the library the go.mod replace wires in?)", grant, value)
		}

		access := payloadOf(t, tokens["access_token"].(string))
		if access["client_id"] != "local-dev" {
			t.Errorf("%s: access token client_id = %v, want local-dev (RFC 9068 2.2)", grant, access["client_id"])
		}
	}

	tokens := redeem(t, b, b.authorizeWith(map[string]string{"scope": "openid offline_access"}, ""))
	check("authorization_code", tokens)

	refreshToken, _ := tokens["refresh_token"].(string)
	if refreshToken == "" {
		t.Fatalf("no refresh token in %v; this test needs one to renew", tokens)
	}
	renewed := postToken(t, server, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {"local-dev"},
	}, "local-dev", http.StatusOK)
	check("refresh_token", renewed)
}

// The guard on the patch itself: the issuer is built with the copy in
// third_party/zitadel-oidc, at the version the require names. A Renovate
// bump of the require leaves the versioned replace behind, and this says so
// by name where the test above only sees the claim come back.
func TestTheOpenIDLibraryIsThePatchedCopy(t *testing.T) {
	t.Parallel()

	info, ok := debug.ReadBuildInfo()
	if !ok {
		t.Fatal("the test binary carries no build information")
	}
	for _, dep := range info.Deps {
		if dep.Path != "github.com/zitadel/oidc/v3" {
			continue
		}
		if dep.Replace == nil || dep.Replace.Path != "./third_party/zitadel-oidc" {
			t.Fatalf("github.com/zitadel/oidc/v3 %s is built from %v, want ./third_party/zitadel-oidc: "+
				"re-apply the patch to this version or drop the copy (third_party/zitadel-oidc/README.md)",
				dep.Version, dep.Replace)
		}
		return
	}
	t.Fatal("github.com/zitadel/oidc/v3 is not among the test binary's modules")
}
