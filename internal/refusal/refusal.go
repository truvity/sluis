// Package refusal is how this service says no.
//
// A caller who is refused learns that they were, and a number: the request
// id. The reason -- which rule, which group, which client, whether a thing
// exists at all -- goes to the log and to the audit record under that same
// id, where an operator can read it and a caller cannot. A reason in the
// response is a question answered for whoever is probing: whether a client
// id exists, which audience a proof would open, which group would have
// been enough.
//
// This package holds the part every surface shares: the request id (one
// middleware, so every HTTP surface has one), the fixed wording, and the
// Connect interceptor. The OAuth endpoints' helper lives beside the
// endpoints, in internal/issuer, because it speaks the library's errors.
package refusal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"

	"github.com/truvity/sluis/audit/sdk/emit"
)

// Header carries the request id, inbound when a gateway set it and
// always on the response.
const Header = "X-Request-Id"

// The least and most a caller-chosen id may be. Shorter is not worth
// correlating on; longer is somebody else's data in this service's logs.
const (
	minIDLength = 8
	maxIDLength = 64
)

type idKey struct{}

// ID is the request id the context carries, or empty outside a request.
func ID(ctx context.Context) string {
	id, _ := ctx.Value(idKey{}).(string)
	return id
}

// WithID carries an id on a context, for work that has no request to
// take one from and for the tests that stand in for one.
func WithID(ctx context.Context, id string) context.Context {
	return emit.WithRequestID(context.WithValue(ctx, idKey{}, id), id)
}

// Middleware gives every request an id, puts it on the response and on
// the context, and hands the audit trail the same one.
//
// An inbound X-Request-Id is taken when it is well-formed and short --
// a gateway's id is how a caller's report reaches the log line -- and
// replaced by a random one otherwise, so a caller cannot put arbitrary
// text into a log through it. Idempotent: a handler already under one
// keeps the id it has, which is how the merged service nests the console
// inside the issuer.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id := ID(r.Context()); id != "" {
			if w.Header().Get(Header) == "" {
				w.Header().Set(Header, id)
			}
			next.ServeHTTP(w, r)
			return
		}
		id := r.Header.Get(Header)
		if !WellFormed(id) {
			id = generate()
		}
		w.Header().Set(Header, id)
		next.ServeHTTP(w, r.WithContext(WithID(r.Context(), id)))
	})
}

// WellFormed reports whether a caller's id may be adopted: letters,
// digits and . _ : - only, between eight and sixty-four of them.
func WellFormed(id string) bool {
	if len(id) < minIDLength || len(id) > maxIDLength {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.', c == '_', c == ':', c == '-':
		default:
			return false
		}
	}
	return true
}

func generate() string {
	var raw [10]byte
	if _, err := rand.Read(raw[:]); err != nil {
		// crypto/rand does not fail on the platforms this runs on; an id
		// that is not unique is still better than no answer.
		return "0000000000000000"
	}
	return hex.EncodeToString(raw[:])
}

// The fixed wording. Every refusal a caller reads is one of these and the
// id; nothing about why is ever formatted into them.
const (
	// NotAuthorised is the answer to every refusal.
	NotAuthorised = "not authorised"
	// ServerError is the answer to a fault of ours.
	ServerError = "server error"
	// InvalidRequest is the answer to a request that is not well-formed.
	InvalidRequest = "invalid request"
	// SignInFirst is the answer to a caller who is nobody yet.
	SignInFirst = "sign in first"
)

// Text is a fixed phrase with the request id, "not authorised (request
// 1f3a...)". With no id -- a call outside a request -- it is the phrase
// alone.
func Text(ctx context.Context, phrase string) string {
	if id := ID(ctx); id != "" {
		return phrase + " (request " + id + ")"
	}
	return phrase
}
