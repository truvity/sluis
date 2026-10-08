//nolint:lll // fixtures
package cloudflare_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truvity/sluis/storage/cloudflare"
	"github.com/truvity/sluis/storage/state"
	"github.com/truvity/sluis/storage/state/memory"
)

type fake struct {
	mu      sync.Mutex
	tokens  map[string]cloudflare.Token
	groups  map[string]string
	next    int
	now     func() time.Time
	creates int
	deletes int
}

func (f *fake) GetToken(_ context.Context, id string) (cloudflare.Token, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tokens[id]
	if !ok {
		return cloudflare.Token{}, cloudflare.ErrNotFound
	}
	return t, nil
}

func (f *fake) CreateToken(_ context.Context, in cloudflare.NewToken) (cloudflare.Created, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates++
	f.next++
	id := fmt.Sprintf("tok%03d", f.next)
	f.tokens[id] = cloudflare.Token{ID: id, Name: in.Name, Status: cloudflare.StatusActive, ExpiresOn: in.ExpiresOn, Policies: in.Policies, Condition: in.Condition}
	return cloudflare.Created{ID: id, ExpiresOn: in.ExpiresOn, Value: "value-" + id}, nil
}

func (f *fake) DeleteToken(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes++
	if _, ok := f.tokens[id]; !ok {
		return cloudflare.ErrNotFound
	}
	delete(f.tokens, id)
	return nil
}

func (f *fake) PermissionGroups(context.Context) (map[string]string, error) { return f.groups, nil }

func newProvider(t *testing.T, mut func(*cloudflare.ProviderConfig)) (*cloudflare.Provider, *fake, *time.Time, state.Store) {
	t.Helper()
	clock := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	f := &fake{tokens: map[string]cloudflare.Token{}, now: func() time.Time { return clock },
		groups: map[string]string{"g-r2": "Workers R2 Storage Write", "g-bill": "Billing Write"}}
	f.tokens["proto-r2-00001"] = cloudflare.Token{ID: "proto-r2-00001", Status: cloudflare.StatusDisabled,
		Policies: json.RawMessage(`[{"id":"p","effect":"allow","resources":{"com.cloudflare.edge.r2.bucket.acct_default_b":"*"},"permission_groups":[{"id":"g-r2","name":"x"}]}]`)}
	rec := memory.New()
	cfg := cloudflare.ProviderConfig{
		Instance: "audit", Preset: "r2-audit", Account: "acct", Prototype: "proto-r2-00001", Lifetime: 15 * time.Minute,
		Minter: func(context.Context) (string, error) { return "minter-secret", nil },
		Dial:   func(string, string) cloudflare.API { return f },
		Record: rec, Propagation: -1, Now: func() time.Time { return clock },
	}
	if mut != nil {
		mut(&cfg)
	}
	p, err := cloudflare.NewProvider(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return p, f, &clock, rec
}

func TestTheProviderMintsRenewsAndMintsAgain(t *testing.T) {
	ctx := context.Background()
	p, f, clock, _ := newProvider(t, nil)
	c, err := p.Retrieve(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c.AccessKeyID != "tok001" || c.SecretAccessKey != cloudflare.R2Secret("value-tok001") || !c.CanExpire || !c.Expires.Equal(clock.Add(10*time.Minute)) {
		t.Fatalf("creds = %+v", c)
	}
	if got := f.tokens["tok001"]; got.Name != "sluis/audit/r2-audit/self/2026-10-09T12:00:00Z" ||
		string(got.Policies) != `[{"effect":"allow","resources":{"com.cloudflare.edge.r2.bucket.acct_default_b":"*"},"permission_groups":[{"id":"g-r2"}]}]` {
		t.Errorf("clone = %+v", got)
	}
	*clock = clock.Add(9 * time.Minute)
	if c, err = p.Retrieve(ctx); err != nil || c.AccessKeyID != "tok001" || !p.Valid() {
		t.Fatalf("before the renewal point: %+v %v", c, err)
	}
	*clock = clock.Add(2 * time.Minute)
	if p.Valid() {
		t.Error("valid past the renewal point")
	}
	if c, err = p.Retrieve(ctx); err != nil || c.AccessKeyID != "tok002" {
		t.Fatalf("renewed: %+v %v", c, err)
	}
	if c, err = p.Remint(ctx); err != nil || c.AccessKeyID != "tok003" || f.creates != 3 {
		t.Fatalf("remint: %+v %v creates %d", c, err, f.creates)
	}
}

func TestTheProviderRefusesWhatTheMinterRefusesAndBuiltInOnly(t *testing.T) {
	ctx := context.Background()
	p, f, _, _ := newProvider(t, nil)
	proto := f.tokens["proto-r2-00001"]
	proto.Status = cloudflare.StatusActive
	f.tokens["proto-r2-00001"] = proto
	if _, err := p.Retrieve(ctx); err == nil || f.creates != 0 {
		t.Fatalf("an active prototype: %v creates %d", err, f.creates)
	}
	proto.Status, proto.Policies = cloudflare.StatusDisabled, json.RawMessage(`[{"effect":"allow","resources":{"r":"*"},"permission_groups":[{"id":"g-bill"}]}]`)
	f.tokens["proto-r2-00001"] = proto
	if pe, ok := cloudflare.IsPrototypeError(func() error { _, err := p.Retrieve(ctx); return err }()); !ok || pe.Reason != cloudflare.ReasonPrototypeForbidden {
		t.Fatalf("forbidden: %v", pe)
	}
	p2, _, _, _ := newProvider(t, func(c *cloudflare.ProviderConfig) { c.ExtraForbidden = []string{"Workers R2 Storage Write"} })
	if _, err := p2.Retrieve(ctx); err == nil {
		t.Error("an added name did not refuse")
	}
}

func TestTheProviderRecordsAndSweepsItsOwnExpiredTokens(t *testing.T) {
	ctx := context.Background()
	p, f, clock, rec := newProvider(t, nil)
	if _, err := p.Retrieve(ctx); err != nil {
		t.Fatal(err)
	}
	f.tokens["foreign"] = cloudflare.Token{ID: "foreign", Name: "something else"}
	*clock = clock.Add(20 * time.Minute) // tok001 has expired
	if _, err := p.Retrieve(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.tokens["tok001"]; ok {
		t.Error("the expired token is still in the account")
	}
	it, err := rec.Get(ctx, "cloudflare-minted/r2-audit")
	if err != nil || !strings.Contains(string(it.Value), "tok002") || strings.Contains(string(it.Value), "tok001") {
		t.Errorf("record = %s %v", it.Value, err)
	}
	if _, ok := f.tokens["foreign"]; !ok {
		t.Error("a foreign token was touched")
	}
}

func TestTheProviderNeedsWhatItNeeds(t *testing.T) {
	for name, mut := range map[string]func(*cloudflare.ProviderConfig){
		"no minter":      func(c *cloudflare.ProviderConfig) { c.Minter = nil },
		"short lifetime": func(c *cloudflare.ProviderConfig) { c.Lifetime = time.Second },
		"no prototype":   func(c *cloudflare.ProviderConfig) { c.Prototype = "" },
		"bad instance":   func(c *cloudflare.ProviderConfig) { c.Instance = "a/b" },
	} {
		cfg := cloudflare.ProviderConfig{Instance: "audit", Preset: "p", Account: "a", Prototype: "x", Lifetime: time.Minute,
			Minter: func(context.Context) (string, error) { return "", nil }}
		mut(&cfg)
		if _, err := cloudflare.NewProvider(cfg); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	p, _, _, _ := newProvider(t, func(c *cloudflare.ProviderConfig) {
		c.Minter = func(context.Context) (string, error) { return "", errors.New("no such address") }
	})
	if _, err := p.Retrieve(context.Background()); err == nil || strings.Contains(err.Error(), "secret") {
		t.Errorf("minter read: %v", err)
	}
}

func TestTheHTTPClientSpeaksCloudflare(t *testing.T) {
	var posted string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		b, _ := io.ReadAll(r.Body)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/accounts/acct/tokens/proto":
			_, _ = io.WriteString(w, `{"success":true,"result":{"id":"proto","name":"p","status":"disabled","policies":[],"condition":{"request_ip":{"in":["203.0.113.0/24"]}}}}`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/missing"):
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"success":false,"errors":[{"code":1003,"message":"nope"}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/accounts/acct/tokens":
			posted = string(b)
			_, _ = io.WriteString(w, `{"success":true,"result":{"id":"new","value":"v","expires_on":"2026-10-09T12:15:00Z"}}`)
		case r.Method == http.MethodDelete:
			_, _ = io.WriteString(w, `{"success":true,"result":{"id":"gone"}}`)
		case r.URL.Path == "/accounts/acct/tokens/permission_groups":
			_, _ = io.WriteString(w, `{"success":true,"result":[{"id":"g1","name":"DNS Write"}]}`)
		default:
			w.WriteHeader(http.StatusTeapot)
			_, _ = io.WriteString(w, `{"success":false,"errors":[{"code":1,"message":"unexpected"}]}`)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	h := cloudflare.NewHTTP("acct", "tok", nil, srv.URL)
	tok, err := h.GetToken(ctx, "proto")
	if err != nil || tok.Status != "disabled" || !strings.Contains(string(tok.Condition), "request_ip") {
		t.Fatalf("get: %+v %v", tok, err)
	}
	if _, err = h.GetToken(ctx, "missing"); !errors.Is(err, cloudflare.ErrNotFound) {
		t.Errorf("404: %v", err)
	}
	c, err := h.CreateToken(ctx, cloudflare.NewToken{Name: "n", ExpiresOn: time.Date(2026, 10, 9, 12, 15, 0, 0, time.UTC),
		Policies: json.RawMessage(`[{"effect":"allow"}]`), Condition: json.RawMessage(`{"request_ip":{}}`)})
	if err != nil || c.ID != "new" || c.Value != "v" || !strings.Contains(posted, `"expires_on":"2026-10-09T12:15:00Z"`) || !strings.Contains(posted, `"policies":[{"effect":"allow"}]`) {
		t.Errorf("create: %+v %v posted %s", c, err, posted)
	}
	if err = h.DeleteToken(ctx, "x"); err != nil {
		t.Error(err)
	}
	if g, err := h.PermissionGroups(ctx); err != nil || g["g1"] != "DNS Write" {
		t.Errorf("groups: %v %v", g, err)
	}
	if _, err = cloudflare.NewHTTP("acct", "wrong", nil, srv.URL).GetToken(ctx, "proto"); err == nil || strings.Contains(err.Error(), "wrong") {
		t.Errorf("auth failure: %v", err)
	}
}
