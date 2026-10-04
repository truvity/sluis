package main

import (
	"os/exec"
	"strings"
	"testing"
)

// The Lambda binary carries the AWS adapters and the shared core, and none of
// the Kubernetes build's storage. A client-go, NATS or Valkey client in it would
// be tens of megabytes of cold start for code that cannot run there, and the
// way it gets in is one import in a package three levels down: so the whole
// transitive set is listed and held to the rule.
//
// A prefix, not a substring: sigs.k8s.io/yaml is a YAML parser the audit SDK
// uses and is no cluster client; the controller-runtime and client-go packages
// that are, are under k8s.io/ or named below.
var forbidden = []string{
	"k8s.io/",
	"sigs.k8s.io/controller-runtime",
	"github.com/nats-io/",
	"github.com/valkey-io/",
	"github.com/redis/",
	"github.com/truvity/sluis/internal/kube",
	"github.com/truvity/sluis/internal/valkey",
	"github.com/truvity/sluis/internal/port/legacy",
	"github.com/truvity/sluis/internal/port/nats",
}

// required are what the binary is for: if the sweep does not see them, it
// listed the wrong thing (a green guard that scanned nothing).
var required = []string{
	"github.com/aws/aws-lambda-go/lambda",
	"github.com/truvity/sluis/internal/port/dynamodb",
	"github.com/truvity/sluis/internal/port/s3blob",
	"github.com/truvity/sluis/internal/port/invoke",
	"github.com/truvity/sluis/internal/lambdaapp",
	"github.com/truvity/sluis/internal/issuer",
}

func TestTheLambdaRootImportsNoKubernetesNATSOrValkey(t *testing.T) {
	// `go list -deps` of the package as the release builds it.
	out, err := exec.Command("go", "list", "-tags", "lambda,lambda.norpc", "-deps", "-f", "{{.ImportPath}}", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	imports := strings.Fields(string(out))
	if len(imports) < 200 {
		t.Fatalf("the sweep found %d packages: it listed the wrong thing", len(imports))
	}
	for _, want := range required {
		found := false
		for _, p := range imports {
			if p == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("the sweep of %d packages did not contain %s", len(imports), want)
		}
	}
	for _, p := range imports {
		for _, bad := range forbidden {
			if strings.HasPrefix(p, bad) {
				t.Errorf("the Lambda root imports %s (forbidden: %s): find who with `go mod why -m` or "+
					"`go list -tags lambda -deps -f '{{.ImportPath}}: {{.Imports}}' ./cmd/sluis-lambda`", p, bad)
			}
		}
	}
}

// Built without the tag, the root would carry the cluster storage: the test
// above holds the tagged build to the rule, and this holds the tag itself.
func TestTheLambdaRootBuildsWithTheLambdaTag(t *testing.T) {
	if out, err := exec.Command("go", "build", "-tags", "lambda,lambda.norpc", "-o", "/dev/null", ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
}
