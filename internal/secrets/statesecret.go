package secrets

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
)

// ParseStateSecret reads a state secret: one trailing newline trimmed,
// then hex or base64 for at least 32 bytes that look random. A file of one
// repeated character, or of a few distinct bytes, is a placeholder and not a
// secret, and is refused.
func ParseStateSecret(raw []byte) ([]byte, error) {
	text := strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")
	var decoded []byte
	if b, err := hex.DecodeString(text); err == nil {
		decoded = b
	} else {
		for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
			if b, err := enc.DecodeString(text); err == nil {
				decoded = b
				break
			}
		}
	}
	if len(decoded) < 32 {
		return nil, errors.New("must be hex or base64 of at least 32 bytes (for example `openssl rand -base64 32`)")
	}
	distinct := map[byte]bool{}
	for _, b := range decoded {
		distinct[b] = true
	}
	if len(distinct) < 8 {
		return nil, errors.New("looks like a placeholder, not a secret: it has too few distinct bytes")
	}
	return decoded, nil
}
