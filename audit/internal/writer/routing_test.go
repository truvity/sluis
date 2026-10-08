package writer_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/audit/internal/writer"
	"github.com/truvity/sluis/audit/profile"
	"github.com/truvity/sluis/audit/sdk/catalogue"
	"github.com/truvity/sluis/audit/sdk/record"
	"github.com/truvity/sluis/audit/sdk/sink"
	"github.com/truvity/sluis/audit/store"
	"github.com/truvity/sluis/audit/store/storetest"
)

// An action declares one category and takes no destination by name; each
// destination says which categories it takes. The emitter sends one record, and
// each destination that takes the category stores its own projection.
func categorised(t *testing.T) *catalogue.Composed {
	t.Helper()
	doc := strings.Replace(walletDoc, "    profiles: [security, billing, history, nowhere]\n", "    category: activity\n", 1)
	c, err := catalogue.Load([]byte(doc), [][]byte{[]byte(walletSchema)})
	if err != nil {
		t.Fatal(err)
	}
	x, err := c.Compose("wallet.credential.issued")
	if err != nil {
		t.Fatal(err)
	}
	return x
}

func destinations(t *testing.T, doc string) map[string]*profile.Profile {
	t.Helper()
	d, err := profile.ParseDeployment([]byte("apiVersion: " + profile.DeploymentAPIVersion + "\n" + doc))
	if err != nil {
		t.Fatal(err)
	}
	fw, err := profile.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	out, err := d.Compose(fw)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestARecordIsRoutedByCategoryToEveryDestinationThatTakesIt(t *testing.T) {
	s := splitter(t)
	s.Profiles = destinations(t, `
profiles:
  security:
    frameworks: [security]
    categories: [activity, access]
  billing:
    frameworks: [billing-nl]
    categories: [activity]
  history:
    frameworks: [history]
    categories: [history-only]
`)
	x := categorised(t)
	copies, err := s.Split(context.Background(), issued(t), x)
	if err != nil {
		t.Fatal(err)
	}
	got := byProfile(t, copies)
	if len(got) != 2 || got["security"] == nil || got["billing"] == nil || got["history"] != nil {
		t.Fatalf("destinations: %v", got)
	}
	// One record, two projections: billing keeps no identity, security keeps the actor.
	if got["billing"].GetActor() != nil || got["security"].GetActor() == nil {
		t.Errorf("actor: security %v, billing %v", got["security"].GetActor(), got["billing"].GetActor())
	}
	if got["billing"].GetMeter() == nil {
		t.Error("the billing projection lost its meter")
	}
	if got["security"].GetMeter() != nil {
		t.Error("the security projection kept the meter, which is not its field")
	}
	if h := s.Handled(x); len(h) != 2 {
		t.Errorf("handled = %v", h)
	}
}

func TestACategoryNobodyTakesIsReported(t *testing.T) {
	s := splitter(t)
	s.Profiles = destinations(t, "profiles:\n  history:\n    frameworks: [history]\n    categories: [other]\n")
	x := categorised(t)
	if got := s.Unhandled(x); len(got) != 1 || got[0] != "category:activity" {
		t.Fatalf("unhandled = %v", got)
	}
	copies, err := s.Split(context.Background(), issued(t), x)
	if err != nil || len(copies) != 0 {
		t.Fatalf("copies = %d, %v", len(copies), err)
	}
}

func TestADeprecatedProfilesListStillRoutes(t *testing.T) {
	s := splitter(t)
	s.Profiles = destinations(t, "profiles:\n  security:\n    frameworks: [security]\n")
	copies, err := s.Split(context.Background(), issued(t), composed(t))
	if err != nil || len(copies) != 1 || copies[0].GetProfile() != "security" {
		t.Fatalf("copies = %v, %v", copies, err)
	}
}

// The whole path: one record, sent once with every field, lands under the
// prefix of every destination that takes its category, each holding only that
// destination's projection, and each in the store that destination writes with.
func TestARecordLandsInEveryDestinationThatTakesItsCategory(t *testing.T) {
	c, err := catalogue.Load([]byte(strings.Replace(walletDoc, "    profiles: [security, billing, history, nowhere]\n", "    category: activity\n", 1)),
		[][]byte{[]byte(walletSchema)})
	if err != nil {
		t.Fatal(err)
	}
	registry := &writer.Registry{}
	registry.Register(c)

	locked, unlocked := storetest.NewMemory(), storetest.NewMemory()
	s := splitter(t)
	s.Profiles = destinations(t, `
profiles:
  security:
    frameworks: [security]
    categories: [activity]
  billing:
    frameworks: [billing-nl]
    categories: [activity]
  evidence:
    frameworks: [evidence-etsi]
    categories: [activity]
    key_alias: alias/acme-evidence
`)
	at := fixedDay(t)
	w, err := writer.New(&writer.Writer{
		Catalogues: registry,
		Splitter:   s,
		Roller: &writer.Roller{
			// Evidence is attested and writes to the locked store; the others
			// write nothing they cannot clear.
			Store: unlocked, Stores: map[string]store.Store{"evidence": locked},
			Instance: "writer-1", Now: func() time.Time { return at },
		},
		DeadLetter: &writer.StoreDeadLetter{Store: unlocked, Instance: "writer-1", Now: func() time.Time { return at }},
		Identity:   func(context.Context) string { return "workload:wallet" },
		Now:        func() time.Time { return at },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close(context.Background()) })

	if _, err := w.Write(context.Background(), &sink.Request{Records: []*record.Record{fresh(t)}, Delivery: sink.Block}); err != nil {
		t.Fatal(err)
	}

	prefixes := func(m *storetest.Memory) map[string]int {
		out := map[string]int{}
		for _, key := range m.Keys() {
			if parts := strings.Split(key, "/"); parts[0] == "records" {
				out[parts[1]]++
			}
		}
		return out
	}
	if got := prefixes(locked); len(got) != 1 || got["evidence"] != 1 {
		t.Errorf("the locked store holds %v", got)
	}
	if got := prefixes(unlocked); len(got) != 2 || got["security"] != 1 || got["billing"] != 1 {
		t.Errorf("the unlocked store holds %v", got)
	}
	byDestination := byProfile(t, append(decode(t, locked), decode(t, unlocked)...))
	for _, dest := range []string{"security", "billing", "evidence"} {
		if byDestination[dest] == nil {
			t.Fatalf("no copy for %s: %v", dest, byDestination)
		}
	}
	// Different projections of one record.
	if byDestination["billing"].GetActor() != nil || byDestination["security"].GetActor() == nil {
		t.Error("billing should keep no actor and security should")
	}
	if byDestination["security"].GetMeter() != nil || byDestination["billing"].GetMeter() == nil {
		t.Error("only billing keeps the meter")
	}
	if byDestination["billing"].GetId() == "" || byDestination["billing"].GetOriginHash() != byDestination["security"].GetOriginHash() {
		t.Error("the copies must descend from one original")
	}
}
