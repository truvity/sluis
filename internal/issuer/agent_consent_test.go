package issuer_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/demo"
	"github.com/truvity/sluis/internal/issuer"
)

// The agent consent page (docs/decisions/0040-agent-class-sessions.md,
// decision 6): an agent-class authorization never completes silently, and
// completes only on an acceptance made, after authentication, in the
// browser the page was shown in.

// consentKey is the sign-in state key every sign-in server in these tests
// is built with ([signInServerWith]), so a test can mint the states an
// attacker could mint with it -- which is to say, none it can use.
const consentKey = "a-test-key-for-signing-state"

// consentCookie is the acceptance cookie's name on a plain-HTTP test
// server.
const consentCookie = access.AgentConsentCookieName

// consentPolicy is the demonstration policy with an agent client.
func consentPolicy() string {
	return strings.Replace(demo.Policy, "clients:\n", "clients:\n"+
		"  "+agentClient+": { kind: public, redirects: [http://localhost:8000/callback], "+
		"requires: [devel:k8s:viewer], session: agent }\n", 1)
}

// agentAuthorize starts an authorization for the agent client and returns
// its request id, and what /login answered: status, where and body.
func agentAuthorize(b *browser, extra string) (request string, status int, location, body string) {
	b.t.Helper()

	sum := sha256.Sum256([]byte(pkceVerifier))
	query := url.Values{
		"client_id":             {agentClient},
		"redirect_uri":          {"http://localhost:8000/callback"},
		"response_type":         {"code"},
		"scope":                 {"openid offline_access"},
		"state":                 {"agent-test"},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
	}

	status, where, body := b.do(http.MethodGet, "/authorize?"+query.Encode()+extra)
	if status != http.StatusFound {
		b.t.Fatalf("/authorize: %d %s", status, body)
	}

	to, err := url.Parse(where)
	if err != nil || to.Query().Get("auth") == "" {
		b.t.Fatalf("/authorize sent the browser to %q, want the login page with a request", where)
	}

	status, location, body = b.do(http.MethodGet, where)

	return to.Query().Get("auth"), status, location, body
}

// providerRoundTrip follows a start link for request to the provider and
// back, as a browser whose provider answers silently would, and returns
// what the callback answered.
func providerRoundTrip(b *browser, request string) (status int, location, body string, header http.Header) {
	b.t.Helper()

	status, toProvider, _ := b.do(http.MethodGet, "/login/google/start?auth="+url.QueryEscape(request))
	if status != http.StatusFound {
		b.t.Fatalf("provider start: %d", status)
	}

	state := toProvider[strings.Index(toProvider, "state=")+len("state="):]

	return b.send(http.MethodGet, "/login/google/callback?code=x&state="+state, nil)
}

var consentToken = regexp.MustCompile(`name="state" value="([^"]+)"`)

// tokenOn reads the acceptance token off a consent page, or fails.
func tokenOn(t *testing.T, page string) string {
	t.Helper()

	match := consentToken.FindStringSubmatch(page)
	if match == nil || !strings.Contains(page, `action="/login/agent-consent"`) {
		t.Fatalf("not a consent page: %s", page)
	}

	return match[1]
}

// accept posts the consent page's form.
func accept(b *browser, token string) (status int, location, body string, header http.Header) {
	b.t.Helper()

	return b.post("/login/agent-consent", url.Values{"state": {token}})
}

// completed reports whether a request has been completed: whether the
// library's callback for it hands out a code. Asked from a browser of its
// own, so nothing the test's browsers hold is involved.
func completed(t *testing.T, b *browser, request string) bool {
	t.Helper()

	_, where, _ := newBrowser(t, b.server).do(http.MethodGet, "/authorize/callback?id="+url.QueryEscape(request))

	return strings.Contains(where, "code=")
}

// The whole of it, from a browser with no sign-in: the chooser is shown
// rather than skipped, the provider round trip ends on the consent page
// rather than at the client, and the accept completes the request, whose
// code opens an agent-class chain.
func TestAnAgentAuthorizationCompletesOnlyOnTheConsentPage(t *testing.T) {
	t.Parallel()

	server, iss := signInServerWith(t, "ada@north.example", consentPolicy())
	b := newBrowser(t, server)

	request, status, _, body := agentAuthorize(b, "")
	if status != http.StatusOK || !strings.Contains(body, "/login/google/start") {
		t.Fatalf("/login for an agent client: %d %s, want the chooser rendered rather than skipped", status, body)
	}

	status, where, page, header := providerRoundTrip(b, request)
	if status != http.StatusOK {
		t.Fatalf("the callback answered %d to %q, want the consent page", status, where)
	}

	token := tokenOn(t, page)
	for _, want := range []string{agentClient, "a program on this computer", "agent connection", "Signed in as <strong>ada@north.example</strong>", "at the latest"} {
		if !strings.Contains(page, want) {
			t.Errorf("the consent page does not say %q: %s", want, page)
		}
	}

	// The computed deadline: thirty days from this sign-in.
	if deadline := time.Now().Add(30 * day).UTC().Format("2 January 2006"); !strings.Contains(page, deadline) {
		t.Errorf("the consent page does not show the deadline %s: %s", deadline, page)
	}

	if b.cookies[consentCookie] != token {
		t.Errorf("the acceptance cookie holds %q, want the page's token", b.cookies[consentCookie])
	}

	if header.Get("X-Frame-Options") != "DENY" {
		t.Errorf("X-Frame-Options = %q", header.Get("X-Frame-Options"))
	}

	if completed(t, b, request) {
		t.Fatal("the request completed before the person accepted")
	}

	status, where, _, _ = accept(b, token)
	if status != http.StatusFound || !strings.Contains(where, "/authorize/callback") {
		t.Fatalf("the accept answered %d to %q, want the library's callback", status, where)
	}

	if _, kept := b.cookies[consentCookie]; kept {
		t.Error("the acceptance cookie was not spent")
	}

	tokens := redeemFor(t, b, where, agentClient)
	if _, has := tokens["refresh_token"]; !has {
		t.Fatalf("no refresh token: %v", tokens)
	}

	sessions, err := iss.Sessions().List(t.Context(), issuer.Query{Identity: "ada@north.example", ClientID: agentClient})
	if err != nil || len(sessions) != 1 || !sessions[0].Agent() {
		t.Fatalf("sessions = %+v, %v; want one agent-class chain", sessions, err)
	}

	// And a browser that is signed in is not completed silently either: it
	// is shown the page.
	request, status, where, body = agentAuthorize(b, "")
	if status != http.StatusOK {
		t.Fatalf("a signed-in browser's agent request answered %d to %q, want the consent page", status, where)
	}

	tokenOn(t, body)

	if completed(t, b, request) {
		t.Error("a signed-in browser's agent request completed silently")
	}
}

// N1, the start link: an attacker begins /authorize for an agent client and
// sends the victim the start link (or the login link). The victim's
// provider -- or their browser's sign-in -- answers silently, and before
// this the code went to the attacker's client. Now the victim's browser
// ends on the consent page, and the request is not completed.
func TestAStartLinkOpenedInAnotherBrowserDoesNotComplete(t *testing.T) {
	t.Parallel()

	server, _ := signInServerWith(t, "ada@north.example", consentPolicy())
	attacker := newBrowser(t, server)
	victim := newBrowser(t, server)
	victim.signIn() // a live sign-in, and a provider that answers silently

	request, _, _, _ := agentAuthorize(attacker, "")

	status, where, page, _ := providerRoundTrip(victim, request)
	if status != http.StatusOK || strings.Contains(where, "/authorize/callback") {
		t.Fatalf("the start link answered %d to %q in the victim's browser, want the consent page", status, where)
	}

	tokenOn(t, page)

	status, where, page = victim.do(http.MethodGet, "/login?auth="+url.QueryEscape(request))
	if status != http.StatusOK || strings.Contains(where, "/authorize/callback") {
		t.Fatalf("the login link answered %d to %q in the victim's browser, want the consent page", status, where)
	}

	tokenOn(t, page)

	// The attacker holds the request and neither the token nor the cookie.
	if status, _, _, _ = accept(attacker, ""); status == http.StatusFound {
		t.Error("an empty accept from the attacker's browser was taken")
	}

	if completed(t, attacker, request) {
		t.Error("the attacker's request completed as the victim")
	}
}

// N1, the accept: an accept made in the attacker's browser never completes
// the victim's request -- not with the victim's token alone, not with it
// beside the attacker's own acceptance cookie, and not with the victim's
// token copied into a cookie of the attacker's own, since the token is
// bound to the victim's sign-in. Only the victim's own accept completes
// it.
func TestAnAcceptFromTheAttackersBrowserDoesNotCompleteTheVictimsRequest(t *testing.T) {
	t.Parallel()

	server, _ := signInServerWith(t, "ada@north.example", consentPolicy())
	victim := newBrowser(t, server)
	attacker := newBrowser(t, server)

	request, _, _, _ := agentAuthorize(victim, "")
	_, _, page, _ := providerRoundTrip(victim, request)
	victimToken := tokenOn(t, page)

	// The attacker has a sign-in and an acceptance of their own.
	own, _, _, _ := agentAuthorize(attacker, "")
	_, _, page, _ = providerRoundTrip(attacker, own)
	attackerToken := tokenOn(t, page)

	for name, prepare := range map[string]func(){
		"the victim's token, no cookie": func() { delete(attacker.cookies, consentCookie) },
		"the victim's token beside the attacker's cookie": func() {
			attacker.cookies[consentCookie] = attackerToken
		},
		"the victim's token in a cookie of the attacker's own": func() {
			attacker.cookies[consentCookie] = victimToken
		},
	} {
		prepare()

		if status, where, _, _ := accept(attacker, victimToken); status == http.StatusFound {
			t.Errorf("%s: the accept was taken (%q)", name, where)
		}

		if completed(t, attacker, request) {
			t.Fatalf("%s: the victim's request completed from the attacker's browser", name)
		}
	}

	// The victim's own accept still works: the request was waiting all
	// along, and nothing above spent it.
	if status, where, _, _ := accept(victim, victimToken); status != http.StatusFound || !strings.Contains(where, "/authorize/callback") {
		t.Errorf("the victim's own accept answered %d to %q", status, where)
	}
}

// The consent page refuses to be framed, and so does the accept: a
// one-click page in a frame is a click another page can steal.
func TestTheConsentPageRefusesToBeFramed(t *testing.T) {
	t.Parallel()

	server, _ := signInServerWith(t, "ada@north.example", consentPolicy())
	b := newBrowser(t, server)

	request, _, _, _ := agentAuthorize(b, "")
	_, _, page, header := providerRoundTrip(b, request)
	token := tokenOn(t, page)

	check := func(what string, header http.Header) {
		if got := header.Get("Content-Security-Policy"); !strings.HasPrefix(got, "frame-ancestors 'none'") {
			t.Errorf("%s: Content-Security-Policy = %q, want frame-ancestors 'none'", what, got)
		}
		if got := header.Get("X-Frame-Options"); got != "DENY" {
			t.Errorf("%s: X-Frame-Options = %q, want DENY", what, got)
		}
	}

	check("the page from the callback", header)

	// From the browser's sign-in, too.
	request, _, _, _ = agentAuthorize(b, "")
	status, _, _, header := b.send(http.MethodGet, "/login?auth="+url.QueryEscape(request), nil)
	if status != http.StatusOK {
		t.Fatalf("/login: %d", status)
	}
	check("the page from the browser's sign-in", header)

	_, _, _, header = accept(b, token)
	check("the accept", header)
}

// `prompt=none` for an agent client is answered `consent_required` at the
// client, with a sign-in to complete from or without one.
func TestPromptNoneForAnAgentClientIsConsentRequired(t *testing.T) {
	t.Parallel()

	server, _ := signInServerWith(t, "ada@north.example", consentPolicy())

	for name, signedIn := range map[string]bool{"signed in": true, "not signed in": false} {
		b := newBrowser(t, server)
		if signedIn {
			b.signIn()
		}

		request, status, where, body := agentAuthorize(b, "&prompt=none")
		if status != http.StatusFound {
			t.Fatalf("%s: /login answered %d %s", name, status, body)
		}

		back, err := url.Parse(where)
		if err != nil || !strings.HasPrefix(where, "http://localhost:8000/callback") ||
			back.Query().Get("error") != "consent_required" || back.Query().Get("state") != "agent-test" {
			t.Errorf("%s: sent to %q, want the client's callback with consent_required", name, where)
		}

		if completed(t, b, request) {
			t.Errorf("%s: prompt=none completed an agent request", name)
		}
	}
}

// What is posted must be the acceptance minted for this browser: never the
// login state `start` sets, a legacy four-part state, one signed by
// another key, or one past its lifetime -- even with a cookie that matches
// it, which is what an attacker's own browser can always arrange.
func TestAStaleForeignOrLegacyAcceptanceIsRefused(t *testing.T) {
	t.Parallel()

	server, iss := signInServerWith(t, "ada@north.example", consentPolicy())
	b := newBrowser(t, server)

	request, _, _, _ := agentAuthorize(b, "")
	_, _, page, _ := providerRoundTrip(b, request)
	genuine := tokenOn(t, page)
	actor := access.AgentConsentActor("ada@north.example", b.signInID(iss.SSO()))

	codec := access.NewStateCodec([]byte(consentKey), 0)
	login, _ := codec.Issue(request)
	recovery, _ := codec.IssueAs(access.Binding{Bind: request, Owner: access.RecoveryPurpose})
	foreign, _ := access.NewStateCodec([]byte("another-installation's-key"), 0).IssueAgentConsent(request, actor)
	past := access.NewStateCodec([]byte(consentKey), 0)
	past.SetClock(func() time.Time { return time.Now().Add(-11 * time.Minute) })
	stale, _ := past.IssueAgentConsent(request, actor)

	for name, token := range map[string]string{
		"the login state start sets": login,
		"a recovery form's state":    recovery,
		"a legacy four-part state":   fourPart(request, actor),
		"another installation's":     foreign,
		"a stale acceptance":         stale,
	} {
		b.cookies[consentCookie] = token

		if status, where, _, _ := accept(b, token); status == http.StatusFound {
			t.Errorf("%s: the accept was taken (%q)", name, where)
		}

		if completed(t, b, request) {
			t.Fatalf("%s completed the request", name)
		}
	}

	b.cookies[consentCookie] = genuine
	if status, _, _, _ := accept(b, genuine); status != http.StatusFound {
		t.Errorf("the genuine acceptance answered %d", status)
	}
}

// fourPart is a state as issued before owners were carried, signed with
// the test key: it verifies, with no owner.
func fourPart(bind, actor string) string {
	body := strings.Join([]string{
		base64.RawURLEncoding.EncodeToString([]byte("nonce-0123456789")),
		base64.RawURLEncoding.EncodeToString([]byte(bind)),
		base64.RawURLEncoding.EncodeToString([]byte(actor)),
		strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10),
	}, ":")
	mac := hmac.New(sha256.New, []byte(consentKey))
	mac.Write([]byte(body))

	return body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// The accept re-checks the browser's sign-in: a person who signed out
// between seeing the page and clicking completes nothing.
func TestTheAcceptRechecksTheSignIn(t *testing.T) {
	t.Parallel()

	server, _ := signInServerWith(t, "ada@north.example", consentPolicy())
	b := newBrowser(t, server)

	request, _, _, _ := agentAuthorize(b, "")
	_, _, page, _ := providerRoundTrip(b, request)
	token := tokenOn(t, page)

	if status, _, _ := b.do(http.MethodGet, "/logout"); status != http.StatusFound {
		t.Fatalf("/logout: %d", status)
	}

	if status, where, _, _ := accept(b, token); status != http.StatusForbidden {
		t.Errorf("an accept after signing out answered %d (%q), want 403", status, where)
	}

	if completed(t, b, request) {
		t.Error("an accept after signing out completed the request")
	}
}

// redeemFor is [redeem] for a client of the test's choosing.
func redeemFor(t *testing.T, b *browser, sentTo, clientID string) map[string]any {
	t.Helper()

	for strings.HasPrefix(sentTo, "/") {
		_, sentTo, _ = b.do(http.MethodGet, sentTo)
	}

	back, err := url.Parse(sentTo)
	if err != nil || back.Query().Get("code") == "" {
		t.Fatalf("the browser was sent to %q, want the callback with a code", sentTo)
	}

	status, body := tokenAnswer(t, b.server.URL, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {back.Query().Get("code")},
		"redirect_uri":  {"http://localhost:8000/callback"},
		"client_id":     {clientID},
		"code_verifier": {pkceVerifier},
	})
	if status != http.StatusOK {
		t.Fatalf("redeem answered %d: %v", status, body)
	}

	return body
}

// tokenAnswer posts a form to /token as a public client does, with its
// client_id in the form, and returns the status and the decoded body.
func tokenAnswer(t *testing.T, serverURL string, form url.Values) (int, map[string]any) {
	t.Helper()

	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, serverURL+"/token", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("build the request: %v", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("post /token: %v", err)
	}
	defer func() { _ = response.Body.Close() }()

	var body map[string]any
	if err = json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode the response: %v", err)
	}

	return response.StatusCode, body
}

// The sign-in's record carries the class of the chain it opens and the
// computed deadline (audit catalogue 1.10.0, whose schema the recorder
// validates every record against): agent and thirty days for the agent
// client, interactive and the installation's limit for any other.
func TestTheSignInRecordCarriesTheClassAndDeadline(t *testing.T) {
	t.Parallel()

	rig := newSSORigWith(t, issuer.Config{}, consentPolicy())
	b, _ := rig.signedInBrowser(t)

	_, _, _, page := agentAuthorize(b, "")
	if status, _, _, _ := accept(b, tokenOn(t, page)); status != http.StatusFound {
		t.Fatalf("accept: %d", status)
	}

	byClass := map[string]string{}
	for _, signedIn := range rig.trail.Find("roster.person.signed_in") {
		class, _ := fieldOf(signedIn, "class").(string)
		deadline, _ := fieldOf(signedIn, "deadline").(string)
		byClass[class] = deadline
	}

	for class, lasts := range map[string]time.Duration{"agent": 30 * day, "interactive": issuer.DefaultAbsoluteLifetime} {
		deadline, err := time.Parse(time.RFC3339, byClass[class])
		if err != nil {
			t.Errorf("no %s sign-in with a deadline: %v", class, byClass)
			continue
		}
		if want := time.Now().Add(lasts); deadline.Before(want.Add(-time.Minute)) || deadline.After(want.Add(time.Minute)) {
			t.Errorf("the %s sign-in's deadline is %s, want about %s", class, deadline, want)
		}
	}
}

// DoubleClickjacking: the Allow button is disabled in the markup and armed
// only by the page's own script, half a second after the page is visible,
// and disarmed whenever it is hidden; the script runs by a nonce the
// page's Content-Security-Policy names, so no other script runs there.
func TestTheAllowButtonIsInertUntilThePageHasBeenVisible(t *testing.T) {
	t.Parallel()

	server, _ := signInServerWith(t, "ada@north.example", consentPolicy())
	b := newBrowser(t, server)

	request, _, _, _ := agentAuthorize(b, "")
	_, _, page, header := providerRoundTrip(b, request)
	tokenOn(t, page)

	if !strings.Contains(page, `<button type="submit" id="allow" disabled>`) {
		t.Errorf("the Allow button is not disabled in the markup: %s", page)
	}

	nonce := regexp.MustCompile(`<script nonce="([^"]+)">`).FindStringSubmatch(page)
	if nonce == nil {
		t.Fatalf("no nonce'd script on the page: %s", page)
	}

	if got, want := header.Get("Content-Security-Policy"), "frame-ancestors 'none'; script-src 'nonce-"+nonce[1]+"'"; got != want {
		t.Errorf("Content-Security-Policy = %q, want %q", got, want)
	}

	for _, want := range []string{`visibilitychange`, `document.visibilityState==="visible"`, `b.disabled=false`, `},500);`} {
		if !strings.Contains(page, want) {
			t.Errorf("the script does not arm on visibility after 500 ms (%q): %s", want, page)
		}
	}

	// A fresh nonce on every page.
	request, _, _, _ = agentAuthorize(b, "")
	_, _, again, _ := b.send(http.MethodGet, "/login?auth="+url.QueryEscape(request), nil)
	if other := regexp.MustCompile(`<script nonce="([^"]+)">`).FindStringSubmatch(again); other == nil || other[1] == nonce[1] {
		t.Errorf("the nonce was reused: %v", other)
	}
}
