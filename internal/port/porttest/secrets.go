package porttest

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/port"
)

// RunSecrets runs the assertions of the [port.Secrets] port against an
// adapter, fresh for every call of the factory: a read of what was written, a
// version that changes on every write, a conditional write that loses to a
// moved version and creates with an empty one, a listing by whole segments
// (the `export/` convention included), a delete of what is absent, a path and
// a value the port refuses, and a value that is stored as bytes.
func RunSecrets(t *testing.T, factory func(t *testing.T) port.Secrets) {
	t.Helper()
	cases := []struct {
		name string
		run  func(t *testing.T, s port.Secrets)
	}{
		{"secrets/get-absent", secretsGetAbsent},
		{"secrets/put-get", secretsPutGet},
		{"secrets/version-changes", secretsVersionChanges},
		{"secrets/put-if-version", secretsPutIfVersion},
		{"secrets/put-if-absent", secretsPutIfAbsent},
		{"secrets/delete", secretsDelete},
		{"secrets/list-by-segment", secretsList},
		{"secrets/export-prefix", secretsExportPrefix},
		{"secrets/refuses-bad-paths", secretsBadPaths},
		{"secrets/refuses-too-large", secretsTooLarge},
		{"secrets/binary-value", secretsBinary},
		{"secrets/returns-a-copy", secretsCopy},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.run(t, factory(t)) })
	}
}

func mustPutSecret(t *testing.T, s port.Secrets, path, value string) string {
	t.Helper()
	v, err := s.Put(ctx(), path, []byte(value))
	if err != nil {
		t.Fatalf("Put %s: %v", path, err)
	}
	if v == "" {
		t.Fatalf("Put %s returned no version", path)
	}
	return v
}

func secretsGetAbsent(t *testing.T, s port.Secrets) {
	if _, err := s.Get(ctx(), "app/none"); !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("Get of an absent secret: %v, want ErrNotFound", err)
	}
}

func secretsPutGet(t *testing.T, s port.Secrets) {
	v := mustPutSecret(t, s, "app/token", "one")
	got, err := s.Get(ctx(), "app/token")
	if err != nil || string(got.Value) != "one" || got.Version != v {
		t.Fatalf("Get = %q v%q (%v), want one v%q", got.Value, got.Version, err, v)
	}
}

func secretsVersionChanges(t *testing.T, s port.Secrets) {
	v1 := mustPutSecret(t, s, "app/token", "one")
	v2 := mustPutSecret(t, s, "app/token", "one")
	if v1 == v2 {
		t.Fatalf("two writes share version %q", v1)
	}
	got, _ := s.Get(ctx(), "app/token")
	if got.Version != v2 {
		t.Fatalf("Get version %q, want the latest %q", got.Version, v2)
	}
}

func secretsPutIfVersion(t *testing.T, s port.Secrets) {
	v1 := mustPutSecret(t, s, "app/token", "one")
	v2, err := s.PutIfVersion(ctx(), "app/token", []byte("two"), v1)
	if err != nil || v2 == v1 {
		t.Fatalf("PutIfVersion at the current version: %q, %v", v2, err)
	}
	if _, err := s.PutIfVersion(ctx(), "app/token", []byte("three"), v1); !errors.Is(err, port.ErrConflict) {
		t.Fatalf("PutIfVersion at a moved version: %v, want ErrConflict", err)
	}
	if got, _ := s.Get(ctx(), "app/token"); string(got.Value) != "two" {
		t.Fatalf("a lost write changed the value to %q", got.Value)
	}
	if _, err := s.PutIfVersion(ctx(), "app/gone", []byte("x"), "7"); !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("PutIfVersion of an absent secret: %v, want ErrNotFound", err)
	}
}

func secretsPutIfAbsent(t *testing.T, s port.Secrets) {
	if _, err := s.PutIfVersion(ctx(), "app/new", []byte("a"), ""); err != nil {
		t.Fatalf("PutIfVersion with no version over an absent secret: %v", err)
	}
	if _, err := s.PutIfVersion(ctx(), "app/new", []byte("b"), ""); !errors.Is(err, port.ErrConflict) {
		t.Fatalf("PutIfVersion with no version over a live secret: %v, want ErrConflict", err)
	}
}

func secretsDelete(t *testing.T, s port.Secrets) {
	mustPutSecret(t, s, "app/token", "one")
	if err := s.Delete(ctx(), "app/token"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx(), "app/token"); !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("Get after Delete: %v", err)
	}
	if err := s.Delete(ctx(), "app/token"); err != nil {
		t.Fatalf("Delete of an absent secret: %v", err)
	}
}

func listSecrets(t *testing.T, s port.Secrets, prefix string) []string {
	t.Helper()
	got, err := s.List(ctx(), prefix)
	if err != nil {
		t.Fatalf("List %q: %v", prefix, err)
	}
	return got
}

func secretsList(t *testing.T, s port.Secrets) {
	for _, p := range []string{"a/one", "a/two", "a/deep/three", "ab/four", "b"} {
		mustPutSecret(t, s, p, "x")
	}
	want := map[string][]string{
		"a":      {"a/deep/three", "a/one", "a/two"},
		"a/":     {"a/deep/three", "a/one", "a/two"},
		"a/deep": {"a/deep/three"},
		"ab":     {"ab/four"},
		"nope":   {},
		"":       {"a/deep/three", "a/one", "a/two", "ab/four", "b"},
	}
	for prefix, w := range want {
		got := listSecrets(t, s, prefix)
		if !slices.Equal(got, w) && (len(got) != 0 || len(w) != 0) {
			t.Errorf("List %q = %v, want %v", prefix, got, w)
		}
	}
}

func secretsExportPrefix(t *testing.T, s port.Secrets) {
	mustPutSecret(t, s, port.ExportPrefix+"slack-apps/alerts", "x")
	mustPutSecret(t, s, "app/token", "y")
	got := listSecrets(t, s, port.ExportPrefix)
	if !slices.Equal(got, []string{"export/slack-apps/alerts"}) {
		t.Fatalf("List %q = %v", port.ExportPrefix, got)
	}
}

func secretsBadPaths(t *testing.T, s port.Secrets) {
	for _, p := range []string{"", "/a", "a/", "a//b", "a/../b", "a/./b", "a b", "a?b", "a*"} {
		if _, err := s.Put(ctx(), p, []byte("x")); !errors.Is(err, port.ErrUnsupported) {
			t.Errorf("Put %q: %v, want ErrUnsupported", p, err)
		}
		if _, err := s.Get(ctx(), p); !errors.Is(err, port.ErrUnsupported) {
			t.Errorf("Get %q: %v, want ErrUnsupported", p, err)
		}
	}
}

func secretsTooLarge(t *testing.T, s port.Secrets) {
	if _, err := s.Put(ctx(), "app/big", bytes.Repeat([]byte("x"), port.MaxSecret+1)); !errors.Is(err, port.ErrTooLarge) {
		t.Fatalf("Put over MaxSecret: %v, want ErrTooLarge", err)
	}
	if _, err := s.Put(ctx(), "app/max", bytes.Repeat([]byte("x"), port.MaxSecret)); err != nil {
		t.Fatalf("Put of exactly MaxSecret: %v", err)
	}
}

func secretsBinary(t *testing.T, s port.Secrets) {
	in := []byte("-----BEGIN\n\x00\xff line\r\n  trailing  \n")
	if _, err := s.Put(ctx(), "app/pem", in); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx(), "app/pem")
	if err != nil || !bytes.Equal(got.Value, in) {
		t.Fatalf("Get = %q (%v), want the bytes written", got.Value, err)
	}
	if strings.TrimSpace(string(got.Value)) == string(got.Value) {
		t.Fatal("the value was trimmed")
	}
}

func secretsCopy(t *testing.T, s port.Secrets) {
	in := []byte("abc")
	if _, err := s.Put(ctx(), "app/copy", in); err != nil {
		t.Fatal(err)
	}
	in[0] = 'X'
	got, _ := s.Get(ctx(), "app/copy")
	got.Value[1] = 'Y'
	again, _ := s.Get(ctx(), "app/copy")
	if string(again.Value) != "abc" {
		t.Fatalf("the store shares memory with its callers: %q", again.Value)
	}
}
