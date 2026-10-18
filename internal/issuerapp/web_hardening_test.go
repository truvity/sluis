package issuerapp_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/httpsec"
)

// What a Lambda serves is this handler and nothing of ours in front of it,
// so the cap on a request body and the security headers are the handler's
// own.
func TestTheIssuerHandlerCapsRequestBodiesAndSetsSecurityHeaders(t *testing.T) {
	app := bootWith(t, stubHub(t, true, false))
	handler := app.Handler()

	t.Run("a declared length over the cap is refused", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "/token", strings.NewReader(strings.Repeat("x", httpsec.MaxRequestBody+1)))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("POST /token with an oversize body = %d, want 413", recorder.Code)
		}
	})

	t.Run("a body of unknown length is cut off", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "/token", strings.NewReader(
			"grant_type=client_credentials&x="+strings.Repeat("x", httpsec.MaxRequestBody+1)))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.ContentLength = -1
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)

		if recorder.Code < http.StatusBadRequest || recorder.Code >= http.StatusInternalServerError {
			t.Errorf("POST /token with an oversize chunked body = %d, want a 4xx refusal", recorder.Code)
		}
	})

	t.Run("a small body is read", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "/token", strings.NewReader("grant_type=nonsense"))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)

		if recorder.Code == http.StatusRequestEntityTooLarge {
			t.Errorf("a small body was refused as too large")
		}
	})

	t.Run("pages carry the headers", func(t *testing.T) {
		for _, path := range []string{"/login", "/signed-out", "/.well-known/openid-configuration"} {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))

			policy := recorder.Header().Get("Content-Security-Policy")
			if !strings.Contains(policy, "frame-ancestors 'none'") || recorder.Header().Get("X-Frame-Options") != "DENY" ||
				recorder.Header().Get("Referrer-Policy") != "no-referrer" {
				t.Errorf("GET %s (%d): policy %q, headers %v", path, recorder.Code, policy, recorder.Header())
			}
		}
	})
}
