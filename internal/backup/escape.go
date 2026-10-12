package backup

import (
	"fmt"
	"strings"
)

const upperHex = "0123456789ABCDEF"

func plain(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.'
}

// EscapeKey turns an object key into a relative path inside the tar: split at
// '/', each segment keeps letters, digits, '-', '_' and '.', and writes every
// other byte as %XX, as well as a '.' that begins the segment; an empty segment
// is "%". The result never holds "." or ".." as a segment, an absolute path or
// a byte a file system treats specially. [UnescapeKey] reverses it.
func EscapeKey(key string) string {
	segs := strings.Split(key, "/")
	for i, s := range segs {
		if s == "" {
			segs[i] = "%"
			continue
		}
		var b strings.Builder
		for j := 0; j < len(s); j++ {
			c := s[j]
			if plain(c) && (c != '.' || j > 0) {
				b.WriteByte(c)
				continue
			}
			b.WriteByte('%')
			b.WriteByte(upperHex[c>>4])
			b.WriteByte(upperHex[c&15])
		}
		segs[i] = b.String()
	}
	return strings.Join(segs, "/")
}

// UnescapeKey reverses [EscapeKey]. It accepts only the form EscapeKey writes,
// so one key has one path.
func UnescapeKey(path string) (string, error) {
	segs := strings.Split(path, "/")
	for i, s := range segs {
		if s == "%" {
			segs[i] = ""
			continue
		}
		var b strings.Builder
		for j := 0; j < len(s); j++ {
			c := s[j]
			if c != '%' {
				b.WriteByte(c)
				continue
			}
			if j+2 >= len(s) {
				return "", fmt.Errorf("%w: blob path %q has a short escape", ErrFormat, path)
			}
			hi, lo := strings.IndexByte(upperHex, s[j+1]), strings.IndexByte(upperHex, s[j+2])
			if hi < 0 || lo < 0 {
				return "", fmt.Errorf("%w: blob path %q has a bad escape", ErrFormat, path)
			}
			b.WriteByte(byte(hi<<4 | lo))
			j += 2
		}
		segs[i] = b.String()
	}
	key := strings.Join(segs, "/")
	if EscapeKey(key) != path {
		return "", fmt.Errorf("%w: blob path %q is not in its escaped form", ErrFormat, path)
	}
	return key, nil
}
