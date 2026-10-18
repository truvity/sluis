// Package httpsec is what every listener and every HTML response of this
// service shares for the web's own defences: the headers a browser needs to
// refuse framing and sniffing, the limits on what a client may send, and the
// timeouts a server holds a connection to.
//
// They live in one place because they are one decision. Two copies of a
// header list drift: a page served with five of the six is the page the
// audit finds.
package httpsec

import (
	"net/http"
	"strings"
	"time"
)

// Limits on a listener. They are deliberately generous for what the service
// really receives -- a token request is a few KiB, a console call a few
// more -- and small beside what net/http would otherwise read for anybody
// who asks: the standard library reads a header block of 1 MiB and a body
// of any size, with no deadline on either.
const (
	// MaxRequestBody is the most a request body may carry, in bytes. The
	// largest thing a client legitimately sends is a signing key or a
	// catalogue in a console call, well under this.
	MaxRequestBody = 2 << 20

	// MaxHeaderBytes is the most a request's header block may carry,
	// cookies and a bearer included. A bearer carrying every group an
	// operator holds is several KiB.
	MaxHeaderBytes = 64 << 10

	// ReadHeaderTimeout is how long a client has to send its headers.
	ReadHeaderTimeout = 10 * time.Second

	// ReadTimeout is how long a client has to send the whole request,
	// body included. It is what ends a client that sends one byte a
	// second.
	ReadTimeout = 60 * time.Second

	// WriteTimeout is how long a handler has to answer, counted from the
	// end of the request headers. It is long because the console runs a
	// backup, and a restore preview, as one synchronous call that Lambda
	// stops at 15 minutes (internal/server: backupRunTimeout, 16
	// minutes): a shorter deadline would cut the answer to a backup that
	// is still running. It still ends a client that does not read.
	WriteTimeout = 20 * time.Minute

	// IdleTimeout is how long a kept-alive connection may sit unused.
	IdleTimeout = 2 * time.Minute
)

// NewServer is an [http.Server] holding the limits above.
func NewServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: ReadHeaderTimeout,
		ReadTimeout:       ReadTimeout,
		WriteTimeout:      WriteTimeout,
		IdleTimeout:       IdleTimeout,
		MaxHeaderBytes:    MaxHeaderBytes,
	}
}

// LimitBody refuses a request whose body is larger than limit, and stops
// reading one that turns out larger than it said.
//
// A declared length over the limit is refused at once with 413, before a
// handler runs; a body of unknown length (chunked) or one that lies about
// its length is cut off by [http.MaxBytesHandler], and the handler that
// reads it sees the error.
func LimitBody(next http.Handler, limit int64) http.Handler {
	limited := http.MaxBytesHandler(next, limit)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > limit {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Connection", "close")
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			_, _ = w.Write([]byte("request body too large\n"))

			return
		}

		limited.ServeHTTP(w, r)
	})
}

// Policy is the Content-Security-Policy of one page.
type Policy struct {
	// ScriptNonce lets the page's one inline script run. Empty is no script
	// at all beyond this origin's own files.
	ScriptNonce string

	// FormsMayRedirect leaves out `form-action`. A page whose form ends a
	// sign-in is answered with a redirect to the application, and a browser
	// that enforces `form-action` checks the redirect's target too: with
	// `'self'` it would stop the person on a blank page at the very end of
	// signing in. These pages are the issuer's recovery form and its agent
	// consent page.
	FormsMayRedirect bool
}

// String is the header's value.
//
// `style-src` allows inline styles because the service's own pages carry
// one `<style>` block, and the console's component library writes style
// rules at run time; scripts are this origin's files, and nothing inline
// except by nonce. `img-src` allows `data:` for the few inline images.
func (p Policy) String() string {
	directives := []string{
		"default-src 'self'",
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data:",
		"frame-ancestors 'none'",
		"base-uri 'none'",
		"object-src 'none'",
	}
	if p.ScriptNonce != "" {
		directives = append(directives, "script-src 'nonce-"+p.ScriptNonce+"'")
	}
	if !p.FormsMayRedirect {
		directives = append(directives, "form-action 'self'")
	}

	return strings.Join(directives, "; ")
}

// Apply sets the four headers for one response under p. A handler whose
// page needs a different policy calls it again: the last write wins.
func Apply(w http.ResponseWriter, p Policy) {
	header := w.Header()
	header.Set("Content-Security-Policy", p.String())
	header.Set("X-Frame-Options", "DENY")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
}

// Headers sets the default policy on every response, so that no page is
// served without it by having been forgotten. A handler that needs another
// policy sets it with [Apply].
func Headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Apply(w, Policy{})
		next.ServeHTTP(w, r)
	})
}

// NoListing answers a request for a directory with 404 instead of the file
// server's listing of what is in it.
func NoListing(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)

			return
		}

		next.ServeHTTP(w, r)
	})
}
