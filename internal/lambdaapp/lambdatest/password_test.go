package lambdatest_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"html"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/aws/aws-lambda-go/events"

	"github.com/truvity/sluis/internal/lambdaapp/lambdatest"
	"github.com/truvity/sluis/srp"
)

// call sends one HTTP request to the function as the API gateway would, with the
// cookies a browser holds, and returns the answer.
func call(t *testing.T, h *lambdatest.Harness, fn, method, path, query, contentType, body string, cookies map[string]string) events.APIGatewayV2HTTPResponse {
	t.Helper()
	e := events.APIGatewayV2HTTPRequest{
		Version: "2.0", RawPath: path, RawQueryString: query, Body: body,
		Headers: map[string]string{"host": "access.acme.example", "x-forwarded-proto": "https"},
	}
	if contentType != "" {
		e.Headers["content-type"] = contentType
	}
	e.RequestContext.HTTP.Method = method
	e.RequestContext.HTTP.SourceIP = "203.0.113.9"
	for name, value := range cookies {
		e.Cookies = append(e.Cookies, name+"="+value)
	}
	raw, _ := json.Marshal(e)
	res, err := h.Emulator.Invoke(context.Background(), fn, "live", raw)
	if err != nil {
		t.Fatal(err)
	}
	if res.FunctionError != "" {
		t.Fatalf("%s %s: %s", method, path, res.Payload)
	}
	var out events.APIGatewayV2HTTPResponse
	if err = json.Unmarshal(res.Payload, &out); err != nil {
		t.Fatalf("%s %s: %v: %s", method, path, err, res.Payload)
	}
	return out
}

func header(res events.APIGatewayV2HTTPResponse, name string) string {
	for k, v := range res.Headers {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

func keep(cookies map[string]string, res events.APIGatewayV2HTTPResponse) {
	for _, c := range res.Cookies {
		name, rest, _ := strings.Cut(c, "=")
		value, attrs, _ := strings.Cut(rest, ";")
		if value == "" || strings.Contains(attrs, "Max-Age=0") || strings.Contains(attrs, "Max-Age=-1") {
			delete(cookies, name)
			continue
		}
		cookies[name] = value
	}
}

// The Lambda issuer's /authorize leads to a page with the password door, not
// straight to the upstream identity provider, and the recovery account signs in
// there by SRP against the verifier in SSM. The password parameter is never
// read, and no request carries the password (docs/decisions/0076).
func TestTheLambdaIssuerOffersThePasswordDoorAndNeverReadsThePassword(t *testing.T) {
	h := lambdatest.NewHarness(t)
	fn := h.OpenIssuer(t)
	cookies := map[string]string{}

	sum := sha256.Sum256([]byte("a-verifier-long-enough-to-be-a-real-one-0123456789"))
	query := url.Values{
		"client_id": {"console"}, "redirect_uri": {"https://access.acme.example/console/callback"}, "response_type": {"code"},
		"scope": {"openid"}, "state": {"s"}, "code_challenge_method": {"S256"},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])},
	}
	res := call(t, h, fn, "GET", "/authorize", query.Encode(), "", "", cookies)
	where := header(res, "location")
	if res.StatusCode != 302 || !strings.HasPrefix(where, "/login") {
		t.Fatalf("/authorize = %d -> %q", res.StatusCode, where)
	}
	keep(cookies, res)

	loginPath, loginQuery, _ := strings.Cut(where, "?")
	page := call(t, h, fn, "GET", loginPath, loginQuery, "", "", cookies)
	keep(cookies, page)
	if page.StatusCode != 200 || !strings.Contains(page.Body, "/login/srp.js") || !strings.Contains(page.Body, "data-srp") {
		t.Fatalf("the sign-in page = %d %s", page.StatusCode, page.Body)
	}
	state := html.UnescapeString(regexp.MustCompile(`data-state="([^"]+)"`).FindStringSubmatch(page.Body)[1])

	post := func(path string, payload map[string]string) (int, map[string]any) {
		raw, _ := json.Marshal(payload)
		res := call(t, h, fn, "POST", path, "", "application/json", string(raw), cookies)
		keep(cookies, res)
		out := map[string]any{}
		_ = json.Unmarshal([]byte(res.Body), &out)
		return res.StatusCode, out
	}
	status, begun := post("/login/srp/start", map[string]string{"state": state, "username": "recovery"})
	if status != 200 {
		t.Fatalf("start = %d %v", status, begun)
	}
	salt, _ := hex.DecodeString(begun["salt"].(string))
	pub, _ := hex.DecodeString(begun["B"].(string))
	client, _ := srp.Default.NewClient("recovery", lambdatest.RecoveryPassword, rand.Reader)
	m1, err := client.Respond(salt, pub, int(begun["iterations"].(float64)))
	if err != nil {
		t.Fatal(err)
	}
	status, done := post("/login/srp/finish", map[string]string{
		"state": state, "handle": begun["handle"].(string), "A": hex.EncodeToString(client.Public()), "M1": hex.EncodeToString(m1),
	})
	if status != 200 {
		t.Fatalf("finish = %d %v", status, done)
	}
	m2, _ := hex.DecodeString(done["M2"].(string))
	if err = client.VerifyServer(m2); err != nil {
		t.Errorf("the server's proof did not verify: %v", err)
	}
	if next, _ := done["next"].(string); !strings.Contains(next, "/authorize/callback") {
		t.Errorf("next = %q", next)
	}

	for _, read := range h.SSM.Reads() {
		if strings.Contains(read, "recovery-password") || strings.Contains(read, "conformance-password") {
			t.Errorf("the function read %s", read)
		}
	}
	if !contains(h.SSM.Reads(), lambdatest.RecoveryVerifierParam) {
		t.Errorf("the function did not read the verifier: %v", h.SSM.Reads())
	}
	for _, c := range h.Emulator.Calls() {
		if strings.Contains(string(c.Payload), lambdatest.RecoveryPassword) {
			t.Errorf("a request carried the password: %s", c.Payload)
		}
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
