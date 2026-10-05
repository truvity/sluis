package issuerapp_test

import (
	"testing"

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
