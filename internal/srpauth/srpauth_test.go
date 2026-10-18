package srpauth_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/port/memory"
	"github.com/truvity/sluis/internal/srpauth"
	"github.com/truvity/sluis/internal/throttle"
	"github.com/truvity/sluis/srp"
)

const password = "a generated password of forty characters!"

func credential(user, pw string) srp.Credential { return srp.Default.NewCredential(user, pw) }

// accounts is a lookup holding the recovery account only.
func accounts(allowed ...string) srpauth.Lookup {
	cred := credential(srpauth.RecoveryUser, password)
	return func(_ context.Context, user string) (srp.Credential, bool, error) {
		for _, a := range allowed {
			if a == user && user == srpauth.RecoveryUser {
				return cred, true, nil
			}
		}
		return srp.Credential{}, false, nil
	}
}

func newService(store srpauth.Store) *srpauth.Service {
	return &srpauth.Service{
		Params:   srp.Default,
		Store:    store,
		DecoyKey: func() ([]byte, error) { return []byte("the decoy key every container shares"), nil },
		Refusals: throttle.Default(),
		Starts:   srpauth.NewStarts(nil),
	}
}

// run is the client's two requests against service.
func run(t *testing.T, s *srpauth.Service, source, bind, user, pw string, lookup srpauth.Lookup) (srpauth.Proved, error) {
	t.Helper()
	ctx := context.Background()
	begun, err := s.Start(ctx, source, bind, user, lookup)
	if err != nil {
		return srpauth.Proved{}, err
	}
	client, err := srp.Default.NewClient(user, pw, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	m1, err := client.Respond(begun.Salt, begun.B)
	if err != nil {
		t.Fatal(err)
	}
	proved, err := s.Finish(ctx, source, bind, begun.Handle, client.Public(), m1, lookup)
	if err == nil {
		if verr := client.VerifyServer(proved.M2); verr != nil {
			t.Errorf("the server's proof did not verify: %v", verr)
		}
	}
	return proved, err
}

func TestAnAccountProvesItself(t *testing.T) {
	s := newService(srpauth.StateStore{State: memory.New()})
	proved, err := run(t, s, "10.0.0.1", "state-1", "recovery", password, accounts("recovery"))
	if err != nil {
		t.Fatal(err)
	}
	if proved.User != "recovery" {
		t.Errorf("user = %q", proved.User)
	}
}

func TestAWrongPasswordIsRefused(t *testing.T) {
	s := newService(srpauth.StateStore{State: memory.New()})
	if _, err := run(t, s, "10.0.0.1", "state-1", "recovery", "not it", accounts("recovery")); !errors.Is(err, srpauth.ErrRefused) {
		t.Errorf("err = %v, want ErrRefused", err)
	}
}

// An unknown username, an account this request may not use and a wrong password
// all end in the same refusal, and the start of each answers the same shape: a
// salt that does not change between two asks and a B of the right width.
func TestAnUnknownUsernameIsIndistinguishable(t *testing.T) {
	s := newService(srpauth.StateStore{State: memory.New()})
	ctx := context.Background()
	lookup := accounts("recovery")

	cases := map[string]string{
		"unknown": "nobody", "other account the request may not use": "conformance", "empty": "",
		"very long": string(bytes.Repeat([]byte("x"), 500)),
	}
	for name, user := range cases {
		t.Run(name, func(t *testing.T) {
			first, err := s.Start(ctx, "src", "bind", user, lookup)
			if err != nil {
				t.Fatal(err)
			}
			second, err := s.Start(ctx, "src", "bind", user, lookup)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(first.Salt, second.Salt) {
				t.Error("the salt of a username with no account changed between two asks")
			}
			if len(first.B) != srp.Default.Size() || len(first.Handle) != 22 {
				t.Errorf("shape: B is %d bytes, handle %d characters", len(first.B), len(first.Handle))
			}
			if _, err = run(t, s, "src", "bind", user, "guess", lookup); !errors.Is(err, srpauth.ErrRefused) {
				t.Errorf("err = %v, want ErrRefused", err)
			}
		})
	}

	known, err := s.Start(ctx, "src", "bind", "recovery", lookup)
	if err != nil {
		t.Fatal(err)
	}
	if len(known.Salt) != len(mustStart(t, s, "nobody", lookup).Salt) {
		t.Error("an account's salt is told apart from a decoy's by its length")
	}
}

func mustStart(t *testing.T, s *srpauth.Service, user string, lookup srpauth.Lookup) srpauth.Begun {
	t.Helper()
	b, err := s.Start(context.Background(), "src", "bind", user, lookup)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAHandleIsSpentByItsFirstFinish(t *testing.T) {
	s := newService(srpauth.StateStore{State: memory.New()})
	ctx := context.Background()
	lookup := accounts("recovery")
	begun := mustStart(t, s, "recovery", lookup)
	client, _ := srp.Default.NewClient("recovery", password, rand.Reader)
	m1, _ := client.Respond(begun.Salt, begun.B)

	if _, err := s.Finish(ctx, "src", "bind", begun.Handle, client.Public(), m1, lookup); err != nil {
		t.Fatalf("first finish: %v", err)
	}
	// The identical, valid request again: a replay.
	if _, err := s.Finish(ctx, "src", "bind", begun.Handle, client.Public(), m1, lookup); !errors.Is(err, srpauth.ErrRefused) {
		t.Errorf("replay: err = %v, want ErrRefused", err)
	}
}

func TestAnExpiredHandleIsRefused(t *testing.T) {
	mem := memory.New()
	s := newService(srpauth.StateStore{State: mem})
	lookup := accounts("recovery")
	begun := mustStart(t, s, "recovery", lookup)
	client, _ := srp.Default.NewClient("recovery", password, rand.Reader)
	m1, _ := client.Respond(begun.Salt, begun.B)

	mem.Advance(srpauth.ExchangeTTL + time.Second)
	if _, err := s.Finish(context.Background(), "src", "bind", begun.Handle, client.Public(), m1, lookup); !errors.Is(err, srpauth.ErrRefused) {
		t.Errorf("err = %v, want ErrRefused", err)
	}
}

func TestAnotherBrowsersBindingIsRefusedAndSpendsTheHandle(t *testing.T) {
	s := newService(srpauth.StateStore{State: memory.New()})
	ctx := context.Background()
	lookup := accounts("recovery")
	begun := mustStart(t, s, "recovery", lookup)
	client, _ := srp.Default.NewClient("recovery", password, rand.Reader)
	m1, _ := client.Respond(begun.Salt, begun.B)

	if _, err := s.Finish(ctx, "src", "someone else's state", begun.Handle, client.Public(), m1, lookup); !errors.Is(err, srpauth.ErrRefused) {
		t.Errorf("other binding: err = %v, want ErrRefused", err)
	}
	if _, err := s.Finish(ctx, "src", "bind", begun.Handle, client.Public(), m1, lookup); !errors.Is(err, srpauth.ErrRefused) {
		t.Errorf("the right binding after a wrong one: err = %v, want ErrRefused (the handle is spent)", err)
	}
}

// Two services on one State are two containers: the second answers the finish
// of an exchange the first began, and the first cannot answer it again.
func TestTwoContainersShareOneExchange(t *testing.T) {
	mem := memory.New()
	one, two := newService(srpauth.StateStore{State: mem}), newService(srpauth.StateStore{State: mem})
	ctx := context.Background()
	lookup := accounts("recovery")

	begun := mustStart(t, one, "recovery", lookup)
	client, _ := srp.Default.NewClient("recovery", password, rand.Reader)
	m1, _ := client.Respond(begun.Salt, begun.B)
	proved, err := two.Finish(ctx, "src", "bind", begun.Handle, client.Public(), m1, lookup)
	if err != nil {
		t.Fatalf("the other container: %v", err)
	}
	if err = client.VerifyServer(proved.M2); err != nil {
		t.Error(err)
	}
	if _, err = one.Finish(ctx, "src", "bind", begun.Handle, client.Public(), m1, lookup); !errors.Is(err, srpauth.ErrRefused) {
		t.Errorf("the first container after the second: err = %v, want ErrRefused", err)
	}
}

// Of several finishes racing for one handle, exactly one takes it.
func TestOnlyOneFinishTakesAHandle(t *testing.T) {
	mem := memory.New()
	store := srpauth.StateStore{State: mem}
	s := newService(store)
	lookup := accounts("recovery")
	begun := mustStart(t, s, "recovery", lookup)

	var wg sync.WaitGroup
	won := make(chan struct{}, 20)
	for range 20 {
		wg.Go(func() {
			if _, err := store.Take(context.Background(), begun.Handle); err == nil {
				won <- struct{}{}
			}
		})
	}
	wg.Wait()
	if len(won) != 1 {
		t.Errorf("%d takers won one handle, want exactly 1", len(won))
	}
}

func TestAnAccountTurnedOffBetweenTheRequestsIsRefused(t *testing.T) {
	s := newService(srpauth.StateStore{State: memory.New()})
	ctx := context.Background()
	begun := mustStart(t, s, "recovery", accounts("recovery"))
	client, _ := srp.Default.NewClient("recovery", password, rand.Reader)
	m1, _ := client.Respond(begun.Salt, begun.B)

	off := accounts() // the gate was thrown after the start
	if _, err := s.Finish(ctx, "src", "bind", begun.Handle, client.Public(), m1, off); !errors.Is(err, srpauth.ErrRefused) {
		t.Errorf("err = %v, want ErrRefused", err)
	}
}

func TestRefusalsAreThrottledPerSourceAndTheRightProofFromAnotherSourceStillWorks(t *testing.T) {
	s := newService(srpauth.StateStore{State: memory.New()})
	lookup := accounts("recovery")
	for range throttle.DefaultPerSource {
		if _, err := run(t, s, "noisy", "bind", "recovery", "wrong", lookup); !errors.Is(err, srpauth.ErrRefused) {
			t.Fatalf("err = %v, want ErrRefused", err)
		}
	}
	if _, err := s.Start(context.Background(), "noisy", "bind", "recovery", lookup); !errors.Is(err, srpauth.ErrThrottled) {
		t.Errorf("a source that spent its refusals: err = %v, want ErrThrottled", err)
	}
	if _, err := run(t, s, "quiet", "bind", "recovery", password, lookup); err != nil {
		t.Errorf("another source with the right password: %v", err)
	}
}

func TestAMalformedClientValueIsRefused(t *testing.T) {
	s := newService(srpauth.StateStore{State: memory.New()})
	lookup := accounts("recovery")
	for name, a := range map[string][]byte{"zero": make([]byte, srp.Default.Size()), "empty": nil} {
		begun := mustStart(t, s, "recovery", lookup)
		if _, err := s.Finish(context.Background(), "src", "bind", begun.Handle, a, make([]byte, 32), lookup); !errors.Is(err, srpauth.ErrRefused) {
			t.Errorf("A is %s: err = %v, want ErrRefused", name, err)
		}
	}
}

func TestAStateThatCannotBeReadIsNotARefusal(t *testing.T) {
	s := newService(failingStore{})
	_, err := s.Start(context.Background(), "src", "bind", "recovery", accounts("recovery"))
	if !errors.Is(err, srpauth.ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
}

type failingStore struct{}

func (failingStore) Put(context.Context, string, srpauth.Record, time.Duration) error {
	return errors.New("down")
}
func (failingStore) Take(context.Context, string) (srpauth.Record, error) {
	return srpauth.Record{}, errors.New("down")
}
