package issuer

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// What a device says about itself is shown and recorded without the
// characters that would make it read as something else: C0 and C1 controls,
// bidirectional overrides and isolates, zero-width characters. And at most
// 128 runes of it.
func TestADevicesUserAgentLosesControlAndFormatCharacters(t *testing.T) {
	t.Parallel()

	for raw, want := range map[string]string{
		"sluisctl/1.0":                        "sluisctl/1.0",
		"sluis\x00ctl\x1b[31m/1.0\x7f":        "sluisctl[31m/1.0",
		"curl\u0085/8\u009b":                  "curl/8",
		"evil\u202egnp.exe":                   "evilgnp.exe",
		"a\u2066b\u2069c\u200bd\u200de\ufeff": "abcde",
		"  padded  ":                          "padded",
		"bad\xffutf8":                         "badutf8",
		"caf\u00e9 \u4e2d\u6587":              "caf\u00e9 \u4e2d\u6587",
	} {
		if got := deviceUserAgent(raw); got != want {
			t.Errorf("deviceUserAgent(%q) = %q, want %q", raw, got, want)
		}
	}

	long := deviceUserAgent(strings.Repeat("\u00e9", 500))
	if n := utf8.RuneCountInString(long); n != deviceUserAgentLimit || deviceUserAgentLimit != 128 {
		t.Errorf("a long User-Agent kept %d runes, want 128", n)
	}
}
