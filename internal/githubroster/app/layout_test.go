package app

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/storage/state"
	statememory "github.com/truvity/sluis/storage/state/memory"
)

// A github controller assembled from the one service document builds its stores
// from the document's secrets section, not from the controller's own: on layout
// v4 a blob's credentialsRef resolves, so the controller starts and its stores
// see layout v4. (Built with and without the lambda tag; CI runs both.)
func TestTheControllerCarriesTheServiceDocumentsSecretsLayout(t *testing.T) {
	dir := t.TempDir()
	policy := filepath.Join(dir, "policy.yaml")
	if err := os.WriteFile(policy, []byte("apiVersion: sluis.truvity.github.io/policy/v2\n"+
		"groups: {x:y:z: {}}\ngithub: {a: {members: [x:y:z]}}\ncontrollers: {github: {enabledOrgs: [a]}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "sluis.yaml")
	doc := "apiVersion: sluis.truvity.github.io/sluis/v3\nissuerURL: https://access.example\npublicURL: https://access.example/console\n" +
		"policy: {file: " + policy + "}\n" +
		"ports:\n  adapter: memory\n  blob:\n    adapter: s3\n    s3: {bucket: b, endpoint: 'http://127.0.0.1:1', pathStyle: true,\n" +
		"      credentialsRef: internal/blobs/r2}\n" +
		"secrets: {source: ssm, root: /sluis/example, layout: v4}\nadapters:\n  secrets: {adapter: ssm}\n" +
		"controllers: {github: {}}\n"
	if err := os.WriteFile(file, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.stores.SecretsLayout != config.SecretsLayoutV4 || cfg.stores.SecretsRoot != "/sluis/example" {
		t.Fatalf("the controller dropped the document's secrets: layout %q root %q", cfg.stores.SecretsLayout, cfg.stores.SecretsRoot)
	}

	// The backend of the v4 stores is memory, holding the blob's credentials.
	mem := statememory.New()
	cfg.stores.OpenState = func(context.Context, string, ...state.Option) (state.Store, error) { return mem, nil }
	creds, err := secretstore.FromStore(mem, secretstore.LayoutV4, "").Internal.S3Credentials("internal/blobs/r2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = creds.Put(context.Background(), secretstore.S3Credentialsv1{AccessKeyID: "example-id", SecretAccessKey: "example-secret"}, ""); err != nil {
		t.Fatal(err)
	}

	a, err := New(context.Background(), cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("the controller did not start: %v", err)
	}
	_ = a
}
