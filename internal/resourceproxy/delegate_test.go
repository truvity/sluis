package resourceproxy

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// delegationIssuer is a token endpoint that records what each delegated
// exchange presented, and answers with a numbered token or a refusal.
type delegationIssuer struct {
	*httptest.Server
	mu       sync.Mutex
	forms    []map[string]string
	lifetime int
	status   int // 0 answers 200
	code     string
}

func newDelegationIssuer(t *testing.T, lifetime int) *delegationIssuer {
	t.Helper()
	f := &delegationIssuer{lifetime: lifetime}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		defer f.mu.Unlock()
		form := map[string]string{}
		for k := range r.PostForm {
			form[k] = r.PostForm.Get(k)
		}
		f.forms = append(f.forms, form)
		if f.status != 0 {
			w.WriteHeader(f.status)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": f.code})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "delegated-" + string(rune('0'+len(f.forms))), "expires_in": f.lifetime, "token_type": "Bearer",
		})
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *delegationIssuer) calls() []map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]string(nil), f.forms...)
}

func (f *delegationIssuer) refuse(status int, code string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status, f.code = status, code
}

func delegateCfg(t *testing.T, issuer, target, upstream, iss string) Config {
	t.Helper()
	sa := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(sa, []byte("sa-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		Listen: ":0", Upstream: upstream, IssuerURL: iss, ResourceURL: resURL, Scope: "openid",
		OutboundListen: "127.0.0.1:0", OutboundTarget: target, OutboundTokenEndpoint: issuer + "/token",
		OutboundClientID: "observability-mcp", OutboundAudience: "store", OutboundSATokenFile: sa,
		OutboundMode: ModeDelegate,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// The whole chain: the caller's request reaches the stock server with a
// handle and no credential, the stock server's backend call carries the
// handle back to the outbound side, and the backend sees a token exchanged
// for the caller with this workload as the actor. The caller's token goes to
// the issuer as the subject and nowhere else; a second call is served from
// the cache, and a handle replayed after its request is refused.
func TestDelegateModeCallsTheBackendAsTheCaller(t *testing.T) {
	t.Parallel()
	tokenEP := newDelegationIssuer(t, 600)
	var backendAuth []string
	var mu sync.Mutex
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		backendAuth = append(backendAuth, r.Header.Get("Authorization")+"|"+r.Header.Get(DefaultDelegationHeader))
		mu.Unlock()
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(backend.Close)

	var outboundURL string
	var stockSaw []string
	var lastHandle string
	stock := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handle := r.Header.Get(DefaultDelegationHeader)
		mu.Lock()
		stockSaw = append(stockSaw, r.Header.Get("Authorization"))
		lastHandle = handle
		mu.Unlock()
		// MCP_PASSTHROUGH_HEADERS: the handle goes on the backend call.
		req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, outboundURL+"/api/v1/query", nil)
		req.Header.Set(DefaultDelegationHeader, handle)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		w.WriteHeader(resp.StatusCode)
	})
	r := newRig(t, stock, "/mcp")
	up := httptest.NewServer(stock)
	t.Cleanup(up.Close)

	logs := &syncBuffer{}
	log := slog.New(slog.NewJSONHandler(logs, nil))
	cfg := delegateCfg(t, tokenEP.URL, backend.URL, up.URL+"/mcp", r.iss.URL)
	delegator, err := NewDelegator(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	out, err := NewOutbound(cfg, log, nil)
	if err != nil {
		t.Fatal(err)
	}
	handles := NewHandles()
	out.ActFor(handles, cfg.DelegationHeader, delegator)
	outSrv := httptest.NewServer(out.Handler())
	t.Cleanup(outSrv.Close)
	outboundURL = outSrv.URL
	in, err := NewInbound(cfg, log, delegator.Ready)
	if err != nil {
		t.Fatal(err)
	}
	in.Delegate(handles)
	inSrv := httptest.NewServer(in.Handler())
	t.Cleanup(inSrv.Close)

	caller := r.token(t)
	for range 2 {
		req, _ := http.NewRequest(http.MethodPost, inSrv.URL+"/metrics", strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+caller)
		req.Header.Set(DefaultDelegationHeader, "a-handle-the-caller-chose")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d", resp.StatusCode)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	for _, auth := range stockSaw {
		if auth != "" {
			t.Errorf("the stock server saw Authorization %q", auth)
		}
	}
	if lastHandle == "" || lastHandle == "a-handle-the-caller-chose" || strings.Contains(lastHandle, caller) {
		t.Errorf("the stock server saw handle %q, want one the proxy made", lastHandle)
	}
	for _, got := range backendAuth {
		if got != "Bearer delegated-1|" {
			t.Errorf("the backend saw %q, want the delegated token and no handle", got)
		}
	}
	calls := tokenEP.calls()
	if len(calls) != 1 {
		t.Fatalf("%d exchanges, want one and then the cache", len(calls))
	}
	want := map[string]string{
		"subject_token": caller, "subject_token_type": "urn:ietf:params:oauth:token-type:access_token",
		"actor_token": "sa-token", "actor_token_type": "urn:ietf:params:oauth:token-type:jwt", "audience": "store",
	}
	for k, v := range want {
		if calls[0][k] != v {
			t.Errorf("exchange %s = %q, want %q", k, calls[0][k], v)
		}
	}

	// The request is over: its handle opens nothing.
	req, _ := http.NewRequest(http.MethodGet, outSrv.URL+"/api/v1/query", nil)
	req.Header.Set(DefaultDelegationHeader, lastHandle)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("a replayed handle = %d, want 403", resp.StatusCode)
	}
	if strings.Contains(logs.String(), caller) {
		t.Error("a log line carries the caller's token")
	}
}

// The cache: one exchange per caller token, again inside the refresh margin,
// and at most size callers, the soonest to expire dropped first.
func TestTheDelegatorCachesPerCallerUntilTheMarginAndIsBounded(t *testing.T) {
	t.Parallel()
	tokenEP := newDelegationIssuer(t, 600)
	cfg := delegateCfg(t, tokenEP.URL, "http://127.0.0.1:1", "http://127.0.0.1:1", "https://i.example")
	cfg.DelegationCacheSize = 2
	d, err := NewDelegator(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now()
	d.now = func() time.Time { return clock }

	first, cached, err := d.Token(t.Context(), "caller-a")
	if err != nil || cached || first != "delegated-1" {
		t.Fatalf("first = %q %v %v", first, cached, err)
	}
	if again, cached, _ := d.Token(t.Context(), "caller-a"); again != first || !cached {
		t.Errorf("not cached: %q %v", again, cached)
	}
	if other, _, _ := d.Token(t.Context(), "caller-b"); other != "delegated-2" {
		t.Errorf("another caller = %q, want its own exchange", other)
	}

	clock = clock.Add(9*time.Minute + 30*time.Second) // inside the one-minute margin
	if next, cached, _ := d.Token(t.Context(), "caller-a"); next != "delegated-3" || cached {
		t.Errorf("inside the margin = %q %v, want a fresh exchange", next, cached)
	}
	if _, _, err := d.Token(t.Context(), "caller-c"); err != nil {
		t.Fatal(err)
	}
	if n := d.cached(); n != 2 {
		t.Errorf("cached %d, want the bound of 2", n)
	}
	// caller-b expired soonest, so it was the one dropped.
	if _, cached, _ := d.Token(t.Context(), "caller-b"); cached {
		t.Error("caller-b was still cached past the bound")
	}
}

// A refusal by the issuer is the caller's, 403; an issuer that fails is 502;
// a backend call with no handle is 403. None reaches the backend, and each
// is an audit line that names why.
func TestADelegationRefusalIsForbiddenAndAudited(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		status       int
		code         string
		handle       bool
		want         int
		reasonLogged string
	}{
		"refused by the issuer": {http.StatusBadRequest, "invalid_target", true, http.StatusForbidden, "invalid_target"},
		"the issuer failing":    {http.StatusServiceUnavailable, "temporarily_unavailable", true, http.StatusBadGateway, "exchange_failed"},
		"no handle":             {0, "", false, http.StatusForbidden, "no_caller"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tokenEP := newDelegationIssuer(t, 600)
			if tc.status != 0 {
				tokenEP.refuse(tc.status, tc.code)
			}
			called := false
			backend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
			t.Cleanup(backend.Close)
			cfg := delegateCfg(t, tokenEP.URL, backend.URL, "http://127.0.0.1:1", "https://i.example")
			logs := &syncBuffer{}
			d, _ := NewDelegator(cfg, nil)
			out, _ := NewOutbound(cfg, slog.New(slog.NewJSONHandler(logs, nil)), nil)
			handles := NewHandles()
			out.ActFor(handles, cfg.DelegationHeader, d)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/query", nil)
			if tc.handle {
				handle, done := handles.Open("caller-token", "ada@example.com")
				defer done()
				req.Header.Set(cfg.DelegationHeader, handle)
			}
			rec := httptest.NewRecorder()
			out.Handler().ServeHTTP(rec, req)
			if rec.Code != tc.want || called {
				t.Errorf("status %d, backend called %v, want %d and no call", rec.Code, called, tc.want)
			}
			var line map[string]any
			_ = json.Unmarshal([]byte(strings.TrimSpace(logs.String())), &line)
			if line["msg"] != "mcp_delegation" || line["outcome"] != "refused" || line["reason"] != tc.reasonLogged {
				t.Errorf("audit line %v, want a refusal for %s", line, tc.reasonLogged)
			}
			if tc.handle && line["sub"] != "ada@example.com" {
				t.Errorf("audit line sub = %v, want the caller", line["sub"])
			}
		})
	}
}

func TestDelegateModeIsCheckedAtStart(t *testing.T) {
	t.Parallel()
	base := Config{Listen: ":0", Upstream: "http://127.0.0.1:1", IssuerURL: "https://i.example", ResourceURL: resURL}
	for name, mutate := range map[string]func(*Config){
		"delegate without the outbound side": func(c *Config) { c.OutboundMode = ModeDelegate },
		"an unknown mode":                    func(c *Config) { c.OutboundMode = "impersonate" },
		"a credential header as the handle":  func(c *Config) { c.DelegationHeader = "authorization" },
		"not a header name":                  func(c *Config) { c.DelegationHeader = "x handle" },
	} {
		cfg := base
		mutate(&cfg)
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
