package server

import (
	"net/http"
	"net/url"
	pathpkg "path"
	"strings"

	"github.com/truvity/sluis/gen/directoryroster/v1/directoryrosterv1connect"
	"github.com/truvity/sluis/gen/sluis/v1/sluisv1connect"
	"github.com/truvity/sluis/internal/access"
)

// UseBearers supplies a reader of the issuer's own access tokens, for the same
// reason and with the same timing as [ConsoleServer.UseSignedIn]: the keys that
// verify them live with the issuer.
//
// It is how `sluisctl` reaches the BackupService with the sign-in it holds,
// and it opens that one service and no other. A laptop's token is the weaker
// credential (a file, not a browser session behind the directory's own
// checks), so it is not a way to every operator page: anything else answers as
// it does to a request with no identity. Who the bearer is, is then asked of the
// directory and the policy exactly as for a browser session.
func (s *ConsoleServer) UseBearers(read func(*http.Request) (access.Principal, bool)) {
	s.bearers = read
}

// bearerServices are the services a bearer from [ConsoleServer.UseBearers]
// may call, under both their names.
var bearerServices = []string{
	"/" + sluisv1connect.BackupServiceName + "/",
	"/" + directoryrosterv1connect.BackupServiceName + "/",
}

// bearerMayCall says a bearer from [ConsoleServer.UseBearers] may call the
// request's path: exactly `/<package>.BackupService/<Method>`, in its cleaned and
// unescaped form. This runs before the mux cleans or redirects anything, so it
// does not trust a path that only becomes a BackupService one once something
// has resolved it: `..`, an encoded `/` or `.`, a doubled slash and a trailing
// slash are all refused.
func bearerMayCall(u *url.URL) bool {
	path := u.Path
	if u.RawPath != "" && u.RawPath != path || strings.Contains(u.EscapedPath(), "%") || path != pathpkg.Clean(path) {
		return false
	}
	for _, prefix := range bearerServices {
		method, ok := strings.CutPrefix(path, prefix)
		if ok && method != "" && !strings.ContainsFunc(method, func(r rune) bool {
			return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9')
		}) {
			return true
		}
	}
	return false
}
