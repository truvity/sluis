package issuer_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/audit/audittest"
	"github.com/truvity/sluis/internal/clientcreds"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/policy"
)

// The endpoint behind the real verifier: a bearer this issuer signed, whose
// audience is the client it was issued to.
func TestTheClientSecretsEndpointChecksTheRealTokensAudience(t *testing.T) {
	t.Parallel()
	declared, err := policy.Parse([]byte("version: 1\n" +
		"groups:\n  " + policy.GroupOperators + ": { members: [ops@north.example] }\n" +
		"clients:\n" +
		"  accessctl: { kind: public, requires: [" + policy.GroupOperators + "], redirects: ['http://127.0.0.1/cb'] }\n" +
		"  console:   { kind: public, requires: [" + policy.GroupOperators + "], redirects: ['https://console.example/cb'] }\n" +
		"  grafana:   { kind: public, requires: [" + policy.GroupOperators + "], redirects: ['https://grafana.example/cb'] }\n"))
	if err != nil {
		t.Fatal(err)
	}
	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatal(err)
	}
	dir := &fakeDirectory{standing: map[string]issuer.Standing{"ada@north.example": live("ops@north.example")}}
	iss := issuer.New(issuer.Config{URL: "https://issuer.example"}, set, dir, issuer.NewMemoryState())
	trail := audittest.New(t)
	iss.UseAudit(trail)
	store := memory.NewSecrets()
	clientcreds.Reconcile(context.Background(), []string{"grafana"}, store, nil, time.Now(), nil, clientcreds.Hooks{})
	iss.UseClientSecrets(&clientcreds.Manager{Store: store, Generated: func(id string) bool { return id == "grafana" }})
	storage, err := issuer.NewTestStorage(iss, fakeVerifier{}, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := handlerWithSignIn(iss, storage, issuer.SignInDeps{})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	show := func(client string) (int, string) {
		token, _, err := storage.MintFor(context.Background(), "ada@north.example", client, time.Minute)
		if err != nil {
			t.Fatalf("mint for %s: %v", client, err)
		}
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+issuer.ClientSecretsPath+"/show", strings.NewReader(`{"client":"grafana"}`))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}
	for _, client := range []string{"accessctl", "console"} {
		if code, body := show(client); code != http.StatusOK {
			t.Errorf("a token for %s: %d %s", client, code, body)
		}
	}
	if code, body := show("grafana"); code != http.StatusForbidden {
		t.Errorf("a token for another client: %d %s", code, body)
	}
	denied := trail.Find("roster.client.secret.denied")
	if len(denied) != 1 || denied[0].GetOutcome().GetReason() != "wrong_audience" || denied[0].GetActor().GetId() != "ada@north.example" {
		t.Errorf("audited %v", trail.Actions())
	}
}
