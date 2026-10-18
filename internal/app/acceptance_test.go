package app_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/secrets"

	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/app"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/store"
	"github.com/truvity/sluis/srp"
)

// recoverPassword is the fixture's recovery password: long and random enough to sit
// behind a verifier.
const recoverPassword = "aB3-Kf9xQ2mLpR7vTz1WcY8nHd5JgS4e"

// issuerFile is the service's configuration file as a test states it: the
// defaults a test wants, then what the test changes.
func issuerFile(t *testing.T, change ...func(*config.Serve)) *config.Serve {
	t.Helper()
	// The recovery password is a declared secret: the file names the variable.
	t.Setenv("SLUIS_SECRET_RECOVERY_PASSWORD", recoverPassword)
	enabled := true
	f := &config.Serve{
		IssuerURL: "https://issuer.example",
		Demo:      true,
		Store:     "memory",
		Recovery:  &config.Recovery{Enabled: &enabled, LoginSecret: "recovery/password"},
	}
	for _, c := range change {
		c(f)
	}
	return f
}

// openStores builds the storage ports the way the service does, from the file.
func openStores(t *testing.T, f *config.Serve) *store.Stores {
	t.Helper()
	sc, err := store.FromServe(f)
	if err != nil {
		t.Fatalf("store.FromServe: %v", err)
	}
	sc.Secrets = secrets.Env{}
	st, err := store.Open(context.Background(), sc, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(st.Close)
	return st
}

// boot assembles a hub the way a deployment would: from the configuration
// file's settings, which is the contract the chart writes to.
func boot(t *testing.T, change ...func(*config.Serve)) *app.App {
	t.Helper()
	f := issuerFile(t, change...)
	cfg, err := app.FromConfig(f, nil)
	if err != nil {
		t.Fatalf("FromConfig: %v", err)
	}
	assembled, err := app.New(context.Background(), cfg, openStores(t, f), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(assembled.Close)
	return assembled
}

// noDirectoryLogin turns the directory sign-in off.
func noDirectoryLogin(f *config.Serve) {
	off := false
	f.Login = &config.Login{Directory: &off}
}

// console boots a hub that knows its own address.
//
// The order matters: a sign-in redirect is built from publicURL, so the
// hub has to be told where it is before it can send a browser back to
// itself. The listener is opened first with a handler it does not have
// yet, which is the only way round the circle.
func console(t *testing.T, change ...func(*config.Serve)) (*http.Client, string, *app.App) {
	t.Helper()
	var handler http.Handler
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)

	assembled := boot(t, append([]func(*config.Serve){func(f *config.Serve) { f.PublicURL = server.URL }}, change...)...)
	handler = assembled.ConsoleHandler()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	return &http.Client{Jar: jar}, server.URL, assembled
}

func get(t *testing.T, client *http.Client, url string) (int, string) {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	request.Header.Set("Accept", "text/html")
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("get %s: %v", url, err)
	}
	defer func() { _ = response.Body.Close() }()
	body, _ := io.ReadAll(response.Body)
	return response.StatusCode, string(body)
}

// recoverWith signs in as the recovery account the way the sign-in page's script
// does: it loads the page (which sets the cookie the state is bound to), runs
// SRP against the console's two endpoints and returns the status of the last
// answer. The password is never sent.
func recoverWith(t *testing.T, client *http.Client, at, username, password string) (int, string) {
	t.Helper()
	code, page := get(t, client, at+"/login")
	if code != http.StatusOK {
		t.Fatalf("the sign-in page = %d", code)
	}
	match := regexp.MustCompile(`data-state="([^"]+)"`).FindStringSubmatch(page)
	if match == nil {
		return http.StatusNotFound, "the sign-in page offers no password form"
	}
	state := html.UnescapeString(match[1])

	code, body := rpc(t, client, at+"/login/srp/start", fmt.Sprintf(`{"state":%q,"username":%q}`, state, username))
	if code != http.StatusOK {
		return code, body
	}
	var begun struct {
		Handle, Username, Salt, B string
		Iterations                int
	}
	if err := json.Unmarshal([]byte(body), &begun); err != nil {
		t.Fatalf("start: %v: %s", err, body)
	}
	salt, _ := hex.DecodeString(begun.Salt)
	b, _ := hex.DecodeString(begun.B)
	proof, err := srp.Default.NewClient(begun.Username, password, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	m1, err := proof.Respond(salt, b, begun.Iterations)
	if err != nil {
		t.Fatal(err)
	}
	code, body = rpc(t, client, at+"/login/srp/finish", fmt.Sprintf(`{"state":%q,"handle":%q,"A":%q,"M1":%q}`,
		state, begun.Handle, hex.EncodeToString(proof.Public()), hex.EncodeToString(m1)))
	if code == http.StatusOK {
		var done struct{ M2 string }
		if err = json.Unmarshal([]byte(body), &done); err != nil {
			t.Fatalf("finish: %v: %s", err, body)
		}
		m2, _ := hex.DecodeString(done.M2)
		if err = proof.VerifyServer(m2); err != nil {
			t.Errorf("the server's proof did not verify: %v", err)
		}
	}
	return code, body
}

func rpc(t *testing.T, client *http.Client, url, body string) (int, string) {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	defer func() { _ = response.Body.Close() }()
	raw, _ := io.ReadAll(response.Body)
	return response.StatusCode, string(raw)
}

// A person signs in through a directory and reaches the console with the
// role their membership grants. This is the walk-through the whole
// service exists for, and until recently the button on the sign-in page
// led to a route that did not exist.
func TestAPersonSignsInAndReachesTheConsole(t *testing.T) {
	client, at, _ := console(t)

	code, page := get(t, client, at+"/login")
	if code != http.StatusOK || !strings.Contains(page, "Continue with") {
		t.Fatalf("the sign-in page = %d, %q", code, page)
	}
	// Never one button per company: an anonymous page that names the
	// tenants has published them.
	for _, company := range []string{"north.example", "south.example"} {
		if strings.Contains(page, company) {
			t.Errorf("the sign-in page names the tenant %s", company)
		}
	}

	if code, page = get(t, client, at+"/login/demo/start"); code != http.StatusOK {
		t.Fatalf("signing in = %d, %q", code, page)
	}
	code, who := get(t, client, at+"/.access/whoami")
	if code != http.StatusOK || !strings.Contains(who, `"email":"ada@north.example"`) {
		t.Fatalf("whoami = %d, %q", code, who)
	}
	if !strings.Contains(who, `"operator"`) {
		t.Errorf("whoami = %q, want the role the membership grants", who)
	}
	if !strings.Contains(who, `"source":"directory"`) {
		t.Errorf("whoami = %q, want the directory as the source", who)
	}

	// And the console answers an operator call, which is the thing the
	// session is for.
	code, body := rpc(t, client, at+"/directoryroster.v1.WorkspaceService/ListWorkspaces", "{}")
	if code != http.StatusOK || !strings.Contains(body, "C0demo-north") {
		t.Fatalf("ListWorkspaces = %d, %q", code, body)
	}
}

// Both OAuth redirect URIs are built from publicURL, and so are the
// values the setup steps tell an operator to paste. A deployment that
// leaves it unset registers a redirect no browser will reach — which is
// what the chart was doing.
func TestTheRedirectsFollowThePublicURL(t *testing.T) {
	// A fixed public URL, so the values are assertable — and recovery
	// rather than a sign-in to read them, because a sign-in redirect
	// built from that URL would go somewhere this test is not.
	client, at, _ := console(t, func(f *config.Serve) { f.PublicURL = "https://directory.example" })
	if code, body := recoverWith(t, client, at, "recovery", recoverPassword); code != http.StatusOK {
		t.Fatalf("recovery = %d, %q", code, body)
	}
	code, body := rpc(t, client, at+"/directoryroster.v1.SettingsService/GetSettings", "{}")
	if code != http.StatusOK {
		t.Fatalf("GetSettings = %d, %q", code, body)
	}
	for _, want := range []string{
		"https://directory.example/connect/google/callback",
		"https://directory.example/login/google/callback",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the setup step does not offer %q: %s", want, body)
		}
	}
	if strings.Contains(body, "localhost") {
		t.Errorf("the setup step still names localhost: %s", body)
	}
}

// Turning the hub's own sign-in off closes the routes, and leaves the
// recovery path and the API alone.
func TestSignInOffLeavesOneDoor(t *testing.T) {
	client, at, _ := console(t, noDirectoryLogin)

	code, page := get(t, client, at+"/login")
	if code != http.StatusOK || strings.Contains(page, "Continue with") {
		t.Errorf("the page still offers a sign-in: %q", page)
	}
	if !strings.Contains(page, "Recovery sign-in") {
		t.Error("recovery went with it")
	}
	if code, _ = get(t, client, at+"/login/demo/start"); code != http.StatusNotFound {
		t.Errorf("the sign-in route = %d, want it closed", code)
	}
}

// Recovery is the way in when the ordinary one is broken, and it grants
// operator without any membership at all.
func TestRecoveryReachesTheConsole(t *testing.T) {
	client, at, _ := console(t)

	code, body := recoverWith(t, client, at, "recovery", "wrong")
	if code != http.StatusForbidden {
		t.Fatalf("a wrong proof = %d, %q", code, body)
	}
	if code, body = recoverWith(t, client, at, "recovery", recoverPassword); code != http.StatusOK {
		t.Fatalf("recovery = %d, %q", code, body)
	}
	code, who := get(t, client, at+"/.access/whoami")
	if code != http.StatusOK || !strings.Contains(who, `"source":"recovery"`) {
		t.Fatalf("whoami = %d, %q", code, who)
	}
	if !strings.Contains(who, `"operator"`) {
		t.Errorf("recovery did not grant operator: %q", who)
	}
}

// There is no way to present the recovery password: not in a JSON body, not as a
// bearer, not in a form. The sign-in page's script proves it without sending it
// (docs/decisions/0076), and the console's recovery route takes a token and
// nothing else.
func TestNoPlaintextRecoveryPasswordIsAccepted(t *testing.T) {
	client, at, _ := console(t)

	code, page := get(t, client, at+"/login")
	if code != http.StatusOK || !strings.Contains(page, "data-srp") || !strings.Contains(page, "/login/srp.js") {
		t.Fatalf("the sign-in page offers no password form: %d %q", code, page)
	}
	if regexp.MustCompile(`name="(proof|password)"`).MatchString(page) {
		t.Errorf("the page has a field that would carry the password: %q", page)
	}
	state := regexp.MustCompile(`data-state="([^"]+)"`).FindStringSubmatch(page)[1]

	bearer, err := http.NewRequestWithContext(context.Background(), http.MethodPost, at+"/login/recovery", nil)
	if err != nil {
		t.Fatal(err)
	}
	bearer.Header.Set("Authorization", "Bearer "+recoverPassword)
	for name, send := range map[string]func() (int, string){
		"a JSON body": func() (int, string) { return rpc(t, client, at+"/login/recovery", `{"proof":"`+recoverPassword+`"}`) },
		"a bearer": func() (int, string) {
			resp, err := client.Do(bearer)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			body, _ := io.ReadAll(resp.Body)
			return resp.StatusCode, string(body)
		},
		"a form": func() (int, string) {
			resp, err := client.PostForm(at+"/login/recovery", url.Values{"state": {html.UnescapeString(state)}, "proof": {recoverPassword}})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			body, _ := io.ReadAll(resp.Body)
			return resp.StatusCode, string(body)
		},
	} {
		if code, body := send(); code != http.StatusBadRequest {
			t.Errorf("the password in %s = %d %q, want 400", name, code, body)
		}
	}
	code, who := get(t, client, at+"/.access/whoami")
	if code != http.StatusOK || strings.Contains(who, `"source":"recovery"`) {
		t.Errorf("a presented password signed somebody in: %d %q", code, who)
	}
}

// A deployment with no recovery path has none: the page offers nothing
// and the route is closed.
func TestRecoveryCanBeTurnedOff(t *testing.T) {
	client, at, _ := console(t, func(f *config.Serve) { off := false; f.Recovery.Enabled = &off })

	code, page := get(t, client, at+"/login")
	if code != http.StatusOK || strings.Contains(page, "Recovery sign-in") {
		t.Errorf("the page still offers recovery: %q", page)
	}
	if code, _ = rpc(t, client, at+"/login/recovery", `{"proof":"`+recoverPassword+`"}`); code != http.StatusForbidden {
		t.Errorf("the recovery route = %d, want it closed", code)
	}
	if code, _ = rpc(t, client, at+"/login/srp/start", `{"state":"x","username":"recovery"}`); code != http.StatusNotFound && code != http.StatusMethodNotAllowed {
		t.Errorf("the password route = %d, want it closed", code)
	}
}

// Configuration that cannot work is refused at start rather than
// producing a hub that behaves unlike the one that was asked for.
func TestImpossibleConfigurationIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*config.Serve)
	}{
		{"an unknown store", func(f *config.Serve) { f.Store = "postgres" }},
		{"a log level that is not one", func(f *config.Serve) { f.Log = &config.Log{Level: "chatty"} }},
	} {
		if _, err := app.FromConfig(issuerFile(t, tc.change), nil); err == nil {
			t.Errorf("%s was accepted", tc.name)
		}
	}
}

// The console admits somebody the ISSUER signed in, with no console
// session of its own and no proxy in front of it.
//
// This is what one origin and one process buy. Before it, a
// console on the issuer's own host was authenticated either by a proxy —
// which ran an OpenID flow against a service in the same process, a
// network round trip and a second session store to learn something
// already known — or by a login of its own, which is the second door an
// installation with a gateway deliberately turns off.
//
// It does NOT remove the need for a way to START a sign-in: a person
// arriving with no session anywhere still has to get one. What it
// removes is the second session for a person who already has one.
func TestTheConsoleAdmitsWhoeverTheIssuerSignedIn(t *testing.T) {
	client, at, assembled := console(t, noDirectoryLogin)

	// Nobody yet: the console has no opinion and sends them to sign in.
	if code, _ := get(t, client, at+"/.access/whoami"); code != http.StatusOK {
		t.Fatalf("whoami before signing in = %d", code)
	}

	// Now the issuer says who the browser is. A real one reads its own
	// session cookie; what the console depends on is only the answer.
	assembled.ConsoleServer().UseSignedIn(func(http.ResponseWriter, *http.Request) (access.Principal, bool) {
		return access.Principal{Email: "ada@north.example", Source: access.SourceOIDC}, true
	})

	code, body := get(t, client, at+"/.access/whoami")
	if code != http.StatusOK {
		t.Fatalf("whoami = %d, %q", code, body)
	}
	if !strings.Contains(body, "ada@north.example") {
		t.Errorf("whoami = %q, want the person the issuer signed in", body)
	}
	if !strings.Contains(body, `"status":"signed-in"`) {
		t.Errorf("whoami = %q, want a signed-in status", body)
	}
}

// Somebody with no session is sent to the ISSUER to get one, not to a
// sign-in page of the console's own. That is what makes one
// door: the console is a client of the issuer like any other
// application, and holds nothing special.
func TestSomebodyWithNoSessionIsSentToTheIssuer(t *testing.T) {
	client, at, assembled := console(t, noDirectoryLogin)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	// Before: the console's own page, which is the split deployment's
	// shape and the second door.
	if to := where(t, client, at+"/"); !strings.HasSuffix(to, "/login") {
		t.Fatalf("with no entry the console sends them to %q, want its own page", to)
	}

	assembled.ConsoleServer().UseSignInEntry(func() string {
		return "https://access.example/authorize?client_id=directory-console"
	})

	if to := where(t, client, at+"/"); !strings.HasPrefix(to, "https://access.example/authorize") {
		t.Errorf("the console sends them to %q, want the issuer's authorization endpoint", to)
	}
}

// And the code the flow hands back is stripped rather than carried into
// the page. The console never redeems it — what it needed was the
// session the flow established — so leaving it in the URL would only put
// it in a bookmark and in every referrer.
func TestTheCodeIsStrippedFromTheConsolesURL(t *testing.T) {
	client, at, assembled := console(t, noDirectoryLogin)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	assembled.ConsoleServer().UseSignedIn(func(http.ResponseWriter, *http.Request) (access.Principal, bool) {
		return access.Principal{Email: "ada@north.example", Source: access.SourceOIDC}, true
	})

	to := where(t, client, at+"/?code=abc123&state=xyz")
	if to != "/" {
		t.Errorf("after the flow the console went to %q, want the clean page", to)
	}
}

// where is the Location a request is redirected to.
func where(t *testing.T, client *http.Client, url string) string {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("get %s: %v", url, err)
	}
	defer func() { _ = response.Body.Close() }()
	return response.Header.Get("Location")
}
