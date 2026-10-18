// Package httpsrv is the limits every HTTP listener of the audit services
// holds: how long a client has, how much it may send, and how large its
// headers may be. net/http applies none of them unless asked, so a client
// that opens a connection and sends a byte a second holds it for ever.
package httpsrv

import (
	"net/http"
	"time"
)

const (
	// MaxRequestBody is the most a request body may carry, in bytes. The
	// largest request is a sink batch (at most ten records) or a catalogue
	// registration; the same figure is the Connect handlers' message limit
	// ([wire.MaxRequestBytes]).
	MaxRequestBody = 4 << 20

	// MaxHeaderBytes is the most a request's header block may carry.
	MaxHeaderBytes = 64 << 10

	// ReadHeaderTimeout is how long a client has to send its headers.
	ReadHeaderTimeout = 10 * time.Second

	// ReadTimeout is how long a client has to send the whole request.
	ReadTimeout = 60 * time.Second

	// WriteTimeout is how long a handler has to answer, from the end of
	// the request headers.
	WriteTimeout = 5 * time.Minute

	// IdleTimeout is how long a kept-alive connection may sit unused.
	IdleTimeout = 2 * time.Minute
)

// New is an [http.Server] holding the limits above, with a body cap on its
// handler: a declared length over the cap is refused at once with 413, and
// a body that turns out larger than it said is cut off for the handler that
// reads it.
func New(addr string, handler http.Handler) *http.Server {
	limited := http.MaxBytesHandler(handler, MaxRequestBody)

	return &http.Server{
		Addr: addr,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > MaxRequestBody {
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				w.Header().Set("Connection", "close")
				w.WriteHeader(http.StatusRequestEntityTooLarge)
				_, _ = w.Write([]byte("request body too large\n"))

				return
			}

			limited.ServeHTTP(w, r)
		}),
		ReadHeaderTimeout: ReadHeaderTimeout,
		ReadTimeout:       ReadTimeout,
		WriteTimeout:      WriteTimeout,
		IdleTimeout:       IdleTimeout,
		MaxHeaderBytes:    MaxHeaderBytes,
	}
}
