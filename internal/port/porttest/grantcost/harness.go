// Package grantcost measures what one token grant costs the storage ports --
// the State and Index calls, the directory snapshot reads, and the times the
// hub is asked about a person -- through the real issuer behind its real HTTP
// surface, over whichever adapter a test hands it.
//
// It is test-only, like internal/port/porttest: each State adapter's tests
// call [Run] with a [port.Set] of their own, so one budget holds the
// in-memory adapter and the DynamoDB one alike. The budget is the point. In
// an installation where MCP clients refresh every few minutes, every refresh
// is paid for in table writes and reads, and a change that adds one should
// fail here rather than show up on the bill.
//
// The assembly is the production one minus the network: the issuer's State
// over the ports (issuer.NewPortState), the hub over the same State for its
// workspaces (internal/portstore) and a Blob for its snapshots, reached
// in-process through internal/hublocal, exactly as issuerapp wires it.
package grantcost

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/truvity/sluis/backend"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/demo"
	"github.com/truvity/sluis/internal/hub"
	"github.com/truvity/sluis/internal/hublocal"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/internal/portstore"
	"github.com/truvity/sluis/policy"
)

// Env is one adapter under measurement.
type Env struct {
	// Set holds the adapter's State and Index. A nil Blob is an in-memory
	// one, as S3 stands beside DynamoDB in a deployment; a nil Secrets is
	// fine, since nothing here keeps a credential.
	Set port.Set
	// Advance moves the State's clock past a lifetime without sleeping.
	Advance func(time.Duration)
	// Calls, when set, is the engine's own count of each request so far
	// (DynamoDB: PutItem, GetItem, Query, ...). It is what is billed, and
	// a step's share of it is reported and held to the same budget.
	Calls func() map[string]int
}

// The person every grant here is for, the client it signs in to, and the
// directory that vouches for them.
const (
	Person    = "ada@north.example"
	Group     = "engineering@north.example"
	Domain    = "north.example"
	Workspace = "ws-north"
	ClientID  = "local-dev"
	Redirect  = "http://localhost:8000/callback"
	verifier  = "a-verifier-long-enough-to-be-a-real-one-0123456789"
)

// Harness is the issuer over one [Env], with a browser and the counters.
type Harness struct {
	t         *testing.T
	env       Env
	Server    *httptest.Server
	Issuer    *issuer.Issuer
	Counter   *Counter
	Directory issuer.Directory
	snapshots *hub.BlobSnapshots
	// storage, set and hub are what the console in front of this issuer
	// is assembled from ([Harness.Console]): the issuer's own storage, the
	// policy in force and the counted hub.
	storage *issuer.Storage
	set     *policy.Set
	hub     Resolver
	console http.Handler
	// accounts is the issuer's session service, which the console calls
	// at this origin by the browser's cookie.
	accounts *issuer.SessionsService
	cookies  map[string]string
	// skew is how far [Harness.Advance] has moved the clocks, which the
	// issuer's session index reads as well as the State.
	skew atomic.Int64
}

// New assembles the issuer over env with a fresh snapshot of the directory
// in place, and counts nothing of the assembly.
func New(t *testing.T, env Env) *Harness {
	t.Helper()
	ctx := context.Background()

	counter := &Counter{}
	blob := env.Set.Blob
	if blob == nil {
		blob = memory.New().Set().Blob
	}
	counted := port.Set{
		State:   counter.State(env.Set.State),
		Index:   counter.Index(env.Set.Index),
		Blob:    counter.Blob(blob),
		Secrets: env.Set.Secrets,
	}

	// The directory: one workspace serving the domain, healthy, and a
	// snapshot of it in the blob port, as the hub's refresher leaves them.
	workspaces := portstore.NewWorkspaces(portstore.New(counted))
	if err := workspaces.Put(ctx, hub.Workspace{
		ID: Workspace, Backend: "google", Domains: []string{Domain},
		Health: hub.Health{OK: true, ProbedAt: time.Now()},
	}); err != nil {
		t.Fatalf("store the workspace: %v", err)
	}
	snapshots := hub.NewBlobSnapshots(counted.Blob, counted.State)
	directory := hub.New(workspaces, snapshots, hub.Config{}, slog.New(slog.DiscardHandler))

	declared, err := policy.Parse([]byte(demo.Policy))
	if err != nil {
		t.Fatalf("parse the policy: %v", err)
	}
	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatalf("policy set: %v", err)
	}

	asked := counter.Directory(directory)
	dir := hublocal.New(asked, 0)
	state := issuer.NewPortState(counted.State, counted.Index)
	iss := issuer.New(issuer.Config{URL: "http://issuer.example", AllowInsecure: true}, set, dir, state)
	storage, err := issuer.NewStorage(iss, issuer.Verifiers{}, nil, nil, nil, state)
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	handler, err := issuer.HandlerWithSignIn(iss, storage, issuer.SignInDeps{
		Providers: []issuer.SignIn{provider{}},
		State:     access.NewStateCodec([]byte("a-test-key-for-signing-state"), 0),
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	h := &Harness{
		t: t, env: env, Server: server, Issuer: iss, Counter: counter, Directory: dir,
		snapshots: snapshots, cookies: map[string]string{},
		storage: storage, set: set, hub: asked,
	}
	// The session index decides the grace window of a spent refresh token
	// by its own clock, so time that passes for the State passes for it
	// too.
	iss.Sessions().SetClock(func() time.Time { return time.Now().Add(time.Duration(h.skew.Load())) })
	h.Snapshot(time.Now())
	counter.Reset()
	return h
}

// Snapshot replaces the directory's snapshot with one taken at takenAt: the
// person is a live account and a member of [Group].
func (h *Harness) Snapshot(takenAt time.Time) {
	h.t.Helper()
	snap := hub.NewSnapshot(Workspace, takenAt,
		[]backend.Account{{Email: Person, Live: true, GivenName: "Ada", FamilyName: "Lovelace"}},
		[]backend.Group{{Email: Group, Members: []string{Person}}}, nil)
	if err := h.snapshots.Put(context.Background(), snap); err != nil {
		h.t.Fatalf("store the snapshot: %v", err)
	}
}

// Advance moves the State's clock, and the issuer's session index's with it.
func (h *Harness) Advance(d time.Duration) {
	h.t.Helper()
	if h.env.Advance == nil {
		h.t.Fatal("this Env cannot advance its clock")
	}
	h.env.Advance(d)
	h.skew.Add(int64(d))
}

// now is the clock the issuer's session index reads: the real one, moved by
// every [Harness.Advance].
func (h *Harness) now() time.Time { return time.Now().Add(time.Duration(h.skew.Load())) }

// Measure runs step and returns what it cost.
func (h *Harness) Measure(step func()) Counts {
	var before map[string]int
	if h.env.Calls != nil {
		before = h.env.Calls()
	}
	h.Counter.Reset()
	step()
	out := countsOf(h.Counter.Ops())
	if h.env.Calls != nil {
		out.Engine = map[string]int{}
		for name, n := range h.env.Calls() {
			if d := n - before[name]; d != 0 {
				out.Engine[name] = d
			}
		}
	}
	return out
}

// provider stands in for the corporate directory's sign-in: it vouches for
// [Person] whatever it is shown. What the directory says ABOUT them is the
// hub's to answer, which is the part under measurement.
type provider struct{}

func (provider) Kind() string { return "google" }

func (provider) URL(state string) (string, error) {
	return "https://idp.example/authorize?state=" + url.QueryEscape(state), nil
}

func (provider) Identify(context.Context, string) (string, error) { return Person, nil }

// do is one browser request: cookies kept, redirects not followed.
func (h *Harness) do(method, path string) (status int, location string) {
	h.t.Helper()
	request, err := http.NewRequestWithContext(h.t.Context(), method, h.Server.URL+path, nil)
	if err != nil {
		h.t.Fatalf("build the request: %v", err)
	}
	for name, value := range h.cookies {
		request.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	response, err := client.Do(request)
	if err != nil {
		h.t.Fatalf("request %s: %v", path, err)
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, response.Body)
	for _, cookie := range response.Cookies() {
		if cookie.MaxAge < 0 {
			delete(h.cookies, cookie.Name)
			continue
		}
		h.cookies[cookie.Name] = cookie.Value
	}
	return response.StatusCode, response.Header.Get("Location")
}

// SignIn walks a browser through /authorize and the provider round trip and
// returns the authorization code the client is sent back with, or "" when
// the sign-in did not end in one.
func (h *Harness) SignIn() string {
	h.t.Helper()
	sum := sha256.Sum256([]byte(verifier))
	query := url.Values{
		"client_id":             {ClientID},
		"redirect_uri":          {Redirect},
		"response_type":         {"code"},
		"scope":                 {"openid profile email offline_access"},
		"state":                 {"grant-cost"},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
	}
	where := "/authorize?" + query.Encode()
	for range 12 {
		if strings.HasPrefix(where, "https://idp.example/") {
			state := where[strings.Index(where, "state=")+len("state="):]
			where = "/login/google/callback?code=x&state=" + state
		}
		if !strings.HasPrefix(where, "/") {
			break
		}
		status, next := h.do(http.MethodGet, where)
		if status != http.StatusFound && status != http.StatusSeeOther {
			return ""
		}
		where = next
	}
	back, err := url.Parse(where)
	if err != nil || !strings.HasPrefix(where, Redirect) {
		return ""
	}
	return back.Query().Get("code")
}

// Tokens is a token endpoint's answer.
type Tokens struct {
	Status  int
	Access  string `json:"access_token"`
	Refresh string `json:"refresh_token"`
	ID      string `json:"id_token"`
	Error   string `json:"error"`
}

func (h *Harness) post(path string, form url.Values) Tokens {
	h.t.Helper()
	request, err := http.NewRequestWithContext(h.t.Context(), http.MethodPost,
		h.Server.URL+path, strings.NewReader(form.Encode()))
	if err != nil {
		h.t.Fatalf("build the request: %v", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := h.Server.Client().Do(request)
	if err != nil {
		h.t.Fatalf("post %s: %v", path, err)
	}
	defer func() { _ = response.Body.Close() }()
	out := Tokens{Status: response.StatusCode}
	raw, _ := io.ReadAll(response.Body)
	_ = json.Unmarshal(raw, &out)
	return out
}

// Redeem is the authorization_code grant.
func (h *Harness) Redeem(code string) Tokens {
	h.t.Helper()
	return h.post("/token", url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {Redirect},
		"client_id":     {ClientID},
		"code_verifier": {verifier},
	})
}

// Refresh is the refresh_token grant.
func (h *Harness) Refresh(token string) Tokens {
	h.t.Helper()
	return h.post("/token", url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {token},
		"client_id":     {ClientID},
	})
}

// Revoke is RFC 7009 revocation of a refresh token, by the public client.
func (h *Harness) Revoke(token string) int {
	h.t.Helper()
	return h.post("/revoke", url.Values{
		"token":           {token},
		"token_type_hint": {"refresh_token"},
		"client_id":       {ClientID},
	}).Status
}

// Grant is a sign-in and its redemption: the tokens a client starts with.
func (h *Harness) Grant() Tokens {
	h.t.Helper()
	code := h.SignIn()
	if code == "" {
		h.t.Fatal("the sign-in ended in no authorization code")
	}
	tokens := h.Redeem(code)
	if tokens.Status != http.StatusOK || tokens.Refresh == "" {
		h.t.Fatalf("redeem: %d %q, want 200 and a refresh token", tokens.Status, tokens.Error)
	}
	return tokens
}
