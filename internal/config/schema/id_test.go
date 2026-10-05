package schema_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/config/schema"
)

// TestTheIDVersionIsTheDocumentVersion holds every schema's `$id` to the
// version of the `apiVersion` its document carries. The service document
// moved to v3 while its schema was still named .../schemas/v2/config/.
func TestTheIDVersionIsTheDocumentVersion(t *testing.T) {
	for _, name := range schema.Names {
		body, ok := schema.Schema(name)
		if !ok {
			t.Fatalf("no schema %s", name)
		}
		var doc struct {
			ID         string `json:"$id"`
			Properties struct {
				APIVersion struct {
					Const string `json:"const"`
				} `json:"apiVersion"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(body, &doc); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		api := doc.Properties.APIVersion.Const
		if !strings.Contains(api, "/"+name+"/v") {
			t.Fatalf("%s: apiVersion %q does not name the document", name, api)
		}
		version := api[strings.LastIndex(api, "/")+1:]
		if want := "https://truvity.github.io/sluis/schemas/" + version + "/config/" + name + ".schema.json"; doc.ID != want {
			t.Errorf("%s: $id is %q, but the document is %s: want %q", name, doc.ID, version, want)
		}
	}
}
