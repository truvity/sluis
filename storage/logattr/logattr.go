// Package logattr builds slog attributes for UNTRUSTED values: a request
// field, a token claim, a caller-supplied name, the text of an error that may
// quote either. The stdlib handlers already quote or escape CR, LF and
// U+2028/2029, so line injection is not exploitable with them today; these
// constructors make that explicit at the call site, where a code scanner
// looks, and cover what the handlers leave alone: bidirectional controls,
// other control characters and unbounded length.
//
// Trusted values (configuration, constants, identifiers this program
// generated) stay plain slog.String. sloglint cannot tell the two apart, so
// choosing between them is a review rule (CONTRIBUTING.md).
package logattr

import (
	"log/slog"
	"strings"
	"unicode"
)

// MaxRunes is the longest value kept, in runes, before the marker.
const MaxRunes = 256

const marker = "…"

// Safe returns v on one line with control and bidi-control characters
// removed and at most MaxRunes runes, ending in "…" when it was cut.
func Safe(v string) string {
	// The two ReplaceAll calls are the sanitiser code scanners recognise
	// (go/log-injection); keep them inline and first.
	v = strings.ReplaceAll(v, "\n", "")
	v = strings.ReplaceAll(v, "\r", "")
	v = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsControl(r): // C0, DEL, C1 (ESC included)
			return -1
		case r == ' ' || r == ' ':
			return -1
		case r >= '‪' && r <= '‮', r >= '⁦' && r <= '⁩': // bidi embeddings, overrides, isolates
			return -1
		case r == '‎' || r == '‏' || r == '؜': // implicit directional marks
			return -1
		}
		return r
	}, v)
	if len([]rune(v)) > MaxRunes {
		v = string([]rune(v)[:MaxRunes]) + marker
	}
	return v
}

// SafeString is slog.String(key, Safe(v)).
func SafeString(key, v string) slog.Attr {
	return slog.String(key, Safe(v))
}

// SafeStrings is a group-free list attribute: the elements sanitised and
// joined with ", ", the joined text capped like any other value.
func SafeStrings(key string, v []string) slog.Attr {
	parts := make([]string, len(v))
	for i, s := range v {
		parts[i] = Safe(s)
	}
	return slog.String(key, Safe(strings.Join(parts, ", ")))
}

// Error is [Safe] over an error's message, "" for nil. Errors are worth their
// own call because the interesting ones are built from the input that caused
// them: a directory read that failed carries the address it was asked about.
func Error(err error) string {
	if err == nil {
		return ""
	}
	return Safe(err.Error())
}

// SafeError is the sanitised text of err as a string attribute.
func SafeError(key string, err error) slog.Attr {
	return slog.String(key, Error(err))
}
