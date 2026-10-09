//nolint:lll // messages and fixtures are prose and one-line tables
package minter_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/audit/audittest"
	"github.com/truvity/sluis/internal/cloudflare"
	"github.com/truvity/sluis/internal/cloudflare/minter"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/storage/state/memory"
)

// fakeAPI is one Cloudflare account in memory.
type fakeAPI struct {
	mu     sync.Mutex
	tokens map[string]cloudflare.Token
	groups map[string]string
	next   int
	now    func() time.Time

	creates, deletes, lists int
	createErr               error
	gotMinter               string
}

func newFake(now func() time.Time) *fakeAPI {
	return &fakeAPI{tokens: map[string]cloudflare.Token{}, now: now, groups: map[string]string{
		"g-dns": "DNS Write", "g-r2": "Workers R2 Storage Write", "g-tok": "Account API Tokens Write", "g-bill": "Billing Read",
	}}
}

func (f *fakeAPI) put(t cloudflare.Token) { f.mu.Lock(); f.tokens[t.ID] = t; f.mu.Unlock() }

func (f *fakeAPI) GetToken(_ context.Context, id string) (cloudflare.Token, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tokens[id]
	if !ok {
		return cloudflare.Token{}, cloudflare.ErrNotFound
	}
	// Cloudflare answers a GET for an expired token, as `expired`.
	if t.Status != cloudflare.StatusDisabled && !t.ExpiresOn.IsZero() && !t.ExpiresOn.After(f.now()) {
		t.Status = cloudflare.StatusExpired
	}
	return t, nil
}

func (f *fakeAPI) CreateToken(_ context.Context, in cloudflare.NewToken) (cloudflare.Created, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates++
	if f.createErr != nil {
		return cloudflare.Created{}, f.createErr
	}
	f.next++
	id := fmt.Sprintf("tok%03d", f.next)
	f.tokens[id] = cloudflare.Token{ID: id, Name: in.Name, Status: cloudflare.StatusActive, ExpiresOn: in.ExpiresOn, Policies: in.Policies, Condition: in.Condition}
	return cloudflare.Created{ID: id, Name: in.Name, ExpiresOn: in.ExpiresOn, Value: "value-" + id}, nil
}

func (f *fakeAPI) DeleteToken(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes++
	if _, ok := f.tokens[id]; !ok {
		return cloudflare.ErrNotFound
	}
	delete(f.tokens, id)
	return nil
}

func (f *fakeAPI) ListTokens(context.Context) ([]cloudflare.Token, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lists++
	var out []cloudflare.Token
	for _, t := range f.tokens {
		// An expired token is hidden from the list (checked against a live account).
		if !t.ExpiresOn.IsZero() && !t.ExpiresOn.After(f.now()) {
			continue
		}
		out = append(out, t)
	}
	return out, nil
}

func (f *fakeAPI) PermissionGroups(context.Context) (map[string]string, error) {
	return f.groups, nil
}

func (f *fakeAPI) names() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]string{}
	for id, t := range f.tokens {
		out[id] = t.Name
	}
	return out
}

const (
	dnsProto = "proto-dns-0001"
	r2Proto  = "proto-r2-00001"
	acct     = "0123456789abcdef0123456789abcdef"
)

// policies as Cloudflare answers them: with generated ids and names.
func policiesJSON(group string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`[{"id":"pol-1","effect":"allow","resources":{"com.cloudflare.api.account.zone.z1":"*"},"permission_groups":[{"id":%q,"name":"ignored","meta":{"k":"v"}}]}]`, group))
}

type env struct {
	t      *testing.T
	api    *fakeAPI
	m      *minter.Minter
	rec    *audittest.Recorder
	stores *secretstore.Stores
	clock  *time.Time
}

func (e *env) advance(d time.Duration) { *e.clock = e.clock.Add(d) }

func setup(t *testing.T, grants ...config.CloudflareGrant) *env {
	t.Helper()
	clock := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	e := &env{t: t, clock: &clock, rec: audittest.New(t)}
	now := func() time.Time { return clock }
	e.api = newFake(now)
	e.stores = secretstore.FromStore(memory.New(), "")
	cf := &config.Cloudflare{
		Accounts: map[string]config.CloudflareAccount{"main": {ID: acct, Minter: "internal/cloudflare/main/minter"}},
		Presets: map[string]config.CloudflarePreset{
			"dns":      {Account: "main", Prototype: dnsProto, Description: "DNS", Lifetime: config.Duration(15 * time.Minute), Rotation: config.Duration(5 * time.Minute)},
			"dns-edge": {Account: "main", Prototype: dnsProto, Description: "DNS edge", Lifetime: config.Duration(15 * time.Minute), Rotation: config.Duration(5 * time.Minute)},
			"r2": {Account: "main", Prototype: r2Proto, Description: "R2", Lifetime: config.Duration(15 * time.Minute), Rotation: config.Duration(5 * time.Minute),
				Endpoint: "https://" + acct + ".r2.cloudflarestorage.com"},
		},
	}
	mv, err := e.stores.Internal.CloudflareMinter("internal/cloudflare/main/minter")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = mv.Put(context.Background(), secretstore.CloudflareMinterv1{Token: "minter-secret"}, ""); err != nil {
		t.Fatal(err)
	}
	e.api.put(cloudflare.Token{ID: dnsProto, Name: "proto dns", Status: cloudflare.StatusDisabled, Policies: policiesJSON("g-dns"),
		Condition: json.RawMessage(`{"request_ip":{"in":["203.0.113.0/24"],"not_in":[]}}`)})
	e.api.put(cloudflare.Token{ID: r2Proto, Name: "proto r2", Status: cloudflare.StatusDisabled, Policies: policiesJSON("g-r2")})
	e.m, err = minter.New(minter.Config{
		Instance: "example", Cloudflare: cf, Grants: &config.PolicyCloudflare{Grants: grants},
		Internal: e.stores.Internal, External: e.stores.External,
		Dial: func(_ context.Context, id, token string) (minter.API, error) {
			if id != acct {
				return nil, fmt.Errorf("wrong account %s", id)
			}
			e.api.gotMinter = token
			return e.api, nil
		},
		Audit: e.rec, Now: now, Propagation: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}
