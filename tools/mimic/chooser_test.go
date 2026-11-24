package mimic_test

import (
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/truvity/sluis/v2/tools/mimic"
	"github.com/truvity/sluis/v2/tools/mimic/mimictest"
)

// choice is one form of the identity chooser.
type choice struct {
	action string
	fields url.Values
	label  string
}

var (
	formRE   = regexp.MustCompile(`(?s)<form method="post" action="([^"]*)">(.*?)</form>`)
	hiddenRE = regexp.MustCompile(`<input type="hidden" name="([^"]*)" value="([^"]*)">`)
	buttonRE = regexp.MustCompile(`<button type="submit">([^<]*)</button>`)
)

// chooser runs an authorization request without a login_hint and returns the chooser's forms, by username.
func (r *rp) chooser(b *http.Client, edit func(url.Values)) (*http.Response, string, map[string]choice) {
	r.t.Helper()
	q := url.Values{
		"response_type": {"code"}, "client_id": {r.client}, "redirect_uri": {mimictest.Callback}, "scope": {"openid email profile"},
		"state": {"st-1"}, "nonce": {"n-1"}, "code_challenge": {challenge(r.verifier)}, "code_challenge_method": {"S256"},
	}
	if edit != nil {
		edit(q)
	}
	res, err := b.Get(r.issuer + "/authorize?" + q.Encode())
	if err != nil {
		r.t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		r.t.Fatalf("authorize answered %d, not the chooser (Location %q)", res.StatusCode, res.Header.Get("Location"))
	}
	forms := map[string]choice{}
	for _, f := range formRE.FindAllStringSubmatch(string(body), -1) {
		c := choice{action: html.UnescapeString(f[1]), fields: url.Values{}}
		for _, h := range hiddenRE.FindAllStringSubmatch(f[2], -1) {
			c.fields.Set(html.UnescapeString(h[1]), html.UnescapeString(h[2]))
		}
		if l := buttonRE.FindStringSubmatch(f[2]); l != nil {
			c.label = l[1]
		}
		forms[c.fields.Get("login_hint")] = c
	}
	return res, string(body), forms
}

// choose posts a choice to mimic and returns the answer, without following a redirect to the callback.
func choose(t *testing.T, m *mimictest.Mimic, b *http.Client, c choice) *http.Response {
	t.Helper()
	res, err := b.PostForm(m.URL+c.action, c.fields)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	return res
}

func callbackQuery(t *testing.T, res *http.Response) url.Values {
	t.Helper()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("the choice answered %d, not a redirect to the callback", res.StatusCode)
	}
	loc, err := url.Parse(res.Header.Get("Location"))
	if err != nil || !strings.HasPrefix(loc.String(), mimictest.Callback) {
		t.Fatalf("the choice redirected to %q", res.Header.Get("Location"))
	}
	return loc.Query()
}

func TestAPersonChoosesAnIdentity(t *testing.T) {
	m := mimictest.Start(t, nil)
	r := newRP(t, m, "entra", "example-a")
	b := m.HandOff("person-1", []string{actA})

	t.Run("the chooser lists the tenant's identities", func(t *testing.T) {
		res, body, forms := r.chooser(b, nil)
		if ct := res.Header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
			t.Errorf("Content-Type %q", ct)
		}
		if csp := res.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "frame-ancestors 'none'") {
			t.Errorf("Content-Security-Policy %q", csp)
		}
		if res.Header.Get("X-Frame-Options") != "DENY" || res.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("headers %v", res.Header)
		}
		if len(forms) != 2 || forms["alice"].label != "Alice Example (alice)" || forms["bob"].label != "Bob Example (bob)" {
			t.Fatalf("forms %v", forms)
		}
		for _, f := range forms {
			if f.action != "/entra/example-a/authorize/choose" || f.fields.Get("token") == "" || f.fields.Get("request") == "" {
				t.Fatalf("form %v", f)
			}
		}
		if strings.Contains(body, "<script") || strings.Contains(body, "<link") || strings.Contains(body, "src=") {
			t.Errorf("the page loads something: %s", body)
		}
	})

	t.Run("a choice resumes the request and returns a code for that identity", func(t *testing.T) {
		_, _, forms := r.chooser(b, nil)
		q := callbackQuery(t, choose(t, m, b, forms["bob"]))
		if q.Get("error") != "" || q.Get("state") != "st-1" || q.Get("iss") != r.issuer {
			t.Fatalf("callback %v", q)
		}
		c := r.claims(r.redeem(q.Get("code"))["id_token"].(string), "JWT")
		if c["name"] != "Bob Example" || c["email"] != "bob.builder@example-a.test" || c["nonce"] != "n-1" {
			t.Fatalf("claims %v", c)
		}
		if act, _ := c["act"].(map[string]any); act["sub"] != "person-1" {
			t.Fatalf("act = %v", c["act"])
		}
		// The same identity by login_hint has the same subject.
		if hinted := r.signIn(b, "bob"); hinted["sub"] != c["sub"] {
			t.Fatalf("sub %v by choice, %v by login_hint", c["sub"], hinted["sub"])
		}
	})

	t.Run("prompt=none is still refused", func(t *testing.T) {
		q := r.authorize(b, func(q url.Values) { q.Set("prompt", "none") })
		if q.Get("error") != "account_selection_required" || q.Get("code") != "" || q.Get("state") != "st-1" {
			t.Fatalf("not refused with account_selection_required: %v", q)
		}
	})

	t.Run("a choice without the session's token is refused", func(t *testing.T) {
		_, _, forms := r.chooser(b, nil)
		good := forms["alice"]
		other := m.HandOff("person-1", []string{actA}) // another session of the same subject
		_, _, otherForms := r.chooser(other, nil)
		for name, c := range map[string]struct {
			b    *http.Client
			edit func(url.Values)
		}{
			"no token":        {b, func(v url.Values) { v.Del("token") }},
			"a wrong token":   {b, func(v url.Values) { v.Set("token", strings.Repeat("A", 43)) }},
			"not base64url":   {b, func(v url.Values) { v.Set("token", "%%%") }},
			"another's token": {b, func(v url.Values) { v.Set("token", otherForms["alice"].fields.Get("token")) }},
			"another request": {b, func(v url.Values) {
				q, _ := url.ParseQuery(v.Get("request"))
				q.Set("state", "st-attacker")
				v.Set("request", q.Encode())
			}},
			"no session":      {http.DefaultClient, nil},
			"another session": {other, nil},
		} {
			t.Run(name, func(t *testing.T) {
				f := choice{action: good.action, fields: url.Values{}}
				for k, v := range good.fields {
					f.fields[k] = append([]string(nil), v...)
				}
				if c.edit != nil {
					c.edit(f.fields)
				}
				if res := choose(t, m, c.b, f); res.StatusCode != http.StatusForbidden {
					t.Fatalf("answered %d (Location %q)", res.StatusCode, res.Header.Get("Location"))
				}
			})
		}
	})

	t.Run("a choice of an identity the tenant does not have", func(t *testing.T) {
		_, _, forms := r.chooser(b, nil)
		c := forms["alice"]
		c.fields.Set("login_hint", "mallory")
		if q := callbackQuery(t, choose(t, m, b, c)); q.Get("error") != "access_denied" || q.Get("code") != "" {
			t.Fatalf("not refused: %v", q)
		}
	})

}

func TestTheChooserEscapesWhatItShows(t *testing.T) {
	m := mimictest.Start(t, func(c *mimic.Config) { c.Tenants[0].Identities[0].Name = `<script>alert("x")</script> & co` })
	r := newRP(t, m, "entra", "example-a")
	_, body, forms := r.chooser(m.HandOff("person-1", []string{actA}), func(q url.Values) { q.Set("state", `"><script>x</script>`) })
	if strings.Contains(body, "<script") {
		t.Fatalf("the page carries markup from config or the request: %s", body)
	}
	if got := html.UnescapeString(forms["alice"].label); got != `<script>alert("x")</script> & co (alice)` {
		t.Fatalf("label %q", got)
	}
}

func TestTheChooserIsShownOnlyAfterTheChecks(t *testing.T) {
	m := mimictest.Start(t, nil)
	r := newRP(t, m, "entra", "example-a")
	// Without the act-as group the request is refused, and no chooser is shown.
	if q := r.authorize(m.HandOff("person-1", []string{actB}), nil); q.Get("error") != "access_denied" {
		t.Fatalf("not refused: %v", q)
	}
	// Without PKCE the request is refused before the chooser.
	if q := r.authorize(m.HandOff("person-1", []string{actA}), func(q url.Values) { q.Del("code_challenge") }); q.Get("error") != "invalid_request" {
		t.Fatalf("not refused: %v", q)
	}
}
