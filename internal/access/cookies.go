package access

import (
	"net/http"
	"strings"
)

// legacyCookieNames maps each cookie's current base name to the one it had
// before the rename to sluis. A request is read under either name, and
// only the current one is written; a write or a stale of the current name
// also clears the old one, so a browser that held it stops sending it.
var legacyCookieNames = map[string]string{
	CookieName:             "access_roster_session",
	ConnectCookieName:      "access_roster_connect",
	LoginCookieName:        "access_roster_login",
	LinkCookieName:         "access_roster_link",
	RecoveryCookieName:     "access_roster_recovery",
	AgentConsentCookieName: "access_roster_agent_consent",
	SSOCookieName:          "access_issuer_sso",
}

// ReadCookie returns the cookie a request carries under base, written
// under its current name, or else under the name it had before the rename.
// The current name wins when a browser holds both.
func ReadCookie(r *http.Request, base string, secure bool) (*http.Cookie, error) {
	cookie, err := r.Cookie(CookieNameFor(base, secure))
	if err == nil {
		return cookie, nil
	}
	if legacy, ok := legacyCookieNames[base]; ok {
		if old, oldErr := r.Cookie(CookieNameFor(legacy, secure)); oldErr == nil {
			return old, nil
		}
	}

	return nil, err
}

// SetCookie writes cookie, and with it a clearing of the cookie's old
// name, so that setting, replacing or clearing a cookie leaves the browser
// with the current name only. A cookie with no old name is written as is.
func SetCookie(w http.ResponseWriter, cookie *http.Cookie) {
	http.SetCookie(w, cookie)
	legacy, ok := legacyCookieNames[strings.TrimPrefix(cookie.Name, HostPrefix)]
	if !ok {
		return
	}
	stale := *cookie
	stale.Name = legacy
	if strings.HasPrefix(cookie.Name, HostPrefix) {
		stale.Name = HostPrefix + legacy
	}
	stale.Value = ""
	stale.Expires = cookie.Expires.Truncate(0)
	stale.MaxAge = -1
	http.SetCookie(w, &stale)
}

// SSOCookieName is the issuer's browser-session cookie. It is named here,
// with the others, so that the legacy-name table can hold it.
const SSOCookieName = "sluis_sso"
