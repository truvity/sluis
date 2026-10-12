package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/truvity/sluis/internal/access"
)

// SessionsService is the issuer's session service as the console reaches it:
// in this process, as the person signed in to the console.
//
// The console's Sessions page and the sessions on a person's or a client's
// page used to call the issuer from the browser with the issuer's own session
// cookie. A sign-in that never touched the issuer -- the console's recovery
// sign-in -- has no such cookie, so every one of those calls was refused. The
// console has already established who is signed in to it, so it passes that on
// itself and the browser needs no second session.
type SessionsService struct {
	// Handler serves the session service at /<package>.SessionService/<method>.
	Handler http.Handler
	// As returns ctx carrying the caller the service is to take the request
	// for: identity, and whether they may act on anybody's sessions.
	As func(ctx context.Context, identity string, operator bool) context.Context
}

// UseSessions connects the console to the issuer's session service in this
// process, with the same timing as [ConsoleServer.UseSignedIn]: the issuer does
// not exist yet when the console is built.
func (s *ConsoleServer) UseSessions(sessions *SessionsService) {
	s.sessionsAPI = sessions
}

// sessionsPrefixes are the session service's two names, current and legacy.
var sessionsPrefixes = []string{"/sluis.v1.SessionService/", "/accessissuer.v1.SessionService/"}

// sessionsCall serves the browser's calls to the session service.
func (s *ConsoleServer) sessionsCall(w http.ResponseWriter, r *http.Request) {
	if s.sessionsAPI == nil {
		http.NotFound(w, r)
		return
	}
	id, ok := IdentityFrom(r.Context())
	if !ok {
		connectRefusal(w, http.StatusUnauthorized, "unauthenticated", "sign in first")
		return
	}
	if r.Method != http.MethodPost || strings.Contains(r.URL.Path, "..") {
		http.NotFound(w, r)
		return
	}
	// Who the person is to the issuer's index: the address, or the subject for
	// a sign-in with none.
	ctx := s.sessionsAPI.As(r.Context(), strings.ToLower(id.Who()), id.Role.Implies(access.RoleOperator))
	// The console's own credentials stay here.
	out := r.Clone(ctx)
	out.Header.Del("Cookie")
	out.Header.Del("Authorization")
	s.sessionsAPI.Handler.ServeHTTP(w, out)
}
