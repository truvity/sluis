package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The one server main is built once per platform (docs/decisions/0071): the
// Lambda zip with `-tags lambda,lambda.norpc`, the Kubernetes image with no
// tag. Each build is held to its platform's adapters. The sweep lists the
// whole transitive set, since a forbidden package gets in as one import three
// levels down.
//
// A prefix, not a substring: sigs.k8s.io/yaml is a YAML parser the audit SDK
// uses and is no cluster client.
var platforms = []struct {
	name, tags string
	// forbidden are prefixes no package of the build may have.
	forbidden []string
	// required are packages the build is for: if the sweep does not see
	// them, it listed the wrong thing (a green guard that scanned nothing).
	required []string
}{
	{
		name: "lambda",
		tags: "lambda,lambda.norpc",
		forbidden: []string{
			"k8s.io/",
			"sigs.k8s.io/controller-runtime",
			"github.com/nats-io/",
			"github.com/valkey-io/",
			"github.com/redis/",
			"github.com/truvity/sluis/internal/kube",
			"github.com/truvity/sluis/internal/valkey",
			"github.com/truvity/sluis/internal/port/legacy",
			"github.com/truvity/sluis/internal/port/nats",
			"github.com/truvity/sluis/internal/migrate",
		},
		required: []string{
			"github.com/truvity/sluis/internal/port/dynamodb",
			"github.com/truvity/sluis/internal/port/s3blob",
			"github.com/truvity/sluis/internal/issuer",
			"github.com/aws/aws-lambda-go/lambda",
			"github.com/truvity/sluis/internal/port/invoke",
			"github.com/truvity/sluis/internal/lambdaapp",
		},
	},
	{
		name: "k8s",
		tags: "",
		forbidden: []string{
			"github.com/aws/aws-lambda-go",
			"github.com/truvity/sluis/internal/lambdaapp",
		},
		required: []string{
			"github.com/truvity/sluis/internal/kube",
			"github.com/truvity/sluis/internal/issuer",
		},
	},
}

func TestEachPlatformBuildImportsOnlyItsAdapters(t *testing.T) {
	for _, p := range platforms {
		t.Run(p.name, func(t *testing.T) {
			// `go list -deps` of the package as the release builds it.
			out, err := exec.Command("go", "list", "-tags", p.tags, "-deps", "-f", "{{.ImportPath}}", ".").CombinedOutput()
			if err != nil {
				t.Fatalf("go list: %v\n%s", err, out)
			}
			imports := strings.Fields(string(out))
			if len(imports) < 200 {
				t.Fatalf("the sweep found %d packages: it listed the wrong thing", len(imports))
			}
			for _, want := range p.required {
				if !slices.Contains(imports, want) {
					t.Errorf("the sweep of %d packages did not contain %s", len(imports), want)
				}
			}
			for _, pkg := range imports {
				for _, bad := range p.forbidden {
					if strings.HasPrefix(pkg, bad) {
						t.Errorf("the %s build imports %s (forbidden: %s): find who with "+
							"`go list -tags %q -deps -f '{{.ImportPath}}: {{.Imports}}' ./cmd/sluis`", p.name, pkg, bad, p.tags)
					}
				}
			}
		})
	}
}

// Both builds must compile: the tag selects files, and a file in neither set
// would break one platform without the other noticing.
func TestEachPlatformBuilds(t *testing.T) {
	for _, p := range platforms {
		if out, err := exec.Command("go", "build", "-tags", p.tags, "-o", "/dev/null", ".").CombinedOutput(); err != nil {
			t.Errorf("%s: go build: %v\n%s", p.name, err, out)
		}
	}
}

// The provider modules (github, slack, google) are processes of their own
// (docs/decisions/0071): none of their packages may import the issuer's
// assembly (internal/issuerapp) or the signer. A package that does not exist
// yet (the Google module lands in its own change) is skipped, not passed: the
// sweep must still find the others.
//
// Two levels are held. A direct import of either is refused in every package
// of the roles. The transitive set is held to no issuerapp; the signer is not
// held transitively yet, because internal/store reaches internal/issuer through
// internal/valkey (issuer.State), and internal/issuer imports the signer.
func TestProviderModulesImportNeitherIssuerappNorSigner(t *testing.T) {
	const mod = "github.com/truvity/sluis/"
	roles := map[string][]string{
		"github": {"internal/module/github", "internal/githubroster", "internal/githubapp", "internal/lambdaapp/githubfn"},
		"slack":  {"internal/module/slack", "internal/slackroster", "internal/slackapp", "internal/lambdaapp/slackfn"},
		"google": {"internal/module/google", "internal/googledir", "internal/lambdaapp/googlefn"},
	}
	forbidden := []string{mod + "internal/issuerapp", mod + "internal/signer"}
	for role, dirs := range roles {
		t.Run(role, func(t *testing.T) {
			var patterns []string
			for _, d := range dirs {
				if _, err := os.Stat(filepath.Join("..", "..", d)); err == nil {
					patterns = append(patterns, "./"+d+"/...")
				}
			}
			if len(patterns) == 0 {
				t.Skipf("the %s module has no package yet", role)
			}
			// Direct imports of every package of the role, tests excluded.
			list := exec.Command("go", append([]string{"list", "-f", "{{.ImportPath}} {{join .Imports \" \"}}"}, patterns...)...)
			list.Dir = "../.."
			out, err := list.CombinedOutput()
			if err != nil {
				t.Fatalf("go list: %v\n%s", err, out)
			}
			lines := strings.Split(strings.TrimSpace(string(out)), "\n")
			if len(lines) < 1 || lines[0] == "" {
				t.Fatalf("the sweep of %v found no package: it listed the wrong thing", patterns)
			}
			for _, line := range lines {
				f := strings.Fields(line)
				for _, imp := range f[1:] {
					if slices.Contains(forbidden, imp) {
						t.Errorf("%s imports %s: a provider module hosts no issuer and no signer", f[0], imp)
					}
				}
			}
			// The whole closure of the role holds no issuerapp.
			deps := exec.Command("go", append([]string{"list", "-deps", "-f", "{{.ImportPath}}"}, patterns...)...)
			deps.Dir = "../.."
			out, err = deps.CombinedOutput()
			if err != nil {
				t.Fatalf("go list -deps: %v\n%s", err, out)
			}
			closure := strings.Fields(string(out))
			if !slices.Contains(closure, mod+"internal/config") {
				t.Fatalf("the sweep of %d packages did not contain internal/config: it listed the wrong thing", len(closure))
			}
			if slices.Contains(closure, mod+"internal/issuerapp") {
				t.Errorf("the %s module's closure holds internal/issuerapp: find who with `go list -deps -f '{{.ImportPath}}: {{.Imports}}' %v`", role, patterns)
			}
		})
	}
}
