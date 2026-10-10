package app

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/truvity/sluis/storage/logtest"

	"github.com/truvity/sluis/internal/secrets"
)

// Outside a cluster the password is the secret recovery.passwordSecret names
// when it names one: a platform keeps it in its secret store, so it is the same
// one across restarts rather than one generated per start.
func TestTheRecoveryPasswordIsReadFromItsSecret(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	dir := t.TempDir()

	src := secrets.File{Root: dir}
	file := filepath.Join(dir, "recovery", "password")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("a-long-generated-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	recovery, err := openRecovery(ctx, Config{recoveryEnabled: true, recoveryLogin: "recovery/password"}, stores{}, src, nil, log)
	if err != nil || recovery == nil {
		t.Fatalf("openRecovery = %v, %v", recovery, err)
	}
	if recovery.Kind() != "password" {
		t.Errorf("kind = %q", recovery.Kind())
	}
	if _, err = recovery.Verify(ctx, "a-long-generated-password"); err != nil {
		t.Errorf("the file's password was refused: %v", err)
	}
	if _, err = recovery.Verify(ctx, "something-else"); err == nil {
		t.Error("another password was accepted")
	}

	// Turned off keeps the file and builds nothing; turning it on again is
	// the same file, no new password.
	off, err := openRecovery(ctx, Config{recoveryEnabled: false, recoveryLogin: "recovery/password"}, stores{}, src, nil, log)
	if err != nil || off != nil {
		t.Errorf("recovery turned off = %v, %v, want none", off, err)
	}
	if _, err = os.Stat(file); err != nil {
		t.Errorf("turning recovery off touched the password file: %v", err)
	}

	if err = os.WriteFile(filepath.Join(dir, "recovery", "empty"), []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = openRecovery(ctx, Config{recoveryEnabled: true, recoveryLogin: "recovery/empty"}, stores{}, src, nil, log); err == nil ||
		!strings.Contains(err.Error(), "recovery.passwordSecret") {
		t.Errorf("an empty password = %v, want a refusal naming the key", err)
	}
	if _, err = openRecovery(ctx, Config{recoveryEnabled: true, recoveryLogin: "recovery/absent"}, stores{}, src, nil, log); err == nil {
		t.Error("a missing password was not refused")
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
