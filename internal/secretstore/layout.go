package secretstore

import (
	"fmt"

	"github.com/truvity/sluis/internal/config"
)

// Layout is which storage layout an installation reads and writes.
type Layout string

const (
	// LayoutV3 is the layout of ADR 0036: `private/`, and `export/` copies
	// made by the exports controller. It is the default.
	LayoutV3 Layout = config.SecretsLayoutV3
	// LayoutTransition reads v4 first, falls back to v3, and writes every
	// value to v4 and then to v3. The exports controller still runs.
	LayoutTransition Layout = config.SecretsLayoutTransition
	// LayoutV4 is the layout of ADR 0041: `internal/` and `external/`.
	LayoutV4 Layout = config.SecretsLayoutV4
)

// ParseLayout reads a configured layout; empty is [LayoutV3].
func ParseLayout(s string) (Layout, error) {
	switch s {
	case "":
		return LayoutV3, nil
	case config.SecretsLayoutV3, config.SecretsLayoutTransition, config.SecretsLayoutV4:
		return Layout(s), nil
	}
	return "", fmt.Errorf("secrets.layout: %q is %s, %s or %s", s,
		config.SecretsLayoutV3, config.SecretsLayoutTransition, config.SecretsLayoutV4)
}

// ReadsV4 reports whether v4 is read (first, in transition).
func (l Layout) ReadsV4() bool { return l == LayoutTransition || l == LayoutV4 }

// WritesV4 reports whether a write goes to v4.
func (l Layout) WritesV4() bool { return l == LayoutTransition || l == LayoutV4 }

// WritesV3 reports whether a write goes to v3.
func (l Layout) WritesV3() bool { return l == LayoutV3 || l == LayoutTransition }
