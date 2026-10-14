// Package logsafe makes a caller-supplied string safe to put in a log line.
package logsafe

import (
	"strings"
	"unicode"
)

// MaxValue bounds a caller-supplied value in a log line, in bytes.
const MaxValue = 256

// Value is a caller-supplied string (a method name from the request, the
// transport's request id, the caller's subject) made safe to put in a log
// line: line breaks and other control characters are removed, so the value
// cannot start a forged line, and its length is bounded by [MaxValue]. An
// ordinary value is returned unchanged.
func Value(v string) string { return Bounded(v, MaxValue) }

// Bounded is [Value] with a caller-chosen cap in bytes. Line breaks go first and
// by name, then every other control character; a cut that lands inside a
// multi-byte character drops the broken tail rather than logging it.
func Bounded(v string, limit int) string {
	v = strings.ReplaceAll(strings.ReplaceAll(v, "\n", ""), "\r", "")
	v = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, v)
	if len(v) > limit {
		v = strings.ToValidUTF8(v[:limit], "")
	}
	return v
}
