package lambdaapp_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/lambdaapp"
	_ "github.com/truvity/sluis/internal/lambdaapp/issuerfn"
	"github.com/truvity/sluis/internal/version"
)

func backupDocument(t *testing.T) func(string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "sluis.yaml")
	if err := os.WriteFile(file, []byte("apiVersion: "+config.BackupAPIVersion+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return func(k string) string {
		if k == config.EnvConfig {
			return file
		}
		return ""
	}
}

// A zip is the module it was built for: another module's document is refused
// before anything is assembled, and a module's function that is not linked into
// the binary is not run at all.
func TestAZipRefusesAnotherModulesDocument(t *testing.T) {
	getenv := backupDocument(t)
	for _, module := range []string{lambdaapp.ModuleIssuer, lambdaapp.ModuleCloudflare} {
		_, err := lambdaapp.OpenModule(context.Background(), getenv, module)
		if err == nil || !strings.Contains(err.Error(), "sluis-"+module+" function") || !strings.Contains(err.Error(), "backup document") {
			t.Errorf("sluis-%s with a backup document: %v", module, err)
		}
	}
	// This test binary links the issuer only: the backup module is not in it.
	_, err := lambdaapp.OpenModule(context.Background(), getenv, lambdaapp.ModuleBackup)
	if err == nil || !strings.Contains(err.Error(), "does not carry the backup module") {
		t.Errorf("a binary without the backup module opened its document: %v", err)
	}
}

func TestACheckedPinIsTheModuleOrNothing(t *testing.T) {
	for pinned, ok := range map[string]bool{"": true, "issuer": true, "backup": false, "cloudflare": false} {
		if err := lambdaapp.CheckPin(pinned, "issuer"); (err == nil) != ok {
			t.Errorf("pinned %q: %v", pinned, err)
		}
	}
	if err := lambdaapp.CheckPin("backup", "issuer"); !strings.Contains(err.Error(), `pinned to the "backup" module`) {
		t.Errorf("the refusal does not say why: %v", err)
	}
}

// The release pins each zip with -ldflags -X: the binary built that way for
// another module's name refuses to start, and exits non-zero before it touches
// the Lambda runtime.
func TestABuiltZipPinnedToAnotherModuleRefusesToStart(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	for _, c := range []struct{ main, pin string }{
		{"sluis-issuer", "backup"}, {"sluis-backup", "cloudflare"}, {"sluis-cloudflare", "issuer"},
		{"sluis-google", "issuer"}, {"sluis-issuer", "google"},
	} {
		bin := filepath.Join(t.TempDir(), "bootstrap")
		build := exec.Command("go", "build", "-tags", "lambda,lambda.norpc",
			"-ldflags", "-X github.com/truvity/sluis/internal/version.Module="+version.ModulePrefix+c.pin, "-o", bin, "./cmd/"+c.main)
		build.Dir = "../.."
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("go build %s: %v\n%s", c.main, err, out)
		}
		run := exec.Command(bin)
		run.Env = []string{"PATH=" + os.Getenv("PATH")}
		out, err := run.CombinedOutput()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			t.Errorf("%s pinned to %s: %v\n%s", c.main, c.pin, err, out)
		}
		if !strings.Contains(string(out), `pinned to the "`+c.pin+`" module`) {
			t.Errorf("%s pinned to %s did not say why: %s", c.main, c.pin, out)
		}
	}
}

// documents is one valid-enough document per module, by apiVersion alone: the
// module is chosen before anything is assembled.
var documents = map[string]string{
	lambdaapp.ModuleIssuer: "apiVersion: " + config.APIVersion("sluis") + "\nissuerURL: https://access.example\n",
	lambdaapp.ModuleCloudflare: "apiVersion: " + config.APIVersion("sluis") + `
issuerURL: https://access.example
cloudflare:
  serve: {}
  accounts:
    main:
      id: 0123456789abcdef0123456789abcdef
      minter: internal/cloudflare/main/minter
  presets:
    dns-example:
      account: main
      prototype: 9d3b1c7a5e2f4b8a9c0d1e2f3a4b5c6d
      description: DNS edits
      lifetime: 15m
      rotation: 5m
`,
	lambdaapp.ModuleBackup: "apiVersion: " + config.BackupAPIVersion + "\n",
	lambdaapp.ModuleGitHub: "apiVersion: " + config.APIVersion("controller-github") + "\n",
	lambdaapp.ModuleSlack:  "apiVersion: " + config.APIVersion("controller-slack") + "\n",
	lambdaapp.ModuleGoogle: "apiVersion: " + config.Group + "/sluis-google/v1\n",
}

func writeDocument(t *testing.T, body string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "sluis.yaml")
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestModuleOf(t *testing.T) {
	for _, c := range []struct{ name, body, want string }{
		{"issuer sluis/v3", documents[lambdaapp.ModuleIssuer], lambdaapp.ModuleIssuer},
		{"cloudflare serve", documents[lambdaapp.ModuleCloudflare], lambdaapp.ModuleCloudflare},
		{"backup", documents[lambdaapp.ModuleBackup], lambdaapp.ModuleBackup},
		{"github", documents[lambdaapp.ModuleGitHub], lambdaapp.ModuleGitHub},
		{"slack", documents[lambdaapp.ModuleSlack], lambdaapp.ModuleSlack},
		{"google", documents[lambdaapp.ModuleGoogle], lambdaapp.ModuleGoogle},
		{"v2 serve is the issuer's", "apiVersion: " + config.APIVersion("serve") + "\nissuerURL: https://access.example\n", lambdaapp.ModuleIssuer},
		{"no apiVersion is the issuer's", "issuerURL: https://access.example\n", lambdaapp.ModuleIssuer},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := lambdaapp.ModuleOf(writeDocument(t, c.body))
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("ModuleOf = %q, want %q", got, c.want)
			}
		})
	}
}

// A google document of another version is not the Google module's: it falls to
// the issuer's loader, which refuses it.
func TestModuleOfAnUnknownVersionIsNotAModule(t *testing.T) {
	m, err := lambdaapp.ModuleOf(writeDocument(t, "apiVersion: "+config.Group+"/sluis-google/v2\n"))
	if err == nil {
		t.Errorf("ModuleOf = %q, want a refusal", m)
	}
}

// Every module's zip refuses every other module's document, before anything is
// assembled and whether or not the other module is linked into the binary.
func TestEveryModulePinRefusesEveryForeignDocument(t *testing.T) {
	if len(documents) != 6 {
		t.Fatalf("the matrix covers %d modules, want 6", len(documents))
	}
	for pin := range documents {
		for doc, body := range documents {
			if doc == pin {
				continue
			}
			file := writeDocument(t, body)
			getenv := func(k string) string {
				if k == config.EnvConfig {
					return file
				}
				return ""
			}
			_, err := lambdaapp.OpenModule(context.Background(), getenv, pin)
			if err == nil || !strings.Contains(err.Error(), "sluis-"+pin+" function") || !strings.Contains(err.Error(), doc+" document") {
				t.Errorf("sluis-%s with a %s document: %v", pin, doc, err)
			}
		}
	}
}
