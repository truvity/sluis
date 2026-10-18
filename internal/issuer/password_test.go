package issuer_test

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"html"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/audit/audittest"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/internal/srpauth"
	"github.com/truvity/sluis/internal/throttle"
	"github.com/truvity/sluis/policy"
	"github.com/truvity/sluis/srp"
)

// The password sign-in (docs/decisions/0076): the recovery account and the
// conformance account prove a password by SRP, in two requests the sign-in page's
// script makes, and the issuer holds a verifier and not the password.

const (
	recoveryPassword    = "r3covery-password-of-forty-characters-xyz"
	conformancePassword = "c0nformance-password-of-forty-chars-xyz"
	conformanceSubject  = "system:serviceaccount:sluis:conformance"
)

// passwordPolicy: the recovery ServiceAccount is the operators; "app" is an
// ordinary client; the two harness clients are marked. Nothing maps the
// conformance subject to any group.
const passwordPolicy = `
version: 1
groups:
  all:sluis:operator:
    matchers:
      - service_account: { namespace: sluis, name: recovery }
clients:
  app:
    kind: public
    redirects: [http://localhost:8000/callback]
    requires: [all:sluis:operator]
  harness-rp:
    kind: public
    redirects: [http://localhost:8000/callback]
    requires: [all:sluis:operator]
    harness: true
`

// sharedWorld is what two containers of one installation share: the State, the
// port State the exchanges are kept in, and the key the sign-in state is signed with.
type sharedWorld struct {
	state issuer.State
	ports *memory.Store
	codec *access.StateCodec
}

func newShared() *sharedWorld {
	return &sharedWorld{state: issuer.NewMemoryState(), ports: memory.New(), codec: access.NewStateCodec([]byte("a-test-key-for-signing-state"), 0)}
}

// captured is every request a server received, flattened to text: the URL, the
// headers and the body. A password that was sent anywhere is in it.
type captured struct {
	mu   sync.Mutex
	text strings.Builder
}

func (c *captured) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		c.mu.Lock()
		c.text.WriteString(r.Method + " " + r.URL.String() + "\n")
		for name, values := range r.Header {
			c.text.WriteString(name + ": " + strings.Join(values, ",") + "\n")
		}
		c.text.Write(body)
		c.text.WriteString("\n")
		c.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

func (c *captured) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.text.String()
}

type pwOptions struct {
	conformance bool
	noRecovery  bool
	shared      *sharedWorld
	// refusals replaces the limiter of failed exchanges.
	refusals *throttle.Limiter
}

type pwWorld struct {
	server *httptest.Server
	iss    *issuer.Issuer
	trail  *audittest.Recorder
	gate   *flipGate
	seen   *captured
}

func newPasswordWorld(t *testing.T, o pwOptions) *pwWorld {
	t.Helper()
	declared, err := policy.Parse([]byte(passwordPolicy))
	if err != nil {
		t.Fatal(err)
	}
	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatal(err)
	}
	if o.shared == nil {
		o.shared = newShared()
	}
	iss := issuer.New(issuer.Config{URL: "http://issuer.example", AllowInsecure: true}, set, &fakeDirectory{}, o.shared.state)
	trail := audittest.New(t)
	iss.UseAudit(trail)
	gate := &flipGate{}
	iss.UseRecoveryGate(gate)
	iss.UseConformance(o.conformance)
	storage, err := issuer.NewTestStorage(iss, fakeVerifier{}, nil, nil, nil, o.shared.state)
	if err != nil {
		t.Fatal(err)
	}
	refusals := o.refusals
	if refusals == nil {
		refusals = throttle.Default()
	}
	door := &issuer.PasswordDoor{
		Service: &srpauth.Service{
			Params:   srp.Default,
			Store:    srpauth.StateStore{State: o.shared.ports},
			DecoyKey: func() ([]byte, error) { return []byte("the decoy key"), nil },
			Refusals: refusals,
		},
		Conformance:        srpauth.Fixed(srp.Default.NewCredential("conformance", conformancePassword)),
		ConformanceSubject: conformanceSubject,
	}
	if !o.noRecovery {
		door.Recovery = srpauth.Fixed(srp.Default.NewCredential("recovery", recoveryPassword))
		door.RecoverySubject = lookAlike
	}
	h, err := handlerWithSignIn(iss, storage, issuer.SignInDeps{
		Providers: []issuer.SignIn{oneProvider{email: "ada@north.example"}},
		State:     o.shared.codec,
		Password:  door,
	})
	if err != nil {
		t.Fatal(err)
	}
	seen := &captured{}
	server := httptest.NewServer(seen.wrap(h))
	t.Cleanup(server.Close)
	return &pwWorld{server: server, iss: iss, trail: trail, gate: gate, seen: seen}
}

var stateField = regexp.MustCompile(`data-state="([^"]+)"`)

// open starts an authorization request for client and returns the chooser page.
func openChooser(t *testing.T, b *browser, client string) string {
	t.Helper()
	query := url.Values{
		"client_id": {client}, "redirect_uri": {"http://localhost:8000/callback"}, "response_type": {"code"},
		"scope": {"openid"}, "state": {"pw-test"}, "code_challenge_method": {"S256"},
		"code_challenge": {challengeOf(pkceVerifier)},
	}
	status, where, body := b.do(http.MethodGet, "/authorize?"+query.Encode())
	if status != http.StatusFound {
		t.Fatalf("/authorize: %d %s", status, body)
	}
	status, _, page := b.do(http.MethodGet, where)
	if status != http.StatusOK {
		t.Fatalf("the chooser: %d %s", status, page)
	}
	return page
}

func challengeOf(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// postJSON sends the JSON the page's script sends.
func (b *browser) postJSON(path string, body any) (int, map[string]any) {
	b.t.Helper()
	raw, _ := json.Marshal(body)
	request, err := http.NewRequestWithContext(b.t.Context(), http.MethodPost, b.server.URL+path, bytes.NewReader(raw))
	if err != nil {
		b.t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	for name, value := range b.cookies {
		request.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		b.t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	for _, cookie := range response.Cookies() {
		if cookie.MaxAge < 0 || cookie.Value == "" {
			delete(b.cookies, cookie.Name)
			continue
		}
		b.cookies[cookie.Name] = cookie.Value
	}
	out := map[string]any{}
	_ = json.NewDecoder(response.Body).Decode(&out)
	return response.StatusCode, out
}

// signInWith runs both requests of the exchange the way the script does and
// returns the status and body of the last answer it got.
func signInWith(t *testing.T, b *browser, state, username, password string) (int, map[string]any) {
	t.Helper()
	status, begun := b.postJSON("/login/srp/start", map[string]string{"state": state, "username": username})
	if status != http.StatusOK {
		return status, begun
	}
	salt, _ := hex.DecodeString(begun["salt"].(string))
	pub, _ := hex.DecodeString(begun["B"].(string))
	client, err := srp.Default.NewClient(username, password, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	m1, err := client.Respond(salt, pub)
	if err != nil {
		t.Fatal(err)
	}
	status, done := b.postJSON("/login/srp/finish", map[string]string{
		"state": state, "handle": begun["handle"].(string),
		"A": hex.EncodeToString(client.Public()), "M1": hex.EncodeToString(m1),
	})
	if status == http.StatusOK {
		m2, _ := hex.DecodeString(done["M2"].(string))
		if err = client.VerifyServer(m2); err != nil {
			t.Errorf("the server's proof did not verify: %v", err)
		}
	}
	return status, done
}

func stateOf(t *testing.T, page string) string {
	t.Helper()
	m := stateField.FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("the page has no password form: %s", page)
	}
	return html.UnescapeString(m[1])
}

// tokensFrom redeems the code the finish pointed at.
func tokensFrom(t *testing.T, b *browser, client string, done map[string]any) map[string]any {
	t.Helper()
	next, _ := done["next"].(string)
	return redeemAs(t, b, client, next)
}

func TestTheRecoveryAccountSignsInWithoutSendingItsPassword(t *testing.T) {
	t.Parallel()
	w := newPasswordWorld(t, pwOptions{})
	b := newBrowser(t, w.server)
	page := openChooser(t, b, "app")
	if !strings.Contains(page, "/login/srp.js") || !strings.Contains(page, "data-srp") {
		t.Fatalf("the page offers no password form: %s", page)
	}
	state := stateOf(t, page)

	status, done := signInWith(t, b, state, "recovery", recoveryPassword)
	if status != http.StatusOK {
		t.Fatalf("recovery = %d %v", status, done)
	}
	tokens := tokensFrom(t, b, "app", done)

	claims := payloadOf(t, tokens["id_token"].(string))
	if claims["sub"] != lookAlike || claims["acr"] != issuer.ACRRecovery {
		t.Errorf("sub %v acr %v, want the recovery ServiceAccount at the recovery class", claims["sub"], claims["acr"])
	}
	if !slices.Contains(groupsIn(t, tokens["access_token"].(string)), "all:sluis:operator") {
		t.Errorf("recovery lost its operator group: %v", groupsIn(t, tokens["access_token"].(string)))
	}
	if amr, _ := claims["amr"].([]any); len(amr) != 1 || amr[0] != "swk" {
		t.Errorf("amr = %v, want the recovery marker unchanged", claims["amr"])
	}

	// Nothing the server was sent carries the password, in a body, a header or a
	// URL, and nothing it answered does.
	for _, secret := range []string{recoveryPassword, url.QueryEscape(recoveryPassword), hex.EncodeToString([]byte(recoveryPassword))} {
		if strings.Contains(w.seen.String(), secret) {
			t.Errorf("the password reached the server: %q", secret)
		}
	}

	// The audit trail has the sign-in, as the recovery kind, kept durably.
	if got := w.trail.Actions(); !slices.Contains(got, "roster.recovery.signed_in") {
		t.Errorf("actions = %v, want roster.recovery.signed_in", got)
	}
}

func TestAWrongPasswordAndAnUnknownUsernameAreRefusedTheSameWay(t *testing.T) {
	t.Parallel()
	w := newPasswordWorld(t, pwOptions{})
	var answers []string
	for name, c := range map[string][2]string{
		"wrong password":                 {"recovery", "not-the-password"},
		"unknown username":               {"nobody", "not-the-password"},
		"the conformance account off":    {"conformance", conformancePassword},
		"the conformance account, wrong": {"conformance", "nope"},
	} {
		b := newBrowser(t, w.server)
		state := stateOf(t, openChooser(t, b, "app"))
		status, body := signInWith(t, b, state, c[0], c[1])
		if status != http.StatusForbidden {
			t.Errorf("%s: %d %v, want 403", name, status, body)
		}
		raw, _ := json.Marshal(body)
		answers = append(answers, string(raw))
	}
	for _, a := range answers[1:] {
		if a != answers[0] {
			t.Errorf("refusals differ: %s vs %s", a, answers[0])
		}
	}
	if slices.Contains(w.trail.Actions(), "roster.recovery.signed_in") {
		t.Error("a refused attempt was recorded as a recovery sign-in")
	}
}

// The start of an exchange answers an unknown username with a salt that does not
// change, in the shape of a real one's, so asking tells nothing.
func TestAnUnknownUsernameGetsAStableSaltOfTheRightShape(t *testing.T) {
	t.Parallel()
	w := newPasswordWorld(t, pwOptions{})
	b := newBrowser(t, w.server)
	state := stateOf(t, openChooser(t, b, "app"))
	ask := func(user string) map[string]any {
		status, body := b.postJSON("/login/srp/start", map[string]string{"state": state, "username": user})
		if status != http.StatusOK {
			t.Fatalf("start %q = %d %v", user, status, body)
		}
		return body
	}
	one, two, known := ask("nobody"), ask("nobody"), ask("recovery")
	if one["salt"] != two["salt"] {
		t.Error("the salt of an unknown username changed between two asks")
	}
	for name, body := range map[string]map[string]any{"unknown": one, "known": known} {
		if len(body["salt"].(string)) != len(known["salt"].(string)) || len(body["B"].(string)) != 2*srp.Default.Size() ||
			len(body["handle"].(string)) != 22 {
			t.Errorf("%s: the answer's shape differs: %v", name, body)
		}
	}
}

func TestAHandleIsGoodForOneFinishAndForTwoMinutes(t *testing.T) {
	t.Parallel()
	shared := newShared()
	w := newPasswordWorld(t, pwOptions{shared: shared})
	b := newBrowser(t, w.server)
	state := stateOf(t, openChooser(t, b, "app"))

	begin := func() (string, map[string]string) {
		_, begun := b.postJSON("/login/srp/start", map[string]string{"state": state, "username": "recovery"})
		salt, _ := hex.DecodeString(begun["salt"].(string))
		pub, _ := hex.DecodeString(begun["B"].(string))
		client, _ := srp.Default.NewClient("recovery", recoveryPassword, rand.Reader)
		m1, _ := client.Respond(salt, pub)
		return begun["handle"].(string), map[string]string{"state": state, "handle": begun["handle"].(string),
			"A": hex.EncodeToString(client.Public()), "M1": hex.EncodeToString(m1)}
	}

	// Expired: two minutes pass between the requests.
	_, expired := begin()
	shared.ports.Advance(srpauth.ExchangeTTL + 1)
	if status, body := b.postJSON("/login/srp/finish", expired); status != http.StatusForbidden {
		t.Errorf("an expired handle = %d %v, want 403", status, body)
	}

	// Replayed: the identical, valid request sent twice, the second with the
	// browser's cookie still in hand, so that it is the handle that refuses.
	_, valid := begin()
	saved := maps.Clone(b.cookies)
	if status, body := b.postJSON("/login/srp/finish", valid); status != http.StatusOK {
		t.Fatalf("the first finish = %d %v", status, body)
	}
	b.cookies = saved
	if status, body := b.postJSON("/login/srp/finish", valid); status != http.StatusForbidden {
		t.Errorf("a replayed finish = %d %v, want 403", status, body)
	}
}

// An exchange begun in one browser is not finished in another, and the attempt
// spends it.
func TestAnotherBrowsersStateCannotFinishAnExchange(t *testing.T) {
	t.Parallel()
	w := newPasswordWorld(t, pwOptions{})
	victim, attacker := newBrowser(t, w.server), newBrowser(t, w.server)
	stateV := stateOf(t, openChooser(t, victim, "app"))
	stateA := stateOf(t, openChooser(t, attacker, "app"))

	_, begun := victim.postJSON("/login/srp/start", map[string]string{"state": stateV, "username": "recovery"})
	salt, _ := hex.DecodeString(begun["salt"].(string))
	pub, _ := hex.DecodeString(begun["B"].(string))
	client, _ := srp.Default.NewClient("recovery", recoveryPassword, rand.Reader)
	m1, _ := client.Respond(salt, pub)
	finish := map[string]string{"state": stateA, "handle": begun["handle"].(string),
		"A": hex.EncodeToString(client.Public()), "M1": hex.EncodeToString(m1)}

	// The attacker's state is bound to the attacker's browser, so it passes the
	// cookie check, and it is not the state the handle was begun under.
	if status, body := attacker.postJSON("/login/srp/finish", finish); status != http.StatusForbidden {
		t.Errorf("a handle finished under another state = %d %v, want 403", status, body)
	}
	// The victim's state posted without the victim's cookie is refused as well.
	finish["state"] = stateV
	if status, body := attacker.postJSON("/login/srp/finish", finish); status != http.StatusBadRequest {
		t.Errorf("a state without its browser's cookie = %d %v, want 400", status, body)
	}
}

// Two containers of one installation: the exchange begun at one is finished at
// the other, and only once.
func TestAnExchangeBegunInOneContainerIsFinishedInAnother(t *testing.T) {
	t.Parallel()
	shared := newShared()
	one := newPasswordWorld(t, pwOptions{shared: shared})
	two := newPasswordWorld(t, pwOptions{shared: shared})
	b := newBrowser(t, one.server)
	state := stateOf(t, openChooser(t, b, "app"))

	_, begun := b.postJSON("/login/srp/start", map[string]string{"state": state, "username": "recovery"})
	salt, _ := hex.DecodeString(begun["salt"].(string))
	pub, _ := hex.DecodeString(begun["B"].(string))
	client, _ := srp.Default.NewClient("recovery", recoveryPassword, rand.Reader)
	m1, _ := client.Respond(salt, pub)
	finish := map[string]string{"state": state, "handle": begun["handle"].(string),
		"A": hex.EncodeToString(client.Public()), "M1": hex.EncodeToString(m1)}

	other := newBrowser(t, two.server)
	other.cookies = maps.Clone(b.cookies) // the same browser, at the other container
	if status, body := other.postJSON("/login/srp/finish", finish); status != http.StatusOK {
		t.Fatalf("the other container = %d %v", status, body)
	}
	if status, _ := b.postJSON("/login/srp/finish", finish); status != http.StatusForbidden {
		t.Errorf("the first container finished the same handle again = %d, want 403", status)
	}
}

func TestTheConformanceAccountSignsInToAHarnessClientOnlyAndIsNoOperator(t *testing.T) {
	t.Parallel()
	w := newPasswordWorld(t, pwOptions{conformance: true})

	// The harness client's page offers both accounts; an ordinary client's only
	// the recovery account.
	b := newBrowser(t, w.server)
	harnessPage := openChooser(t, b, "harness-rp")
	if !strings.Contains(harnessPage, "conformance test account") {
		t.Errorf("a harness client's page does not offer the conformance account: %s", harnessPage)
	}
	if page := openChooser(t, newBrowser(t, w.server), "app"); strings.Contains(page, "conformance") {
		t.Errorf("an ordinary client's page offers the conformance account: %s", page)
	}

	status, done := signInWith(t, b, stateOf(t, harnessPage), "conformance", conformancePassword)
	if status != http.StatusOK {
		t.Fatalf("conformance at a harness client = %d %v", status, done)
	}
	tokens := tokensFrom(t, b, "harness-rp", done)
	claims := payloadOf(t, tokens["id_token"].(string))
	if claims["sub"] != conformanceSubject || claims["acr"] != issuer.ACRConformance {
		t.Errorf("sub %v acr %v", claims["sub"], claims["acr"])
	}
	if amr, _ := claims["amr"].([]any); len(amr) != 2 || amr[0] != "pwd" || amr[1] != "conformance" {
		t.Errorf("amr = %v, want [pwd conformance]", claims["amr"])
	}
	if groups := groupsIn(t, tokens["access_token"].(string)); len(groups) != 0 {
		t.Errorf("the conformance subject holds %v: it is a test subject with no group", groups)
	}
	if got := w.trail.Actions(); slices.Contains(got, "roster.recovery.signed_in") || !slices.Contains(got, "roster.person.signed_in") {
		t.Errorf("actions = %v: a conformance sign-in is not a recovery sign-in", got)
	}

	// An ordinary client is refused even with the right password: the account
	// is not offered there, so it ends as an unknown username does.
	b2 := newBrowser(t, w.server)
	status, body := signInWith(t, b2, stateOf(t, openChooser(t, b2, "app")), "conformance", conformancePassword)
	if status != http.StatusForbidden {
		t.Errorf("conformance at an ordinary client = %d %v, want 403", status, body)
	}
}

func TestTheConformanceAccountIsOffUnlessTheFlagIsOn(t *testing.T) {
	t.Parallel()
	w := newPasswordWorld(t, pwOptions{conformance: false})
	b := newBrowser(t, w.server)
	page := openChooser(t, b, "harness-rp")
	if strings.Contains(page, "conformance") {
		t.Errorf("the page offers the account with the flag off: %s", page)
	}
	if status, body := signInWith(t, b, stateOf(t, page), "conformance", conformancePassword); status != http.StatusForbidden {
		t.Errorf("conformance with the flag off = %d %v, want 403", status, body)
	}
}

// One button closes every password door: the recovery switch of ADR 0075 shuts
// the recovery account and the conformance account alike, and what they opened.
func TestTheRecoverySwitchShutsBothAccounts(t *testing.T) {
	t.Parallel()
	w := newPasswordWorld(t, pwOptions{conformance: true})

	// Signed in before the switch.
	rb, cb := newBrowser(t, w.server), newBrowser(t, w.server)
	rState, cState := stateOf(t, openChooser(t, rb, "harness-rp")), stateOf(t, openChooser(t, cb, "harness-rp"))

	w.gate.off.Store(true)
	for name, c := range map[string]struct {
		b        *browser
		state    string
		user, pw string
	}{
		"recovery":    {rb, rState, "recovery", recoveryPassword},
		"conformance": {cb, cState, "conformance", conformancePassword},
	} {
		if status, body := signInWith(t, c.b, c.state, c.user, c.pw); status != http.StatusForbidden {
			t.Errorf("%s with the switch thrown = %d %v, want 403", name, status, body)
		}
	}
	// And the page offers neither: with one provider left it sends the browser
	// straight there.
	late := newBrowser(t, w.server)
	query := url.Values{
		"client_id": {"harness-rp"}, "redirect_uri": {"http://localhost:8000/callback"}, "response_type": {"code"},
		"scope": {"openid"}, "state": {"pw-test"}, "code_challenge_method": {"S256"}, "code_challenge": {challengeOf(pkceVerifier)},
	}
	_, where, _ := late.do(http.MethodGet, "/authorize?"+query.Encode())
	if status, next, page := late.do(http.MethodGet, where); strings.Contains(page, "data-srp") || status == http.StatusOK {
		t.Errorf("the page still offers a password form: %d %s %s", status, next, page)
	}
}

func TestTheSwitchEndsAConformanceSessionAtItsNextRefresh(t *testing.T) {
	t.Parallel()
	w := newPasswordWorld(t, pwOptions{conformance: true})
	session, err := w.iss.Sessions().Record(t.Context(), issuer.Opened{
		Identity: conformanceSubject, ClientID: "harness-rp", How: issuer.HowCode, Method: issuer.ConformanceHow,
		Token: "refresh-conformance", Scopes: []string{"openid"},
	})
	if err != nil {
		t.Fatal(err)
	}
	refresh := func() int {
		form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"refresh-conformance"}, "client_id": {"harness-rp"}}
		resp, err := http.Post(w.server.URL+"/token", "application/x-www-form-urlencoded", strings.NewReader(form.Encode())) //nolint:noctx // a test
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode
	}
	if got := refresh(); got != http.StatusOK {
		t.Fatalf("a conformance refresh with the account on = %d", got)
	}
	w.gate.off.Store(true)
	if got := refresh(); got != http.StatusBadRequest {
		t.Errorf("a conformance refresh with the switch thrown = %d, want 400", got)
	}
	if _, live, _ := w.iss.Sessions().ByID(t.Context(), session.ID); live {
		t.Error("the conformance session survived the switch")
	}
}

func TestAConformanceSessionIsRefusedForAClientThatIsNotAHarness(t *testing.T) {
	t.Parallel()
	w := newPasswordWorld(t, pwOptions{conformance: true})
	if _, err := w.iss.Sessions().Record(t.Context(), issuer.Opened{
		Identity: conformanceSubject, ClientID: "app", How: issuer.HowCode, Method: issuer.ConformanceHow,
		Token: "refresh-elsewhere", Scopes: []string{"openid"},
	}); err != nil {
		t.Fatal(err)
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"refresh-elsewhere"}, "client_id": {"app"}}
	resp, err := http.Post(w.server.URL+"/token", "application/x-www-form-urlencoded", strings.NewReader(form.Encode())) //nolint:noctx // a test
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusOK {
		t.Error("a conformance session was refreshed at a client that is not a harness's")
	}
}

func TestRefusalsAreThrottledPerSourceBeforeTheNextStart(t *testing.T) {
	t.Parallel()
	w := newPasswordWorld(t, pwOptions{})
	b := newBrowser(t, w.server)
	state := stateOf(t, openChooser(t, b, "app"))
	for range throttle.DefaultPerSource {
		if status, _ := signInWith(t, b, state, "recovery", "wrong"); status != http.StatusForbidden {
			t.Fatalf("a refusal = %d", status)
		}
	}
	status, body := signInWith(t, b, state, "recovery", recoveryPassword)
	if status != http.StatusTooManyRequests {
		t.Errorf("the right password after the limit = %d %v, want 429", status, body)
	}
}

// There is no route that takes a password: not the old form post, not a JSON
// field, not a header.
func TestNoPlaintextPasswordPathRemains(t *testing.T) {
	t.Parallel()
	w := newPasswordWorld(t, pwOptions{})
	b := newBrowser(t, w.server)
	state := stateOf(t, openChooser(t, b, "app"))

	// The recovery form post of the token door has no token recovery behind it.
	status, _, _, _ := b.post("/login/recovery", url.Values{"state": {state}, "proof": {recoveryPassword}})
	if status != http.StatusNotFound {
		t.Errorf("POST /login/recovery with a password = %d, want 404", status)
	}
	// The exchange refuses a request that carries a password in a field, without
	// starting anything.
	status, body := b.postJSON("/login/srp/start", map[string]string{"state": state, "username": "recovery", "password": recoveryPassword})
	if status != http.StatusBadRequest {
		t.Errorf("a start with a password field = %d %v, want 400", status, body)
	}
	status, body = b.postJSON("/login/srp/finish", map[string]string{"state": state, "handle": "x", "password": recoveryPassword})
	if status != http.StatusBadRequest {
		t.Errorf("a finish with a password field = %d %v, want 400", status, body)
	}
	// A form-encoded body is not read by the exchange at all.
	status, _, _, _ = b.post("/login/srp/start", url.Values{"state": {state}, "username": {"recovery"}, "password": {recoveryPassword}})
	if status != http.StatusUnsupportedMediaType {
		t.Errorf("a form post to the exchange = %d, want 415", status)
	}
	// No input of the page carries a name, so a browser that does not run the
	// script submits nothing.
	page := openChooser(t, newBrowser(t, w.server), "app")
	if regexp.MustCompile(`<input[^>]*type="password"[^>]*name=`).MatchString(page) ||
		regexp.MustCompile(`<input[^>]*name="(password|proof)"`).MatchString(page) {
		t.Errorf("the page has a named password input: %s", page)
	}
}

// The service refuses to start when the policy would make the conformance
// account an operator.
func TestThePolicyMayNotMakeTheConformanceAccountAnOperator(t *testing.T) {
	t.Parallel()
	build := func(text string) *policy.Set {
		declared, err := policy.Parse([]byte(text))
		if err != nil {
			t.Fatal(err)
		}
		set, err := policy.NewSet(declared)
		if err != nil {
			t.Fatal(err)
		}
		return set
	}
	if err := issuer.ConformanceSubject(build(passwordPolicy), conformanceSubject); err != nil {
		t.Errorf("a policy that grants it nothing: %v", err)
	}
	operatorsToo := passwordPolicy + `
`
	operatorsToo = strings.Replace(operatorsToo, "      - service_account: { namespace: sluis, name: recovery }",
		"      - service_account: { namespace: sluis, name: recovery }\n      - service_account: { namespace: sluis, name: conformance }", 1)
	if err := issuer.ConformanceSubject(build(operatorsToo), conformanceSubject); err == nil {
		t.Error("a policy that makes the conformance account an operator was accepted")
	}
}

func TestTheScriptIsServedFromTheIssuersOrigin(t *testing.T) {
	t.Parallel()
	w := newPasswordWorld(t, pwOptions{})
	resp, err := http.Get(w.server.URL + "/login/srp.js") //nolint:noctx // a test
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/javascript") {
		t.Errorf("the script: %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if csp := resp.Header.Get("Content-Security-Policy"); csp != "" && strings.Contains(csp, "unsafe-eval") {
		t.Errorf("the script is served under %q", csp)
	}
}

// The sweep that follows the console's turn-off of recovery ends what the
// conformance account opened too, and nobody else's.
func TestTheRecoverySweepEndsConformanceSessionsToo(t *testing.T) {
	t.Parallel()
	w := newPasswordWorld(t, pwOptions{conformance: true})
	ctx := t.Context()
	conformance, err := w.iss.Sessions().Record(ctx, issuer.Opened{
		Identity: conformanceSubject, ClientID: "harness-rp", How: issuer.HowCode, Method: issuer.ConformanceHow, Token: "t-conformance", Scopes: []string{"openid"},
	})
	if err != nil {
		t.Fatal(err)
	}
	person, err := w.iss.Sessions().Record(ctx, issuer.Opened{
		Identity: "ada@north.example", ClientID: "app", How: issuer.HowCode, Method: "google", Token: "t-person", Scopes: []string{"openid"},
	})
	if err != nil {
		t.Fatal(err)
	}
	signIn, _, err := w.iss.SSO().Begin(ctx, conformanceSubject, issuer.ConformanceHow)
	if err != nil {
		t.Fatal(err)
	}
	ended, err := w.iss.EndRecovery(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ended.SignIns != 1 || ended.Sessions != 1 {
		t.Errorf("ended = %+v, want the conformance sign-in and session", ended)
	}
	if _, live, _ := w.iss.Sessions().ByID(ctx, conformance.ID); live {
		t.Error("the conformance session survived the sweep")
	}
	if _, live, _ := w.iss.SSO().Get(ctx, signIn.ID); live {
		t.Error("the conformance sign-in survived the sweep")
	}
	if _, live, _ := w.iss.Sessions().ByID(ctx, person.ID); !live {
		t.Error("a person's session went with it")
	}
}
