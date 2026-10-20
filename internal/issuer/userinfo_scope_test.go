package issuer_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/issuer"
)

// What `/userinfo` answers for each scope the issuer serves, from each source
// it has: the directory for a person, and the account itself for the
// conformance account. The other claims of `profile` (middle_name, nickname,
// profile, picture, website, gender, birthdate, zoneinfo, locale, updated_at)
// have no source, and none is ever returned.

// unsourced are the `profile` claims sluis has no source for.
var unsourced = []string{
	"middle_name", "nickname", "profile", "picture", "website",
	"gender", "birthdate", "zoneinfo", "locale", "updated_at",
}

// personUserinfo signs a person in with scope, through a refresh as the other
// claims tests do, and returns what `/userinfo` answers for the access token.
func personUserinfo(t *testing.T, standing issuer.Standing, scope string) map[string]any {
	t.Helper()
	const who = "ada@north.example"
	server, iss := serveIssuerFor(t, &fakeDirectory{standing: map[string]issuer.Standing{who: standing}})

	refresh := "refresh-" + strings.ReplaceAll(scope, " ", "-")
	if _, err := iss.Sessions().Record(t.Context(), issuer.Opened{
		Identity: who, ClientID: "local-dev", How: issuer.HowCode,
		Token: refresh, Scopes: strings.Fields(scope),
	}); err != nil {
		t.Fatalf("record the session: %v", err)
	}
	tokens := postToken(t, server, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refresh},
		"client_id":     {"local-dev"},
		"scope":         {scope},
	}, "local-dev", http.StatusOK)

	return userinfoOf(t, server, accessOf(t, tokens))
}

// accessOf is the access token of a token response.
func accessOf(t *testing.T, tokens map[string]any) string {
	t.Helper()
	access, _ := tokens["access_token"].(string)
	if access == "" {
		t.Fatalf("no access token in %v", tokens)
	}
	return access
}

// noneOf fails for each of names that got carries.
func noneOf(t *testing.T, got map[string]any, names ...string) {
	t.Helper()
	for _, name := range names {
		if value, present := got[name]; present {
			t.Errorf("userinfo carries %s = %v, which has no source here", name, value)
		}
	}
}

// The `email` scope: the address a person signed in as, which the directory
// vouched for, and that it is verified.
func TestUserinfoAnswersTheEmailScopeFromTheSignIn(t *testing.T) {
	t.Parallel()
	got := personUserinfo(t, issuer.Standing{Found: true, Authoritative: true, Groups: []string{"engineering@north.example"}},
		"openid email")

	if got["sub"] != "ada@north.example" || got["email"] != "ada@north.example" || got["email_verified"] != true {
		t.Errorf("sub %v email %v email_verified %v, want the address the person signed in as, verified",
			got["sub"], got["email"], got["email_verified"])
	}
}

// The `profile` scope, from a directory that supplies the names (Google, with
// the user-read scope): the names, the name made of them, and the address as
// the username.
func TestUserinfoAnswersTheProfileScopeFromTheDirectory(t *testing.T) {
	t.Parallel()
	got := personUserinfo(t, issuer.Standing{
		Found: true, Authoritative: true, Groups: []string{"engineering@north.example"},
		GivenName: "Ada", FamilyName: "Lovelace",
	}, "openid profile")

	for name, want := range map[string]string{
		"name": "Ada Lovelace", "given_name": "Ada", "family_name": "Lovelace", "preferred_username": "ada@north.example",
	} {
		if got[name] != want {
			t.Errorf("%s = %v, want %q", name, got[name], want)
		}
	}
	noneOf(t, got, unsourced...)
}

// The `profile` scope, from a directory that supplies no names: the username
// only, and no empty name.
func TestUserinfoNamesNobodyWhenTheDirectoryHasNoNames(t *testing.T) {
	t.Parallel()
	got := personUserinfo(t, issuer.Standing{Found: true, Authoritative: true, Groups: []string{"engineering@north.example"}},
		"openid profile")

	if got["preferred_username"] != "ada@north.example" {
		t.Errorf("preferred_username = %v, want the address", got["preferred_username"])
	}
	noneOf(t, got, append([]string{"name", "given_name", "family_name"}, unsourced...)...)
}

// conformanceUserinfo signs the conformance account in at the harness client
// with scope and returns the token response and what `/userinfo` answers.
func conformanceUserinfo(t *testing.T, scope string) (tokens, got map[string]any) {
	t.Helper()
	w := newPasswordWorld(t, pwOptions{conformance: true})
	b := newBrowser(t, w.server)
	status, done := signInWith(t, b, stateOf(t, openChooserFor(t, b, "harness-rp", scope)), "conformance", conformancePassword)
	if status != http.StatusOK {
		t.Fatalf("conformance sign-in = %d %v", status, done)
	}
	tokens = tokensFrom(t, b, "harness-rp", done)
	return tokens, userinfoOf(t, w.server, accessOf(t, tokens))
}

// The conformance account, granted `profile`: what the account is called and
// the username it signs in with, in userinfo and not in the ID token. It has no
// given or family name and no address.
func TestTheConformanceAccountsProfileIsItsNameAndUsername(t *testing.T) {
	t.Parallel()
	tokens, got := conformanceUserinfo(t, "openid profile")

	if got["sub"] != conformanceSubject || got["name"] != issuer.ConformanceName || got["preferred_username"] != "conformance" {
		t.Errorf("sub %v name %v preferred_username %v, want the account's own", got["sub"], got["name"], got["preferred_username"])
	}
	noneOf(t, got, append([]string{"given_name", "family_name", "email", "email_verified"}, unsourced...)...)

	idToken := payloadOf(t, tokens["id_token"].(string))
	noneOf(t, idToken, "name", "preferred_username")
}

// Without `profile` the conformance account's userinfo is its subject: it has
// no address for `email`, and its name is not returned unasked.
func TestTheConformanceAccountAnswersNothingUnaskedAndHasNoAddress(t *testing.T) {
	t.Parallel()
	for _, scope := range []string{"openid", "openid email"} {
		_, got := conformanceUserinfo(t, scope)
		if got["sub"] != conformanceSubject {
			t.Errorf("%s: sub = %v", scope, got["sub"])
		}
		noneOf(t, got, "name", "preferred_username", "email", "email_verified")
	}
}
