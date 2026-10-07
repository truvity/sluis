package dynamodb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/internal/port"
)

// SSOCookieFlow is the browser sign-in over one adapter's State and Index:
// begin, resolve the cookie, refuse the id, end by id and for an identity,
// and let the pointer expire. It is exported to the LocalStack test, in the
// external package, by this file alone.
func SSOCookieFlow(t *testing.T, set port.Set, advance func(time.Duration)) {
	t.Helper()

	ctx := context.Background()
	state := issuer.NewPortState(set.State, set.Index)
	sso := issuer.NewSSO(state, time.Hour)

	pointer := func(secret string) string {
		sum := sha256.Sum256([]byte("sluis issuer sso-cookie v1" + "\x00" + secret))

		return "issuer:sso-cookie:" + hex.EncodeToString(sum[:])
	}

	held := func(key string) string {
		t.Helper()

		raw, found, err := state.Get(ctx, key)
		if err != nil {
			t.Fatalf("get %s: %v", key, err)
		}

		if !found {
			return ""
		}

		return string(raw)
	}

	// The pointer's address on the table: the kind has a partition of its own.
	if got, err := port.Locate("issuer:sso-cookie:x"); err != nil || got.Kind != "issuer-sso-cookie" || got.ID != "x" {
		t.Fatalf("Locate(issuer:sso-cookie:x) = %+v, %v; want issuer-sso-cookie/x", got, err)
	}

	one, secret, err := sso.Begin(ctx, "ada@north.example", "google")
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	if held(pointer(secret)) != one.ID {
		t.Fatalf("the pointer holds %q, want the sign-in id %q", held(pointer(secret)), one.ID)
	}

	if session, live, err := sso.Resolve(ctx, secret); err != nil || !live || session.ID != one.ID {
		t.Fatalf("the cookie did not resolve: %+v live=%v err=%v", session, live, err)
	}

	if _, live, err := sso.Resolve(ctx, one.ID); err != nil || live {
		t.Errorf("the sign-in id resolved as a cookie: live=%v err=%v", live, err)
	}

	listed, err := sso.List(ctx, "ada@north.example")
	if err != nil || len(listed) != 1 || listed[0].ID != one.ID {
		t.Fatalf("listing: %v, %v", listed, err)
	}

	// End by id removes the pointer with the record.
	if err = sso.End(ctx, one.ID); err != nil {
		t.Fatalf("end: %v", err)
	}

	if held(pointer(secret)) != "" {
		t.Error("ending the sign-in left its pointer")
	}

	if _, live, _ := sso.Resolve(ctx, secret); live {
		t.Error("an ended sign-in's cookie resolved")
	}

	// Sign out everywhere.
	var secrets []string

	for range 2 {
		_, s, err := sso.Begin(ctx, "ada@north.example", "google")
		if err != nil {
			t.Fatal(err)
		}

		secrets = append(secrets, s)
	}

	if ended, err := sso.EndFor(ctx, "ada@north.example"); err != nil || ended != 2 {
		t.Fatalf("EndFor ended %d: %v", ended, err)
	}

	for _, s := range secrets {
		if held(pointer(s)) != "" {
			t.Error("EndFor left a pointer")
		}
	}

	// A pointer outlives nothing: it is written with the record's lifetime.
	_, secret, err = sso.Begin(ctx, "ada@north.example", "google")
	if err != nil {
		t.Fatal(err)
	}

	advance(2 * time.Hour)

	if held(pointer(secret)) != "" {
		t.Error("the pointer survived the sign-in's lifetime")
	}

	if _, live, _ := sso.Resolve(ctx, secret); live {
		t.Error("the cookie resolved after the lifetime")
	}
}

func TestSSOCookieOverTheFake(t *testing.T) {
	t.Parallel()

	s := fakeStore(t, newFake())
	SSOCookieFlow(t, s.Set(), s.Advance)
}
