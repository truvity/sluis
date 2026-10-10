// Package connectalias serves one Connect service under a second name.
//
// The proto packages directoryroster.v1 and accessissuer.v1 are being
// renamed to sluis.v1. Their messages and services are identical, field
// for field and number for number, so a request addressed to the new name
// is the same bytes as one addressed to the old, and the cheapest way to
// answer both from one handler is to change the path and nothing else.
// There is no second implementation and no copying between the two
// generations of message types, so the two names cannot disagree.
//
// For the release that serves both, the metrics and traces a call leaves
// still carry the old procedure name: it is the name the handler was built
// with. Removing the old packages removes this package with them.
package connectalias

import (
	"net/http"
	"strings"
)

// Rewrite answers requests for the service newName by handing them to next
// as requests for oldName. Both are fully qualified service names, such as
// "sluis.v1.AccessService".
//
// Only the leading service segment changes, so the method, the query, the
// headers and the body reach next exactly as they were sent.
func Rewrite(next http.Handler, newName, oldName string) http.Handler {
	from, to := "/"+newName+"/", "/"+oldName+"/"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, ok := strings.CutPrefix(r.URL.Path, from)
		if !ok {
			// Not this service: the mux sends here only what matches,
			// so this is a handler used outside one. Refuse rather than
			// guess.
			http.NotFound(w, r)

			return
		}

		rewritten := r.Clone(r.Context())
		rewritten.URL.Path = to + method
		rewritten.URL.RawPath = ""
		rewritten.RequestURI = ""
		next.ServeHTTP(w, rewritten)
	})
}

// Mount makes mux answer newName as well as oldName. The handler for
// oldName must already be registered on mux; Mount adds no handler of its
// own.
func Mount(mux *http.ServeMux, newName, oldName string) {
	mux.Handle("/"+newName+"/", Rewrite(mux, newName, oldName))
}
