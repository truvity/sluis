package app

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Outside a cluster the password comes from recovery.passwordFile when it is
// named: a platform keeps it in its secret store and writes it there, so it
// is the same one across restarts rather than one generated per start.
func TestTheRecoveryPasswordIsReadFromItsFile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dir := t.TempDir()

	file := filepath.Join(dir, "password")
	if err := os.WriteFile(file, []byte("a-long-generated-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	recovery, err := openRecovery(ctx, Config{recoveryEnabled: true, recoveryFile: file}, stores{}, log)
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
	off, err := openRecovery(ctx, Config{recoveryEnabled: false, recoveryFile: file}, stores{}, log)
	if err != nil || off != nil {
		t.Errorf("recovery turned off = %v, %v, want none", off, err)
	}
	if _, err = os.Stat(file); err != nil {
		t.Errorf("turning recovery off touched the password file: %v", err)
	}

	empty := filepath.Join(dir, "empty")
	if err = os.WriteFile(empty, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = openRecovery(ctx, Config{recoveryEnabled: true, recoveryFile: empty}, stores{}, log); err == nil ||
		!strings.Contains(err.Error(), "recovery.passwordFile") {
		t.Errorf("an empty password file = %v, want a refusal naming the key", err)
	}
	if _, err = openRecovery(ctx, Config{recoveryEnabled: true, recoveryFile: filepath.Join(dir, "absent")}, stores{}, log); err == nil {
		t.Error("a missing password file was not refused")
	}
}

// On a Lambda function there is nowhere to read a generated password but the
// function's own log, so with no password configured recovery is not built and
// nothing is printed.
func TestOnLambdaAMissingPasswordFailsClosedWithoutPrintingOne(t *testing.T) {
	t.Setenv("AWS_LAMBDA_FUNCTION_NAME", "sluis-http")
	var logs strings.Builder
	log := slog.New(slog.NewTextHandler(&logs, nil))

	recovery, err := openRecovery(context.Background(), Config{recoveryEnabled: true}, stores{}, log)
	if err != nil || recovery != nil {
		t.Fatalf("openRecovery = %v, %v, want no recovery and no error", recovery, err)
	}
	if !strings.Contains(logs.String(), "level=ERROR") || !strings.Contains(logs.String(), "NOT available") {
		t.Errorf("no clear error was logged: %q", logs.String())
	}
}
