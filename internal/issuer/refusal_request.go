package issuer

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/truvity/sluis/storage/logattr"
)

// What a refusal's log line says about the request it refused.
//
// The reason alone does not tell an operator who is being refused: "the
// authorization request was not started in this browser" from one client
// over and over is a broken integration, and from a hundred user agents it
// is somebody's link going round. So every refusal also names the method,
// the path without its query, the client when one is known, and the user
// agent.
//
// What it never names is anything that works as a handle or a secret: the
// query string as a whole, `state`, `nonce`, a code or its challenge, the
// authorization request's id, a cookie, a token. The path is taken without
// its query for that reason, and the client id is the one public name in
// the lot.

// maxUserAgent is the most of a user agent a refusal keeps, in runes. A
// browser's is well under it; anything longer is somebody else's text in
// this service's log.
const maxUserAgent = 200

// requestFacts is what [withRequestFacts] noted about the request a
// handler is serving. The client is filled in as soon as anything below
// learns it from the pending authorization, so that a refusal later in the
// same request can name it.
type requestFacts struct {
	method, path, userAgent, clientID string
}

type requestFactsKey struct{}

// withRequestFacts notes the request's method, path, user agent and
// client on its context, for [deny], which is given a context and no
// request, to log.
func withRequestFacts(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.Context().Value(requestFactsKey{}).(*requestFacts); ok {
			next.ServeHTTP(w, r)
			return
		}
		facts := factsOf(r)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestFactsKey{}, facts)))
	})
}

func factsOf(r *http.Request) *requestFacts {
	return &requestFacts{
		method:    r.Method,
		path:      r.URL.Path,
		userAgent: r.UserAgent(),
		clientID:  clientNamedBy(r),
	}
}

// clientNamedBy is the client a request names in the open: its `client_id`
// query parameter, the one in a form somebody has already read (never read
// here: the body is the handler's), or the user of HTTP Basic, which is a
// client's id and never its secret.
func clientNamedBy(r *http.Request) string {
	if id := r.URL.Query().Get("client_id"); id != "" {
		return id
	}
	if r.PostForm != nil {
		if id := r.PostForm.Get("client_id"); id != "" {
			return id
		}
	}
	if user, _, ok := r.BasicAuth(); ok {
		return user
	}
	return ""
}

// noteClient records the client a request is for, once a handler has read
// it from the pending authorization: the request itself names only the
// authorization's id, which is not for the log.
func noteClient(ctx context.Context, clientID string) {
	if facts, ok := ctx.Value(requestFactsKey{}).(*requestFacts); ok && clientID != "" {
		facts.clientID = clientID
	}
}

// withRequest is a refusal's attributes with the request's added: the
// facts on the context, or r's own where the context has none. A field the
// caller already set is kept as it is, so a refusal that names its client
// as `client` or `client_id` is not given a second.
func withRequest(ctx context.Context, r *http.Request, attrs []any) []any {
	facts, ok := ctx.Value(requestFactsKey{}).(*requestFacts)
	if !ok {
		if r == nil {
			return attrs
		}
		facts = factsOf(r)
	}
	clientID := facts.clientID
	if clientID == "" && r != nil {
		clientID = clientNamedBy(r)
	}
	have := keysOf(attrs)
	add := func(attr slog.Attr) {
		if !have[attr.Key] {
			attrs = append(attrs, attr)
		}
	}
	add(logattr.SafeString("method", facts.method))
	add(logattr.SafeString("path", facts.path))
	if clientID != "" && !have["client"] {
		add(logattr.SafeString("client_id", clientID))
	}
	if facts.userAgent != "" {
		add(logattr.SafeString("user_agent", truncate(facts.userAgent, maxUserAgent)))
	}
	return attrs
}

// keysOf is the keys a list of slog arguments sets, in either of the forms
// slog takes: an Attr, or a key followed by its value.
func keysOf(args []any) map[string]bool {
	keys := map[string]bool{}
	for i := 0; i < len(args); i++ {
		switch arg := args[i].(type) {
		case slog.Attr:
			keys[arg.Key] = true
		case string:
			keys[arg] = true
			i++
		}
	}
	return keys
}

// truncate is s cut to at most n runes, ending in "…" when it was cut.
func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}
