package minter_test

import (
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/truvity/sluis/internal/cloudflare/minter"
)

// TestMain runs the suite on layout v4 and then on layout v5 (ADR 0072).
func TestMain(m *testing.M) {
	code := m.Run()
	if code == 0 {
		useV5 = true
		fmt.Println("minter suite again, secrets on layout v5")
		code = m.Run()
	}
	os.Exit(code)
}

// On layout v5 the minter credential is read at the address the account names
// below internal/cloudflare/, the record of minted tokens is
// internal/cloudflare/minted/<preset> and the stored credential
// external/cloudflare/<preset>.
func TestTheMinterKeepsItsSecretsAtTheLayoutV5Addresses(t *testing.T) {
	was := useV5
	useV5 = true
	defer func() { useV5 = was }()
	e := setup(t)
	if res := e.m.TickPreset(ctx, "dns"); res.Err != nil {
		t.Fatalf("tick = %+v", res)
	}
	if e.api.gotMinter != "minter-secret" {
		t.Errorf("the account was opened with %q", e.api.gotMinter)
	}
	root := e.v5Root
	for _, at := range []struct{ path, key string }{
		{"external/cloudflare", "dns"},
		{"internal/cloudflare/minted", "dns"},
		{"internal/cloudflare/main", "minter"},
	} {
		if _, err := root.Child(at.path).Get(ctx, at.key); err != nil {
			t.Errorf("nothing at %s/%s: %v", at.path, at.key, err)
		}
	}
	if got, _ := root.Child("internal").Child("config").List(ctx); len(got) != 0 {
		t.Errorf("something under the layout v4 config level: %v", got)
	}

	// A minter that is missing is named at the address the document gives.
	if err := root.Child("internal/cloudflare/main").Delete(ctx, "minter"); err != nil {
		t.Fatal(err)
	}
	res := e.m.TickPreset(ctx, "dns-edge")
	if res.Err == nil || !errors.Is(res.Err, minter.ErrNoMinter) {
		t.Fatalf("missing minter: %+v", res)
	}
}
