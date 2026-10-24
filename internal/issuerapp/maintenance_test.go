package issuerapp_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/issuerapp"
	"github.com/truvity/sluis/internal/maintenance"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/internal/store"
)

// issuerOverState is an issuer whose own table is state.
func issuerOverState(t *testing.T, state port.State) http.Handler {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "policy.yaml"), []byte(`
version: 1
groups:
  platform: { members: [platform@north.example] }
lifetimes: { default: 12h }
clients:
  console: { kind: public, requires: [platform], redirects: ["https://console.example/callback"] }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := issuerapp.FromConfig(withPolicy(t, &config.Serve{
		IssuerURL: "https://issuer.example", Policy: &config.PolicyRef{File: filepath.Join(dir, "policy.yaml")},
		Listen: &config.Address{Address: ":0"}, Probes: &config.Address{Address: ":0"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	set := memory.New().Set()
	set.State = state
	app, err := issuerapp.New(context.Background(), cfg, issuerapp.Deps{
		Directory: nobody{},
		Stores:    &store.Stores{Adapter: store.AdapterMemory, Usable: true, Ports: set},
	}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	return app.Handler()
}

func status(h http.Handler, method, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(method, path, strings.NewReader(""))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(w, r)
	return w
}

// While the oidc table carries the flag, the issuer signs nobody in and issues
// nothing, and answers what a relying party needs to verify what it holds.
func TestTheIssuerRefusesWritesUnderMaintenanceAndKeepsDiscoveryAndKeys(t *testing.T) {
	state := memory.New()
	if err := maintenance.Write(t.Context(), state, maintenance.Flag{State: maintenance.StateRestoring, By: "restore"}); err != nil {
		t.Fatal(err)
	}
	h := issuerOverState(t, state)

	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/authorize?client_id=console"},
		{http.MethodPost, "/oauth/token"},
		{http.MethodPost, "/token"},
		{http.MethodPost, "/revoke"},
		{http.MethodGet, "/end_session"},
		{http.MethodGet, "/login"},
		{http.MethodGet, "/login/google/start"},
		{http.MethodPost, "/login/recovery"},
		{http.MethodPost, "/logout"},
		{http.MethodPost, "/.access/client-secrets/console"},
		{http.MethodPost, "/device_authorization"},
	} {
		w := status(h, c.method, c.path)
		if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") == "" {
			t.Errorf("%s %s under maintenance = %d (Retry-After %q), want 503 with Retry-After", c.method, c.path, w.Code, w.Header().Get("Retry-After"))
		}
	}
	for _, path := range []string{"/.well-known/openid-configuration", "/keys"} {
		if w := status(h, http.MethodGet, path); w.Code != http.StatusOK {
			t.Errorf("GET %s under maintenance = %d, want 200: relying parties keep verifying", path, w.Code)
		}
	}
}

// And serves again once the flag is gone.
func TestTheIssuerServesAgainWhenTheFlagIsCleared(t *testing.T) {
	state := memory.New()
	h := issuerOverState(t, state)
	if w := status(h, http.MethodPost, "/oauth/token"); w.Code == http.StatusServiceUnavailable {
		t.Fatalf("no flag, and the token endpoint refused with 503")
	}
	if w := status(h, http.MethodGet, "/login"); w.Code == http.StatusServiceUnavailable {
		t.Fatalf("no flag, and the sign-in page refused with 503")
	}
}

// A migration freeze refuses every write the same way, and still serves
// discovery, the key set, userinfo and the two token grants that change nothing.
func TestAMigrationFreezeServesReadsAndTheReadOnlyGrants(t *testing.T) {
	state := memory.New()
	if err := maintenance.Write(t.Context(), state, maintenance.Flag{State: maintenance.StateMigration, By: "operator"}); err != nil {
		t.Fatal(err)
	}
	h := issuerOverState(t, state)
	post := func(path, form string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		h.ServeHTTP(w, r)
		return w
	}
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/authorize?client_id=console"},
		{http.MethodPost, "/revoke"},
		{http.MethodGet, "/end_session"},
		{http.MethodGet, "/login"},
		{http.MethodPost, "/login/recovery"},
		{http.MethodPost, "/logout"},
		{http.MethodPost, "/.access/client-secrets/console"},
		{http.MethodPost, "/device_authorization"},
	} {
		w := status(h, c.method, c.path)
		if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") == "" {
			t.Errorf("%s %s frozen = %d (Retry-After %q), want 503 with Retry-After", c.method, c.path, w.Code, w.Header().Get("Retry-After"))
		}
	}
	for _, form := range []string{
		"grant_type=authorization_code&code=x",
		"grant_type=client_credentials",
		"grant_type=urn:ietf:params:oauth:grant-type:device_code&device_code=x",
		"",
	} {
		for _, path := range []string{"/token", "/oauth/token"} {
			if w := post(path, form); w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") == "" {
				t.Errorf("POST %s %q frozen = %d, want 503 with Retry-After", path, form, w.Code)
			}
		}
	}
	for _, form := range []string{
		"grant_type=urn:ietf:params:oauth:grant-type:token-exchange&subject_token=x",
		"grant_type=refresh_token&refresh_token=x&client_id=console",
	} {
		for _, path := range []string{"/token", "/oauth/token"} {
			if w := post(path, form); strings.Contains(w.Body.String(), `"error":"maintenance"`) {
				t.Errorf("POST %s %q was refused by the freeze: %s", path, form, w.Body.String())
			}
		}
	}
	for _, path := range []string{"/.well-known/openid-configuration", "/keys"} {
		if w := status(h, http.MethodGet, path); w.Code != http.StatusOK {
			t.Errorf("GET %s frozen = %d, want 200", path, w.Code)
		}
	}
}

// A restore's flag serves neither token grant: only a freeze does.
func TestARestoreFlagRefusesEveryTokenGrant(t *testing.T) {
	state := memory.New()
	if err := maintenance.Write(t.Context(), state, maintenance.Flag{State: maintenance.StateRestoring}); err != nil {
		t.Fatal(err)
	}
	h := issuerOverState(t, state)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader("grant_type=urn:ietf:params:oauth:grant-type:token-exchange"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("token exchange under a restore = %d, want 503", w.Code)
	}
}
