package config_test

import (
	"testing"

	"github.com/truvity/sluis/internal/config"
)

// The `secret` of a client is a name or {generate: true}; the schema is what a
// document is held to before the Go types read it.
func TestThePolicySchemaChecksAClientSecret(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		secret any
		valid  bool
	}{
		"a name":               {"grafana", true},
		"generate":             {map[string]any{"generate": true}, true},
		"an empty name":        {"", false},
		"generate false":       {map[string]any{"generate": false}, false},
		"an empty object":      {map[string]any{}, false},
		"an extra key":         {map[string]any{"generate": true, "name": "x"}, false},
		"only another key":     {map[string]any{"name": "x"}, false},
		"generate as a string": {map[string]any{"generate": "true"}, false},
		"generate as a number": {map[string]any{"generate": 1}, false},
		"a list":               {[]any{"grafana"}, false},
		"a number":             {7, false},
		"a bool":               {true, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			doc := map[string]any{
				"apiVersion": config.APIVersion("policy"),
				"groups":     map[string]any{"all:access-roster:operator": map[string]any{}},
				"clients": map[string]any{
					"grafana": map[string]any{
						"kind":     "confidential",
						"secret":   tc.secret,
						"requires": []any{"all:access-roster:operator"},
					},
				},
			}
			err := config.Validate("policy", doc)
			if tc.valid && err != nil {
				t.Errorf("refused: %v", err)
			}
			if !tc.valid && err == nil {
				t.Error("accepted")
			}
		})
	}
}
