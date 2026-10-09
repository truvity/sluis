package issuerapp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/clientcreds"
	"github.com/truvity/sluis/internal/issuerapp"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
)

// countingSecrets counts the reads of a Secrets port.
type countingSecrets struct {
	port.Secrets
	mu   sync.Mutex
	gets int
}

func (c *countingSecrets) Get(ctx context.Context, path string) (port.Secret, error) {
	c.mu.Lock()
	c.gets++
	c.mu.Unlock()
	return c.Secrets.Get(ctx, path)
}

func (c *countingSecrets) reads() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gets
}

// A Lambda start reads no generated client's record: a herd of cold starts
// would read one per client each, and SSM throttles a herd. The scheduled pass
// settles them, and the token endpoint reads a record once when its client
// first authenticates, then reuses it within the resolver's cache.
func TestAStartThatSkipsTheSecretsPassReadsNoRecordAndTheTickSettlesThem(t *testing.T) {
	counted := &countingSecrets{Secrets: memory.NewSecrets()}
	app, err := tryBoot(t, issuerapp.Deps{
		Directory:            nobody{},
		Stores:               withSecrets(counted, "memory"),
		SkipStartSecretsPass: true,
	}, replacePolicy(t, generatingPolicy))
	if err != nil {
		t.Fatal(err)
	}
	if n := counted.reads(); n != 0 {
		t.Fatalf("start made %d secrets reads, want 0", n)
	}
	if _, err = counted.Secrets.Get(context.Background(), clientcreds.Path("grafana")); err == nil {
		t.Fatal("a record exists after a start that skipped the pass")
	}

	res := app.ReconcileClientSecrets(context.Background())
	if res.Outcomes["grafana"] != clientcreds.OutcomeCreated {
		t.Fatalf("the tick did not settle the secret: %+v", res)
	}
	if _, err = counted.Secrets.Get(context.Background(), clientcreds.Path("grafana")); err != nil {
		t.Fatalf("no record after the tick: %v", err)
	}
}

func TestTheFirstTokenRequestOfAClientReadsItsRecordOnceAndTheNextReadsNothing(t *testing.T) {
	mem := memory.NewSecrets()
	body, err := clientcreds.Record{Current: "the-record-secret", Created: time.Now()}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = mem.Put(context.Background(), clientcreds.Path("grafana"), body); err != nil {
		t.Fatal(err)
	}
	counted := &countingSecrets{Secrets: mem}
	app, err := tryBoot(t, issuerapp.Deps{
		Directory:            nobody{},
		Stores:               withSecrets(counted, "memory"),
		SkipStartSecretsPass: true,
	}, replacePolicy(t, generatingPolicy))
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
	if n := counted.reads(); n != 0 {
		t.Fatalf("start and discovery made %d reads, want 0", n)
	}
	authenticate := func() {
		t.Helper()
		form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"nothing"}}
		req := httptest.NewRequest(http.MethodPost, discovery.Token, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetBasicAuth("grafana", "the-record-secret")
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		if rec.Code == http.StatusUnauthorized || strings.Contains(rec.Body.String(), "invalid_client") {
			t.Fatalf("the client did not authenticate: %d %s", rec.Code, rec.Body.String())
		}
	}
	authenticate()
	if n := counted.reads(); n != 1 {
		t.Fatalf("the first token request made %d reads, want 1", n)
	}
	authenticate()
	if n := counted.reads(); n != 1 {
		t.Errorf("the second token request within the cache made %d reads in all, want 1", n)
	}
}
