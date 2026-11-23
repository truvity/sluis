package issuer_test

import (
	"context"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/audit/audittest"
	"github.com/truvity/sluis/internal/demo"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/policy"
)

// devicePolicy declares one client with the device grant, one other client
// that also has it (for a code presented by the wrong client), and one
// without it. The display name carries markup, which every page must escape.
const devicePolicy = `
version: 1
groups:
  devel:k8s:viewer: { members: [engineering@north.example] }
lifetimes: { default: 12h }
clients:
  cli:
    kind: public
    display_name: "The <b>CLI</b>"
    redirects: [http://127.0.0.1/callback]
    requires: [devel:k8s:viewer]
    device_authorization: true
  other-cli:
    kind: public
    redirects: [http://127.0.0.1/callback]
    requires: [devel:k8s:viewer]
    device_authorization: true
  local-dev:
    kind: public
    redirects: [http://localhost:8000/callback]
    requires: [devel:k8s:viewer]
`

// clock is a settable time, for what ends with it.
type clock struct {
	mu sync.Mutex
	at time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

type deviceServer struct {
	server *httptest.Server
	clock  *clock
	trail  *audittest.Recorder
}

// newDeviceServer is the issuer with its sign-in pages under a policy, with
// a clock the test moves and an audit trail it reads.
func newDeviceServer(t *testing.T, policyYAML string) *deviceServer {
	t.Helper()

	declared, err := policy.Parse([]byte(policyYAML))
	if err != nil {
		t.Fatalf("parse the policy: %v", err)
	}
	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatalf("policy set: %v", err)
	}
	dir := &fakeDirectory{standing: map[string]issuer.Standing{
		"ada@north.example": {Found: true, Authoritative: true, Groups: []string{"engineering@north.example"}},
		"bob@north.example": {Found: true, Authoritative: true, Groups: []string{"sales@north.example"}},
	}}
	iss := issuer.New(issuer.Config{URL: "http://issuer.example", AllowInsecure: true}, set, dir, issuer.NewMemoryState())
	trail := audittest.New(t)
	iss.UseAudit(trail)

	storage, err := issuer.NewTestStorage(iss, fakeVerifier{}, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	at := &clock{at: time.Now()}
	issuer.SetStorageClock(storage, at.now)

	handler, err := handlerWithSignIn(iss, storage, issuer.SignInDeps{
		Providers: []issuer.SignIn{signInAs{}},
		State:     access.NewStateCodec([]byte("a-test-key-for-signing-state"), 0),
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return &deviceServer{server: server, clock: at, trail: trail}
}

// signInAs is a provider whose answer is the code it is handed back, so
// that one test can sign two different people in.
type signInAs struct{}

func (signInAs) Kind() string { return "google" }

func (signInAs) URL(state string) (string, error) {
	return "https://idp.example/authorize?state=" + url.QueryEscape(state), nil
}

func (signInAs) Identify(_ context.Context, code string) (string, error) {
	if code == "x" || code == "" {
		return "ada@north.example", nil
	}
	return code, nil
}

// post sends a form as a device does, with a User-Agent.
func (d *deviceServer) post(t *testing.T, path string, form url.Values) (int, map[string]any, http.Header) {
	t.Helper()

	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, d.server.URL+path, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("build the request: %v", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("User-Agent", "sluisctl/1.0 <script>alert(1)</script>")
	response, err := d.server.Client().Do(request)
	if err != nil {
		t.Fatalf("post %s: %v", path, err)
	}
	defer func() { _ = response.Body.Close() }()

	raw, _ := io.ReadAll(response.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)

	return response.StatusCode, body, response.Header
}

type issued struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

func (d *deviceServer) issue(t *testing.T, client string) issued {
	t.Helper()

	status, body, header := d.post(t, "/device_authorization", url.Values{
		"client_id": {client}, "scope": {"openid email offline_access"},
	})
	if status != http.StatusOK {
		t.Fatalf("device_authorization = %d %v", status, body)
	}
	if header.Get("Cache-Control") != "no-store" {
		t.Errorf("the device code was answered without no-store: %q", header.Get("Cache-Control"))
	}
	raw, _ := json.Marshal(body)
	var out issued
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func (d *deviceServer) poll(t *testing.T, client, code string) (int, map[string]any) {
	t.Helper()

	status, body, _ := d.post(t, "/token", url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}, "device_code": {code}, "client_id": {client},
	})
	return status, body
}

func wantError(t *testing.T, status int, body map[string]any, wantStatus int, code string) {
	t.Helper()

	if status != wantStatus || body["error"] != code {
		t.Fatalf("answer = %d %v, want %d %s", status, body, wantStatus, code)
	}
}

// signInFromDevicePage walks a browser with no sign-in from the verification
// URI through the provider and back to the device page, and returns the
// approval page.
func signInFromDevicePage(t *testing.T, b *browser, path string) string {
	t.Helper()

	status, where, body := b.do(http.MethodGet, path)
	if status != http.StatusFound || !strings.HasPrefix(where, "/login?auth=") {
		t.Fatalf("an unsigned browser at %s = %d %q %s, want the sign-in", path, status, where, body)
	}
	status, where, _ = b.do(http.MethodGet, where)
	if status != http.StatusFound || !strings.Contains(where, "/login/google/start") {
		t.Fatalf("the chooser sent the browser to %q (%d)", where, status)
	}
	status, toProvider, _ := b.do(http.MethodGet, where)
	if status != http.StatusFound {
		t.Fatalf("provider start: %d", status)
	}
	state := toProvider[strings.Index(toProvider, "state=")+len("state="):]
	status, back, body := b.do(http.MethodGet, "/login/google/callback?code=x&state="+state)
	if status != http.StatusFound || !strings.HasPrefix(back, "/device?user_code=") {
		t.Fatalf("the callback sent the browser to %q (%d %s), want the device page", back, status, body)
	}
	status, _, body = b.do(http.MethodGet, back)
	if status != http.StatusOK {
		t.Fatalf("the device page after sign-in = %d %s", status, body)
	}
	return body
}

var (
	deviceCSRF          = regexp.MustCompile(`name="csrf" value="([^"]+)"`)
	deviceUserCodeField = regexp.MustCompile(`name="user_code" value="([^"]+)"`)
)

func decide(t *testing.T, b *browser, page, decision string) (int, string) {
	t.Helper()

	csrf, code := deviceCSRF.FindStringSubmatch(page), deviceUserCodeField.FindStringSubmatch(page)
	if csrf == nil || code == nil {
		t.Fatalf("the approval page has no form: %s", page)
	}
	status, _, body, _ := b.post("/device", url.Values{
		"csrf": {html.UnescapeString(csrf[1])}, "user_code": {html.UnescapeString(code[1])}, "decision": {decision},
	})
	return status, body
}

// The whole grant: a device asks, polls, the person signs in at the
// verification URI, sees what asked, approves, and the device's next poll
// gets the tokens a loopback sign-in to the same client gets. A second poll
// with the same code gets nothing.
func TestADeviceIsSignedInOnceThePersonApproves(t *testing.T) {
	t.Parallel()
	d := newDeviceServer(t, devicePolicy)

	got := d.issue(t, "cli")
	if !regexp.MustCompile(`^[BCDFGHJKLMNPQRSTVWXZ]{4}-[BCDFGHJKLMNPQRSTVWXZ]{4}$`).MatchString(got.UserCode) {
		t.Errorf("user_code = %q, want XXXX-XXXX from the alphabet without look-alikes", got.UserCode)
	}
	if len(got.DeviceCode) < 43 {
		t.Errorf("device_code = %q, want 256 bits", got.DeviceCode)
	}
	if got.ExpiresIn != 600 || got.Interval != 5 {
		t.Errorf("expires_in, interval = %d, %d, want 600, 5", got.ExpiresIn, got.Interval)
	}
	if got.VerificationURI != "http://issuer.example/device" ||
		got.VerificationURIComplete != "http://issuer.example/device?user_code="+got.UserCode {
		t.Errorf("verification = %q, %q", got.VerificationURI, got.VerificationURIComplete)
	}

	// Before anybody decides: pending, and slow_down for a device that
	// polls sooner than it was told to.
	status, body := d.poll(t, "cli", got.DeviceCode)
	wantError(t, status, body, http.StatusBadRequest, "authorization_pending")
	status, body = d.poll(t, "cli", got.DeviceCode)
	wantError(t, status, body, http.StatusBadRequest, "slow_down")
	// The interval is now ten seconds: six is still too soon.
	d.clock.advance(6 * time.Second)
	status, body = d.poll(t, "cli", got.DeviceCode)
	wantError(t, status, body, http.StatusBadRequest, "slow_down")
	d.clock.advance(16 * time.Second)
	status, body = d.poll(t, "cli", got.DeviceCode)
	wantError(t, status, body, http.StatusBadRequest, "authorization_pending")

	// The person, in a browser of their own, from the complete URI with the
	// code typed in lower case and without its dash.
	b := newBrowser(t, d.server)
	typed := strings.ToLower(strings.ReplaceAll(got.UserCode, "-", ""))
	page := signInFromDevicePage(t, b, "/device?user_code="+typed)

	for _, want := range []string{
		"The &lt;b&gt;CLI&lt;/b&gt;", "ada@north.example", got.UserCode, "127.0.0.1",
		"sluisctl/1.0 &lt;script&gt;alert(1)&lt;/script&gt;", "UTC", "Approve", "Deny",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the approval page does not show %q:\n%s", want, page)
		}
	}
	if strings.Contains(page, "<script>") || strings.Contains(page, "<b>CLI") {
		t.Errorf("the approval page carries unescaped markup:\n%s", page)
	}

	status, answer := decide(t, b, page, "approve")
	if status != http.StatusOK || !strings.Contains(answer, "Device approved") {
		t.Fatalf("approve = %d %s", status, answer)
	}

	d.clock.advance(11 * time.Second)
	status, tokens := d.poll(t, "cli", got.DeviceCode)
	if status != http.StatusOK {
		t.Fatalf("the poll after approval = %d %v", status, tokens)
	}
	for _, field := range []string{"access_token", "refresh_token", "id_token"} {
		if s, _ := tokens[field].(string); s == "" {
			t.Errorf("the token response has no %s: %v", field, tokens)
		}
	}
	claims := jwtPart(t, tokens["id_token"].(string), 1)
	if aud, _ := claims["aud"].([]any); claims["sub"] != "ada@north.example" || len(aud) != 1 || aud[0] != "cli" {
		t.Errorf("id token sub, aud = %v, %v", claims["sub"], claims["aud"])
	}
	access := jwtPart(t, tokens["access_token"].(string), 1)
	if access["client_id"] != "cli" {
		t.Errorf("access token client_id = %v", access["client_id"])
	}

	// The same code again is nothing.
	d.clock.advance(11 * time.Second)
	status, body = d.poll(t, "cli", got.DeviceCode)
	wantError(t, status, body, http.StatusBadRequest, "invalid_grant")

	// The page says it was decided.
	status, _, page = b.do(http.MethodGet, "/device?user_code="+got.UserCode)
	if status != http.StatusBadRequest || !strings.Contains(page, "already approved") {
		t.Errorf("the device page after approval = %d %s", status, page)
	}

	for _, action := range []string{"roster.device.issued", "roster.device.approved", "roster.person.signed_in"} {
		if len(d.trail.Find(action)) == 0 {
			t.Errorf("no %s record: %v", action, d.trail.Actions())
		}
	}
	approved := d.trail.Find("roster.device.approved")[0]
	if approved.GetActor().GetId() != "ada@north.example" || approved.GetData().GetFields()["requested_from"].GetStringValue() != "127.0.0.1" {
		t.Errorf("the approval record = %v", approved)
	}
	// The first sign-in at the device page and the approval are one sign-in.
	if n := len(d.trail.Find("roster.person.signed_in")); n != 1 {
		t.Errorf("sign-in records = %d, want 1", n)
	}
}

// A browser already signed in goes straight to the approval page, and its
// approval completes a fresh request as that person.
func TestASignedInBrowserIsAskedAtOnce(t *testing.T) {
	t.Parallel()
	d := newDeviceServer(t, devicePolicy)
	b := newBrowser(t, d.server)

	first := d.issue(t, "cli")
	signInFromDevicePage(t, b, "/device?user_code="+first.UserCode)

	second := d.issue(t, "cli")
	status, _, page := b.do(http.MethodGet, "/device?user_code="+second.UserCode)
	if status != http.StatusOK || !strings.Contains(page, "Approve a device?") {
		t.Fatalf("a signed-in browser = %d %s, want the approval page", status, page)
	}
	if status, answer := decide(t, b, page, "approve"); status != http.StatusOK {
		t.Fatalf("approve = %d %s", status, answer)
	}
	if status, tokens := d.poll(t, "cli", second.DeviceCode); status != http.StatusOK {
		t.Fatalf("poll = %d %v", status, tokens)
	}
	// The first code is still waiting.
	status, body := d.poll(t, "cli", first.DeviceCode)
	wantError(t, status, body, http.StatusBadRequest, "authorization_pending")
}

// Deny is final: the device is told access_denied, and the page says so.
func TestADeniedDeviceIsToldAccessDenied(t *testing.T) {
	t.Parallel()
	d := newDeviceServer(t, devicePolicy)
	got := d.issue(t, "cli")

	b := newBrowser(t, d.server)
	page := signInFromDevicePage(t, b, got.VerificationURIComplete[len("http://issuer.example"):])
	if status, answer := decide(t, b, page, "deny"); status != http.StatusOK || !strings.Contains(answer, "Device denied") {
		t.Fatalf("deny = %d %s", status, answer)
	}
	status, body := d.poll(t, "cli", got.DeviceCode)
	wantError(t, status, body, http.StatusBadRequest, "access_denied")
	d.clock.advance(time.Hour)
	status, body = d.poll(t, "cli", got.DeviceCode)
	wantError(t, status, body, http.StatusBadRequest, "access_denied")

	if len(d.trail.Find("roster.device.denied")) != 1 {
		t.Errorf("records: %v", d.trail.Actions())
	}
	// The approval form, posted again after the denial, decides nothing.
	if status, answer := decide(t, b, page, "approve"); status != http.StatusBadRequest || !strings.Contains(answer, "denied") {
		t.Errorf("approve after deny = %d %s", status, answer)
	}
}

// Ten minutes and nobody decided: expired_token, recorded once.
func TestAnUnansweredCodeExpires(t *testing.T) {
	t.Parallel()
	d := newDeviceServer(t, devicePolicy)
	got := d.issue(t, "cli")

	d.clock.advance(10*time.Minute + time.Second)
	for range 2 {
		status, body := d.poll(t, "cli", got.DeviceCode)
		wantError(t, status, body, http.StatusBadRequest, "expired_token")
		d.clock.advance(time.Minute)
	}
	if n := len(d.trail.Find("roster.device.expired")); n != 1 {
		t.Errorf("expiry records = %d, want 1", n)
	}

	b := newBrowser(t, d.server)
	status, _, page := b.do(http.MethodGet, "/device?user_code="+got.UserCode)
	if status != http.StatusBadRequest || !strings.Contains(page, "expired") {
		t.Errorf("an expired code at the page = %d %s", status, page)
	}
}

// The device code is the client's: presented by another, by an unknown
// client or by one without the grant, it opens nothing.
func TestADeviceCodeIsBoundToItsClient(t *testing.T) {
	t.Parallel()
	d := newDeviceServer(t, devicePolicy)
	got := d.issue(t, "cli")

	status, body := d.poll(t, "other-cli", got.DeviceCode)
	wantError(t, status, body, http.StatusBadRequest, "invalid_grant")
	status, body = d.poll(t, "local-dev", got.DeviceCode)
	wantError(t, status, body, http.StatusBadRequest, "unauthorized_client")
	status, body = d.poll(t, "nobody", got.DeviceCode)
	wantError(t, status, body, http.StatusUnauthorized, "invalid_client")
	status, body = d.poll(t, "cli", "not-a-code")
	wantError(t, status, body, http.StatusBadRequest, "invalid_grant")
	status, body = d.poll(t, "cli", "")
	wantError(t, status, body, http.StatusBadRequest, "invalid_request")
}

// A client without `device_authorization` is refused a code, as is an
// unknown one and a scope this issuer does not grant.
func TestOnlyADeclaredClientGetsACode(t *testing.T) {
	t.Parallel()
	d := newDeviceServer(t, devicePolicy)

	status, body, _ := d.post(t, "/device_authorization", url.Values{"client_id": {"local-dev"}})
	wantError(t, status, body, http.StatusBadRequest, "unauthorized_client")
	status, body, _ = d.post(t, "/device_authorization", url.Values{"client_id": {"nobody"}})
	wantError(t, status, body, http.StatusUnauthorized, "invalid_client")
	status, body, _ = d.post(t, "/device_authorization", url.Values{"client_id": {"cli"}, "scope": {"openid admin"}})
	wantError(t, status, body, http.StatusBadRequest, "invalid_scope")
	status, body, _ = d.post(t, "/device_authorization", url.Values{"client_id": {"cli", "cli"}})
	wantError(t, status, body, http.StatusBadRequest, "invalid_request")

	refused := d.trail.Find("roster.device.issued")
	if len(refused) != 1 || refused[0].GetTargets()[0].GetId() != "local-dev" {
		t.Errorf("refusal records = %v", refused)
	}
}

// With no client declaring the grant, none of it is served or advertised.
func TestTheDeviceGrantIsNotServedWithoutAClientThatDeclaresIt(t *testing.T) {
	t.Parallel()
	d := newDeviceServer(t, demo.Policy)

	status, _, _ := d.post(t, "/device_authorization", url.Values{"client_id": {"local-dev"}})
	if status != http.StatusNotFound {
		t.Errorf("device_authorization = %d, want 404", status)
	}
	status, body := d.poll(t, "local-dev", "x")
	wantError(t, status, body, http.StatusBadRequest, "unsupported_grant_type")
	if status, _, _ := newBrowser(t, d.server).do(http.MethodGet, "/device"); status != http.StatusNotFound {
		t.Errorf("/device = %d, want 404", status)
	}
	doc := discovery(t, d.server)
	if _, ok := doc["device_authorization_endpoint"]; ok {
		t.Error("the device endpoint is advertised with no client declaring it")
	}
}

// Discovery advertises the endpoint and the grant type once a client
// declares it, in both metadata documents.
func TestDiscoveryAdvertisesTheDeviceGrantWhenServed(t *testing.T) {
	t.Parallel()
	d := newDeviceServer(t, devicePolicy)

	for _, path := range []string{"/.well-known/openid-configuration", "/.well-known/oauth-authorization-server"} {
		doc := fetchJSON(t, d.server, path)
		if doc["device_authorization_endpoint"] != "http://issuer.example/device_authorization" {
			t.Errorf("%s device_authorization_endpoint = %v", path, doc["device_authorization_endpoint"])
		}
		grants, _ := doc["grant_types_supported"].([]any)
		found := false
		for _, g := range grants {
			found = found || g == "urn:ietf:params:oauth:grant-type:device_code"
		}
		if !found || len(grants) != 4 {
			t.Errorf("%s grant_types_supported = %v", path, grants)
		}
	}
}

// The approval form is bound to the browser's sign-in: a post without its
// token, or with a token minted for another code, decides nothing.
func TestTheApprovalNeedsTheFormsToken(t *testing.T) {
	t.Parallel()
	d := newDeviceServer(t, devicePolicy)
	got := d.issue(t, "cli")
	other := d.issue(t, "cli")

	b := newBrowser(t, d.server)
	page := signInFromDevicePage(t, b, "/device?user_code="+got.UserCode)
	csrf := html.UnescapeString(deviceCSRF.FindStringSubmatch(page)[1])

	for name, form := range map[string]url.Values{
		"no token":      {"user_code": {got.UserCode}, "decision": {"approve"}},
		"a wrong token": {"user_code": {got.UserCode}, "decision": {"approve"}, "csrf": {"forged"}},
		"another code":  {"user_code": {other.UserCode}, "decision": {"approve"}, "csrf": {csrf}},
	} {
		status, _, body, _ := b.post("/device", form)
		if status != http.StatusBadRequest || !strings.Contains(body, "Nothing was decided") {
			t.Errorf("%s: %d %s", name, status, body)
		}
	}

	// Another browser, signed in as somebody else, cannot use this one's token.
	stranger := newBrowser(t, d.server)
	signInFromDevicePage(t, stranger, "/device?user_code="+other.UserCode)
	status, _, body, _ := stranger.post("/device", url.Values{"user_code": {got.UserCode}, "decision": {"approve"}, "csrf": {csrf}})
	if status != http.StatusBadRequest {
		t.Errorf("another browser's post = %d %s", status, body)
	}

	for _, code := range []string{got.DeviceCode, other.DeviceCode} {
		status, answer := d.poll(t, "cli", code)
		wantError(t, status, answer, http.StatusBadRequest, "authorization_pending")
	}
}

// A person the client does not admit is refused at the approval, and the
// device gets nothing.
func TestAnApprovalBySomebodyNotAdmittedIsRefused(t *testing.T) {
	t.Parallel()
	d := newDeviceServer(t, devicePolicy)
	first := d.issue(t, "cli")

	// Bob holds none of the groups `cli` requires: the sign-in from the
	// device page is refused, as a loopback one would be.
	b := newBrowser(t, d.server)
	status, where, _ := b.do(http.MethodGet, "/device?user_code="+first.UserCode)
	if status != http.StatusFound {
		t.Fatalf("device page = %d", status)
	}
	_, where, _ = b.do(http.MethodGet, where)
	_, toProvider, _ := b.do(http.MethodGet, where)
	state := toProvider[strings.Index(toProvider, "state=")+len("state="):]
	status, _, body := b.do(http.MethodGet, "/login/google/callback?code=bob@north.example&state="+state)
	if status != http.StatusForbidden {
		t.Fatalf("bob's sign-in = %d %s, want it refused", status, body)
	}
	status, answer := d.poll(t, "cli", first.DeviceCode)
	wantError(t, status, answer, http.StatusBadRequest, "authorization_pending")
}

// A user code is short, so a source that types codes that name nothing is
// stopped after a few; and a source asking for many codes is slowed.
func TestGuessingAndFloodingAreLimited(t *testing.T) {
	t.Parallel()
	d := newDeviceServer(t, devicePolicy)
	b := newBrowser(t, d.server)

	for i := range 5 {
		status, _, body := b.do(http.MethodGet, "/device?user_code=BCDF-GHJ"+string("KLMNP"[i]))
		if status != http.StatusOK || !strings.Contains(body, "No device is waiting") {
			t.Fatalf("guess %d = %d %s", i, status, body)
		}
	}
	if status, _, _ := b.do(http.MethodGet, "/device?user_code=BCDF-GHJQ"); status != http.StatusTooManyRequests {
		t.Errorf("the sixth wrong code = %d, want 429", status)
	}
	// A form with a code that cannot be one is a guess too, and is escaped.
	if status, _, body := b.do(http.MethodGet, "/device?user_code=%3Cscript%3E"); status != http.StatusTooManyRequests || strings.Contains(body, "<script>") {
		t.Errorf("a malformed code = %d %s", status, body)
	}

	for i := range 10 {
		if status, body, _ := d.post(t, "/device_authorization", url.Values{"client_id": {"cli"}}); status != http.StatusOK {
			t.Fatalf("code %d = %d %v", i, status, body)
		}
	}
	status, body, header := d.post(t, "/device_authorization", url.Values{"client_id": {"cli"}})
	if status != http.StatusTooManyRequests || header.Get("Retry-After") == "" {
		t.Errorf("the eleventh code in a minute = %d %v", status, body)
	}
	d.clock.advance(time.Minute)
	if status, _, _ := d.post(t, "/device_authorization", url.Values{"client_id": {"cli"}}); status != http.StatusOK {
		t.Errorf("a code a minute later = %d", status)
	}
}

// The library's callback never hands a device's request to its redirect:
// only an approval at the device page issues a code for it.
func TestTheLibraryCallbackRefusesADeviceRequest(t *testing.T) {
	t.Parallel()
	d := newDeviceServer(t, devicePolicy)
	got := d.issue(t, "cli")

	b := newBrowser(t, d.server)
	_, where, _ := b.do(http.MethodGet, "/device?user_code="+got.UserCode)
	id := strings.TrimPrefix(where, "/login?auth=")
	status, location, body := b.do(http.MethodGet, "/authorize/callback?id="+url.QueryEscape(id))
	if status != http.StatusBadRequest || location != "" || !strings.Contains(body, "device") {
		t.Errorf("the callback for a device request = %d %q %s", status, location, body)
	}
}

// The entry page with no code asks for one, and escapes nothing it was not
// given.
func TestTheDevicePageAsksForTheCode(t *testing.T) {
	t.Parallel()
	d := newDeviceServer(t, devicePolicy)

	status, _, body := newBrowser(t, d.server).do(http.MethodGet, "/device")
	if status != http.StatusOK || !strings.Contains(body, `name="user_code"`) || !strings.Contains(body, `action="/device"`) {
		t.Errorf("/device = %d %s", status, body)
	}
}

func discovery(t *testing.T, server *httptest.Server) map[string]any {
	t.Helper()
	return fetchJSON(t, server, "/.well-known/openid-configuration")
}

func fetchJSON(t *testing.T, server *httptest.Server, path string) map[string]any {
	t.Helper()

	response, err := server.Client().Get(server.URL + path) //nolint:noctx // a test
	if err != nil {
		t.Fatalf("get %s: %v", path, err)
	}
	defer func() { _ = response.Body.Close() }()
	var doc map[string]any
	if err = json.NewDecoder(response.Body).Decode(&doc); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return doc
}
