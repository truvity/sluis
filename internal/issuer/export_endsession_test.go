package issuer

import "net/http"

// ConfirmedEndSessionForTest is [confirmedEndSession] with no client lookup.
func ConfirmedEndSessionForTest(signIn SignInDeps, w http.ResponseWriter, r *http.Request) bool {
	return confirmedEndSession(signIn, nil, w, r)
}
