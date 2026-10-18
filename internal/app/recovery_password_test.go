package app

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/truvity/sluis/storage/logtest"

	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/secrets"
	"github.com/truvity/sluis/internal/server"
	"github.com/truvity/sluis/internal/srpauth"
	"github.com/truvity/sluis/srp"
)

// strongPassword is a password that may sit behind a verifier.
const strongPassword = "aB3-Kf9xQ2mLpR7vTz1WcY8nHd5JgS4e"

// withKey is a console with the key its salt is derived from.
var withKey = stores{sessionKey: access.StaticKey([]byte("the session key every container shares"))}

// Outside a cluster the password is the secret recovery.passwordSecret names
// when it names one: a platform keeps it in its secret store, so it is the same
// one across restarts rather than one generated per start. The hub derives the
// recovery account's verifier from it and compares nothing with a password.
func TestTheRecoveryCredentialIsDerivedFromItsPasswordSecret(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	dir := t.TempDir()

	src := secrets.File{Root: dir}
	file := filepath.Join(dir, "recovery", "password")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(strongPassword+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	recovery, err := openRecovery(ctx, Config{recoveryEnabled: true, recoveryLogin: "recovery/password"}, withKey, src, nil, log)
	if err != nil || recovery == nil {
		t.Fatalf("openRecovery = %v, %v", recovery, err)
	}
	if recovery.Kind() != "password" {
		t.Errorf("kind = %q", recovery.Kind())
	}
	got, err := recovery.(*server.PasswordRecovery).Source(ctx)
	if err != nil {
		t.Fatalf("the file's password gave no credential: %v", err)
	}
	want := srp.Default.NewCredential(got.Salt, srpauth.RecoveryUser, strongPassword)
	if string(got.Verifier) != string(want.Verifier) || got.Iterations != srp.Iterations {
		t.Error("the credential is not the one derived from the file's password")
	}
	// The salt is the key's, the same in every container and not a function of the password.
	if again, _ := recovery.(*server.PasswordRecovery).Source(ctx); string(again.Salt) != string(got.Salt) {
		t.Error("the salt changed between two reads")
	}
	other, err := openRecovery(ctx, Config{recoveryEnabled: true, recoveryLogin: "recovery/password"},
		stores{sessionKey: access.StaticKey([]byte("another key"))}, src, nil, log)
	if err != nil {
		t.Fatal(err)
	}
	if o, err := other.(*server.PasswordRecovery).Source(ctx); err != nil || string(o.Salt) == string(got.Salt) {
		t.Errorf("another key gave the same salt: %v", err)
	}

	// Turned off keeps the file and builds nothing; turning it on again is
	// the same file, no new password.
	off, err := openRecovery(ctx, Config{recoveryEnabled: false, recoveryLogin: "recovery/password"}, withKey, src, nil, log)
	if err != nil || off != nil {
		t.Errorf("recovery turned off = %v, %v, want none", off, err)
	}
	if _, err = os.Stat(file); err != nil {
		t.Errorf("turning recovery off touched the password file: %v", err)
	}

	// Opening reads nothing, so a password that is empty or absent is refused
	// at the first use, naming the key.
	if err = os.WriteFile(filepath.Join(dir, "recovery", "empty"), []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"recovery/empty", "recovery/absent"} {
		r, err := openRecovery(ctx, Config{recoveryEnabled: true, recoveryLogin: name}, withKey, src, nil, log)
		if err != nil || r == nil {
			t.Fatalf("%s: opening read the password: %v, %v", name, r, err)
		}
		if _, err = r.(*server.PasswordRecovery).Source(ctx); err == nil || !strings.Contains(err.Error(), "recovery.passwordSecret") {
			t.Errorf("%s: a use = %v, want a refusal naming the key", name, err)
		}
	}
}

// A password too weak to sit behind a verifier is refused by the first use, naming
// the key, and so is one that is not printable ASCII.
func TestAWeakPasswordIsRefusedWhereTheVerifierIsDerivedFromIt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := secrets.File{Root: dir}
	if err := os.MkdirAll(filepath.Join(dir, "recovery"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, pw := range map[string]string{"short": "recover-me", "non-ASCII": strongPassword[:30] + "é"} {
		if err := os.WriteFile(filepath.Join(dir, "recovery", name), []byte(pw), 0o600); err != nil {
			t.Fatal(err)
		}
		r, err := openRecovery(context.Background(), Config{recoveryEnabled: true, recoveryLogin: "recovery/" + name},
			withKey, src, nil, slog.New(slog.DiscardHandler))
		if err != nil || r == nil {
			t.Fatalf("%s: %v, %v", name, r, err)
		}
		if _, err = r.(*server.PasswordRecovery).Source(context.Background()); err == nil || !strings.Contains(err.Error(), "recovery.passwordSecret") {
			t.Errorf("%s: a use = %v, want a refusal naming the key", name, err)
		}
	}
}

// A verifier the document names is read in place of the password: the hub holds
// no password then.
func TestANamedVerifierIsReadAndThePasswordIsNot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	src := secrets.File{Root: dir}
	cred := srp.Default.WithIterations(srp.MinIterations).NewCredential(srp.NewSalt(), srpauth.RecoveryUser, "pw")
	if err := os.MkdirAll(filepath.Join(dir, "recovery"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "recovery", "verifier"), cred.Document(), 0o600); err != nil {
		t.Fatal(err)
	}
	// There is no password file at all.
	r, err := openRecovery(ctx, Config{recoveryEnabled: true, recoveryLogin: "recovery/password", recoveryVerifier: "recovery/verifier"},
		stores{}, src, nil, slog.New(slog.DiscardHandler))
	if err != nil || r == nil {
		t.Fatalf("openRecovery = %v, %v", r, err)
	}
	got, err := r.(*server.PasswordRecovery).Source(ctx)
	if err != nil || string(got.Verifier) != string(cred.Verifier) {
		t.Errorf("credential = %v, %v", got, err)
	}
}

// On a Lambda function there is nowhere to read a generated password but the
// function's own log, so with no password configured recovery is not built and
// nothing is printed.
func TestOnLambdaAMissingPasswordFailsClosedWithoutPrintingOne(t *testing.T) {
	t.Setenv("AWS_LAMBDA_FUNCTION_NAME", "sluis-http")
	log, logs := logtest.Logger()

	recovery, err := openRecovery(context.Background(), Config{recoveryEnabled: true}, stores{}, nil, nil, log)
	if err != nil || recovery != nil {
		t.Fatalf("openRecovery = %v, %v, want no recovery and no error", recovery, err)
	}
	if logs.CountLevel(slog.LevelError) == 0 || !logs.Mentions("NOT available") {
		t.Errorf("no clear error was logged: %q", logs.Messages())
	}
}
