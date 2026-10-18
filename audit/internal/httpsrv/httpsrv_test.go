package httpsrv_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/truvity/sluis/audit/internal/httpsrv"
)

func TestServerHoldsItsLimits(t *testing.T) {
	t.Parallel()

	server := httpsrv.New(":0", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			http.Error(w, "read failed", http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	if server.ReadHeaderTimeout == 0 || server.ReadTimeout == 0 || server.WriteTimeout == 0 ||
		server.IdleTimeout == 0 || server.MaxHeaderBytes == 0 || server.MaxHeaderBytes >= http.DefaultMaxHeaderBytes {
		t.Fatalf("a limit is unset: %+v", server)
	}

	over := strings.NewReader(strings.Repeat("x", httpsrv.MaxRequestBody+1))

	// Declared over the limit: refused before the handler runs.
	declared := httptest.NewRecorder()
	server.Handler.ServeHTTP(declared, httptest.NewRequest(http.MethodPost, "/", over))
	if declared.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("a body declared over the limit was answered %d, want 413", declared.Code)
	}

	// Of unknown length: cut off while it is read.
	chunked := httptest.NewRequest(http.MethodPost, "/", io.NopCloser(strings.NewReader(strings.Repeat("x", httpsrv.MaxRequestBody+1))))
	chunked.ContentLength = -1
	cut := httptest.NewRecorder()
	server.Handler.ServeHTTP(cut, chunked)
	if cut.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("a body of unknown length over the limit was answered %d, want 413", cut.Code)
	}

	// Within it: served.
	ok := httptest.NewRecorder()
	server.Handler.ServeHTTP(ok, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("small")))
	if ok.Code != http.StatusNoContent {
		t.Fatalf("a small body was answered %d, want 204", ok.Code)
	}
}
