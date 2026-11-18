package groupsclaim_test

import (
	"encoding/json"
	"math/rand/v2"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/truvity/sluis/v2/kits/go/groupsclaim"
)

// The vector file is shared (kits/conformance/token): the store's loader reads its delimiters, and session and exchange
// are held to the same rule through this package.
const vectorFile = "../../conformance/token/groups-vectors.json"

type vectors struct {
	Narrowing []struct {
		Name      string   `json:"name"`
		Held      []string `json:"held"`
		Requires  []string `json:"requires"`
		Things    []string `json:"things"`
		Delimiter string   `json:"delimiter"`
		Groups    []string `json:"groups"`
	} `json:"narrowing"`
}

func load(t *testing.T) vectors {
	t.Helper()
	raw, err := os.ReadFile(vectorFile)
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Narrowing) == 0 {
		t.Fatal("the vector file holds no narrowing vector")
	}
	return v
}

// verifies: REQ-TOKEN-033, REQ-TOKEN-046, REQ-TOKEN-047
func TestGroupsMatchTheVectors(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range load(t).Narrowing {
		if seen[c.Name] {
			t.Errorf("two vectors are named %q", c.Name)
		}
		seen[c.Name] = true
		got := groupsclaim.For(c.Held, groupsclaim.Audience{Requires: c.Requires, Things: c.Things, Delimiter: c.Delimiter})
		if got == nil {
			t.Errorf("%s: the claim is nil, want an empty slice at least", c.Name)
		}
		if !slices.Equal(got, c.Groups) && (len(got) != 0 || len(c.Groups) != 0) {
			t.Errorf("%s: groups = %q, want %q", c.Name, got, c.Groups)
		}
	}
}

// The names of a small world, so that random sets collide often: a collision is where narrowing could go wrong.
var (
	scopes = []string{"dev", "acme", "prod"}
	things = []string{"app", "billing", "x"}
	roles  = []string{"user", "operator", "admin"}
)

func randomName(r *rand.Rand) string {
	switch r.IntN(12) {
	case 0:
		return "bad"
	case 1:
		return "dev:app:"
	case 2:
		return "*:" + things[r.IntN(len(things))] + ":" + roles[r.IntN(len(roles))]
	case 3:
		return scopes[r.IntN(len(scopes))] + ":*:" + roles[r.IntN(len(roles))]
	case 4:
		return "dev:app:user:extra"
	}
	return scopes[r.IntN(len(scopes))] + ":" + things[r.IntN(len(things))] + ":" + roles[r.IntN(len(roles))]
}

func randomNames(r *rand.Rand) []string {
	out := make([]string, r.IntN(9))
	for i := range out {
		out[i] = randomName(r)
	}
	return out
}

func pairOf(name string) (string, string, bool) {
	p := strings.Split(name, ":")
	if len(p) != 3 || slices.Contains(p, "") || strings.Contains(name, "*") {
		return "", "", false
	}
	return p[0] + ":" + p[1], p[1], true
}

// verifies: REQ-TOKEN-033, REQ-TOKEN-046
func TestNarrowOnlyEverKeepsWhatWasHeldAndWhatTheAudienceAsksFor(t *testing.T) {
	r := rand.New(rand.NewPCG(4, 8)) //nolint:gosec // G404: a fixed seed on purpose, so a failure repeats
	for range 20000 {
		held, requires, override := randomNames(r), randomNames(r), []string{things[r.IntN(len(things))], "*", "", "app:x"}[:r.IntN(5)]
		got := groupsclaim.Narrow(held, requires, override)
		if got == nil {
			t.Fatalf("Narrow(%v, %v, %v) is nil", held, requires, override)
		}
		if !slices.IsSorted(got) || len(slices.Compact(slices.Clone(got))) != len(got) {
			t.Fatalf("Narrow(%v, %v, %v) = %v: not sorted or with repeats", held, requires, override, got)
		}
		pairs := map[string]bool{}
		for _, q := range requires {
			if p, _, ok := pairOf(q); ok {
				pairs[p] = true
			}
		}
		keeps := func(name string) bool {
			p, thing, ok := pairOf(name)
			return ok && (pairs[p] || slices.Contains(override, thing))
		}
		for _, g := range got {
			// Never wider than the held set, never a wildcard, and only a pair or a thing the audience asks for.
			if !slices.Contains(held, g) || strings.Contains(g, "*") || !keeps(g) {
				t.Fatalf("Narrow(%v, %v, %v) gave %q", held, requires, override, g)
			}
		}
		// Complete: every held name the audience asks for is kept.
		for _, h := range held {
			if keeps(h) && !slices.Contains(got, h) {
				t.Fatalf("Narrow(%v, %v, %v) dropped %q", held, requires, override, h)
			}
		}
		// Idempotent, and monotone in the held set: a person who holds more never gets less.
		if again := groupsclaim.Narrow(got, requires, override); !slices.Equal(again, got) {
			t.Fatalf("Narrow is not idempotent: %v then %v", got, again)
		}
		for _, g := range groupsclaim.Narrow(held[:len(held)/2], requires, override) {
			if !slices.Contains(got, g) {
				t.Fatalf("a smaller held set gave %q, which the larger did not", g)
			}
		}
		// An override only ever adds.
		for _, g := range groupsclaim.Narrow(held, requires, nil) {
			if !slices.Contains(got, g) {
				t.Fatalf("the override took %q away", g)
			}
		}
	}
}

// verifies: REQ-TOKEN-047
func TestRespellKeepsDifferentNamesDifferent(t *testing.T) {
	names := []string{"dev:app:user", "dev:app:admin", "prod:app:user", "dev:app-x:user", "dev:app_x:user", "dev-app:x:user"}
	for _, d := range []string{".", "/", "~~", "|"} {
		seen := map[string]string{}
		for _, n := range groupsclaim.Respell(names, d) {
			if strings.Contains(n, ":") {
				t.Errorf("delimiter %q left a colon in %q", d, n)
			}
			if prev, dup := seen[n]; dup {
				t.Errorf("delimiter %q made %q and %q one name", d, prev, n)
			}
			seen[n] = n
		}
	}
	// A second pass finds no colon to rewrite.
	once := groupsclaim.Respell(names, ".")
	if twice := groupsclaim.Respell(once, "."); !slices.Equal(once, twice) {
		t.Errorf("a second pass changed %v to %v", once, twice)
	}
	// No delimiter, no copy.
	if got := groupsclaim.Respell(names, ""); !slices.Equal(got, names) {
		t.Errorf("an empty delimiter changed %v to %v", names, got)
	}
}
