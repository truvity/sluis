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
	"context"
	"net"
	"net/http"
	"net/url"
	"strconv"
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

	// FormTargets are further origins a page's own forms may be submitted
	// to, written after `'self'` in `form-action`. Each is an origin
	// (scheme and host, no path), named exactly: no wildcard. Ignored when
	// FormsMayRedirect leaves `form-action` out. The console names GitHub's
	// web origin, because GitHub creates an App only from a manifest the
	// browser POSTs to github.com.
	FormTargets []string
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
		directives = append(directives, strings.Join(append([]string{"form-action 'self'"}, p.FormTargets...), " "))
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

// DefaultHSTSMaxAge is how long a browser is told to keep using https for a
// host: a year, the figure the preload list asks for and the longest anybody
// has reason to ask.
const DefaultHSTSMaxAge = 365 * 24 * time.Hour

// HSTS is what a service says about Strict-Transport-Security. The zero
// value says nothing.
//
// The header is sent on a response only when the request arrived over https
// as this service sees it ([HSTS.secure]) -- a header on a plain-http
// response is ignored by browsers (RFC 6797 section 8.1) and a server is
// told not to send it (section 7.2). It never carries `preload`: that is a
// commitment about every subdomain of a registrable domain, made once by the
// owner of the domain, not by one service on it.
type HSTS struct {
	// MaxAge is how long a browser keeps the rule, in whole seconds. Zero
	// sends no header.
	MaxAge time.Duration

	// IncludeSubDomains extends the rule to every subdomain of the host.
	IncludeSubDomains bool

	// PublicURL is the address the service is published at. When it is
	// https, a request whose Host is that host is taken to have arrived over
	// https at a front that ends TLS, unless the front says it did not
	// ([HSTS.secure]). Empty or http leaves only a TLS connection and
	// [TrustHTTPS] to say so.
	PublicURL string
}

// Value is the header's value, empty when there is none to send.
func (h HSTS) Value() string {
	seconds := int64(h.MaxAge / time.Second)
	if seconds <= 0 {
		return ""
	}

	value := "max-age=" + strconv.FormatInt(seconds, 10)
	if h.IncludeSubDomains {
		value += "; includeSubDomains"
	}

	return value
}

type httpsKey struct{}

// TrustHTTPS marks a request as having reached the service over https, for a
// front the service itself trusts to say so: the Lambda adapter, whose
// payload comes from API Gateway and from nowhere else. Only code in this
// process can set it; nothing a client sends does.
func TrustHTTPS(ctx context.Context) context.Context {
	return context.WithValue(ctx, httpsKey{}, true)
}

func trustedHTTPS(ctx context.Context) bool {
	trusted, _ := ctx.Value(httpsKey{}).(bool)

	return trusted
}

// secure says whether r arrived over https, as this service sees it:
//
//   - the connection is TLS; or
//   - the front the process trusts said so ([TrustHTTPS]); or
//   - the service is published at an https address, the request is for that
//     host, and nothing in front of it said the hop was plain. A deployment
//     behind a gateway that ends TLS sees plain http on its own listener,
//     and that is why the cookie flag follows the public URL too.
//
// An X-Forwarded-Proto of the request is read in one direction only: `http`
// withholds the header, and no value grants it, so a client cannot make the
// service say something it would not have.
//
// A loopback host is never secure: a browser does not apply HSTS to it, and
// a developer's localhost must not be pinned to https by a service that was
// run there once.
func (h HSTS) secure(r *http.Request) bool {
	if loopbackHost(r.Host) {
		return false
	}

	if r.TLS != nil || trustedHTTPS(r.Context()) {
		return true
	}

	public, err := url.Parse(h.PublicURL)
	if err != nil || public.Scheme != "https" || loopbackHost(public.Host) {
		return false
	}

	if strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "http") {
		return false
	}

	return strings.EqualFold(hostOnly(r.Host), hostOnly(public.Host))
}

// hostOnly is host without a port.
func hostOnly(host string) string {
	if name, _, err := net.SplitHostPort(host); err == nil {
		return name
	}

	return strings.Trim(host, "[]")
}

// loopbackHost reports whether host (with or without a port) names this
// machine: localhost, a name under .localhost, or a loopback address.
func loopbackHost(host string) bool {
	name := strings.TrimSuffix(strings.ToLower(hostOnly(host)), ".")
	if name == "localhost" || strings.HasSuffix(name, ".localhost") {
		return true
	}

	ip := net.ParseIP(name)

	return ip != nil && ip.IsLoopback()
}

// Headers sets the default policy on every response, so that no page is
// served without it by having been forgotten. A handler that needs another
// policy sets it with [Apply]. It says nothing about HSTS: see [HeadersWith].
func Headers(next http.Handler) http.Handler {
	return HeadersWith(next, HSTS{})
}

// HeadersWith is [Headers] that also sends Strict-Transport-Security under
// hsts to a request that arrived over https. A header already on the
// response when the request reaches this middleware is left as it is.
func HeadersWith(next http.Handler, hsts HSTS) http.Handler {
	return HeadersFor(next, hsts, Policy{})
}

// HeadersFor is [HeadersWith] under base as the default policy instead of
// the zero [Policy].
func HeadersFor(next http.Handler, hsts HSTS, base Policy) http.Handler {
	value := hsts.Value()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Apply(w, base)

		if value != "" && w.Header().Get("Strict-Transport-Security") == "" && hsts.secure(r) {
			w.Header().Set("Strict-Transport-Security", value)
		}

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
