package app

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"testing"

	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/internal/secretstore/secretrec"
	"github.com/truvity/sluis/internal/server"
	"github.com/truvity/sluis/internal/srpauth"
	"github.com/truvity/sluis/srp"
)

// On layout v5 the console reads the recovery account's verifier, at
// internal/oidc/recovery-verifier whatever name the document gives, and never
// reads the password (docs/decisions/0076).
func TestTheRecoveryVerifierOnLayoutV5IsReadFromTheOIDCModuleAndThePasswordIsNot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	rec := secretrec.New()
	v5 := secretstore.FromStoreV5(rec, "")
	cred := srp.Default.NewCredential(srp.NewSalt(), srpauth.RecoveryUser, "a-long-generated-password")
	if _, err := v5.OIDC().RecoveryVerifier().Put(ctx, cred.Document(), ""); err != nil {
		t.Fatal(err)
	}
	// The password is there for operators; the console must not touch it.
	if _, err := v5.OIDC().RecoveryPassword().Put(ctx, []byte("a-long-generated-password"), ""); err != nil {
		t.Fatal(err)
	}
	rec.Reset()

	recovery, err := openRecovery(ctx, Config{recoveryEnabled: true, recoveryLogin: "recovery/password"}, stores{}, nil, v5, log)
	if err != nil || recovery == nil {
		t.Fatalf("openRecovery = %v, %v", recovery, err)
	}
	password, ok := recovery.(*server.PasswordRecovery)
	if !ok {
		t.Fatalf("recovery is a %T", recovery)
	}
	got, err := password.Source(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Verifier) != string(cred.Verifier) || string(got.Salt) != string(cred.Salt) {
		t.Error("the console read another credential than the one stored")
	}
	if reads := rec.Addresses("get"); !slices.Equal(reads, []string{"internal/oidc/recovery-verifier"}) {
		t.Errorf("read %v, want the verifier alone", reads)
	}
	if password.Where != "internal/oidc/recovery-password" && password.Where != v5.OIDC().Prefix()+"/recovery-password" {
		t.Errorf("the operator is told %q", password.Where)
	}

	// An absent verifier is no account (the Pulumi library that writes it has not
	// run), and an empty one an error that names the account: by the first use,
	// since opening reads nothing.
	empty := secretstore.FromStoreV5(secretrec.New(), "")
	r, err := openRecovery(ctx, Config{recoveryEnabled: true, recoveryLogin: "recovery/password"}, stores{}, nil, empty, log)
	if err != nil || r == nil {
		t.Fatalf("opening read the verifier: %v, %v", r, err)
	}
	if _, err = r.(*server.PasswordRecovery).Source(ctx); !errors.Is(err, srpauth.ErrNoCredential) {
		t.Errorf("an absent verifier = %v, want ErrNoCredential", err)
	}
	if _, err = empty.OIDC().RecoveryVerifier().Put(ctx, []byte{}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = r.(*server.PasswordRecovery).Source(ctx); err == nil || errors.Is(err, srpauth.ErrNoCredential) {
		t.Errorf("an empty verifier = %v, want an error", err)
	}
}
