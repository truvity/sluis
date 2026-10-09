//nolint:lll // the documents are one-line tables
package sluispulumi

import "testing"

// Only `cloudflare.presets.<name>.endpoint` is a client's endpoint; the same
// key one level off, or anywhere else, is the function's.
func TestOnlyAPresetEndpointIsAClientEndpoint(t *testing.T) {
	const e = "https://example.invalid"
	for name, c := range map[string]struct {
		doc  map[string]any
		want string
	}{
		"a preset's":              {map[string]any{"cloudflare": map[string]any{"presets": map[string]any{"r2": map[string]any{"endpoint": e}}}}, ""},
		"the section's own":       {map[string]any{"cloudflare": map[string]any{"endpoint": e}}, "cloudflare.endpoint"},
		"below a preset":          {map[string]any{"cloudflare": map[string]any{"presets": map[string]any{"r2": map[string]any{"x": map[string]any{"endpoint": e}}}}}, "cloudflare.presets.r2.x.endpoint"},
		"an account's":            {map[string]any{"cloudflare": map[string]any{"accounts": map[string]any{"main": map[string]any{"endpoint": e}}}}, "cloudflare.accounts.main.endpoint"},
		"presets elsewhere":       {map[string]any{"ports": map[string]any{"presets": map[string]any{"r2": map[string]any{"endpoint": e}}}}, "ports.presets.r2.endpoint"},
		"secrets":                 {map[string]any{"secrets": map[string]any{"endpoint": e}}, "secrets.endpoint"},
		"in a list":               {map[string]any{"a": []any{map[string]any{"endpoint": e}}}, "a[0].endpoint"},
		"an empty one is nothing": {map[string]any{"secrets": map[string]any{"endpoint": ""}}, ""},
	} {
		if got := endpointIn(c.doc, nil); got != c.want {
			t.Errorf("%s: endpointIn = %q, want %q", name, got, c.want)
		}
	}
}
