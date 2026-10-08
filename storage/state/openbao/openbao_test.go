package openbao_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/truvity/sluis/storage/openbao/openbaotest"
	"github.com/truvity/sluis/storage/state"
	"github.com/truvity/sluis/storage/state/conformance"
	baostate "github.com/truvity/sluis/storage/state/openbao"
)

// policy is the one the package documentation gives an estate, for a prefix.
func policy(mount, prefix string) string {
	return fmt.Sprintf(`
path "%[1]s/config" { capabilities = ["read"] }
path "%[1]s/data/%[2]s/*" { capabilities = ["create", "update", "read"] }
path "%[1]s/metadata/%[2]s" { capabilities = ["list"] }
path "%[1]s/metadata/%[2]s/*" { capabilities = ["read", "list", "delete"] }
`, mount, prefix)
}

func TestConformance(t *testing.T) {
	dev := openbaotest.Env(t)
	mount := dev.MountKV(t, 0)
	c := dev.Client(t, policy(mount, "t"))
	conformance.Run(t, func(t *testing.T) state.Store {
		s, err := baostate.Open(t.Context(), c, baostate.Config{
			Mount: mount, Prefix: "t/" + strings.ReplaceAll(t.Name(), "/", "-"),
		})
		if err != nil {
			t.Fatal(err)
		}
		return s
	})
}

func TestOpenRefusesOneVersion(t *testing.T) {
	dev := openbaotest.Env(t)
	mount := dev.MountKV(t, 1)
	c := dev.Client(t, policy(mount, "t"))
	if _, err := baostate.Open(t.Context(), c, baostate.Config{Mount: mount, Prefix: "t"}); !errors.Is(err, baostate.ErrTooFewVersions) {
		t.Fatalf("Open on max_versions=1: %v, want ErrTooFewVersions", err)
	}
	if _, err := baostate.Open(t.Context(), c, baostate.Config{Mount: mount, Prefix: "t", AssumeVersions: true}); err != nil {
		t.Fatalf("AssumeVersions: %v", err)
	}
	two := dev.MountKV(t, 2)
	c2 := dev.Client(t, policy(two, "t"))
	if _, err := baostate.Open(t.Context(), c2, baostate.Config{Mount: two, Prefix: "t"}); err != nil {
		t.Fatalf("max_versions=2: %v", err)
	}
}

func TestOpenRefusesWhatIsNotKV2(t *testing.T) {
	dev := openbaotest.Env(t)
	mount := dev.MountTransit(t)
	c := dev.Client(t, `path "`+mount+`/*" { capabilities = ["read", "list"] }`)
	if _, err := baostate.Open(t.Context(), c, baostate.Config{Mount: mount}); err == nil {
		t.Fatal("Open accepted a transit mount as KV")
	}
}

// The document's top-level members are the secret's keys, and come back as
// one object.
func TestFieldsAreTheSecretKeys(t *testing.T) {
	dev := openbaotest.Env(t)
	mount := dev.MountKV(t, 0)
	c := dev.Client(t, policy(mount, "t"))
	s, err := baostate.Open(t.Context(), c, baostate.Config{Mount: mount, Prefix: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(t.Context(), "doc", []byte(`{"name":"a","n":{"x":[1,2]},"flag":true}`), ""); err != nil {
		t.Fatal(err)
	}
	raw := dev.MustRoot(t, "GET", mount+"/data/t/doc", nil)
	for _, want := range []string{`"name":"a"`, `"n":{"x":[1,2]}`, `"flag":true`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("the secret does not hold %s: %s", want, raw)
		}
	}
	it, err := s.Get(t.Context(), "doc")
	if err != nil || string(it.Value) != `{"flag":true,"n":{"x":[1,2]},"name":"a"}` {
		t.Fatalf("Get = %q, %v", it.Value, err)
	}
}

// An older version that the mount no longer keeps is not found, which
// Rotating turns into "no previous".
func TestPrunedPrevious(t *testing.T) {
	dev := openbaotest.Env(t)
	mount := dev.MountKV(t, 2)
	c := dev.Client(t, policy(mount, "t"))
	s, err := baostate.Open(t.Context(), c, baostate.Config{Mount: mount, Prefix: "t"})
	if err != nil {
		t.Fatal(err)
	}
	var rev state.Rev
	for i := range 4 {
		if rev, err = s.Put(t.Context(), "k", fmt.Appendf(nil, `{"v":%d}`, i), rev); err != nil {
			t.Fatal(err)
		}
	}
	it, _ := s.Get(t.Context(), "k")
	if it.Rev != "4" || it.Previous != "3" {
		t.Fatalf("rev %q previous %q, want 4 and 3", it.Rev, it.Previous)
	}
	if _, err := s.GetRev(t.Context(), "k", "3"); err != nil {
		t.Fatalf("version 3 should be kept: %v", err)
	}
	if _, err := s.GetRev(t.Context(), "k", "1"); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("version 1 is pruned: %v, want ErrNotFound", err)
	}
	for _, bad := range []state.Rev{"0", "-1", "x", "1.5", ""} {
		if _, err := s.GetRev(t.Context(), "k", bad); !errors.Is(err, state.ErrNotFound) {
			t.Errorf("GetRev(%q) = %v, want ErrNotFound", bad, err)
		}
	}
	if _, err := s.Put(t.Context(), "k", []byte(`{}`), "x"); !errors.Is(err, state.ErrConflict) {
		t.Fatalf("Put with a non-version rev: %v, want ErrConflict", err)
	}
}

// The policy is enforced by the server: a store under one prefix cannot
// reach another, and the error says so rather than reading as not found.
func TestPolicyBoundsThePrefix(t *testing.T) {
	dev := openbaotest.Env(t)
	mount := dev.MountKV(t, 0)
	c := dev.Client(t, policy(mount, "mine"))
	other, err := baostate.Open(t.Context(), c, baostate.Config{Mount: mount, Prefix: "theirs"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = other.Put(t.Context(), "k", []byte(`{}`), "")
	if err == nil || errors.Is(err, state.ErrConflict) || errors.Is(err, state.ErrNotFound) {
		t.Fatalf("Put outside the policy: %v, want a permission error", err)
	}
}
