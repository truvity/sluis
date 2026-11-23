package export

import (
	"testing"

	"github.com/truvity/sluis/internal/port"
)

// A backup carries the issuer's lasting records and leaves out what lives for
// minutes and would be a credential handed back by a restore: an SRP exchange
// and a device code, with its user code.
func TestABackupLeavesOutShortLivedCredentials(t *testing.T) {
	t.Parallel()

	for key, want := range map[string]bool{
		"issuer:session:ada.s1":   true,
		"issuer:code:c1":          true,
		"srp.h1":                  false,
		"issuer:device:9f86d081":  false,
		"issuer:device-user:9f86": false,
		"issuer:device-poll:9f86": false,
		"rec.maintenance":         false,
	} {
		if got := owned(port.ModuleOIDC, key); got != want {
			t.Errorf("owned(%q) = %v, want %v", key, got, want)
		}
	}
}
