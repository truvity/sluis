package main

import (
	"os/exec"
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
