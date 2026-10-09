package secretstore

import (
	"fmt"

	"github.com/truvity/sluis/internal/config"
)

// CheckLayout refuses a configured `secrets.layout` that is not v4, the only
// layout; empty is v4. The v3 and transition layouts were removed: an
// installation on one runs v1.74.x and migrates first.
func CheckLayout(s string) error {
	switch s {
	case "", config.SecretsLayoutV4:
		return nil
	}
	return fmt.Errorf("secrets.layout: %q is not %s, the only layout (v3 and transition were removed: migrate on v1.74.x first)",
		s, config.SecretsLayoutV4)
}
