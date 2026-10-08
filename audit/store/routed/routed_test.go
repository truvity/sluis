package routed_test

import (
	"context"
	"errors"
	"testing"

	"github.com/truvity/sluis/audit/store"
	"github.com/truvity/sluis/audit/store/routed"
	"github.com/truvity/sluis/audit/store/storetest"
)

func put(t *testing.T, s store.Store, key string) {
	t.Helper()
	if err := s.Put(context.Background(), store.Object{Key: key, Body: []byte(key)}); err != nil {
		t.Fatalf("put %s: %v", key, err)
	}
}

func keys(es []store.Entry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.Key)
	}
	return out
}

func TestProfilesLiveInTheirPresetsStore(t *testing.T) {
	ctx := context.Background()
	std, att := storetest.NewMemory(), storetest.NewMemory()
	r, err := routed.New(att, map[string]store.Store{"security": std, "payments": att, "history": std})
	if err != nil {
		t.Fatal(err)
	}
	put(t, r, "records/security/t1/2026/10/08/12/01")
	put(t, r, "records/payments/t1/2026/10/08/12/02")
	put(t, r, "seals/security/t1/x.jws")
	put(t, r, "schema/profile/security/abc")
	put(t, r, "holds/h1")

	if _, err := std.Head(ctx, "records/security/t1/2026/10/08/12/01"); err != nil {
		t.Errorf("security record not in the standard store: %v", err)
	}
	if _, err := att.Head(ctx, "records/security/t1/2026/10/08/12/01"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("security record leaked into the attested store: %v", err)
	}
	if _, err := att.Head(ctx, "records/payments/t1/2026/10/08/12/02"); err != nil {
		t.Errorf("payments record not in the attested store: %v", err)
	}
	if _, err := std.Head(ctx, "seals/security/t1/x.jws"); err != nil {
		t.Errorf("seal not with its profile: %v", err)
	}
	if _, err := std.Head(ctx, "schema/profile/security/abc"); err != nil {
		t.Errorf("composition not with its profile: %v", err)
	}
	if _, err := att.Head(ctx, "holds/h1"); err != nil {
		t.Errorf("a hold is not in the home store: %v", err)
	}

	all, err := r.List(ctx, "records/", "", 0)
	if err != nil || len(all) != 2 {
		t.Fatalf("records/ = %v, %v; want both profiles", keys(all), err)
	}
	if keys(all)[0] != "records/payments/t1/2026/10/08/12/02" {
		t.Errorf("not in key order: %v", keys(all))
	}
	one, _ := r.List(ctx, "records/security/", "", 0)
	if len(one) != 1 {
		t.Errorf("records/security/ = %v", keys(one))
	}
	ps, err := r.Prefixes(ctx, "records/", "/")
	if err != nil || len(ps) != 2 {
		t.Errorf("Prefixes = %v, %v", ps, err)
	}
	profiles, err := store.Profiles(ctx, r)
	if err != nil || len(profiles) != 2 {
		t.Errorf("Profiles = %v, %v", profiles, err)
	}
}

func TestDescriptionsAreInEveryStore(t *testing.T) {
	ctx := context.Background()
	a, b := storetest.NewMemory(), storetest.NewMemory()
	r, _ := routed.New(a, map[string]store.Store{"p": b})
	put(t, r, "catalogue/app/1.0.0")
	put(t, r, "schema/app/1.0.0/x.json")
	for _, st := range []store.Store{a, b} {
		if _, err := st.Head(ctx, "catalogue/app/1.0.0"); err != nil {
			t.Errorf("catalogue missing from a store: %v", err)
		}
	}
	// All of them having it is a collision; a store that lacks it (a preset
	// added later) takes it without one.
	err := r.Put(ctx, store.Object{Key: "catalogue/app/1.0.0", Body: []byte("x")})
	if !errors.Is(err, store.ErrExists) {
		t.Errorf("a second put = %v, want ErrExists", err)
	}
	c := storetest.NewMemory()
	r2, _ := routed.New(a, map[string]store.Store{"p": b, "q": c})
	if err := r2.Put(ctx, store.Object{Key: "catalogue/app/1.0.0", Body: []byte("x")}); err != nil {
		t.Errorf("a new store was not given the catalogue: %v", err)
	}
	if _, err := r2.Get(ctx, "catalogue/app/1.0.0"); err != nil {
		t.Errorf("Get: %v", err)
	}
	if _, err := c.Get(ctx, "catalogue/app/1.0.0"); err != nil {
		t.Errorf("the new store: %v", err)
	}
}
