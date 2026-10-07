package grantcost

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"connectrpc.com/connect"

	accessissuerv1 "github.com/truvity/sluis/gen/accessissuer/v1"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/internal/server"
)

// Whoami is the console's answer about who a request is.
type Whoami struct {
	Status string `json:"status"`
	Email  string `json:"email"`
}

// Console is one request to the console on this issuer's origin, with the
// browser's cookies: the console's own identity step, assembled as the
// merged service assembles it -- the issuer's [issuer.SignedInReader] for
// the browser's sign-in, and the console's authorizer over the same
// (counted) hub -- and served by the console's real handler, so that what
// it costs is what production pays on every console request.
//
// It asks `/.access/whoami`, which reads nothing beyond who the caller is:
// what is measured is the identity step every console RPC pays first.
// Cookies the answer sets are kept, a cleared one removed.
func (h *Harness) Console() Whoami {
	h.t.Helper()
	if h.console == nil {
		read := issuer.SignedInReader(h.Issuer, h.storage, issuer.SignInDeps{})
		sessions, err := access.NewSessions(make([]byte, access.SessionKeyBytes), time.Hour, false)
		if err != nil {
			h.t.Fatalf("console sessions: %v", err)
		}
		h.console = server.NewConsoleServer(server.ConsoleServerDeps{
			Authorizer: access.NewAuthorizer(h.set, h.hub, issuer.DefaultHoldWindow),
			Sessions:   sessions,
			// The issuerapp mapping, which is a name and nothing that
			// costs anything: a person by address.
			SignedIn: func(w http.ResponseWriter, r *http.Request) (access.Principal, bool) {
				session, ok := read(w, r)
				if !ok {
					return access.Principal{}, false
				}
				return access.Principal{
					Email: strings.ToLower(session.Identity), Subject: session.Identity, Source: access.SourceOIDC,
				}, true
			},
			Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		}).Handler()
	}

	request := httptest.NewRequestWithContext(h.t.Context(), http.MethodGet, "/.access/whoami", nil)
	for name, value := range h.cookies {
		request.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	response := httptest.NewRecorder()
	h.console.ServeHTTP(response, request)
	for _, cookie := range response.Result().Cookies() {
		if cookie.MaxAge < 0 {
			delete(h.cookies, cookie.Name)
			continue
		}
		h.cookies[cookie.Name] = cookie.Value
	}

	var out Whoami
	if err := json.NewDecoder(response.Body).Decode(&out); err != nil {
		h.t.Fatalf("console whoami: %d, %v", response.Code, err)
	}
	return out
}

// OwnSessions is one call to the issuer's session service by the browser's
// cookie, as the console's account page makes it: the caller's own
// sessions. What is measured is the cookie's check -- the same decision
// the issuer's silent sign-in makes -- and the listing it guards.
func (h *Harness) OwnSessions() (int, error) {
	h.t.Helper()
	if h.accounts == nil {
		h.accounts = issuer.NewSessionsService(h.Issuer, nil, false)
	}
	request := connect.NewRequest(&accessissuerv1.ListSessionsRequest{Identity: Person})
	for name, value := range h.cookies {
		request.Header().Add("Cookie", (&http.Cookie{Name: name, Value: value}).String())
	}
	response, err := h.accounts.ListSessions(h.t.Context(), request)
	if err != nil {
		return 0, err
	}
	return len(response.Msg.GetSessions()), nil
}
