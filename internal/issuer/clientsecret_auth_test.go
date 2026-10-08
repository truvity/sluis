package issuer_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/truvity/sluis/internal/clientcreds"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/policy"
)

// lookup is a clientcreds.Lookup over a fixed table.
type lookup map[string]clientcreds.Secrets

func (l lookup) Resolve(_ context.Context, id string) (clientcreds.Secrets, bool) {
	s, ok := l[id]
	return s, ok
}

var secretNow = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

func secretStorage(t *testing.T, l clientcreds.Lookup) *issuer.Storage {
	t.Helper()
	declared, err := policy.Parse([]byte(`version: 1
groups: { a: { members: [g@h.example] } }
clients:
  grafana: { kind: confidential, secret: { generate: true }, requires: [a] }
  named:   { kind: confidential, secret: grafana-oidc, requires: [a] }
  console: { kind: public, requires: [a], redirects: ["https://console.example/cb"] }
  target:  { kind: exchange, requires: [a] }
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	iss := issuer.New(issuer.Config{URL: "https://issuer.example"}, set, &fakeDirectory{}, issuer.NewMemoryState())
	storage, err := issuer.NewStorage(iss, fakeVerifier{}, l, nil, nil, nil)
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	issuer.SetStorageClock(storage, func() time.Time { return secretNow })
	return storage
}

func TestAuthorizeClientIDSecretCurrentAndPrevious(t *testing.T) {
	t.Parallel()
	until := secretNow.Add(time.Hour)
	rotated := clientcreds.Secrets{Current: "cur", Previous: "old", PreviousValidUntil: until}
	for _, tc := range []struct {
		name   string
		client string
		have   clientcreds.Secrets
		clock  time.Time
		guess  string
		ok     bool
	}{
		{"current", "grafana", clientcreds.Secrets{Current: "cur"}, secretNow, "cur", true},
		{"current of a named client", "named", clientcreds.Secrets{Current: "cur"}, secretNow, "cur", true},
		{"current beside a previous", "grafana", rotated, secretNow, "cur", true},
		{"previous before expiry", "grafana", rotated, secretNow, "old", true},
		{"previous one tick before expiry", "grafana", rotated, until.Add(-time.Nanosecond), "old", true},
		{"previous at expiry", "grafana", rotated, until, "old", false},
		{"previous after expiry", "grafana", rotated, until.Add(time.Hour), "old", false},
		{"previous with no expiry set", "grafana", clientcreds.Secrets{Current: "cur", Previous: "old"}, secretNow, "old", false},
		{"current after the previous expired", "grafana", rotated, until.Add(time.Hour), "cur", true},
		{"wrong secret", "grafana", rotated, secretNow, "nope", false},
		{"a prefix of the secret", "grafana", clientcreds.Secrets{Current: "cur"}, secretNow, "cu", false},
		{"the secret and more", "grafana", clientcreds.Secrets{Current: "cur"}, secretNow, "cur2", false},
		{"empty guess, only a current", "grafana", clientcreds.Secrets{Current: "cur"}, secretNow, "", false},
		{"empty guess against an empty previous", "grafana", clientcreds.Secrets{Current: "cur", PreviousValidUntil: until}, secretNow, "", false},
		{"empty guess against a live previous", "grafana", rotated, secretNow, "", false},
		{"empty current is never a match", "grafana", clientcreds.Secrets{}, secretNow, "", false},
		{"empty current and a guess", "grafana", clientcreds.Secrets{}, secretNow, "x", false},
		{"no secret for the client", "named", clientcreds.Secrets{}, secretNow, "x", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l := lookup{}
			if tc.name != "no secret for the client" {
				l[tc.client] = tc.have
			}
			storage := secretStorage(t, l)
			clock := tc.clock
			issuer.SetStorageClock(storage, func() time.Time { return clock })
			err := storage.AuthorizeClientIDSecret(context.Background(), tc.client, tc.guess)
			if tc.ok && err != nil {
				t.Errorf("refused: %v", err)
			}
			if !tc.ok && err == nil {
				t.Error("accepted")
			}
		})
	}
}

func TestAuthorizeClientIDSecretLeavesPublicAndExchangeAlone(t *testing.T) {
	t.Parallel()
	// A lookup that would accept anything must not be consulted for them.
	storage := secretStorage(t, lookup{
		"console": {Current: "x"}, "target": {Current: "x"},
	})
	ctx := context.Background()
	for _, id := range []string{"console", "target"} {
		if err := storage.AuthorizeClientIDSecret(ctx, id, ""); err != nil {
			t.Errorf("%s presenting nothing: %v", id, err)
		}
		if err := storage.AuthorizeClientIDSecret(ctx, id, "x"); err == nil {
			t.Errorf("%s presenting a secret was accepted", id)
		}
	}
	if err := storage.AuthorizeClientIDSecret(ctx, "undeclared", "x"); err == nil {
		t.Error("an undeclared client was accepted")
	}
}

// The slot that matched is counted, and a refusal is counted as none.
func TestAuthorizeClientIDSecretCountsTheSlot(t *testing.T) {
	metricsReader()
	until := secretNow.Add(time.Hour)
	storage := secretStorage(t, lookup{"grafana": {Current: "cur", Previous: "old", PreviousValidUntil: until}})
	ctx := context.Background()
	slot := func(s string) int64 {
		return counted(t, "sluis.client_secret.auth", attribute.String("slot", s))
	}
	cur, prev, none := slot(clientcreds.SlotCurrent), slot(clientcreds.SlotPrevious), slot(clientcreds.SlotNone)

	for guess, wantErr := range map[string]bool{"cur": false, "old": false, "wrong": true} {
		if err := storage.AuthorizeClientIDSecret(ctx, "grafana", guess); (err != nil) != wantErr {
			t.Fatalf("%q: %v", guess, err)
		}
	}
	if got := slot(clientcreds.SlotCurrent) - cur; got != 1 {
		t.Errorf("current counted %d times, want 1", got)
	}
	if got := slot(clientcreds.SlotPrevious) - prev; got != 1 {
		t.Errorf("previous counted %d times, want 1", got)
	}
	if got := slot(clientcreds.SlotNone) - none; got != 1 {
		t.Errorf("none counted %d times, want 1", got)
	}

	// Public and exchange clients are not confidential authentications.
	cur, prev, none = slot(clientcreds.SlotCurrent), slot(clientcreds.SlotPrevious), slot(clientcreds.SlotNone)
	_ = storage.AuthorizeClientIDSecret(ctx, "console", "")
	_ = storage.AuthorizeClientIDSecret(ctx, "target", "")
	if slot(clientcreds.SlotCurrent) != cur || slot(clientcreds.SlotPrevious) != prev || slot(clientcreds.SlotNone) != none {
		t.Error("a public or exchange client was counted as a confidential authentication")
	}
}

// A rotation as the token endpoint sees it, through the real record, manager and
// resolver: both secrets until the overlap ends, then only the new one; a hard
// cut at once.
func TestTheTokenEndpointAfterARotation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for name, tc := range map[string]struct {
		overlap     time.Duration
		oldDuring   bool
		oldAtExpiry bool
	}{
		"an hour's overlap": {time.Hour, true, false},
		"a hard cut":        {0, false, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := memory.NewSecrets()
			clientcreds.Reconcile(ctx, []string{"grafana"}, store, nil, secretNow, slog.New(slog.DiscardHandler), clientcreds.Hooks{})
			resolver := clientcreds.NewResolver(store, nil, slog.New(slog.DiscardHandler))
			storage := secretStorage(t, resolver)
			now := secretNow
			issuer.SetStorageClock(storage, func() time.Time { return now })
			manager := &clientcreds.Manager{
				Store: store, Resolver: resolver, Now: func() time.Time { return secretNow },
				Generated: func(id string) bool { return id == "grafana" },
			}

			got, ok := resolver.Resolve(ctx, "grafana")
			if !ok {
				t.Fatal("no secret")
			}
			old := got.Current
			if err := storage.AuthorizeClientIDSecret(ctx, "grafana", old); err != nil {
				t.Fatalf("before the rotation: %v", err)
			}
			if _, err := manager.Rotate(ctx, "grafana", tc.overlap); err != nil {
				t.Fatal(err)
			}
			got, _ = resolver.Resolve(ctx, "grafana")
			fresh := got.Current
			if fresh == old {
				t.Fatal("the secret did not change")
			}

			if err := storage.AuthorizeClientIDSecret(ctx, "grafana", fresh); err != nil {
				t.Errorf("the new secret: %v", err)
			}
			if err := storage.AuthorizeClientIDSecret(ctx, "grafana", old); (err == nil) != tc.oldDuring {
				t.Errorf("the old secret during the overlap: %v, want accepted=%v", err, tc.oldDuring)
			}
			now = secretNow.Add(tc.overlap) // the overlap's last instant is excluded
			if err := storage.AuthorizeClientIDSecret(ctx, "grafana", old); err == nil {
				t.Error("the old secret is accepted at the end of its overlap")
			}
			now = secretNow.Add(tc.overlap + 24*time.Hour)
			if err := storage.AuthorizeClientIDSecret(ctx, "grafana", old); err == nil {
				t.Error("the old secret is accepted after its overlap")
			}
			if err := storage.AuthorizeClientIDSecret(ctx, "grafana", fresh); err != nil {
				t.Errorf("the new secret later: %v", err)
			}
		})
	}
}

// rereading is a Lookup whose first answer is the pair a replica cached before
// a rotation, and whose Reread is the pair now.
type rereading struct {
	cached, now clientcreds.Secrets
	rereads     int
}

func (r *rereading) Resolve(context.Context, string) (clientcreds.Secrets, bool) {
	return r.cached, true
}
func (r *rereading) Reread(context.Context, string) (clientcreds.Secrets, bool) {
	r.rereads++
	return r.now, true
}

// A secret that matches nothing in the cached pair is checked once against a
// fresh read before it is refused (ADR 0041).
func TestAuthorizeClientIDSecretReadsOnceMoreBeforeRefusing(t *testing.T) {
	t.Parallel()
	r := &rereading{
		cached: clientcreds.Secrets{Current: "one"},
		now:    clientcreds.Secrets{Current: "two", Previous: "one", PreviousValidUntil: secretNow.Add(time.Hour)},
	}
	storage := secretStorage(t, r)
	ctx := context.Background()
	if err := storage.AuthorizeClientIDSecret(ctx, "grafana", "one"); err != nil || r.rereads != 0 {
		t.Fatalf("a cached match: %v, rereads %d", err, r.rereads)
	}
	if err := storage.AuthorizeClientIDSecret(ctx, "grafana", "two"); err != nil || r.rereads != 1 {
		t.Fatalf("a secret rotated in after the cache: %v, rereads %d", err, r.rereads)
	}
	if err := storage.AuthorizeClientIDSecret(ctx, "grafana", "nope"); err == nil || r.rereads != 2 {
		t.Fatalf("a wrong secret: %v, rereads %d", err, r.rereads)
	}
}
