package issuerapp_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/clientcreds"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/issuerapp"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/internal/store"
)

// generatingPolicy declares one client whose secret the issuer makes, and one
// whose secret is delivered.
const generatingPolicy = `
version: 1
groups:
  platform: { members: [platform@north.example] }
lifetimes: { default: 12h }
clients:
  grafana: { kind: confidential, secret: { generate: true }, requires: [platform] }
  argocd:  { kind: confidential, secret: argocd-oidc, requires: [platform] }
`

// replacePolicy makes the boot's policy file say body.
func replacePolicy(t *testing.T, body string) func(*config.Serve) {
	return func(f *config.Serve) {
		if err := os.WriteFile(f.Policy.File, []byte(body), 0o600); err != nil {
			t.Fatalf("write the policy: %v", err)
		}
	}
}

// tryBoot is bootDeps that hands back the error of New.
func tryBoot(t *testing.T, deps issuerapp.Deps, change ...func(*config.Serve)) (*issuerapp.App, error) {
	t.Helper()
	// The boot helpers write the policy and call New; here the same by hand,
	// so a refusal is a value and not a fatal.
	policyDir := t.TempDir()
	f := &config.Serve{
		IssuerURL: "https://issuer.example",
		Policy:    &config.PolicyRef{File: policyDir + "/policy.yaml"},
		Listen:    &config.Address{Address: ":0"},
		Probes:    &config.Address{Address: ":0"},
	}
	for _, c := range change {
		c(f)
	}
	cfg, err := issuerapp.FromConfig(withPolicy(t, f))
	if err != nil {
		t.Fatalf("FromConfig: %v", err)
	}
	return issuerapp.New(context.Background(), cfg, deps, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// withSecrets is stores with a Secrets port and a State every replica sees, as
// a generated client needs.
func withSecrets(s port.Secrets, adapter string) *store.Stores {
	st := &store.Stores{Secrets: testSecrets, Ports: port.Set{Secrets: s, State: memory.New().Set().State}, Shared: true}
	if adapter != "" {
		st.Plan = port.Table{port.ConcernSecrets: {Adapter: adapter}}
	}
	return st
}

func TestAGeneratedClientIsRefusedWhereTheSecretsAdapterCannotCreateOnlyIfAbsent(t *testing.T) {
	t.Parallel()
	for name, stores := range map[string]*store.Stores{
		"the legacy adapter": withSecrets(memory.NewSecrets(), store.AdapterLegacy),
		"no secrets port":    withSecrets(nil, ""),
		"legacy and no port": withSecrets(nil, store.AdapterLegacy),
		"no ports at all":    {Secrets: testSecrets},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := tryBoot(t, issuerapp.Deps{Directory: nobody{}, Stores: stores}, replacePolicy(t, generatingPolicy))
			if err == nil {
				t.Fatal("started")
			}
			if !strings.Contains(err.Error(), `"grafana"`) || !strings.Contains(err.Error(), "generate: true") {
				t.Errorf("the refusal does not name the client and the key: %v", err)
			}
		})
	}
}

// The same adapters are fine when nobody asks for a generated secret.
func TestNoGeneratedClientNeedsNoSecretsPort(t *testing.T) {
	t.Parallel()
	const named = `
version: 1
groups:
  platform: { members: [platform@north.example] }
clients:
  argocd: { kind: confidential, secret: argocd-oidc, requires: [platform] }
`
	for _, stores := range []*store.Stores{withSecrets(nil, store.AdapterLegacy), {Secrets: testSecrets}} {
		if _, err := tryBoot(t, issuerapp.Deps{Directory: nobody{}, Stores: stores}, replacePolicy(t, named)); err != nil {
			t.Errorf("a named secret was refused: %v", err)
		}
	}
}

func TestAGeneratedClientHasItsSecretAtStart(t *testing.T) {
	t.Parallel()
	mem := memory.NewSecrets()
	app, err := tryBoot(t, issuerapp.Deps{Directory: nobody{}, Stores: withSecrets(mem, "memory")}, replacePolicy(t, generatingPolicy))
	if err != nil {
		t.Fatal(err)
	}
	rec, err := mem.Get(context.Background(), clientcreds.Path("grafana"))
	if err != nil {
		t.Fatalf("no record after start: %v", err)
	}
	if _, err = clientcreds.DecodeRecord(rec.Value); err != nil {
		t.Errorf("the record: %v", err)
	}
	if _, err = mem.Get(context.Background(), clientcreds.Path("argocd")); !errors.Is(err, port.ErrNotFound) {
		t.Errorf("a client with a named secret has a record: %v", err)
	}
	// A pass after start finds it and only it.
	res := app.ReconcileClientSecrets(context.Background())
	if len(res.Outcomes) != 1 || res.Outcomes["grafana"] != clientcreds.OutcomeExisting {
		t.Errorf("second pass = %+v", res)
	}
}

// failingPuts refuses every create, as a store that is down does.
type failingPuts struct {
	port.Secrets
	mu   sync.Mutex
	fail bool
	puts int
}

func (f *failingPuts) PutIfVersion(ctx context.Context, path string, value []byte, version string) (string, error) {
	f.mu.Lock()
	f.puts++
	fail := f.fail
	f.mu.Unlock()
	if fail {
		return "", errors.New("the secrets store is down")
	}
	return f.Secrets.PutIfVersion(ctx, path, value, version)
}

func (f *failingPuts) set(fail bool) {
	f.mu.Lock()
	f.fail = fail
	f.mu.Unlock()
}

func TestAClientWhoseSecretCannotBeSettledDoesNotStopTheIssuerAndIsRetried(t *testing.T) {
	t.Parallel()
	flaky := &failingPuts{Secrets: memory.NewSecrets(), fail: true}
	app, err := tryBoot(t, issuerapp.Deps{Directory: nobody{}, Stores: withSecrets(flaky, "memory")}, replacePolicy(t, generatingPolicy))
	if err != nil {
		t.Fatalf("a failed reconcile stopped the start: %v", err)
	}
	if flaky.puts != 1 {
		t.Errorf("start tried %d times, want 1", flaky.puts)
	}
	res := app.ReconcileClientSecrets(context.Background())
	if res.Outcomes["grafana"] != clientcreds.OutcomeFailed || res.Failed() != 1 {
		t.Fatalf("while down: %+v", res)
	}
	flaky.set(false)
	res = app.ReconcileClientSecrets(context.Background())
	if res.Outcomes["grafana"] != clientcreds.OutcomeCreated || res.Failed() != 0 {
		t.Errorf("after recovery: %+v", res)
	}
}

func TestAnIssuerWithNoGeneratedClientReconcilesNothing(t *testing.T) {
	t.Parallel()
	app := boot(t)
	if res := app.ReconcileClientSecrets(context.Background()); len(res.Outcomes) != 0 {
		t.Errorf("outcomes = %+v", res)
	}
}

func TestAGeneratedClientIsRefusedWhereTheStateIsNotShared(t *testing.T) {
	t.Parallel()
	// ssm secrets beside a process-local State: two replicas would share the
	// record with leases that do not exclude each other.
	notShared := withSecrets(memory.NewSecrets(), "ssm")
	notShared.Shared = false
	_, err := tryBoot(t, issuerapp.Deps{Directory: nobody{}, Stores: notShared}, replacePolicy(t, generatingPolicy))
	if err == nil {
		t.Fatal("started")
	}
	if !strings.Contains(err.Error(), "the State is not shared between replicas") || !strings.Contains(err.Error(), `"grafana"`) {
		t.Errorf("refusal = %v", err)
	}
	// openbao secrets are shared by every replica too.
	notSharedBao := withSecrets(memory.NewSecrets(), "openbao")
	notSharedBao.Shared = false
	if _, err = tryBoot(t, issuerapp.Deps{Directory: nobody{}, Stores: notSharedBao}, replacePolicy(t, generatingPolicy)); err == nil ||
		!strings.Contains(err.Error(), "the State is not shared between replicas") {
		t.Errorf("openbao beside a process-local State: %v", err)
	}
	// Memory SECRETS are in this process, so there is no second replica to
	// exclude, whatever adapter the State has.
	for name, adapter := range map[string]string{"no adapter named": "", "the legacy adapter": store.AdapterLegacy} {
		memSecrets := withSecrets(memory.NewSecrets(), "memory")
		memSecrets.Shared, memSecrets.Adapter = false, adapter
		if _, err = tryBoot(t, issuerapp.Deps{Directory: nobody{}, Stores: memSecrets}, replacePolicy(t, generatingPolicy)); err != nil {
			t.Errorf("memory secrets beside a process-local State (%s) were refused: %v", name, err)
		}
	}
	inMemory := withSecrets(memory.NewSecrets(), "memory")
	inMemory.Shared, inMemory.Adapter = false, store.AdapterMemory
	if _, err = tryBoot(t, issuerapp.Deps{Directory: nobody{}, Stores: inMemory}, replacePolicy(t, generatingPolicy)); err != nil {
		t.Errorf("the memory adapter was refused: %v", err)
	}
	// And a named secret needs no shared State.
	const named = `
version: 1
groups:
  platform: { members: [platform@north.example] }
clients:
  argocd: { kind: confidential, secret: argocd-oidc, requires: [platform] }
`
	if _, err = tryBoot(t, issuerapp.Deps{Directory: nobody{}, Stores: notShared}, replacePolicy(t, named)); err != nil {
		t.Errorf("a named secret was refused: %v", err)
	}
}

// The token endpoint, as the issuer is assembled: a generated client is served
// by its record, and a client on `secret: <name>` by its input, record or not.
// A resolver never told which clients are generated would treat them all as
// generated, so this pins that the assembly tells it.
func TestTheAssembledTokenEndpointConsultsTheRecordOnlyForAGeneratedClient(t *testing.T) {
	t.Parallel()
	mem := memory.NewSecrets()
	put := func(id, value string) {
		body, err := clientcreds.Record{Current: value, Created: time.Now()}.Encode()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = mem.Put(context.Background(), clientcreds.Path(id), body); err != nil {
			t.Fatal(err)
		}
	}
	put("grafana", "the-record-secret")
	put("argocd", "a-stale-record-secret")
	stores := withSecrets(mem, "memory")
	stores.Secrets = inputDir(t, map[string]string{"grafana": "the-input-secret", "argocd": "the-input-secret"})
	app, err := tryBoot(t, issuerapp.Deps{Directory: nobody{}, Stores: stores}, replacePolicy(t, generatingPolicy))
	if err != nil {
		t.Fatal(err)
	}
	_, doc := get(t, app.Handler(), "/.well-known/openid-configuration")
	var discovery struct {
		Token string `json:"token_endpoint"`
	}
	if err = json.Unmarshal([]byte(doc), &discovery); err != nil || discovery.Token == "" {
		t.Fatalf("discovery: %v %s", err, doc)
	}
	authenticates := func(client, secret string) bool {
		form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"nothing"}}
		req := httptest.NewRequest(http.MethodPost, discovery.Token, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetBasicAuth(url.QueryEscape(client), url.QueryEscape(secret))
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		// A client that does not authenticate is refused as such; one that does
		// goes on and is refused for its (made-up) refresh token.
		return rec.Code != http.StatusUnauthorized && !strings.Contains(rec.Body.String(), "invalid_client")
	}
	for _, tc := range []struct {
		client, secret string
		want           bool
	}{
		{"grafana", "the-record-secret", true},
		{"grafana", "the-input-secret", false},
		{"argocd", "the-input-secret", true},
		{"argocd", "a-stale-record-secret", false},
		{"grafana", "wrong", false},
	} {
		if got := authenticates(tc.client, tc.secret); got != tc.want {
			t.Errorf("%s with %s: authenticated = %v, want %v", tc.client, tc.secret, got, tc.want)
		}
	}
}
