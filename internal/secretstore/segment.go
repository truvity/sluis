package secretstore

import (
	"encoding/hex"
	"strings"
)

// segment makes s one path segment. A segment of letters, digits, `.`, `_`
// and `-` that is not `.`, `..` or begins `u-` is itself; anything else is
// `u-` and its bytes in hex, as the v3 credential layout spells it.
func segment(s string) string {
	if s == "" || s == "." || s == ".." || strings.HasPrefix(s, "u-") {
		return "u-" + hex.EncodeToString([]byte(s))
	}
	for _, r := range s {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-'
		if !ok {
			return "u-" + hex.EncodeToString([]byte(s))
		}
	}
	return s
}
