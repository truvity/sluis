package app

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/internal/secretstore/secretrec"
)

// On layout v5 the recovery password is internal/oidc/recovery-password,
// whatever name the document gives it.
func TestTheRecoveryPasswordOnLayoutV5IsReadFromTheOIDCModule(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	rec := secretrec.New()
	v5 := secretstore.FromStoreV5(rec, "")
	if _, err := v5.OIDC().RecoveryPassword().Put(ctx, []byte("a-long-generated-password"), ""); err != nil {
		t.Fatal(err)
	}
	rec.Reset()

	recovery, err := openRecovery(ctx, Config{recoveryEnabled: true, recoveryLogin: "recovery/password"}, stores{}, nil, v5, log)
	if err != nil || recovery == nil {
		t.Fatalf("openRecovery = %v, %v", recovery, err)
	}
	if _, err = recovery.Verify(ctx, "a-long-generated-password"); err != nil {
		t.Errorf("the stored password was refused: %v", err)
	}
	if _, err = recovery.Verify(ctx, "something-else"); err == nil {
		t.Error("another password was accepted")
	}
	if got := rec.Addresses("get"); !slices.Equal(got, []string{"internal/oidc/recovery-password"}) {
		t.Errorf("read %v", got)
	}

	// An absent or empty password is refused naming the key, as on layout v4.
	empty := secretstore.FromStoreV5(secretrec.New(), "")
	if _, err = openRecovery(ctx, Config{recoveryEnabled: true, recoveryLogin: "recovery/password"}, stores{}, nil, empty, log); err == nil ||
		!strings.Contains(err.Error(), "recovery.passwordSecret") {
		t.Errorf("an absent password = %v", err)
	}
	if _, err = empty.OIDC().RecoveryPassword().Put(ctx, []byte{}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = openRecovery(ctx, Config{recoveryEnabled: true, recoveryLogin: "recovery/password"}, stores{}, nil, empty, log); err == nil ||
		!strings.Contains(err.Error(), "empty") {
		t.Errorf("an empty password = %v", err)
	}
}
