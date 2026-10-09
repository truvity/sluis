package audit_test

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/truvity/sluis/audit/sdk/catalogue/released"
)

// An audit installation keeps every catalogue version it was sent, byte for
// byte, and refuses a different document under a version it already holds,
// which stops the service at start. So a released version is frozen:
// catalogue/testdata/released holds the exact bytes of each version that
// shipped (roster-<version>.yaml, pinned by SHA256SUMS), and roster.yaml must
// equal the record of its version. Changing anything in roster.yaml, a comment
// included, means a new `version` AND its record.
//
// The check is the one audit's own catalogue is held to (package released),
// run over every catalogue in the repository, so that a change sluis's CI sees
// is caught here too.
func TestAReleasedCatalogueVersionIsNeverChanged(t *testing.T) {
	root, ok := released.RepositoryRoot(".")
	if !ok {
		t.Skip("not in a checkout")
	}
	docs, err := released.CheckTree(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "internal", "audit", "catalogue", "roster.yaml"); !slices.Contains(docs, want) {
		t.Errorf("the sweep did not find %s (found %v)", want, docs)
	}
}
