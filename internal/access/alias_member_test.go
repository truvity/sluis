package access_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/truvity/sluis/backend/fake"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/hub"
	"github.com/truvity/sluis/policy"
)

// A person the directory lists in a group by an alias holds what that
// group feeds, under their primary address: HoldersOf is what the GitHub,
// Slack and Outline teams are fed from, and it reads the person's groups
// by the address their account is kept under.
func TestAMemberByAliasHoldsTheGroupItFeeds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	directory := fake.New("C0north", "north.example").
		WithAccount("ada@north.example", "Ada", "North").
		WithAlias("ada@north.example", "ada.lovelace@north.example").
		WithGroup("platform@north.example", "ada.lovelace@north.example")
	h := hub.New(hub.NewMemoryStore(), hub.NewMemorySnapshots(), hub.Config{}, slog.New(slog.DiscardHandler))
	h.SetClock(func() time.Time { return time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC) })
	if _, err := h.Adopt(ctx, hub.Workspace{Admin: "admin@north.example"}, directory); err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	h.Wait()

	p, err := policy.Parse([]byte(`
version: 1
groups:
  devel:k8s:viewer: { members: [platform@north.example] }
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	set, err := policy.NewSet(p)
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}
	people, _, err := h.People(ctx, hub.PeopleQuery{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	holders := access.NewAuthorizer(set, nil, 0).HoldersOf(people, "devel:k8s:viewer", "")
	if len(holders) != 1 || holders[0].Email != "ada@north.example" {
		t.Fatalf("HoldersOf(devel:k8s:viewer) = %+v, want ada through the group that lists her alias", holders)
	}
}
