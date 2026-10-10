package openbao_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/truvity/sluis/storage/openbao"
)

type fakeServer struct {
	*httptest.Server
	logins   atomic.Int32
	requests atomic.Int32
	lastNS   atomic.Value
	jwts     chan string
	// revoke makes the next data request answer 403 once.
	revoke atomic.Bool
}

func newFake(t *testing.T) *fakeServer {
	f := &fakeServer{jwts: make(chan string, 10)}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/auth/jwt-x/login", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in["role"] != "r1" {
			http.Error(w, `{"errors":["role not found"]}`, http.StatusBadRequest)
			return
		}
		f.jwts <- in["jwt"]
		n := f.logins.Add(1)
		_, _ = w.Write([]byte(`{"auth":{"client_token":"tok-` + string(rune('0'+n)) + `","lease_duration":3600}}`))
	})
	mux.HandleFunc("/v1/secret/ok", func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		f.lastNS.Store(r.Header.Get("X-Vault-Namespace"))
		if f.revoke.CompareAndSwap(true, false) {
			http.Error(w, `{"errors":["permission denied"]}`, http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"token":"` + r.Header.Get("X-Vault-Token") + `"},"warnings":["w"]}`))
	})
	mux.HandleFunc("/v1/secret/denied", func(w http.ResponseWriter, _ *http.Request) {
		f.requests.Add(1)
		http.Error(w, `{"errors":["permission denied"]}`, http.StatusForbidden)
	})
	mux.HandleFunc("/v1/secret/redirect", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1/elsewhere", http.StatusTemporaryRedirect)
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func tokenFile(t *testing.T, jwt string) string {
	p := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(p, []byte(jwt+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func client(t *testing.T, f *fakeServer, file string, now func() time.Time) *openbao.Client {
	c, err := openbao.New(openbao.Config{
		Address: f.URL, AllowInsecureHTTP: true, Namespace: "ns1", Now: now,
		Login: &openbao.Login{Mount: "jwt-x", Role: "r1", TokenFile: file},
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestLoginOnceAndNamespace(t *testing.T) {
	f := newFake(t)
	c := client(t, f, tokenFile(t, "jwt-1"), nil)
	for range 3 {
		resp, err := c.Request(t.Context(), "GET", "secret/ok", nil)
		if err != nil || !strings.Contains(string(resp.Data), `"tok-1"`) || len(resp.Warnings) != 1 {
			t.Fatalf("%+v, %v", resp, err)
		}
	}
	if n := f.logins.Load(); n != 1 {
		t.Fatalf("%d logins for 3 requests, want 1", n)
	}
	if got := <-f.jwts; got != "jwt-1" {
		t.Fatalf("logged in with %q", got)
	}
	if ns := f.lastNS.Load(); ns != "ns1" {
		t.Fatalf("namespace header %v", ns)
	}
}

// The token file is read at every login: the kubelet replaces a projected
// token before it expires.
func TestTokenFileIsReReadAtEachLogin(t *testing.T) {
	f := newFake(t)
	file := tokenFile(t, "jwt-1")
	now := time.Now()
	c := client(t, f, file, func() time.Time { return now })
	if _, err := c.Request(t.Context(), "GET", "secret/ok", nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("jwt-2"), 0o600); err != nil {
		t.Fatal(err)
	}
	now = now.Add(59 * time.Minute) // past 80% of the 1h lease
	if _, err := c.Request(t.Context(), "GET", "secret/ok", nil); err != nil {
		t.Fatal(err)
	}
	if a, b := <-f.jwts, <-f.jwts; a != "jwt-1" || b != "jwt-2" {
		t.Fatalf("logins used %q then %q", a, b)
	}
}

func TestRevokedTokenLogsInAgainOnce(t *testing.T) {
	f := newFake(t)
	now := time.Now()
	c := client(t, f, tokenFile(t, "j"), func() time.Time { return now })
	if _, err := c.Request(t.Context(), "GET", "secret/ok", nil); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute) // the token is not new: a 403 may be a revoked token
	f.revoke.Store(true)
	resp, err := c.Request(t.Context(), "GET", "secret/ok", nil)
	if err != nil || !strings.Contains(string(resp.Data), `"tok-2"`) {
		t.Fatalf("%+v, %v", resp, err)
	}
	// A policy that denies is the policy's: one more login, then the refusal.
	now = now.Add(time.Minute)
	before := f.logins.Load()
	_, err = c.Request(t.Context(), "GET", "secret/denied", nil)
	if openbao.Status(err) != 403 || f.logins.Load() != before+1 {
		t.Fatalf("%v after %d more logins", err, f.logins.Load()-before)
	}
}

func TestNewTokenDoesNotRelogin(t *testing.T) {
	f := newFake(t)
	c := client(t, f, tokenFile(t, "j"), nil)
	_, err := c.Request(t.Context(), "GET", "secret/denied", nil)
	if openbao.Status(err) != 403 || f.logins.Load() != 1 || f.requests.Load() != 1 {
		t.Fatalf("%v: %d logins, %d requests; want 1 and 1", err, f.logins.Load(), f.requests.Load())
	}
}

func TestRedirectIsAnError(t *testing.T) {
	f := newFake(t)
	c := client(t, f, tokenFile(t, "j"), nil)
	_, err := c.Request(t.Context(), "GET", "secret/redirect", nil)
	if openbao.Status(err) != http.StatusTemporaryRedirect {
		t.Fatalf("%v, want the 307 itself", err)
	}
}

func TestLoginFailureNamesTheRole(t *testing.T) {
	f := newFake(t)
	c, err := openbao.New(openbao.Config{Address: f.URL, AllowInsecureHTTP: true,
		Login: &openbao.Login{Mount: "jwt-x", Role: "nope", TokenFile: tokenFile(t, "j")}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Request(t.Context(), "GET", "secret/ok", nil)
	if err == nil || !strings.Contains(err.Error(), `role "nope"`) || openbao.Status(err) != 400 {
		t.Fatalf("%v", err)
	}
	c2, _ := openbao.New(openbao.Config{Address: f.URL, AllowInsecureHTTP: true,
		Login: &openbao.Login{Mount: "jwt-x", Role: "r1", TokenFile: filepath.Join(t.TempDir(), "missing")}})
	if _, err = c2.Request(t.Context(), "GET", "secret/ok", nil); err == nil || errors.Is(err, os.ErrNotExist) == false {
		t.Fatalf("a missing token file: %v", err)
	}
}

func TestNewValidates(t *testing.T) {
	login := &openbao.Login{Role: "r", TokenFile: "/x"}
	for name, cfg := range map[string]openbao.Config{
		"http without the opt-in": {Address: "http://bao.example", Login: login},
		"a path":                  {Address: "https://bao.example/v1", Login: login},
		"credentials in the URL":  {Address: "https://u:p@bao.example", Login: login},
		"no address":              {Login: login},
		"no way to log in":        {Address: "https://bao.example"},
		"two ways":                {Address: "https://bao.example", Login: login, Token: "t"},
		"no role":                 {Address: "https://bao.example", Login: &openbao.Login{TokenFile: "/x"}},
		"no token file":           {Address: "https://bao.example", Login: &openbao.Login{Role: "r"}},
	} {
		if _, err := openbao.New(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := openbao.New(openbao.Config{Address: "https://bao.example", Login: login}); err != nil {
		t.Fatalf("a valid configuration: %v", err)
	}
}
