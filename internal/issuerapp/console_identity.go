package issuerapp

import (
	"net/http"
	"strings"

	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/emailaddr"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/policy"
)

// signedIn reads the issuer's own browser session and hands the console
// whoever it belongs to.
//
// This is what the merged service buys, and it is worth stating plainly
// because it removes two things at once. Before it, a console on this
// origin was authenticated either by a proxy in front of it — which ran
// an OpenID flow against an issuer in the SAME PROCESS, a network round
// trip and a second session store to learn something already known — or
// by a login of its own, which is the second door an installation with a
// gateway deliberately turns off. Now it is neither: the browser holds
// the issuer's session cookie at this host, so the console asks who it
// is.
//
// A recovery sign-in completes as a ServiceAccount rather than an
// address, and it is known by how it was made ([issuer.RecoveryHow]), not
// by what its subject looks like. It has to survive this: recovery exists for the day
// nothing else works, and the console is where the operator then
// connects the first directory.
//
// It asks the issuer's own question, not a narrower one. Reading the
// cookie alone admitted a sign-in for as long as the sign-in lived -- up
// to its whole lifetime after the absolute limit had passed, or after the
// directory suspended the person -- to the one surface where client
// secrets are rotated and sessions revoked. read is
// [issuer.SignedInReader]: the same decisions the issuer's silent sign-in
// makes, from the same function, and what does not stand is ended.
func signedIn(read func(http.ResponseWriter, *http.Request) (issuer.SSOSession, bool)) func(http.ResponseWriter, *http.Request) (access.Principal, bool) {
	if read == nil {
		return nil
	}
	return func(w http.ResponseWriter, r *http.Request) (access.Principal, bool) {
		session, ok := read(w, r)
		if !ok || session.Identity == "" {
			return access.Principal{}, false
		}
		// Recovery by what the sign-in RECORDED when it began, never by
		// the shape of its subject: recovery is the global operator, and a
		// subject is whatever an identity provider said. A sign-in that
		// was not made by recovery is a person, asked of the directory
		// like any other, whatever its subject looks like.
		// The conformance account is a test subject and never the console's: it
		// is not recovery, and not a person the directory knows.
		if session.How == issuer.ConformanceHow {
			return access.Principal{}, false
		}
		if session.How == issuer.RecoveryHow || session.How == issuer.RecoveryPasswordHow {
			account, ok := policy.ParseServiceAccountSubject(session.Identity)
			if !ok {
				return access.Principal{}, false
			}
			return access.Principal{
				Subject:        session.Identity,
				Source:         access.SourceRecovery,
				ServiceAccount: &account,
			}, true
		}
		return access.Principal{
			Email:   strings.ToLower(session.Identity),
			Subject: session.Identity,
			Source:  access.SourceOIDC,
		}, true
	}
}

// personBearer turns the subject of a CLI access token into the person it
// names. Only an address qualifies: a workload's exchanged token has a subject
// that is not one, and a recovery sign-in is not read here (recovery has its
// own door, the browser).
func personBearer(read func(*http.Request) (string, bool)) func(*http.Request) (access.Principal, bool) {
	return func(r *http.Request) (access.Principal, bool) {
		subject, ok := read(r)
		if !ok {
			return access.Principal{}, false
		}
		email := strings.ToLower(strings.TrimSpace(subject))
		if _, ok := emailaddr.Domain(email); !ok || strings.ContainsFunc(email, func(r rune) bool { return r < 0x20 || r == 0x7f || r == ' ' }) {
			return access.Principal{}, false
		}
		return access.Principal{Email: email, Subject: subject, Source: access.SourceOIDC}, true
	}
}
