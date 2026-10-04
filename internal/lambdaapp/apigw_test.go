package lambdaapp_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/aws/aws-lambda-go/events"

	"github.com/truvity/sluis/internal/lambdaapp"
)

func event(method, path, query string) events.APIGatewayV2HTTPRequest {
	e := events.APIGatewayV2HTTPRequest{
		Version: "2.0", RawPath: path, RawQueryString: query,
		Headers: map[string]string{"host": "id.example.org", "x-forwarded-proto": "https"},
	}
	e.RequestContext.HTTP.Method = method
	e.RequestContext.HTTP.SourceIP = "203.0.113.9"
	return e
}

func serve(t *testing.T, h http.Handler, e events.APIGatewayV2HTTPRequest) events.APIGatewayV2HTTPResponse {
	t.Helper()
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	out, err := lambdaapp.NewHTTP(h, nil, nil).Handle(context.Background(), raw)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	return out.(events.APIGatewayV2HTTPResponse)
}

func TestAnEventBecomesARequestTheHandlerAnswers(t *testing.T) {
	var got *http.Request
	var body string
	resp := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}), func() events.APIGatewayV2HTTPRequest {
		e := event("POST", "/token", "a=1&b=two%20words")
		e.Body = "grant_type=x"
		e.Headers["content-type"] = "application/x-www-form-urlencoded"
		e.Cookies = []string{"a=1", "b=2"}
		return e
	}())
	if got.Method != "POST" || got.URL.Path != "/token" || got.URL.Query().Get("b") != "two words" {
		t.Errorf("request line: %s %s", got.Method, got.URL)
	}
	if got.Host != "id.example.org" || got.URL.Scheme != "https" || got.RemoteAddr != "203.0.113.9" {
		t.Errorf("host %q scheme %q remote %q", got.Host, got.URL.Scheme, got.RemoteAddr)
	}
	if got.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
		t.Errorf("content type %q", got.Header.Get("Content-Type"))
	}
	if c, err := got.Cookie("b"); err != nil || c.Value != "2" {
		t.Errorf("the cookies array was not made a Cookie header: %q", got.Header.Get("Cookie"))
	}
	if body != "grant_type=x" {
		t.Errorf("body %q", body)
	}
	if resp.StatusCode != 201 || resp.Body != `{"ok":true}` || resp.Headers["Content-Type"] != "application/json" || resp.IsBase64Encoded {
		t.Errorf("response %+v", resp)
	}
}

func TestABase64BodyIsDecodedAndABinaryResponseEncoded(t *testing.T) {
	binary := []byte{0xff, 0xfe, 0x00, 0x01}
	e := event("PUT", "/x", "")
	e.IsBase64Encoded = true
	e.Body = base64.StdEncoding.EncodeToString(binary)
	var got []byte
	resp := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		_, _ = w.Write(binary)
	}), e)
	if string(got) != string(binary) {
		t.Errorf("request body %v", got)
	}
	if !resp.IsBase64Encoded || resp.Body != base64.StdEncoding.EncodeToString(binary) || resp.StatusCode != 200 {
		t.Errorf("response %+v", resp)
	}
}

func TestSetCookieGoesInCookiesAndOtherHeadersAreJoined(t *testing.T) {
	resp := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: "1", HttpOnly: true, Secure: true})
		http.SetCookie(w, &http.Cookie{Name: "csrf", Value: "2"})
		w.Header().Add("Vary", "Cookie")
		w.Header().Add("Vary", "Accept")
		w.Header().Set("Content-Length", "99")
		http.Redirect(w, r, "/console/", http.StatusFound)
	}), event("GET", "/login", ""))
	if len(resp.Cookies) != 2 {
		t.Fatalf("cookies %v", resp.Cookies)
	}
	if _, ok := resp.Headers["Set-Cookie"]; ok {
		t.Error("Set-Cookie is also a header, which the gateway drops")
	}
	if resp.Headers["Vary"] != "Cookie, Accept" {
		t.Errorf("Vary %q", resp.Headers["Vary"])
	}
	if _, ok := resp.Headers["Content-Length"]; ok {
		t.Error("the gateway sets Content-Length itself")
	}
	if resp.StatusCode != 302 || resp.Headers["Location"] != "/console/" {
		t.Errorf("redirect %d %q", resp.StatusCode, resp.Headers["Location"])
	}
}

func TestAHandlerThatPanicsIsA500AndABadBodyA400(t *testing.T) {
	resp := serve(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }), event("GET", "/", ""))
	if resp.StatusCode != 500 {
		t.Errorf("panic: %d", resp.StatusCode)
	}
	bad := event("POST", "/", "")
	bad.IsBase64Encoded, bad.Body = true, "!!not base64!!"
	resp = serve(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("handler called") }), bad)
	if resp.StatusCode != 400 {
		t.Errorf("bad body: %d", resp.StatusCode)
	}
}

func TestAnEventOfAnotherShapeIsRefused(t *testing.T) {
	_, err := lambdaapp.NewHTTP(http.NotFoundHandler(), nil, nil).Handle(context.Background(), json.RawMessage(`{"httpMethod":"GET","path":"/"}`))
	if err == nil {
		t.Error("a REST API event (payload 1.0) was served")
	}
}

func TestSettleRunsAfterEveryRequest(t *testing.T) {
	settled := 0
	h := lambdaapp.NewHTTP(http.NotFoundHandler(), func() { settled++ }, nil)
	raw, _ := json.Marshal(event("GET", "/", ""))
	if _, err := h.Handle(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	if settled != 1 {
		t.Errorf("settled %d times", settled)
	}
}
