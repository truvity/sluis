// Package conformance is the test suite every [state.Store] backend must
// pass. A backend's test calls [Run] with a factory for fresh, empty stores.
package conformance

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/truvity/sluis/storage/state"
)

// Factory returns an empty store no other test shares. Backends that need
// isolation typically return a store under a unique prefix.
type Factory func(t *testing.T) state.Store

type doc struct {
	Name string `json:"name"`
	N    int    `json:"n"`
}

// Run runs the suite as subtests of t.
func Run(t *testing.T, newStore Factory) {
	t.Helper()
	cases := []struct {
		name string
		fn   func(*testing.T, state.Store)
	}{
		{"CRUD", testCRUD},
		{"ConditionalPut", testConditionalPut},
		{"ListDirectChildrenOnly", testList},
		{"ChildPrefixes", testChild},
		{"GetRev", testGetRev},
		{"JSONObjectValues", testObjectValues},
		{"ValueRoundTrip", testValue},
		{"RotatingInsideGrace", testRotatingInside},
		{"RotatingOutsideGrace", testRotatingOutside},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.fn(t, newStore(t)) })
	}
}

func ctx(t *testing.T) context.Context { t.Helper(); return t.Context() }

func mustPut(t *testing.T, s state.Store, key, val string, ifRev state.Rev) state.Rev {
	t.Helper()
	rev, err := s.Put(ctx(t), key, []byte(val), ifRev)
	if err != nil {
		t.Fatalf("Put(%q, ifRev=%q): %v", key, ifRev, err)
	}
	if rev == "" {
		t.Fatalf("Put(%q) returned an empty Rev", key)
	}
	return rev
}

func testCRUD(t *testing.T, s state.Store) {
	if _, err := s.Get(ctx(t), "a"); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("Get missing: %v, want ErrNotFound", err)
	}
	rev := mustPut(t, s, "a", `{"x":1}`, "")
	it, err := s.Get(ctx(t), "a")
	if err != nil {
		t.Fatal(err)
	}
	if string(it.Value) != `{"x":1}` || it.Rev != rev {
		t.Fatalf("Get = %q rev %q, want {\"x\":1} rev %q", it.Value, it.Rev, rev)
	}
	if it.Modified.IsZero() {
		t.Fatal("Modified is zero")
	}
	if err := s.Delete(ctx(t), "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx(t), "a"); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("Get after Delete: %v, want ErrNotFound", err)
	}
	if err := s.Delete(ctx(t), "a"); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("Delete missing: %v, want ErrNotFound", err)
	}
	mustPut(t, s, "a", `{"x":2}`, "") // a deleted key may be created again
}

func testConditionalPut(t *testing.T, s state.Store) {
	r1 := mustPut(t, s, "k", `{"v":1}`, "")
	if _, err := s.Put(ctx(t), "k", []byte(`{"v":2}`), ""); !errors.Is(err, state.ErrConflict) {
		t.Fatalf("create over existing: %v, want ErrConflict", err)
	}
	if _, err := s.Put(ctx(t), "k", []byte(`{"v":2}`), "no-such-rev"); !errors.Is(err, state.ErrConflict) {
		t.Fatalf("stale rev: %v, want ErrConflict", err)
	}
	if _, err := s.Put(ctx(t), "absent", []byte(`{"v":2}`), r1); !errors.Is(err, state.ErrConflict) {
		t.Fatalf("update of a missing key: %v, want ErrConflict", err)
	}
	r2 := mustPut(t, s, "k", `{"v":2}`, r1)
	if r2 == r1 {
		t.Fatal("a new version reused the Rev")
	}
	if _, err := s.Put(ctx(t), "k", []byte(`{"v":3}`), r1); !errors.Is(err, state.ErrConflict) {
		t.Fatalf("second writer with the old rev: %v, want ErrConflict", err)
	}
	it, _ := s.Get(ctx(t), "k")
	if string(it.Value) != `{"v":2}` {
		t.Fatalf("a conflicting Put changed the value: %q", it.Value)
	}
}

func testList(t *testing.T, s state.Store) {
	got, err := s.List(ctx(t))
	if err != nil || len(got) != 0 {
		t.Fatalf("List empty = %v, %v", got, err)
	}
	mustPut(t, s, "a", `{}`, "")
	mustPut(t, s, "b", `{}`, "")
	mustPut(t, s, "dir/c", `{}`, "")
	mustPut(t, s, "dir/sub/d", `{}`, "")
	got, err = s.List(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a", "b"}; !sameSet(got, want) {
		t.Fatalf("List = %v, want %v (direct children only)", got, want)
	}
	got, err = s.Child("dir").List(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"c"}; !sameSet(got, want) {
		t.Fatalf("Child(dir).List = %v, want %v", got, want)
	}
	all, err := state.ListAll(ctx(t), s)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if want := []string{"a", "b", "dir/c", "dir/sub/d"}; !sameSet(all, want) {
		t.Fatalf("ListAll = %v, want %v (every key at any depth)", all, want)
	}
	all, err = state.ListAll(ctx(t), s.Child("dir"))
	if err != nil {
		t.Fatalf("Child(dir) ListAll: %v", err)
	}
	if want := []string{"c", "sub/d"}; !sameSet(all, want) {
		t.Fatalf("Child(dir) ListAll = %v, want %v", all, want)
	}
}

func testChild(t *testing.T, s state.Store) {
	c := s.Child("team")
	mustPut(t, c, "k", `{"in":"child"}`, "")
	it, err := s.Get(ctx(t), "team/k")
	if err != nil || string(it.Value) != `{"in":"child"}` {
		t.Fatalf("parent Get(team/k) = %q, %v", it.Value, err)
	}
	if _, err := s.Get(ctx(t), "k"); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("Get(k) on the parent: %v, want ErrNotFound", err)
	}
	other := s.Child("other")
	if _, err := other.Get(ctx(t), "k"); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("sibling prefix sees the key: %v", err)
	}
	mustPut(t, s.Child("a").Child("b"), "k", `{}`, "")
	if _, err := s.Get(ctx(t), "a/b/k"); err != nil {
		t.Fatalf("nested Child prefix: %v", err)
	}
}

func testGetRev(t *testing.T, s state.Store) {
	r1 := mustPut(t, s, "k", `{"v":1}`, "")
	r2 := mustPut(t, s, "k", `{"v":2}`, r1)
	for rev, want := range map[state.Rev]string{r1: `{"v":1}`, r2: `{"v":2}`} {
		it, err := s.GetRev(ctx(t), "k", rev)
		if err != nil {
			t.Fatalf("GetRev(%q): %v", rev, err)
		}
		if string(it.Value) != want || it.Rev != rev || it.Modified.IsZero() {
			t.Fatalf("GetRev(%q) = %q rev %q modified %v, want %s", rev, it.Value, it.Rev, it.Modified, want)
		}
	}
	it, _ := s.Get(ctx(t), "k")
	if it.Previous != r1 {
		t.Fatalf("Previous = %q, want %q", it.Previous, r1)
	}
	first, _ := s.GetRev(ctx(t), "k", r1)
	if first.Previous != "" {
		t.Fatalf("first version has Previous %q", first.Previous)
	}
	if _, err := s.GetRev(ctx(t), "k", "no-such-rev"); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("GetRev unknown: %v, want ErrNotFound", err)
	}
	if _, err := s.GetRev(ctx(t), "absent", r1); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("GetRev missing key: %v, want ErrNotFound", err)
	}
}

func testObjectValues(t *testing.T, s state.Store) {
	for _, bad := range []string{``, `[]`, `"str"`, `1`, `null`, `true`, `{`, `{"a":1} x`} {
		if _, err := s.Put(ctx(t), "bad", []byte(bad), ""); !errors.Is(err, state.ErrNotObject) {
			t.Errorf("Put(%q) = %v, want ErrNotObject", bad, err)
		}
	}
	if _, err := s.Get(ctx(t), "bad"); !errors.Is(err, state.ErrNotFound) {
		t.Errorf("a refused Put created the key: %v", err)
	}
	mustPut(t, s, "good", `{"nested":{"a":[1,2]}}`, "")
	it, _ := s.Get(ctx(t), "good")
	if string(it.Value) != `{"nested":{"a":[1,2]}}` {
		t.Errorf("object not stored byte for byte: %q", it.Value)
	}
	for _, key := range []string{"", "/abs", "a//b", "a/../b", "trailing/"} {
		if _, err := s.Put(ctx(t), key, []byte(`{}`), ""); !errors.Is(err, state.ErrInvalidKey) {
			t.Errorf("Put(key %q) = %v, want ErrInvalidKey", key, err)
		}
	}
}

func testValue(t *testing.T, s state.Store) {
	v := state.NewValue(s, "doc", state.JSON[doc]())
	if _, _, err := v.Get(ctx(t)); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("Get missing: %v", err)
	}
	r1, err := v.Put(ctx(t), doc{"a", 1}, "")
	if err != nil {
		t.Fatal(err)
	}
	got, rev, err := v.Get(ctx(t))
	if err != nil || !reflect.DeepEqual(got, doc{"a", 1}) || rev != r1 {
		t.Fatalf("Get = %+v %q %v", got, rev, err)
	}
	if _, err := v.Put(ctx(t), doc{"b", 2}, ""); !errors.Is(err, state.ErrConflict) {
		t.Fatalf("Put over existing: %v", err)
	}

	raw := state.NewValue(s, "raw", state.Raw())
	if _, err := raw.Put(ctx(t), []byte{0, 1, 2, 0xff}, ""); err != nil {
		t.Fatal(err)
	}
	b, _, err := raw.Get(ctx(t))
	if err != nil || !reflect.DeepEqual(b, []byte{0, 1, 2, 0xff}) {
		t.Fatalf("Raw round trip = %v, %v", b, err)
	}
	it, _ := s.Get(ctx(t), "raw")
	if err := state.ValidateObject(it.Value); err != nil {
		t.Fatalf("Raw stored %q: %v", it.Value, err)
	}

	str := state.NewValue(s, "str", state.JSON[string]())
	if _, err := str.Put(ctx(t), "x", ""); !errors.Is(err, state.ErrNotObject) {
		t.Fatalf("JSON codec of a string: %v, want ErrNotObject", err)
	}
}

func rotating(t *testing.T, s state.Store) state.Value[doc] {
	v := state.NewValue(s, "key", state.JSON[doc]())
	r1, err := v.Put(ctx(t), doc{"old", 1}, "")
	if err != nil {
		t.Fatal(err)
	}
	cur, prev, err := v.Rotating(ctx(t), time.Hour)
	if err != nil || prev != nil || cur.Name != "old" {
		t.Fatalf("Rotating of a single version = %+v, %v, %v; want no previous", cur, prev, err)
	}
	if _, err := v.Put(ctx(t), doc{"new", 2}, r1); err != nil {
		t.Fatal(err)
	}
	return v
}

func testRotatingInside(t *testing.T, s state.Store) {
	v := rotating(t, s)
	cur, prev, err := v.Rotating(ctx(t), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if cur.Name != "new" || prev == nil || prev.Name != "old" {
		t.Fatalf("inside grace = %+v, %+v; want new and previous old", cur, prev)
	}
}

func testRotatingOutside(t *testing.T, s state.Store) {
	v := rotating(t, s)
	later := time.Now().Add(2 * time.Hour)
	cur, prev, err := v.WithClock(func() time.Time { return later }).Rotating(ctx(t), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if cur.Name != "new" || prev != nil {
		t.Fatalf("outside grace = %+v, %+v; want new and no previous", cur, prev)
	}
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]int{}
	for _, x := range a {
		m[x]++
	}
	for _, x := range b {
		m[x]--
	}
	for _, n := range m {
		if n != 0 {
			return false
		}
	}
	return true
}
