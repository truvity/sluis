package issuer_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/truvity/sluis/internal/demo"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/policy"
)

const (
	testBackupARN  = "arn:aws:lambda:eu-west-1:111122223333:function:example-backup"
	testRestoreARN = "arn:aws:lambda:eu-west-1:111122223333:function:example-restore"
)

func configurationRequest(t *testing.T, server *httptest.Server, method, bearer string) (int, map[string]any, http.Header) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, server.URL+issuer.SluisConfigurationPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body, resp.Header
}

// signedInToken is a real access token of the issuer under test.
func signedInToken(t *testing.T, server *httptest.Server) string {
	t.Helper()
	status, body := exchange(t, server, "k8s:devel/identity-system/authorization-webhook", "directory-roster")
	if status != http.StatusOK {
		t.Fatalf("exchange = %d, %v", status, body)
	}
	token, _ := body["access_token"].(string)
	return token
}

// The document names the installation's functions, so it is for callers the
// issuer signed in and for nobody else.
func TestTheSluisConfigurationIsNotServedToAnonymousCallers(t *testing.T) {
	t.Parallel()
	server, iss := serveIssuer(t)
	iss.UseConfiguration(issuer.SluisConfiguration{Instance: "example",
		Backup: &issuer.SluisConfigEndpoint{Endpoint: testBackupARN}})

	for name, bearer := range map[string]string{"no token": "", "a made-up token": "not-a-token", "a forged one": "eyJhbGciOiJub25lIn0.e30."} {
		status, body, header := configurationRequest(t, server, http.MethodGet, bearer)
		if status != http.StatusUnauthorized {
			t.Errorf("%s: status %d", name, status)
		}
		if header.Get("WWW-Authenticate") == "" {
			t.Errorf("%s: no WWW-Authenticate", name)
		}
		if _, leaked := body["backup"]; leaked || body["instance"] != nil {
			t.Errorf("%s: the document leaked: %v", name, body)
		}
	}
}

func TestTheSluisConfigurationAnswersTheInstallationsDocument(t *testing.T) {
	t.Parallel()
	server, iss := serveIssuer(t)
	iss.UseConfiguration(issuer.SluisConfiguration{
		Instance: "example", Region: "eu-west-1",
		Backup:  &issuer.SluisConfigEndpoint{Endpoint: testBackupARN},
		Restore: &issuer.SluisConfigEndpoint{Endpoint: testRestoreARN},
	})
	status, body, header := configurationRequest(t, server, http.MethodGet, signedInToken(t, server))
	if status != http.StatusOK {
		t.Fatalf("status %d: %v", status, body)
	}
	if body["issuer"] != "http://issuer.example" || body["instance"] != "example" || body["region"] != "eu-west-1" {
		t.Errorf("document = %v", body)
	}
	backup, _ := body["backup"].(map[string]any)
	restore, _ := body["restore"].(map[string]any)
	if backup["endpoint"] != testBackupARN || restore["endpoint"] != testRestoreARN {
		t.Errorf("endpoints = %v, %v", backup, restore)
	}
	if header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", header.Get("Cache-Control"))
	}
}

func TestTheSluisConfigurationIsAbsentWhereTheIssuerKnowsNoFunction(t *testing.T) {
	t.Parallel()
	server, _ := serveIssuer(t)
	if status, _, _ := configurationRequest(t, server, http.MethodGet, signedInToken(t, server)); status != http.StatusNotFound {
		t.Errorf("status %d, want 404", status)
	}
}

func TestTheSluisConfigurationAnswersOnlyReads(t *testing.T) {
	t.Parallel()
	server, iss := serveIssuer(t)
	iss.UseConfiguration(issuer.SluisConfiguration{Instance: "example"})
	if status, _, _ := configurationRequest(t, server, http.MethodPost, signedInToken(t, server)); status == http.StatusOK {
		t.Errorf("a POST was answered")
	}
}

// BearerReader reads a person's laptop token and refuses every other: a token
// minted for another client, a forged one, and none.
func TestBearerReaderNamesTheSubjectOfATokenForAKnownClientOnly(t *testing.T) {
	t.Parallel()
	declared, err := policy.Parse([]byte(demo.Policy))
	if err != nil {
		t.Fatal(err)
	}
	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatal(err)
	}
	iss := issuer.New(issuer.Config{URL: "http://issuer.example", AllowInsecure: true}, set, &fakeDirectory{}, issuer.NewMemoryState())
	storage, err := issuer.NewTestStorage(iss, fakeVerifier{}, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	h, err := handler(iss, storage)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	token := signedInToken(t, server)

	request := func(bearer string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/sluis.v1.BackupService/RunBackup", nil)
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		return r
	}
	// The token is for the client `directory-roster`.
	read := issuer.BearerReader(iss, storage, []string{"directory-roster"})
	if subject, ok := read(request(token)); !ok || subject == "" {
		t.Errorf("a token for a client in the list: %q, %v", subject, ok)
	}
	if _, ok := issuer.BearerReader(iss, storage, issuer.CLIAudiences)(request(token)); ok {
		t.Error("a token for another client was read as a laptop's")
	}
	for name, bearer := range map[string]string{"none": "", "made up": "x.y.z", "tampered": token[:len(token)-3] + "AAA"} {
		if _, ok := read(request(bearer)); ok {
			t.Errorf("%s was read", name)
		}
	}
}
