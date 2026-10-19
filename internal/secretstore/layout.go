package secretstore

import (
	"fmt"

	"github.com/truvity/sluis/internal/config"
)

// CheckLayout refuses a configured `secrets.layout` that is not v5, the only
// layout (ADR 0072). Layout v4 was removed in v1.77.1, and with it the reading
// of an empty layout as v4: an installation still on v4 migrates with
// `sluis migrate v5` of v1.77.0 first.
func CheckLayout(s string) error {
	if s == config.SecretsLayoutV5 {
		return nil
	}
	if s == "" {
		return fmt.Errorf("secrets.layout is not set: it must be %s, the only layout (layout v4 was removed in v1.77.1: migrate with v1.77.0 first)",
			config.SecretsLayoutV5)
	}
	return fmt.Errorf("secrets.layout: %q is not %s, the only layout (v4 was removed in v1.77.1: migrate with v1.77.0 first)",
		s, config.SecretsLayoutV5)
}
