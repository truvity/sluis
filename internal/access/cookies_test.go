package access_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/truvity/sluis/internal/access"
)

func TestReadCookieAcceptsEitherName(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, cookie string
		secure       bool
		base         string
		want         string
	}{
		{"new", "sluis_session", false, access.CookieName, "n"},
		{"old", "access_roster_session", false, access.CookieName, "n"},
		{"old host", "__Host-access_roster_recovery", true, access.RecoveryCookieName, "n"},
		{"old sso", "__Host-access_issuer_sso", true, access.SSOCookieName, "n"},
		{"other", "access_roster_login", false, access.CookieName, ""},
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(&http.Cookie{Name: tc.cookie, Value: "n"})

		got := ""
		if c, err := access.ReadCookie(r, tc.base, tc.secure); err == nil {
			got = c.Value
		}

		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestReadCookiePrefersTheNewName(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: "access_roster_session", Value: "old"})
	r.AddCookie(&http.Cookie{Name: "sluis_session", Value: "new"})

	if c, err := access.ReadCookie(r, access.CookieName, false); err != nil || c.Value != "new" {
		t.Errorf("got %v, %v", c, err)
	}
}

func TestSetCookieWritesTheNewNameAndClearsTheOld(t *testing.T) {
	t.Parallel()

	w := httptest.NewRecorder()
	access.SetCookie(w, access.RecoveryCookie("state", true, 600000000000))

	got := map[string]*http.Cookie{}
	for _, c := range w.Result().Cookies() {
		got[c.Name] = c
	}

	if c := got["__Host-sluis_recovery"]; c == nil || c.Value != "state" || c.MaxAge <= 0 {
		t.Errorf("new cookie = %v", c)
	}

	if c := got["__Host-access_roster_recovery"]; c == nil || c.Value != "" || c.MaxAge >= 0 || !c.Secure {
		t.Errorf("old cookie = %v", c)
	}

	if len(got) != 2 {
		t.Errorf("wrote %d cookies, want 2", len(got))
	}
}
