package mimic

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A choice resumes the authorization request through every check, the act-as group included: a session without the
// group gets no code, even with a valid token of the chooser. Mimic never shows such a session the chooser, so the
// token is made here, as only mimic's own key can make it.
func TestTheActAsGroupIsCheckedOnAChoice(t *testing.T) {
	dir := t.TempDir()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		return "file:" + p
	}
	const callback = "http://127.0.0.1:9/callback"
	cfg := &Config{
		Environment: Environment, PublicURL: "http://127.0.0.1:8080",
		Keys:  Keys{Signing: write("signing.pem", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), Secret: write("secret", []byte(rand.Text()+rand.Text()))},
		Sluis: Sluis{Issuer: "http://127.0.0.1:9", ClientID: "mimic"},
		Tenants: []Tenant{{
			Name: "example-a", Profile: ProfileEntra, Domain: "example-a.test",
			Clients: []Client{{ID: "rp", RedirectURIs: []string{callback}}},
			Identities: []Identity{
				{Username: "alice", Name: "Alice Example"},
				{Username: "bob", Name: "Bob Example"},
			},
		}},
	}
	s, err := New(cfg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	now := s.now()
	cookie, err := s.keys.seal(SessionCookie, session{Sub: "person-1", Groups: []string{"devel:shared:keycloak:example-b:act-as"}, Via: "person", Auth: now.Unix(), Exp: now.Unix() + 600})
	if err != nil {
		t.Fatal(err)
	}
	request := url.Values{
		"response_type": {"code"}, "client_id": {"rp"}, "redirect_uri": {callback}, "scope": {"openid"},
		"code_challenge": {strings.Repeat("A", 43)}, "code_challenge_method": {"S256"}, "state": {"st-1"},
	}.Encode()
	post := func(value string) *httptest.ResponseRecorder {
		form := url.Values{"request": {request}, "login_hint": {"alice"}, "token": {s.keys.choiceToken(value, "/entra/example-a", request)}}
		req := httptest.NewRequest(http.MethodPost, "/entra/example-a/authorize/choose", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(&http.Cookie{Name: SessionCookie, Value: value}) //nolint:gosec // the cookie of a request, not one that is set
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		return w
	}
	w := post(cookie)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("answered %d: %s", w.Code, w.Body)
	}
	loc, _ := url.Parse(w.Header().Get("Location"))
	if q := loc.Query(); !strings.HasPrefix(loc.String(), callback) || q.Get("error") != "access_denied" || q.Get("code") != "" || q.Get("state") != "st-1" {
		t.Fatalf("not refused with access_denied: %s", loc)
	}
}
