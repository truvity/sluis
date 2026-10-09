package logattr_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/truvity/sluis/storage/logattr"
)

func TestSafe(t *testing.T) {
	long := strings.Repeat("a", logattr.MaxRunes+50)
	for name, tc := range map[string]struct{ in, want string }{
		"plain":           {"hello world", "hello world"},
		"lf":              {"a\nb", "ab"},
		"cr":              {"a\rb", "ab"},
		"crlf":            {"a\r\nb", "ab"},
		"fake level line": {"x\nlevel=ERROR msg=pwned", "xlevel=ERROR msg=pwned"},
		"u2028":           {"a\u2028b\u2029c", "abc"},
		"bidi override":   {"a\u202eb\u2066c\u2069d", "abcd"},
		"esc":             {"a\x1b[31mred", "a[31mred"},
		"nul and c1":      {"a\x00b\u0085c", "abc"},
		"unicode kept":    {"héllo 世界", "héllo 世界"},
		"cap":             {long, strings.Repeat("a", logattr.MaxRunes) + "…"},
		"exact":           {strings.Repeat("a", logattr.MaxRunes), strings.Repeat("a", logattr.MaxRunes)},
	} {
		t.Run(name, func(t *testing.T) {
			if got := logattr.Safe(tc.in); got != tc.want {
				t.Fatalf("Safe(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestAttrs(t *testing.T) {
	if a := logattr.SafeString("k", "a\nb"); a.Key != "k" || a.Value.String() != "ab" {
		t.Fatalf("SafeString = %v", a)
	}
	if a := logattr.SafeStrings("k", []string{"a\n", "b\u202e"}); a.Value.String() != "a, b" {
		t.Fatalf("SafeStrings = %v", a)
	}
	if a := logattr.SafeError("error", errors.New("bad\nlevel=ERROR")); a.Value.String() != "badlevel=ERROR" {
		t.Fatalf("SafeError = %v", a)
	}
	if a := logattr.SafeError("error", nil); a.Value.String() != "" {
		t.Fatalf("SafeError(nil) = %v", a)
	}
}

func TestPseudonym(t *testing.T) {
	a, b := logattr.Pseudonym("subject", "ada@example.test"), logattr.Pseudonym("subject", "ada@example.test")
	c := logattr.Pseudonym("subject", "bob@example.test")
	if a.Key != "subject" || a.Value.String() != b.Value.String() || a.Value.String() == c.Value.String() {
		t.Fatalf("not stable or not distinct: %v %v %v", a, b, c)
	}
	if len(a.Value.String()) != 16 || strings.Contains(a.Value.String(), "ada") {
		t.Fatalf("pseudonym = %q", a.Value.String())
	}
}
