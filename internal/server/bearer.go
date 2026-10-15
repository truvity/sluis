package server

import (
	"net/http"
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

// bearerMayCall says a bearer from [ConsoleServer.UseBearers] may call path.
func bearerMayCall(path string) bool {
	for _, prefix := range bearerServices {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}
