package keys_test

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/truvity/sluis/storage/keys"
)

// The schema is a second statement of the Go type; a purpose or adapter added
// to one and not the other would let a file validate and then fail to load.
func TestSchemaMatchesGo(t *testing.T) {
	b, err := os.ReadFile("../schemas/keys.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	var adapter struct {
		Enum []string `json:"enum"`
	}
	if err := json.Unmarshal(s.Properties["adapter"], &adapter); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(adapter.Enum, []string{"kms", "local", "transit"}) {
		t.Errorf("adapters: %v", adapter.Enum)
	}
	if len(s.Properties) != len(keys.Purposes())+1 {
		t.Errorf("schema has %d properties, Go has %d purposes + adapter", len(s.Properties), len(keys.Purposes()))
	}
	for _, p := range keys.Purposes() {
		if _, ok := s.Properties[string(p)]; !ok {
			t.Errorf("schema lacks purpose %q", p)
		}
	}
}
