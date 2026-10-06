package issuer_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/issuer"
)

func TestSSOCookieHostPrefixWhenSecure(t *testing.T) {
	sso := issuer.NewSSO(nil, 0)
	c := sso.Cookie("id-1", true)
	if c.Name != "__Host-"+issuer.SSOCookieName || !c.Secure || c.Path != "/" || c.Domain != "" {
		t.Fatalf("secure SSO cookie breaks the __Host- rules: %+v", c)
	}
	if cleared := sso.Cookie("", true); cleared.Name != c.Name || cleared.MaxAge >= 0 {
		t.Fatalf("clearing must use the same name: %+v", cleared)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	if got := issuer.SSOFromRequest(req, true); got != "id-1" {
		t.Fatalf("prefixed cookie read = %q", got)
	}

	old := httptest.NewRequest(http.MethodGet, "/", nil)
	old.AddCookie(&http.Cookie{Name: issuer.SSOCookieName, Value: "id-1"})
	if got := issuer.SSOFromRequest(old, true); got != "" {
		t.Fatalf("unprefixed cookie must be ignored when secure, read %q", got)
	}
}

func TestSSOCookieKeepsPlainNameWhenNotSecure(t *testing.T) {
	sso := issuer.NewSSO(nil, 0)
	c := sso.Cookie("id-2", false)
	if c.Name != issuer.SSOCookieName || c.Secure || strings.HasPrefix(c.Name, "__Host-") {
		t.Fatalf("plain SSO cookie: %+v", c)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	if got := issuer.SSOFromRequest(req, false); got != "id-2" {
		t.Fatalf("read = %q", got)
	}
}
