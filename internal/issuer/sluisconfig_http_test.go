package issuer_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

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

// sluisctlPolicy declares the CLI the way an installation does: the client
// whose sign-in the console and the discovery document accept.
var sluisctlPolicy = strings.Replace(demo.Policy, "clients:\n",
	"clients:\n  sluisctl: { kind: public, redirects: [http://127.0.0.1/callback], requires: [devel:k8s:viewer, mgmt:k8s:admin], sign_in_exchange: true }\n", 1)

// sluisctlRig is the real issuer under that policy, with its storage.
type sluisctlRig struct {
	server  *httptest.Server
	iss     *issuer.Issuer
	storage *issuer.Storage
}

func newSluisctlRig(t *testing.T) *sluisctlRig {
	t.Helper()
	declared, err := policy.Parse([]byte(sluisctlPolicy))
	if err != nil {
		t.Fatal(err)
	}
	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatal(err)
	}
	iss := issuer.New(issuer.Config{URL: "http://issuer.example", AllowInsecure: true}, set, adaDirectory(), issuer.NewMemoryState())
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
	return &sluisctlRig{server: server, iss: iss, storage: storage}
}

// tokens signs ada in at client and answers the tokens of a refresh.
func (r *sluisctlRig) tokens(t *testing.T, client string) map[string]any {
	t.Helper()
	return signedInTokens(t, r.server, r.iss, client)
}

func (r *sluisctlRig) access(t *testing.T, client string) string {
	t.Helper()
	token, _ := r.tokens(t, client)["access_token"].(string)
	return token
}

func (r *sluisctlRig) revokeAll(t *testing.T) {
	t.Helper()
	sessions, err := r.iss.Sessions().List(t.Context(), issuer.Query{Identity: "ada@north.example"})
	if err != nil || len(sessions) == 0 {
		t.Fatalf("list the sessions: %v %v", sessions, err)
	}
	for i := range sessions {
		if _, err = r.iss.Sessions().RevokeID(t.Context(), sessions[i].ID); err != nil {
			t.Fatal(err)
		}
	}
}

// The document names the installation's functions, so it is for a person
// signed in at the CLI and for nobody else. Everyone else sees the 404 of an
// issuer with nothing to say.
func TestTheSluisConfigurationRevealsNothingToAnyoneButALiveCLISignIn(t *testing.T) {
	t.Parallel()
	rig := newSluisctlRig(t)
	rig.iss.UseConfiguration(issuer.SluisConfiguration{Instance: "example",
		Backup: &issuer.SluisConfigEndpoint{Endpoint: testBackupARN}})

	exchanged := func() string {
		_, body := exchange(t, rig.server, "k8s:devel/identity-system/authorization-webhook", "directory-roster")
		token, _ := body["access_token"].(string)
		return token
	}
	for name, bearer := range map[string]string{
		"no token":                   "",
		"a made-up token":            "not-a-token",
		"a forged one":               "eyJhbGciOiJub25lIn0.e30.",
		"a workload's exchanged one": exchanged(),
		"another client's sign-in":   rig.access(t, "local-dev"),
		"the CLI's ID token":         func() string { s, _ := rig.tokens(t, "sluisctl")["id_token"].(string); return s }(),
	} {
		status, body, _ := configurationRequest(t, rig.server, http.MethodGet, bearer)
		if status != http.StatusNotFound {
			t.Errorf("%s: status %d", name, status)
		}
		if body["backup"] != nil || body["instance"] != nil {
			t.Errorf("%s: the document leaked: %v", name, body)
		}
	}
}

func TestTheSluisConfigurationAnswersTheInstallationsDocument(t *testing.T) {
	t.Parallel()
	rig := newSluisctlRig(t)
	rig.iss.UseConfiguration(issuer.SluisConfiguration{
		Instance: "example", Region: "eu-west-1",
		Backup:  &issuer.SluisConfigEndpoint{Endpoint: testBackupARN},
		Restore: &issuer.SluisConfigEndpoint{Endpoint: testRestoreARN},
	})
	status, body, header := configurationRequest(t, rig.server, http.MethodGet, rig.access(t, "sluisctl"))
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

func TestTheSluisConfigurationStopsWithTheSession(t *testing.T) {
	t.Parallel()
	rig := newSluisctlRig(t)
	rig.iss.UseConfiguration(issuer.SluisConfiguration{Instance: "example"})
	token := rig.access(t, "sluisctl")
	if status, _, _ := configurationRequest(t, rig.server, http.MethodGet, token); status != http.StatusOK {
		t.Fatalf("before: %d", status)
	}
	rig.revokeAll(t)
	if status, _, _ := configurationRequest(t, rig.server, http.MethodGet, token); status != http.StatusNotFound {
		t.Errorf("after the session ended: %d", status)
	}
}

func TestTheSluisConfigurationIsAbsentWhereTheIssuerKnowsNoFunction(t *testing.T) {
	t.Parallel()
	rig := newSluisctlRig(t)
	if status, _, _ := configurationRequest(t, rig.server, http.MethodGet, rig.access(t, "sluisctl")); status != http.StatusNotFound {
		t.Errorf("status %d, want 404", status)
	}
}

func TestTheSluisConfigurationAnswersOnlyReads(t *testing.T) {
	t.Parallel()
	rig := newSluisctlRig(t)
	rig.iss.UseConfiguration(issuer.SluisConfiguration{Instance: "example"})
	if status, _, _ := configurationRequest(t, rig.server, http.MethodPost, rig.access(t, "sluisctl")); status == http.StatusOK {
		t.Errorf("a POST was answered")
	}
}

// CLISubject names the person of a live CLI sign-in and refuses everything else
// this issuer signs.
func TestCLISubjectIsALiveCLISignInAndNothingElse(t *testing.T) {
	t.Parallel()
	clients := []string{"sluisctl"}
	ctx := t.Context()

	t.Run("a live sign-in", func(t *testing.T) {
		rig := newSluisctlRig(t)
		if who, ok := rig.storage.CLISubject(ctx, rig.access(t, "sluisctl"), clients); !ok || who != "ada@north.example" {
			t.Errorf("= %q, %v", who, ok)
		}
	})
	t.Run("a revoked session is refused at once", func(t *testing.T) {
		rig := newSluisctlRig(t)
		token := rig.access(t, "sluisctl")
		rig.revokeAll(t)
		if _, ok := rig.storage.CLISubject(ctx, token, clients); ok {
			t.Error("a token of an ended session was read")
		}
	})
	t.Run("another client's token is refused", func(t *testing.T) {
		rig := newSluisctlRig(t)
		if _, ok := rig.storage.CLISubject(ctx, rig.access(t, "local-dev"), clients); ok {
			t.Error("a relying party's token was read")
		}
		if _, ok := rig.storage.CLISubject(ctx, rig.access(t, "sluisctl"), []string{"local-dev"}); ok {
			t.Error("the client was not in the list")
		}
	})
	t.Run("an ID token for the CLI is refused", func(t *testing.T) {
		rig := newSluisctlRig(t)
		id, _ := rig.tokens(t, "sluisctl")["id_token"].(string)
		if id == "" {
			t.Fatal("no ID token")
		}
		if _, ok := rig.storage.CLISubject(ctx, id, clients); ok {
			t.Error("an ID token was read as a bearer")
		}
	})
	t.Run("an agent-class session is refused", func(t *testing.T) {
		rig := newSluisctlRig(t)
		if _, err := rig.iss.Sessions().Record(ctx, issuer.Opened{Identity: "ada@north.example", ClientID: "sluisctl", How: issuer.HowCode,
			Token: "agent-refresh", Scopes: []string{"openid", "email"}, AuthTime: time.Now(), Class: issuer.ClassAgent}); err != nil {
			t.Fatal(err)
		}
		form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"agent-refresh"}, "client_id": {"sluisctl"}, "scope": {"openid email"}}
		body := postToken(t, rig.server, form, "sluisctl", http.StatusOK)
		access, _ := body["access_token"].(string)
		sessions, _ := rig.iss.Sessions().List(ctx, issuer.Query{Identity: "ada@north.example"})
		if len(sessions) != 1 || !sessions[0].Agent() {
			t.Fatalf("the session is not an agent's: %+v", sessions)
		}
		if _, ok := rig.storage.CLISubject(ctx, access, clients); ok {
			t.Error("an agent session's token was read")
		}
	})
	t.Run("junk is refused", func(t *testing.T) {
		rig := newSluisctlRig(t)
		for _, b := range []string{"", "x.y.z", rig.access(t, "sluisctl") + "A"} {
			if _, ok := rig.storage.CLISubject(ctx, b, clients); ok {
				t.Errorf("%q was read", b)
			}
		}
	})
}
