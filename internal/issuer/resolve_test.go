package issuer_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/issuer"
)

type scriptedDirectory struct {
	standing issuer.Standing
	err      error
}

func (d *scriptedDirectory) ResolveUser(context.Context, string) (issuer.Standing, error) {
	return d.standing, d.err
}

func authoritative(groups ...string) issuer.Standing {
	return issuer.Standing{Found: true, Groups: groups, Authoritative: true}
}

// A new instance, as on Lambda or after a rollout, must find what the one
// before it learned.
func TestHeldAnswerSurvivesAnotherInstance(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	shared := issuer.NewMemoryState()

	a := issuer.NewResolver(&scriptedDirectory{standing: authoritative("eng")}, time.Hour)
	a.UseState(shared)
	if _, err := a.Resolve(ctx, "Ada@North.example"); err != nil {
		t.Fatalf("instance A: %v", err)
	}

	b := issuer.NewResolver(&scriptedDirectory{standing: issuer.Standing{Found: true}}, time.Hour)
	b.UseState(shared)
	got, err := b.Resolve(ctx, "ada@north.example")
	if err != nil {
		t.Fatalf("instance B: %v", err)
	}
	if !got.Held || !slices.Equal(got.Groups, []string{"eng"}) {
		t.Errorf("instance B got %+v, want the groups A learned, held", got)
	}

	// Nothing but groups and a time is stored.
	raw, found, _ := shared.Get(ctx, "issuer:held:ada@north.example")
	if !found {
		t.Fatalf("no record under the lower-cased identity")
	}
	if len(raw) > 200 {
		t.Errorf("record is %d bytes, more than groups and a time", len(raw))
	}
}

func TestHeldAnswerExpiresWithTheWindow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clock := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }
	shared := issuer.NewMemoryState()
	shared.SetClock(now)

	a := issuer.NewResolver(&scriptedDirectory{standing: authoritative("eng")}, time.Hour)
	a.UseState(shared)
	a.SetClock(now)
	if _, err := a.Resolve(ctx, "ada@north.example"); err != nil {
		t.Fatal(err)
	}

	b := issuer.NewResolver(&scriptedDirectory{err: errors.New("down")}, time.Hour)
	b.UseState(shared)
	b.SetClock(now)

	clock = clock.Add(59 * time.Minute)
	if got, err := b.Resolve(ctx, "ada@north.example"); err != nil || !got.Held {
		t.Fatalf("inside the window: %+v, %v", got, err)
	}

	clock = clock.Add(2 * time.Minute)
	if _, err := b.Resolve(ctx, "ada@north.example"); err == nil {
		t.Fatalf("past the window: want an error")
	}
	if _, found, _ := shared.Get(ctx, "issuer:held:ada@north.example"); found {
		t.Errorf("the record outlived the window")
	}
}

func TestSuspendedAndMissingDeleteTheHeldAnswer(t *testing.T) {
	t.Parallel()
	for name, gone := range map[string]issuer.Standing{
		"suspended": {Found: true, Suspended: true, Authoritative: true},
		"not found": {Found: false, Authoritative: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			shared := issuer.NewMemoryState()
			dir := &scriptedDirectory{standing: authoritative("eng")}
			r := issuer.NewResolver(dir, time.Hour)
			r.UseState(shared)
			if _, err := r.Resolve(ctx, "ada@north.example"); err != nil {
				t.Fatal(err)
			}

			dir.standing = gone
			var refused *issuer.Refused
			if _, err := r.Resolve(ctx, "ada@north.example"); !errors.As(err, &refused) {
				t.Fatalf("want a refusal, got %v", err)
			}
			if _, found, _ := shared.Get(ctx, "issuer:held:ada@north.example"); found {
				t.Errorf("the entry is still in the State")
			}

			// Another instance cannot hold what was deleted.
			dir.standing = issuer.Standing{Found: true}
			if _, err := r.Resolve(ctx, "ada@north.example"); err == nil {
				t.Errorf("a deleted answer was held")
			}
		})
	}
}

func TestForgetDeletesFromTheState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	shared := issuer.NewMemoryState()
	r := issuer.NewResolver(&scriptedDirectory{standing: authoritative("eng")}, time.Hour)
	r.UseState(shared)
	if _, err := r.Resolve(ctx, "ada@north.example"); err != nil {
		t.Fatal(err)
	}
	if err := r.Forget(ctx, " Ada@North.example "); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := shared.Get(ctx, "issuer:held:ada@north.example"); found {
		t.Errorf("Forget left the entry")
	}
}

// With no State the answers stay in the process, as before.
func TestHeldAnswerFallsBackToMemory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := &scriptedDirectory{standing: authoritative("eng")}
	r := issuer.NewResolver(dir, time.Hour)
	if _, err := r.Resolve(ctx, "ada@north.example"); err != nil {
		t.Fatal(err)
	}
	dir.standing = issuer.Standing{Found: true}
	got, err := r.Resolve(ctx, "ada@north.example")
	if err != nil || !got.Held {
		t.Fatalf("got %+v, %v; want held from memory", got, err)
	}
	if err := r.Forget(ctx, "ada@north.example"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(ctx, "ada@north.example"); err == nil {
		t.Errorf("held after Forget")
	}
}
