package config

import (
	"errors"
	"strings"
)

// ValidateSecureCookies refuses `secureCookies: false` on a service served
// over https. The default follows the URL's scheme; the override exists for a
// TLS terminator that is not in the URL (true on http) and for a local http
// run. False beside an https URL is never that case: it only sends the
// session cookies without the Secure flag, where a proxy can carry them over a
// plain-http hop. The chart refuses the same value (config.secureCookies with
// route.host).
//
// It reads the issuer URL, and the public root URL the console's cookies
// follow, so a document that is https in either is held to it.
func (s *Serve) ValidateSecureCookies() error {
	if s.SecureCookies == nil || *s.SecureCookies {
		return nil
	}
	for _, u := range []struct{ key, value string }{
		{"issuerURL", s.IssuerURL},
		{"publicRootURL", s.PublicRootURL},
	} {
		if isHTTPS(u.value) {
			return errors.New("secureCookies: false is refused when " + u.key + " is https: " +
				"session cookies would be sent without the Secure flag; remove secureCookies")
		}
	}
	return nil
}

func isHTTPS(u string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(u)), "https://")
}
