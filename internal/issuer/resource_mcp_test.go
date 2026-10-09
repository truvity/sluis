package issuer_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/policy"
)

// A pure OAuth client sends no scope. The library refuses that outright;
// the issuer supplies `openid` so the request goes on to the sign-in.
func TestARequestWithNoScopeIsGivenOpenID(t *testing.T) {
	t.Parallel()
	server, _, _, _ := newMultiAlgServer(t)
	b := newBrowser(t, server)

	base := "/authorize?client_id=local-dev&response_type=code&state=s" +
		"&redirect_uri=" + url.QueryEscape("http://localhost:8000/callback") +
		"&code_challenge=E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM&code_challenge_method=S256" +
		"&resource=" + url.QueryEscape("https://resource.example/rs256")

	for name, path := range map[string]string{
		"absent":     base,
		"empty":      base + "&scope=",
		"whitespace": base + "&scope=%20",
		"sent":       base + "&scope=openid",
	} {
		status, where, body := b.do(http.MethodGet, path)
		if status != http.StatusFound || !strings.HasPrefix(where, "/login") {
			t.Errorf("%s scope: /authorize = %d %q %s, want a redirect to /login", name, status, where, body)
		}
	}
}

// RFC 8707 and RFC 3986: scheme and host are case-insensitive.
func TestAResourceSpelledWithUpperCaseSchemeAndHostIsTheSameResource(t *testing.T) {
	t.Parallel()
	server, _, _, _ := newMultiAlgServer(t)
	b := newBrowser(t, server)
	b.signIn()

	tokens := redeem(t, b, b.authorize("&resource="+url.QueryEscape("HTTPS://Resource.EXAMPLE/rs256")))
	if tokens["access_token"] == nil {
		t.Fatalf("no access token: %v", tokens)
	}
}

// The path is exact: folding it would name a different resource.
func TestAResourcePathIsNotCaseFolded(t *testing.T) {
	t.Parallel()
	server, _, _, _ := newMultiAlgServer(t)
	b := newBrowser(t, server)
	b.signIn()

	status, _, body := b.do(http.MethodGet, "/authorize?client_id=local-dev&response_type=code&scope=openid"+
		"&redirect_uri="+url.QueryEscape("http://localhost:8000/callback")+
		"&resource="+url.QueryEscape("https://resource.example/RS256"))
	if status != http.StatusBadRequest || !strings.Contains(body, "invalid_target") {
		t.Errorf("a different path = %d %s, want invalid_target", status, body)
	}
}

func TestCanonicalResourceID(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]string{
		"HTTPS://MCP.Example/Path":     "https://mcp.example/Path",
		"https://mcp.example/":         "https://mcp.example/",
		"https://mcp.example":          "https://mcp.example",
		"https://MCP.example:8443/A?Q": "https://mcp.example:8443/A?Q",
		"https://Us:Pw@MCP.example/x":  "https://Us:Pw@mcp.example/x",
	} {
		if got := policy.CanonicalResourceID(in); got != want {
			t.Errorf("CanonicalResourceID(%q) = %q, want %q", in, got, want)
		}
	}
}

// The page names the resource, by its display name or else its URL, and
// escapes both.
func TestTheChooserNamesTheResource(t *testing.T) {
	t.Parallel()

	named := chooserFor(t, issuer.Pending{
		ClientID: "c", Resource: "https://mcp.example/x", ResourceName: "Docs <MCP> & friends",
	}, "/login?auth=abc")
	if want := `<p>to access <strong>Docs &lt;MCP&gt; &amp; friends</strong></p>`; !strings.Contains(named, want) {
		t.Errorf("the chooser does not say %q:\n%s", want, named)
	}

	bare := chooserFor(t, issuer.Pending{ClientID: "c"}, "/login?auth=abc")
	if strings.Contains(bare, "to access") {
		t.Errorf("a request with no resource names one:\n%s", bare)
	}
}
