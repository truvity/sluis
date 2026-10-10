package boundaries

import (
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// The Lambda zips of a release are one per module (docs/decisions/0072): each is
// the transitive closure of its main, and the closure is what the zip holds.
// This holds each closure to what the module is for, and refuses what belongs to
// a module that is not in it. The sweep lists the whole transitive set, since a
// package gets in as one import three levels down.

// lambdaForbidden is what no Lambda zip carries: the cluster, NATS and Valkey
// storage of the Kubernetes build.
var lambdaForbidden = []string{
	"k8s.io/", "sigs.k8s.io/controller-runtime", "github.com/nats-io/", "github.com/valkey-io/", "github.com/redis/",
	mod + "internal/kube", mod + "internal/valkey", mod + "internal/port/legacy", mod + "internal/port/nats", mod + "internal/migrate",
}

// providerApps are the packages that assemble the providers' controllers.
var providerApps = []string{
	mod + "internal/githubroster/app", mod + "internal/slackroster/app", mod + "internal/githubapp/mints",
}

// frontEnd are the issuer's process: the issuer library, the server, the
// console and the directory hub.
var frontEnd = []string{
	mod + "internal/issuer", mod + "internal/issuerapp", mod + "internal/rosterapp", mod + "internal/server",
	mod + "internal/hub", mod + "internal/hublocal", mod + "internal/app", mod + "internal/access", mod + "internal/connector",
	mod + "frontend",
}

var zips = []struct {
	module string
	// forbidden are prefixes the zip's closure must not contain, beside lambdaForbidden.
	forbidden []string
	// required are what the module is for: a closure that lacks them listed the wrong thing.
	required []string
}{
	{
		module: "issuer",
		// The signer and the console are in this process: the issuer is the one
		// zip that holds them. It holds neither the backup module's function nor
		// the Cloudflare module's.
		forbidden: []string{mod + "internal/backup/app", mod + "internal/cloudflare/app", mod + "internal/lambdaapp/backupfn", mod + "internal/lambdaapp/cloudflarefn"},
		required: []string{mod + "internal/signer", mod + "internal/server", mod + "frontend", mod + "internal/issuer",
			mod + "internal/lambdaapp/issuerfn", mod + "internal/port/dynamodb", mod + "storage/keys/kms"},
	},
	{
		module:    "cloudflare",
		forbidden: slices.Concat(frontEnd, providerApps, []string{mod + "internal/backup/app", mod + "internal/lambdaapp/issuerfn", mod + "internal/lambdaapp/backupfn"}),
		required:  []string{mod + "internal/cloudflare/app", mod + "internal/cloudflare/minter", mod + "internal/lambdaapp/cloudflarefn"},
	},
	{
		module:    "backup",
		forbidden: slices.Concat(frontEnd, providerApps, []string{mod + "internal/cloudflare/app", mod + "internal/lambdaapp/issuerfn", mod + "internal/lambdaapp/cloudflarefn"}),
		required:  []string{mod + "internal/backup/app", mod + "internal/backup/restorejob", mod + "internal/lambdaapp/backupfn"},
	},
}

func closure(t *testing.T, dir string) []string {
	t.Helper()
	cmd := exec.Command("go", "list", "-tags", "lambda,lambda.norpc", "-deps", "-f", "{{.ImportPath}}", dir)
	cmd.Dir = "../.."
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list %s: %v\n%s", dir, err, out)
	}
	deps := strings.Fields(string(out))
	if len(deps) < 150 {
		t.Fatalf("the sweep of %s found %d packages: it listed the wrong thing", dir, len(deps))
	}
	return deps
}

func TestEachZipHoldsItsModuleAndNoOther(t *testing.T) {
	for _, z := range zips {
		t.Run(z.module, func(t *testing.T) {
			deps := closure(t, "./cmd/sluis-"+z.module)
			for _, want := range z.required {
				if !slices.Contains(deps, want) {
					t.Errorf("sluis-%s does not hold %s: the sweep of %d packages listed the wrong thing", z.module, want, len(deps))
				}
			}
			for _, d := range deps {
				for _, bad := range slices.Concat(lambdaForbidden, z.forbidden) {
					if d == bad || strings.HasPrefix(d, strings.TrimSuffix(bad, "/")+"/") || (strings.HasSuffix(bad, "/") && strings.HasPrefix(d, bad)) {
						t.Errorf("sluis-%s holds %s (forbidden: %s): find who with `go list -tags lambda,lambda.norpc -deps -f '{{.ImportPath}}: {{.Imports}}' ./cmd/sluis-%s`",
							z.module, d, bad, z.module)
					}
				}
			}
		})
	}
}

// The signer is a package of the issuer's process (ADR 0072), never a binary:
// the ADR 0071 separate signer is not built, and nothing in the release names one.
func TestThereIsNoSignerBinary(t *testing.T) {
	if _, err := os.Stat("../../cmd/sluis-signer"); err == nil {
		t.Fatal("cmd/sluis-signer exists: the signer runs inside sluis-issuer")
	}
	raw, err := os.ReadFile("../../.goreleaser.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sluis-signer") {
		t.Error(".goreleaser.yaml names sluis-signer: the signer runs inside sluis-issuer")
	}
}

// The issuer module runs the signer and the console in its own process, not
// behind a call: its closure holds both.
func TestTheIssuerModuleHostsTheSignerAndTheConsole(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", "./internal/module/issuer")
	cmd.Dir = "../.."
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	deps := strings.Fields(string(out))
	for _, want := range []string{mod + "internal/signer", mod + "internal/server", mod + "frontend"} {
		if !slices.Contains(deps, want) {
			t.Errorf("internal/module/issuer does not hold %s", want)
		}
	}
}
