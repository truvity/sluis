package issuer

import "net/http"

// ConfirmedEndSessionForTest is [confirmedEndSession] .
func ConfirmedEndSessionForTest(signIn SignInDeps, w http.ResponseWriter, r *http.Request) bool {
	return confirmedEndSession(signIn, w, r)
}
