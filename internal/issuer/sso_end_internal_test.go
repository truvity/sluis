package issuer

import (
	"context"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/port/memory"
)

// ssoEndStates are the States a sign-in is ended over.
var ssoEndStates = []struct {
	name  string
	build func() State
}{
	{"memory-state", func() State { return NewMemoryState() }},
	{"ports", func() State {
		s := memory.New()
		return NewPortState(s, s)
	}},
}

// opCounter counts the calls a State is asked to make.
type opCounter struct {
	State
	ops int
}

func (c *opCounter) Get(ctx context.Context, key string) ([]byte, bool, error) {
	c.ops++
	return c.State.Get(ctx, key)
}

func (c *opCounter) Delete(ctx context.Context, key string) error {
	c.ops++
	return c.State.Delete(ctx, key)
}

func (c *opCounter) Remove(ctx context.Context, key, member string) error {
	c.ops++
	return c.State.Remove(ctx, key, member)
}

func (c *opCounter) Members(ctx context.Context, key string) ([]string, error) {
	c.ops++
	return c.State.Members(ctx, key)
}

func (c *opCounter) Add(ctx context.Context, key, member string, ttl time.Duration) error {
	c.ops++
	return c.State.Add(ctx, key, member, ttl)
}

func (c *opCounter) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	c.ops++
	return c.State.Set(ctx, key, value, ttl)
}

// signInWith begins a sign-in that two clients were involved in.
func signInWith(t *testing.T, state State) (*SSO, string) {
	t.Helper()

	ctx := context.Background()
	sso := NewSSO(state, time.Hour)

	session, _, err := sso.Begin(ctx, "ada@north.example", "test")
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	for _, client := range []string{"console", "cli"} {
		if err = sso.Involve(ctx, session.ID, client); err != nil {
			t.Fatalf("Involve: %v", err)
		}
	}

	return sso, session.ID
}

func TestEndEmptiesTheClientsSet(t *testing.T) {
	t.Parallel()

	for _, kind := range ssoEndStates {
		t.Run(kind.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			sso, id := signInWith(t, kind.build())

			if err := sso.End(ctx, id); err != nil {
				t.Fatalf("End: %v", err)
			}

			involved, err := sso.Involved(ctx, id)
			if err != nil || len(involved) != 0 {
				t.Errorf("Involved after End = %v, %v; want none", involved, err)
			}
		})
	}
}

func TestEndForEmptiesTheClientsSetAndBothIndexes(t *testing.T) {
	t.Parallel()

	for _, kind := range ssoEndStates {
		t.Run(kind.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			state := kind.build()
			sso, id := signInWith(t, state)

			ended, err := sso.EndFor(ctx, "ada@north.example")
			if err != nil || ended != 1 {
				t.Fatalf("EndFor = %d, %v; want 1", ended, err)
			}

			if involved, _ := sso.Involved(ctx, id); len(involved) != 0 {
				t.Errorf("Involved after EndFor = %v; want none", involved)
			}

			for _, key := range []string{ssoAllKey, ssoOfKey("ada@north.example")} {
				members, _ := state.Members(ctx, key)
				if len(members) != 0 {
					t.Errorf("%s holds %v after EndFor; want none", key, members)
				}
			}
		})
	}
}

// TestEndCosts pins the store operations of ending a sign-in with two
// clients: the record read, the cookie pointer, two indexes, the clients set
// (1 read + 2 removals) and the record.
func TestEndCosts(t *testing.T) {
	t.Parallel()

	for _, kind := range ssoEndStates {
		t.Run(kind.name, func(t *testing.T) {
			t.Parallel()

			counting := &opCounter{State: kind.build()}
			sso, id := signInWith(t, counting)

			counting.ops = 0
			if err := sso.End(context.Background(), id); err != nil {
				t.Fatalf("End: %v", err)
			}

			if counting.ops != 8 {
				t.Errorf("End made %d operations, want 8", counting.ops)
			}
		})
	}
}
