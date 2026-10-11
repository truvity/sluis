//nolint:lll // fixtures and sentences of the isolation rows are one-line tables
package rosterapp_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/truvity/sluis/backend"
	"github.com/truvity/sluis/blobprefix"
	"github.com/truvity/sluis/internal/access"
	cfrpc "github.com/truvity/sluis/internal/cloudflare/rpc"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/deploycheck"
	"github.com/truvity/sluis/internal/githubapp"
	ghconsole "github.com/truvity/sluis/internal/githubapp/console"
	"github.com/truvity/sluis/internal/githubapp/githubfake"
	"github.com/truvity/sluis/internal/githubapp/githubtokens"
	githubroster "github.com/truvity/sluis/internal/githubroster/app"
	"github.com/truvity/sluis/internal/githubroster/catalogueapp"
	"github.com/truvity/sluis/internal/githubroster/consolewire"
	"github.com/truvity/sluis/internal/githubroster/link"
	googleconsole "github.com/truvity/sluis/internal/googleapp/console"
	"github.com/truvity/sluis/internal/hub"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/internal/lambdaapp"
	_ "github.com/truvity/sluis/internal/lambdaapp/cloudflarefn" // registers the Cloudflare module's function
	"github.com/truvity/sluis/internal/lambdaapp/githubfn"
	"github.com/truvity/sluis/internal/lambdaapp/googlefn"
	"github.com/truvity/sluis/internal/lambdaapp/lambdatest"
	"github.com/truvity/sluis/internal/lambdaapp/slackfn"
	"github.com/truvity/sluis/internal/modcall"
	"github.com/truvity/sluis/internal/modcall/lambdacall"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/internal/portstore"
	"github.com/truvity/sluis/internal/rails"
	"github.com/truvity/sluis/internal/rosterapp"
	slackconsole "github.com/truvity/sluis/internal/slackapp/console"
	"github.com/truvity/sluis/policy"
)

// The end-to-end remote mode of v1.76: the issuer runs with `github.remote`,
// `slack.remote`, `google.remote` and `cloudflare.remote` set, and every module
// call crosses the Lambda transport to the module's own function, each answering
// from stores of its own. No provider store is the issuer's.

const (
	e2eIssuerURL = "https://access.acme.example"
	e2eMount     = e2eIssuerURL + "/console"
	e2ePassword  = "correct horse battery staple"
	e2eOrg       = "acme-org"
	e2eLinkUser  = "platform@north.example"
)

func e2eFunction(module string) string { return "sluis-" + lambdatest.Instance + "-" + module }

// e2ePEM is an App's private key.
var e2ePEM = func() string {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}()

// e2eGoogle is the Google connector inside the module: it signs somebody in as
// the address the test wants, and counts how often it is asked.
type e2eGoogle struct {
	email      string
	identified atomic.Int32
}

func (*e2eGoogle) Kind() string { return "google" }

func (*e2eGoogle) AuthURL(state string) (string, error) {
	return "https://accounts.google.example/consent?state=" + url.QueryEscape(state), nil
}

func (*e2eGoogle) Exchange(context.Context, string, string) (hub.Workspace, backend.Backend, error) {
	return hub.Workspace{}, nil, errors.New("no consent in this test")
}

func (*e2eGoogle) SignInURL(state string) (string, error) {
	return "https://accounts.google.example/signin?state=" + url.QueryEscape(state), nil
}

func (g *e2eGoogle) Identify(context.Context, string) (string, error) {
	g.identified.Add(1)
	return g.email, nil
}

// e2eRig is the installation: four module functions on the emulator, and the issuer
// assembled over the Lambda transport to it.
type e2eRig struct {
	t       *testing.T
	h       *lambdatest.Harness
	cfg     rosterapp.Config
	app     *rosterapp.App
	handler http.Handler
	google  *e2eGoogle
	// modules are the sets of ports each module function keeps its records in.
	modules map[string]port.Set
	// cloudflareSSM is the Cloudflare function's secrets store, apart from the
	// issuer's: the issuer's reads are then its own.
	cloudflareSSM *lambdatest.SSM
}

func e2eWrite(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func newE2E(t *testing.T) *e2eRig {
	t.Helper()
	h := lambdatest.NewHarness(t)
	// The console's session key, which the harness's SSM cannot generate.
	h.SSM.Put("/sluis/"+lambdatest.Instance+"/internal/oidc/console-session-key",
		`{"value":"`+base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))+`"}`)
	r := &e2eRig{t: t, h: h, google: &e2eGoogle{email: e2eLinkUser}, modules: map[string]port.Set{},
		cloudflareSSM: lambdatest.NewSSM(t, nil)}
	dir := t.TempDir()
	policyFile := e2eWrite(t, dir, "policy.yaml", `apiVersion: sluis.truvity.github.io/policy/v2
groups:
  all:access-roster:operator: { members: [`+e2eLinkUser+`] }
  all:access-roster:viewer: {}
github: { `+e2eOrg+`: { members: [all:access-roster:operator] } }
apps:
  github:
    apps:
      - id: renovate
        purpose: catalogue
        org: `+e2eOrg+`
        permissions: { contents: read, metadata: read }
        grants:
          - { group: all:access-roster:operator, repositories: ["*"], permissions: { contents: read } }
lifetimes: { default: 12h }
clients:
  console: { kind: public, requires: [all:access-roster:operator], redirects: ["`+e2eMount+`/callback"] }
`)
	log := slog.New(slog.DiscardHandler)
	if os.Getenv("E2E_LOG") != "" {
		log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	}
	for module, fn := range map[string]*lambdaapp.Function{
		"github":     r.githubFunction(t, log, policyFile),
		"slack":      r.slackFunction(t, log, policyFile),
		"google":     r.googleFunction(t, log),
		"cloudflare": r.cloudflareFunction(t, dir, policyFile),
	} {
		h.Emulator.Register(e2eFunction(module), fn)
	}

	doc := `apiVersion: sluis.truvity.github.io/sluis/v3
issuerURL: ` + e2eIssuerURL + `
publicURL: ` + e2eMount + `
policy: {file: ` + policyFile + `}
secrets: {source: ssm, root: /sluis/` + lambdatest.Instance + `, layout: v5, endpoint: ` + h.SSM.URL() + `}
recovery: {enabled: true, passwordSecret: recovery/password}
adapters:
  secrets: {adapter: ssm, settings: {endpoint: ` + h.SSM.URL() + `}}
  state: {adapter: memory}
  blobs: {adapter: memory}
  trigger: {adapter: memory}
`
	for _, m := range []string{"github", "slack", "google"} {
		doc += m + ": {remote: {function: " + e2eFunction(m) + "}}\n"
	}
	doc += "cloudflare:\n  remote: {function: " + e2eFunction("cloudflare") + "}\n"
	cfg, err := rosterapp.Load(e2eWrite(t, dir, "sluis.yaml", doc))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// The transport is the production one, aimed at the emulator: the alias is
	// the caller's class.
	cfg.Dial = func(_ context.Context, c modcall.Config, class string) (modcall.Caller, error) {
		return lambdacall.New(h.Emulator.Client(), c).As(class), nil
	}
	r.cfg = cfg
	if r.app, err = rosterapp.New(context.Background(), cfg, log); err != nil {
		t.Fatalf("an issuer with every provider remote did not start: %v", err)
	}
	t.Cleanup(r.app.Close)
	r.handler = r.app.Handler()
	return r
}

const e2eCloudflareSection = `cloudflare:
  accounts: { main: { id: 0123456789abcdef0123456789abcdef, minter: internal/cloudflare/main/minter } }
  presets:
    dns: { account: main, prototype: 0123456789abcdef0123456789abcdef, description: DNS, lifetime: 1h, rotation: 10m }
`

func e2eCheck(context.Context) (deploycheck.Report, error) { return deploycheck.Report{OK: true}, nil }

// githubFunction is the GitHub function: its handler and its two servers are the
// module's own, over a store of its own, in which a catalogue App and the link
// App are already created.
func (r *e2eRig) githubFunction(t *testing.T, log *slog.Logger, policyFile string) *lambdaapp.Function {
	t.Helper()
	set := memory.New().Set()
	r.modules["github"] = set
	base := portstore.New(set)
	apps := portstore.NewGitHubCatalogueApps(base)
	ctx := context.Background()
	if err := apps.Put(ctx, catalogueapp.Record{ID: "renovate", Org: e2eOrg, AppID: 44, AppSlug: "acme-renovate", InstallationID: 42, ConnectedBy: e2eLinkUser}, e2ePEM); err != nil {
		t.Fatal(err)
	}
	if err := portstore.NewGitHubOrgs(base).PutLinkApp(ctx, linkApp(), linkCredential()); err != nil {
		t.Fatal(err)
	}
	web := r.h.GitHub.Client()
	srv := githubroster.MintServer(apps, web)
	doc, pol := policyOf(t, policyFile)
	registerGitHubConsole(srv, log, doc, pol, base, set, web)
	return &lambdaapp.Function{Handler: githubfn.Handler(log, func(string) bool { return false },
		func(context.Context) (lambdaapp.Pass, error) { return nil, errors.New("no pass in this test") }, srv, e2eCheck)}
}

func (r *e2eRig) slackFunction(t *testing.T, log *slog.Logger, policyFile string) *lambdaapp.Function {
	t.Helper()
	r.modules["slack"] = memory.New().Set()
	_, pol := policyOf(t, policyFile)
	svc := slackconsole.Static(slackconsole.Deps{Log: log, Authorizer: authorizer{pol}})
	return &lambdaapp.Function{Handler: slackfn.WithCalls(
		slackfn.Handler(log, func(string) bool { return false },
			func(context.Context) (lambdaapp.Pass, error) { return nil, errors.New("no pass in this test") }, e2eCheck),
		slackfn.Server(log, func(context.Context) (*slackconsole.Service, error) { return svc, nil }))}
}

func (r *e2eRig) googleFunction(t *testing.T, log *slog.Logger) *lambdaapp.Function {
	t.Helper()
	r.modules["google"] = memory.New().Set()
	// The module's own directory: the workspace it reads and the snapshot its refresh wrote.
	workspaces, snapshots := hub.NewMemoryStore(), hub.NewMemorySnapshots()
	if err := workspaces.Put(context.Background(), e2eWorkspace()); err != nil {
		t.Fatal(err)
	}
	if err := snapshots.Put(context.Background(), e2eSnapshot()); err != nil {
		t.Fatal(err)
	}
	h := hub.New(workspaces, snapshots, hub.Config{}, log)
	svc := googleconsole.NewService(h, []googleconsole.Connector{r.google}, nil, log)
	return &lambdaapp.Function{Handler: googlefn.WithCalls(
		googlefn.Handler(log, func(context.Context, string) (lambdaapp.RefreshResult, error) {
			return lambdaapp.RefreshResult{}, nil
		}, e2eCheck),
		googlefn.Server(log, func(context.Context) (*googleconsole.Service, error) { return svc, nil }))}
}

func (r *e2eRig) cloudflareFunction(t *testing.T, dir, policyFile string) *lambdaapp.Function {
	t.Helper()
	doc := `apiVersion: sluis.truvity.github.io/sluis/v3
issuerURL: ` + e2eIssuerURL + `
policy: {file: ` + policyFile + `}
secrets: {source: ssm, root: /sluis/` + lambdatest.Instance + `, layout: v5, endpoint: ` + r.cloudflareSSM.URL() + `}
adapters:
  secrets: {adapter: ssm, settings: {endpoint: ` + r.cloudflareSSM.URL() + `}}
  state: {adapter: memory}
  blobs: {adapter: memory}
  trigger: {adapter: memory}
` + e2eCloudflareSection + "  serve: {audience: cloudflare}\n"
	file := e2eWrite(t, dir, "cloudflare.yaml", doc)
	fn, err := lambdaapp.OpenModule(context.Background(), func(k string) string {
		if k == config.EnvConfig {
			return file
		}
		return ""
	}, lambdaapp.ModuleCloudflare)
	if err != nil {
		t.Fatalf("open the cloudflare function: %v", err)
	}
	t.Cleanup(fn.Close)
	return fn
}

// session signs in by the recovery password and returns the session cookie.
func (r *e2eRig) session() *http.Cookie {
	r.t.Helper()
	req := httptest.NewRequest(http.MethodPost, e2eMount+"/login/recovery", nil)
	req.Header.Set("Authorization", "Bearer "+e2ePassword)
	rec := httptest.NewRecorder()
	r.handler.ServeHTTP(rec, req)
	res := rec.Result()
	defer func() { _ = res.Body.Close() }()
	for _, c := range res.Cookies() {
		if c.Value != "" && c.MaxAge >= 0 && strings.Contains(c.Name, "session") {
			return c
		}
	}
	r.t.Fatalf("recovery sign-in = %d, no session cookie: %v %s", rec.Code, res.Cookies(), rec.Body.String())
	return nil
}

// rpc calls a console method as the signed-in operator, over the Connect protocol.
func (r *e2eRig) rpc(session *http.Cookie, procedure string, body any) (int, map[string]any) {
	r.t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		r.t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, e2eMount+procedure, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.AddCookie(session)
	rec := httptest.NewRecorder()
	r.handler.ServeHTTP(rec, req)
	var out map[string]any
	data, _ := io.ReadAll(rec.Body)
	_ = json.Unmarshal(data, &out)
	if out == nil {
		out = map[string]any{"raw": string(data)}
	}
	return rec.Code, out
}

// authorizer is the policy in force, as the Slack module reads it.
type authorizer struct{ set *policy.Set }

func (a authorizer) Policy() *policy.Set { return a.set }

func policyOf(t *testing.T, file string) (*config.PolicyDocument, *policy.Set) {
	t.Helper()
	doc, err := config.Load[config.PolicyDocument](file)
	if err != nil {
		t.Fatal(err)
	}
	set, err := policy.NewSet(doc.Policy)
	if err != nil {
		t.Fatal(err)
	}
	return doc, set
}

func linkApp() link.App {
	return link.App{Owner: e2eOrg, AppID: 9, AppSlug: "acme-access-roster-link", ClientID: githubfake.ClientID}
}

func linkCredential() link.AppCredential {
	return link.AppCredential{AppID: 9, ClientID: githubfake.ClientID, ClientSecret: githubfake.ClientSecret}
}

func registerGitHubConsole(srv *modcall.Server, log *slog.Logger, doc *config.PolicyDocument, set *policy.Set,
	base *portstore.Base, ports port.Set, web *http.Client,
) {
	orgs := portstore.NewGitHubOrgs(base)
	ghconsole.Register(srv, ghconsole.New(ghconsole.Deps{
		Log: log, Policy: func() *policy.Set { return set }, PublicURL: e2eMount, Orgs: orgs, HTTP: web,
		Reports:       rails.NewBlobReports(ports.Blob, blobprefix.GitHubReports),
		Confirmations: orgs, LinkApp: orgs, Links: portstore.NewGitHubLinks(base),
		RunnerApps: portstore.NewGitHubRunnerApps(base), RunnerTiers: doc.RunnerTiers(),
		CatalogueApps: portstore.NewGitHubCatalogueApps(base), Catalogue: doc.GitHubCatalogue(),
	}))
}

// get makes a browser request to the issuer's origin.
func (r *e2eRig) get(target string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r.t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("Accept", "text/html")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	r.handler.ServeHTTP(rec, req)
	return rec
}

func cookieNamed(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	res := rec.Result()
	defer func() { _ = res.Body.Close() }()
	for _, c := range res.Cookies() {
		if c.Name == name && c.Value != "" {
			return c
		}
	}
	return nil
}

// callsTo is the invocations the emulator served for a module's function.
func (r *e2eRig) callsTo(module string) []lambdatest.Call {
	var out []lambdatest.Call
	for _, c := range r.h.Emulator.Calls() {
		if c.Function == e2eFunction(module) {
			out = append(out, c)
		}
	}
	return out
}

// methodsOf is the module methods the calls carried, with the alias each came by.
func methodsOf(t *testing.T, calls []lambdatest.Call) []string {
	t.Helper()
	var out []string
	for _, c := range calls {
		var req modcall.Request
		if err := json.Unmarshal(c.Payload, &req); err != nil {
			t.Fatal(err)
		}
		out = append(out, req.Method+"@"+c.Qualifier)
	}
	return out
}

// seedDirectory puts the Google module's output where the issuer reads it: the
// workspace record in the Google table, which the issuer holds a read view of,
// and the snapshot the module's refresh writes.
func (r *e2eRig) seedDirectory() {
	r.t.Helper()
	ctx := context.Background()
	set, err := r.app.Stores().ForModule(port.ModuleGoogle)
	if err != nil {
		r.t.Fatal(err)
	}
	if err = portstore.NewWorkspaces(portstore.New(set)).Put(ctx, e2eWorkspace()); err != nil {
		r.t.Fatal(err)
	}
	ports := r.app.Stores().Ports
	if err = hub.NewBlobSnapshots(ports.Blob, ports.State).Put(ctx, e2eSnapshot()); err != nil {
		r.t.Fatal(err)
	}
}

func e2eWorkspace() hub.Workspace {
	return hub.Workspace{ID: "north", Backend: "google", Domains: []string{"north.example"}, Admin: "admin@north.example", Declared: true,
		Health: hub.Health{OK: true, ProbedAt: time.Now()}}
}

func e2eSnapshot() *hub.Snapshot {
	return hub.NewSnapshot("north", time.Now(), []backend.Account{{Email: e2eLinkUser, Live: true}}, nil, nil)
}

// rpcEvent is the event a module call arrives as.
func rpcEvent(t *testing.T, module, method string) []byte {
	t.Helper()
	raw, err := json.Marshal(modcall.Request{V: modcall.Version, Kind: modcall.Kind, Module: module, Method: method, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// qualifiersOf is the aliases the emulator was invoked through.
func qualifiersOf(calls []lambdatest.Call) map[string]bool {
	out := map[string]bool{}
	for _, c := range calls {
		out[c.Qualifier] = true
	}
	return out
}

func TestTheIssuerRunsEndToEndWithEveryProviderRemote(t *testing.T) {
	r := newE2E(t)
	r.seedDirectory()
	ctx := context.Background()

	t.Run("one console page per module", func(t *testing.T) {
		s := r.session()
		want := map[string]string{
			"/directoryroster.v1.GitHubService/GetGitHubStatus":    `"id":"renovate"`,
			"/directoryroster.v1.SlackService/GetSlackStatus":      "/connect/slack/workspace/callback",
			"/directoryroster.v1.WorkspaceService/ListWorkspaces":  `"id":"north"`,
			"/directoryroster.v1.CloudflareService/ListCloudflare": `"idLast4":"cdef"`,
		}
		for procedure, contains := range want {
			code, out := r.rpc(s, procedure, map[string]any{})
			raw, _ := json.Marshal(out)
			if code != http.StatusOK || !strings.Contains(string(raw), contains) {
				t.Errorf("%s = %d %s, want it to carry %s", procedure, code, raw, contains)
			}
		}
		for module, method := range map[string]string{
			"github": "console.get_status", "slack": "console.get_status", "google": "console.list_workspaces", "cloudflare": "status",
		} {
			if got := methodsOf(t, r.callsTo(module)); len(got) == 0 || got[len(got)-1] != method+"@live-console" {
				t.Errorf("%s was called %v, want %s through live-console", module, got, method)
			}
		}
	})

	t.Run("the GitHub link is two calls, the second sealed", func(t *testing.T) {
		account := r.h.GitHub.AddAccount("ada-gh", e2eLinkUser)
		before := len(r.callsTo("github"))
		page := r.get(e2eMount + consolewire.LinkPath)
		cookie := cookieNamed(page, access.LinkCookieName)
		body := page.Body.String()
		at := strings.Index(body, `href="`+githubapp.WebBase+"/login/oauth/authorize?")
		if page.Code != http.StatusOK || at < 0 || cookie == nil {
			t.Fatalf("link page = %d, no authorize button or cookie:\n%s", page.Code, body)
		}
		href := body[at+len(`href="`):]
		authorize, err := url.Parse(strings.ReplaceAll(href[:strings.IndexByte(href, '"')], "&amp;", "&"))
		if err != nil {
			t.Fatal(err)
		}
		done := r.get(e2eMount+consolewire.LinkCallbackPath+"?"+url.Values{
			"code": {r.h.GitHub.Authorize("ada-gh")}, "state": {authorize.Query().Get("state")}}.Encode(), cookie)
		if done.Code != http.StatusOK || !strings.Contains(done.Body.String(), "@ada-gh is linked") {
			t.Fatalf("callback = %d:\n%s", done.Code, done.Body)
		}
		calls := r.callsTo("github")[before:]
		if got := methodsOf(t, calls); strings.Join(got, " ") != "console.link_page@live-console console.link_complete@live-console console.link_complete@live-console" {
			t.Fatalf("the link made the calls %v", got)
		}
		// The first completion carries GitHub's one-time code and no continuation;
		// the second only the sealed continuation, which names no address.
		first, second := linkPayload(t, calls[1]), linkPayload(t, calls[2])
		if first.Code == "" || first.Continue != "" || second.Code != "" || second.Continue == "" {
			t.Errorf("completions = %+v then %+v, want the code then the sealed continuation", first, second)
		}
		if strings.Contains(second.Continue, "ada-gh") || strings.Contains(second.Continue, e2eLinkUser) || strings.Contains(second.Continue, account.Access) {
			t.Errorf("the sealed continuation is readable: %s", second.Continue)
		}
		// The link is in the GitHub module's store, and not in the issuer's.
		if n := countRecords(t, r.modules["github"], port.ModuleGitHub); n == 0 {
			t.Error("the GitHub module kept no record of the link")
		}
	})

	t.Run("Google identifies the person at sign-in", func(t *testing.T) {
		before := len(r.callsTo("google"))
		start := r.get(e2eMount + "/login/google/start")
		login := cookieNamed(start, access.LoginCookieName)
		if start.Code != http.StatusFound || login == nil || !strings.HasPrefix(start.Header().Get("Location"), "https://accounts.google.example/signin") {
			t.Fatalf("start = %d %q", start.Code, start.Header().Get("Location"))
		}
		done := r.get(e2eMount+"/login/google/callback?code=x&state="+login.Value, login)
		session := cookieNamed(done, "sluis_session")
		if done.Code != http.StatusFound || session == nil {
			t.Fatalf("callback = %d, no session", done.Code)
		}
		if got := methodsOf(t, r.callsTo("google")[before:]); strings.Join(got, " ") != "sign_in_url@live-issuer identify@live-issuer" {
			t.Errorf("sign-in made the calls %v, want sign_in_url then identify through live-issuer", got)
		}
		if n := r.google.identified.Load(); n != 1 {
			t.Errorf("the module identified %d times, want once", n)
		}
	})

	t.Run("the GitHub mint is the module's", func(t *testing.T) {
		before := len(r.callsTo("github"))
		minted, err := r.app.Issuer().MintGitHubToken(ctx, []string{"all:access-roster:operator"},
			issuer.GitHubTokenRequest{App: "renovate", Permissions: map[string]string{"contents": "read"}})
		if err != nil {
			t.Fatalf("mint: %v", err)
		}
		if minted.Token != r.h.GitHub.Token || minted.Installation != 42 {
			t.Errorf("minted %+v, want the fake GitHub's token for installation 42", minted.MintedToken)
		}
		if got := methodsOf(t, r.callsTo("github")[before:]); strings.Join(got, " ") != "mint_installation@live-issuer" {
			t.Errorf("the mint made the calls %v, want mint_installation through live-issuer", got)
		}
		if len(r.h.GitHub.TokenRequests) != 1 || r.h.GitHub.TokenRequests[0].Installation != 42 {
			t.Errorf("GitHub saw %+v, want one access_tokens call", r.h.GitHub.TokenRequests)
		}
	})

	t.Run("a method answers its own class through the real handlers and no other", func(t *testing.T) {
		for _, c := range []struct{ module, method, class string }{
			{"github", githubtokens.MethodMintInstallation, "console"},
			{"github", consolewire.GetStatus, "issuer"},
			{"slack", slackconsole.MethodGetStatus, "issuer"},
			{"google", googleconsole.MethodListWorkspaces, "issuer"},
			{"google", googleconsole.MethodIdentify, "console"},
			{"cloudflare", cfrpc.MethodStatus, "issuer"},
			{"cloudflare", cfrpc.MethodMint, "console"},
			// And a caller that came by no class alias at all.
			{"github", githubtokens.MethodMintInstallation, ""},
			{"cloudflare", cfrpc.MethodStatus, ""},
		} {
			res, err := r.h.Emulator.Invoke(ctx, e2eFunction(c.module), lambdacall.AliasOf(c.class), rpcEvent(t, c.module, c.method))
			if err != nil {
				t.Fatalf("%s.%s as %q: %v", c.module, c.method, c.class, err)
			}
			var out modcall.Response
			if err = json.Unmarshal(res.Payload, &out); err != nil || out.Error == nil || out.Error.Code != modcall.CodeForbidden {
				t.Errorf("%s.%s called as %q: %s, want forbidden", c.module, c.method, c.class, res.Payload)
			}
		}
	})

	t.Run("every call came through a class alias, and the issuer holds no provider store", func(t *testing.T) {
		for q := range qualifiersOf(r.h.Emulator.Calls()) {
			switch q {
			case "live-console", "live-issuer", "live", "":
			default:
				t.Errorf("a call came through the alias %q", q)
			}
		}
		// The calls the issuer made, and not the probes of this test, came by a class alias only.
		for _, c := range r.h.Emulator.Calls() {
			if c.Class == "" {
				continue
			}
			if c.Class != "console" && c.Class != "issuer" {
				t.Errorf("a call came as the class %q", c.Class)
			}
		}

		stores := r.app.Stores()
		if stores.Ports.Module != port.ModuleOIDC {
			t.Errorf("the issuer's store is the %q module's, want oidc", stores.Ports.Module)
		}
		// The only peer is the Google snapshot; the other providers have no view at all.
		for _, m := range []port.Module{port.ModuleGitHub, port.ModuleSlack, port.ModuleCloudflare, port.ModuleBackup} {
			if _, ok := stores.Ports.Peer(m); ok {
				t.Errorf("the issuer holds a view of the %s table", m)
			}
		}
		if _, ok := stores.Ports.Peer(port.ModuleGoogle); !ok {
			t.Error("the issuer lost the Google snapshot view")
		}
		if got := r.cfg.Stores.Peers; len(got) != 1 || got[0] != port.ModuleGoogle || r.cfg.Stores.Module != port.ModuleOIDC {
			t.Errorf("the issuer is configured as %q with peers %v, want oidc with the Google snapshot only", r.cfg.Stores.Module, got)
		}
		// What the traffic wrote is the modules': the issuer's table holds none of it.
		for _, m := range []port.Module{port.ModuleGitHub, port.ModuleSlack, port.ModuleCloudflare} {
			if n := countRecordsIn(t, stores.Ports.State, m); n != 0 {
				t.Errorf("the issuer's table holds %d %s records", n, m)
			}
		}
		// A write of a provider's key through the issuer is refused before it reaches any
		// table. The Cloudflare module keeps no state record (its credentials are
		// parameters), so its keys are the Google module's: the real ones its snapshot
		// view lists.
		var keys []string
		for _, m := range []port.Module{port.ModuleGitHub, port.ModuleSlack} {
			for _, prefix := range port.StatePrefixes5(m) {
				for _, key := range []string{prefix + "x", prefix} {
					if addr, err := port.Locate5(key); err == nil && addr.Module == m {
						keys = append(keys, key)
					}
				}
			}
		}
		view, _ := stores.Ports.Peer(port.ModuleGoogle)
		page, err := view.List(ctx, "ws.dir.", "", 10)
		if err != nil || len(page.Records) == 0 {
			t.Fatalf("the Google workspace records the issuer reads: %d, %v", len(page.Records), err)
		}
		for _, rec := range page.Records {
			keys = append(keys, rec.Key)
		}
		for _, key := range keys {
			if _, err := stores.Ports.State.Put(ctx, key, []byte("{}"), 0); !errors.Is(err, port.ErrNotOwner) {
				t.Errorf("a write of the provider key %q through the issuer: %v, want ErrNotOwner", key, err)
			}
		}
		if len(keys) < 5 {
			t.Errorf("only %d provider keys were probed", len(keys))
		}
		// The Google records the issuer's hub reads are still read (the write side is
		// proved in internal/app).
		h := r.app.Hub()
		if views, err := h.WorkspaceViews(ctx); err != nil || len(views) != 1 || views[0].Workspace.ID != "north" {
			t.Errorf("the issuer's hub reads the workspaces %v, %v, want north", views, err)
		}
		// The issuer never read a provider's parameter, internal or external: it read
		// its own (the recovery password, the session key), and the Cloudflare
		// function read the minter's, in a secrets store of its own.
		if len(r.h.SSM.Reads()) == 0 {
			t.Error("the issuer read no parameter at all: the check below proves nothing")
		}
		if reads := strings.Join(r.cloudflareSSM.Reads(), " "); !strings.Contains(reads, "/internal/cloudflare/main/minter") {
			t.Errorf("the Cloudflare function read %q, want the minter's parameter", reads)
		}
		for _, name := range r.h.SSM.Reads() {
			for _, m := range []string{"github", "slack", "google", "cloudflare"} {
				for _, ns := range []string{"internal", "external"} {
					if strings.Contains(name, "/"+ns+"/"+m+"/") || strings.HasSuffix(name, "/"+ns+"/"+m) {
						t.Errorf("the issuer read the parameter %s", name)
					}
				}
			}
		}
	})
}

// linkPayload is the completion a call carried.
func linkPayload(t *testing.T, c lambdatest.Call) consolewire.LinkComplete {
	t.Helper()
	var req modcall.Request
	if err := json.Unmarshal(c.Payload, &req); err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Msg consolewire.LinkComplete `json:"msg"`
	}
	if err := json.Unmarshal(req.Payload, &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Msg
}

// countRecords is how many state records of the module a set holds.
func countRecords(t *testing.T, set port.Set, m port.Module) int {
	return countRecordsIn(t, set.State, m)
}

func countRecordsIn(t *testing.T, state port.StateReader, m port.Module) int {
	t.Helper()
	n := 0
	for _, prefix := range port.StatePrefixes5(m) {
		page, err := state.List(context.Background(), prefix, "", 100)
		if err != nil {
			t.Fatal(err)
		}
		n += len(page.Records)
	}
	return n
}
