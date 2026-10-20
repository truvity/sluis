package app

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/truvity/sluis/internal/consolecall"
	"github.com/truvity/sluis/internal/githubapp"
	"github.com/truvity/sluis/internal/githubroster/consolewire"
	"github.com/truvity/sluis/internal/modcall"
	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/storage/state"
	statememory "github.com/truvity/sluis/storage/state/memory"
)

// A catalogue App declared `export: true`, created and installed through the
// console's operations the GitHub module's own function serves, is kept where
// the one process keeps it: its key at external/github/<id>, and no internal
// credential beside it. (The function's stores were once not told of the
// exports, and kept the key internal.)
func TestTheGitHubFunctionExportsACatalogueAppItInstalls(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	policy := filepath.Join(dir, "policy.yaml")
	if err := os.WriteFile(policy, []byte("apiVersion: sluis.truvity.github.io/policy/v2\n"+
		"groups: {x:y:z: {}}\ngithub: {globex: {members: [x:y:z]}}\n"+
		"apps:\n  github:\n    apps:\n"+
		"      - {id: renovate, purpose: catalogue, org: globex, export: true, permissions: {contents: read}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "sluis.yaml")
	doc := "apiVersion: sluis.truvity.github.io/sluis/v3\nissuerURL: https://access.example\npublicURL: https://access.example/console\n" +
		"policy: {file: " + policy + "}\n" +
		"ports:\n  adapter: memory\n" +
		"secrets: {source: ssm, root: /sluis/example, layout: v5}\nadapters:\n  secrets: {adapter: ssm}\n" +
		"controllers: {github: {consoleURL: 'http://console.example/console'}}\n"
	if err := os.WriteFile(file, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	mem := statememory.New()
	cfg.stores.OpenState = func(context.Context, string, ...state.Option) (state.Store, error) { return mem, nil }

	startGitHub(t)
	log := slog.New(slog.DiscardHandler)
	minter, err := NewMinter(ctx, cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(minter.Close)
	if err = minter.ServeConsole(ctx, cfg, log); err != nil {
		t.Fatal(err)
	}

	// The console's two callbacks, as the console sends them to the function.
	console := modcall.Local{consolewire.Module: minter.RPC()}.As(consolecall.Class)
	caller := consolecall.Caller{Actor: "ada@example.test", Roles: []string{"operator"}, Source: consolecall.SourcePerson}
	flow := consolewire.Flow{Bind: consolewire.BindCatalogue + "renovate", Actor: caller.Actor, Code: "created"}
	if _, err = consolecall.ProxyJSON[consolewire.Flow, consolewire.Continued](
		ctx, console, consolewire.Module, consolewire.CompleteCatalogueCreate, caller, flow); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err = consolecall.ProxyJSON[consolewire.Flow, consolewire.Done](
		ctx, console, consolewire.Module, consolewire.CompleteCatalogueSetup, caller, flow); err != nil {
		t.Fatalf("install: %v", err)
	}

	v5 := minter.stores.V5
	exported, _, err := v5.GitHubExternal().App("renovate").Get(ctx)
	if err != nil {
		t.Fatalf("external/github/renovate was not written: %v", err)
	}
	if exported.AppID != "42" || exported.InstallationID != "7" || exported.PrivateKey == "" {
		t.Errorf("external/github/renovate holds app %q installation %q (a key: %t)",
			exported.AppID, exported.InstallationID, exported.PrivateKey != "")
	}
	if names, err := v5.InternalStore(secretstore.ModuleGitHub).Child("apps/renovate").List(ctx); err != nil || len(names) != 0 {
		t.Errorf("the key is also an internal credential: %v, %v", names, err)
	}
}

// startGitHub answers the two calls the callbacks make of GitHub: the
// manifest's conversion and the App's installations.
func startGitHub(t *testing.T) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	mux := http.NewServeMux()
	mux.HandleFunc("POST /app-manifests/{code}/conversions", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("code") != "created" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": 42, "slug": "globex-renovate", "pem": pemKey, "client_id": "Iv1.created", "client_secret": "created-secret",
			"html_url": "https://github.example/apps/globex-renovate", "owner": map[string]any{"login": "globex"},
		})
	})
	mux.HandleFunc("GET /app/installations", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{"id": 7, "account": map[string]any{"login": "globex"}}})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	api, web := githubapp.APIBase, githubapp.WebBase
	githubapp.APIBase, githubapp.WebBase = server.URL, server.URL
	t.Cleanup(func() { githubapp.APIBase, githubapp.WebBase = api, web })
}
