package issuerapp_test

import (
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/secrets"

	"github.com/truvity/sluis/internal/config"
)

// withPolicy is a service document and the policy document it names, for
// FromConfig: what the composition root reads before it builds anything.
func withPolicy(t *testing.T, f *config.Serve) (*config.Serve, *config.PolicyDocument) {
	t.Helper()
	p, err := config.PolicyOf(f, nil)
	if err != nil {
		t.Fatalf("the policy document: %v", err)
	}
	return f, p
}

// asName is a temporary file as a secret's name, under the root testSecrets
// reads from: the tests write each secret to a file of its own.
func asName(path string) string { return strings.TrimPrefix(path, "/") }

// testSecrets is the source the tests' issuers read their secrets from.
var testSecrets = secrets.File{Root: "/"}
