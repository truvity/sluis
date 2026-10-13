package modcall

import (
	"strings"
	"testing"
)

func TestLogValue(t *testing.T) {
	forged := "x\n{\"level\":\"ERROR\",\"msg\":\"forged\"}"
	cases := []struct{ name, in, want string }{
		{"plain", "billing.charge", "billing.charge"},
		{"subject", "repo:truvity/sluis:ref:refs/heads/master", "repo:truvity/sluis:ref:refs/heads/master"},
		{"empty", "", ""},
		{"lf", "a\nb", "ab"},
		{"cr", "a\rb", "ab"},
		{"crlf", "a\r\nb", "ab"},
		{"forged line", forged, "x{\"level\":\"ERROR\",\"msg\":\"forged\"}"},
		{"other control", "a\x00b\x1b[31mc\x7f", "ab[31mc"},
		{"unicode kept", "méthode-日本", "méthode-日本"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := logValue(c.in)
			if got != c.want {
				t.Fatalf("logValue(%q) = %q, want %q", c.in, got, c.want)
			}
			if strings.ContainsAny(got, "\n\r") {
				t.Fatalf("logValue(%q) kept a line break", c.in)
			}
		})
	}
	t.Run("bounded", func(t *testing.T) {
		got := logValue(strings.Repeat("é", 1000))
		if len(got) > maxLogValue || len(got) < maxLogValue-1 {
			t.Fatalf("length %d, want about %d", len(got), maxLogValue)
		}
	})
}
