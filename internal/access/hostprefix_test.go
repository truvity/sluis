package access_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/access"
)

func TestCookieNameForPrefixesOnlyWhenSecure(t *testing.T) {
	if got := access.CookieNameFor("x", true); got != "__Host-x" {
		t.Fatalf("secure name = %q", got)
	}
	if got := access.CookieNameFor("x", false); got != "x" {
		t.Fatalf("plain name = %q", got)
	}
}

func TestSecureCookiesSatisfyHostPrefixRules(t *testing.T) {
	key, err := access.NewSessionKey()
	if err != nil {
		t.Fatal(err)
	}
	s, err := access.NewSessions(key, time.Hour, true)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	if err = s.Issue(rec, access.Principal{Email: "eve@example.com"}); err != nil {
		t.Fatal(err)
	}
	cookies := []*http.Cookie{
		access.ConnectCookie("v", true, time.Minute),
		access.LoginCookie("v", true, time.Minute),
		access.LinkCookie("v", true, time.Minute),
	}
	cookies = append(cookies, rec.Result().Cookies()...)
	for _, c := range cookies {
		if !strings.HasPrefix(c.Name, "__Host-") || !c.Secure || c.Path != "/" || c.Domain != "" {
			t.Errorf("cookie %q breaks the __Host- rules: %+v", c.Name, c)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	if _, err = s.Read(req); err != nil {
		t.Fatalf("a session read under the same name must succeed: %v", err)
	}
	old := httptest.NewRequest(http.MethodGet, "/", nil)
	old.AddCookie(&http.Cookie{Name: access.CookieName, Value: rec.Result().Cookies()[0].Value})
	if _, err = s.Read(old); err == nil {
		t.Fatal("the unprefixed name must no longer be read when cookies are secure")
	}
}
