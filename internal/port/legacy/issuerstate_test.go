package legacy_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/internal/port/porttest/grantcost"
)

// The issuer's State, built over the ports, behaves as it always did over the
// Valkey and over memory, on either adapter. The keys are the issuer's own.
func TestTheIssuersStateOverThePortsBehavesAsItAlwaysDid(t *testing.T) {
	t.Parallel()
	for name, open := range map[string]func(*testing.T) (issuer.State, func(time.Duration)){
		"memory adapter": func(*testing.T) (issuer.State, func(time.Duration)) {
			s := memory.New()
			return issuer.NewPortState(s, s), s.Advance
		},
		"legacy adapter": func(t *testing.T) (issuer.State, func(time.Duration)) {
			f := newFixture(t)
			return issuer.NewPortState(f.ports.State, f.ports.Index), f.redis.FastForward
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			state, advance := open(t)

			if _, found, err := state.Get(ctx, "issuer:request:none"); err != nil || found {
				t.Errorf("an unknown key = %v, %v", found, err)
			}
			if err := state.Set(ctx, "issuer:request:r", []byte("a login"), time.Hour); err != nil {
				t.Fatal(err)
			}
			if value, found, err := state.Get(ctx, "issuer:request:r"); err != nil || !found || string(value) != "a login" {
				t.Fatalf("Get = %q, %v, %v", value, found, err)
			}
			if won, err := state.SetIfAbsent(ctx, "issuer:code:c", []byte("first"), time.Hour); err != nil || !won {
				t.Fatalf("the first claim = %v, %v", won, err)
			}
			if won, err := state.SetIfAbsent(ctx, "issuer:code:c", []byte("second"), time.Hour); err != nil || won {
				t.Fatalf("the second claim = %v, %v", won, err)
			}
			if value, _, _ := state.Get(ctx, "issuer:code:c"); string(value) != "first" {
				t.Errorf("the code is %q, want the first claim's", value)
			}
			if err := state.Set(ctx, "issuer:nottl", []byte("x"), 0); err == nil {
				t.Error("a value with no lifetime was stored")
			}
			if err := state.Delete(ctx, "issuer:request:r"); err != nil {
				t.Fatal(err)
			}
			if err := state.Delete(ctx, "issuer:request:r"); err != nil {
				t.Errorf("deleting what is gone: %v", err)
			}

			if members, err := state.Members(ctx, "issuer:sessions-of:ada"); err != nil || len(members) != 0 {
				t.Errorf("a set nobody wrote = %v, %v", members, err)
			}
			for _, id := range []string{"b", "a"} {
				if err := state.Add(ctx, "issuer:sessions-of:ada", id, time.Hour); err != nil {
					t.Fatal(err)
				}
			}
			members, _ := state.Members(ctx, "issuer:sessions-of:ada")
			slices.Sort(members)
			if !slices.Equal(members, []string{"a", "b"}) {
				t.Errorf("members = %v", members)
			}
			if err := state.Remove(ctx, "issuer:sessions-of:ada", "a"); err != nil {
				t.Fatal(err)
			}

			advance(2 * time.Hour)
			if _, found, _ := state.Get(ctx, "issuer:code:c"); found {
				t.Error("an expired claim was still there")
			}
			if members, _ := state.Members(ctx, "issuer:sessions-of:ada"); len(members) != 0 {
				t.Errorf("an expired set still has %v", members)
			}
		})
	}
}

// An idle agent session stays in the index sets as long as its record, and
// every revocation still finds it, over the legacy (Valkey) adapter and the
// memory one (docs/decisions/0040-agent-class-sessions.md).
func TestAnIdleAgentSessionIsRevocableOverThePorts(t *testing.T) {
	t.Parallel()

	t.Run("memory adapter", func(t *testing.T) {
		t.Parallel()
		s := memory.New()
		grantcost.IdleAgentSessionIsRevocable(t, issuer.NewPortState(s, s), s.Advance)
	})
	t.Run("legacy adapter", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		grantcost.IdleAgentSessionIsRevocable(t, issuer.NewPortState(f.ports.State, f.ports.Index), f.redis.FastForward)
	})
}

// A reused authorization code revokes what its first redemption issued over
// the legacy (Valkey) adapter too: the tombstone is its conditional create
// (SET NX), as it is DynamoDB's conditional PutItem over the tables.
//
// The directory's workspace is kept in memory beside it: the legacy adapter
// has no object of its own for a `ws.` key, and the directory is not what is
// under test.
func TestAReusedCodeRevokesWhatItIssuedOverTheLegacyAdapter(t *testing.T) {
	grantcost.CodeReuse(t, func(t *testing.T) grantcost.Env {
		f := newFixture(t)
		set := f.ports
		set.State = workspacesApart{State: f.ports.State, ws: memory.New()}
		return grantcost.Env{Set: set, Advance: f.redis.FastForward}
	})
}

// workspacesApart is a State that keeps `ws.` keys in a store of their own.
type workspacesApart struct {
	port.State
	ws port.State
}

func (w workspacesApart) of(key string) port.State {
	if strings.HasPrefix(key, "ws.") {
		return w.ws
	}
	return w.State
}

func (w workspacesApart) Get(ctx context.Context, key string) (port.Record, error) {
	return w.of(key).Get(ctx, key)
}

func (w workspacesApart) Put(ctx context.Context, key string, value []byte, ttl time.Duration) (port.Revision, error) {
	return w.of(key).Put(ctx, key, value, ttl)
}

func (w workspacesApart) Create(ctx context.Context, key string, value []byte, ttl time.Duration) (port.Revision, error) {
	return w.of(key).Create(ctx, key, value, ttl)
}

func (w workspacesApart) Update(
	ctx context.Context, key string, value []byte, ttl time.Duration, rev port.Revision,
) (port.Revision, error) {
	return w.of(key).Update(ctx, key, value, ttl, rev)
}

func (w workspacesApart) Delete(ctx context.Context, key string) error {
	return w.of(key).Delete(ctx, key)
}

func (w workspacesApart) DeleteIfRevision(ctx context.Context, key string, rev port.Revision) error {
	return w.of(key).DeleteIfRevision(ctx, key, rev)
}

func (w workspacesApart) List(ctx context.Context, prefix, page string, limit int) (port.Page, error) {
	return w.of(prefix).List(ctx, prefix, page, limit)
}
