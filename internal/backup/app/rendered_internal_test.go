package app

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"

	// The binary links the invoke trigger (lambdaapp); the test links it too.
	_ "github.com/truvity/sluis/internal/port/invoke"
	"github.com/truvity/sluis/internal/store"
)

// The backup or restore document the Pulumi library renders is one the module
// starts on. The library's own tests (deploy/pulumi, TestTheRenderedBackupAndRestoreDocumentsStartTheModule)
// render the document and run this test over it: the library cannot import the
// runtime's internals, and a test that stopped at the library's own checks let
// the preset's `invoke` trigger, with no function to invoke, stop every start.
func TestTheRenderedBackupDocumentStartsTheModule(t *testing.T) {
	file := os.Getenv("SLUIS_RENDERED_BACKUP")
	if file == "" {
		t.Skip("SLUIS_RENDERED_BACKUP names the document deploy/pulumi renders; it is set by that test")
	}
	// The clients connect to nothing at construction; these only stop the
	// default chain from looking for a profile.
	t.Setenv("AWS_ACCESS_KEY_ID", "k")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "s")
	t.Setenv("AWS_REGION", "eu-west-1")
	cfg, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	st, err := store.Open(context.Background(), cfg.stores, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("the module cannot start on the rendered document: %v", err)
	}
	st.Close()
}
