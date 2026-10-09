package released_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/truvity/sluis/audit/sdk/catalogue/released"
)

// Every catalogue this repository ships is held to the versions it released:
// the common catalogue here, sluis's own roster catalogue, and any added
// later, which Find picks up without being listed. The common catalogue 2.1.0
// was once changed in place (its data_schema URLs moved) and every writer
// built from it refused an archive written by an earlier one.
func TestAReleasedCatalogueVersionIsNeverChanged(t *testing.T) {
	root, ok := released.RepositoryRoot(".")
	if !ok {
		t.Skip("not in a checkout: the repository's catalogues are not here")
	}
	docs, err := released.CheckTree(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"audit/sdk/catalogue/common.yaml", "internal/audit/catalogue/roster.yaml"} {
		if !slices.Contains(docs, filepath.Join(root, filepath.FromSlash(want))) {
			t.Errorf("the sweep did not find %s (found %v)", want, docs)
		}
	}
}

const doc = `# a comment
source: shop
version: "1.1.0"
actions:
  shop.order.placed:
    summary: An order was placed.
`

func sum(s string) string {
	b := sha256.Sum256([]byte(s))
	return hex.EncodeToString(b[:])
}

// layout writes catalogue/shop.yaml and its released records, and returns the
// document's path.
func layout(t *testing.T, document string, records map[string]string, sums string) string {
	t.Helper()
	dir := t.TempDir()
	rel := filepath.Join(dir, "catalogue", "testdata", "released")
	if err := os.MkdirAll(rel, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "catalogue", "shop.yaml")
	if err := os.WriteFile(path, []byte(document), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, body := range records {
		if err := os.WriteFile(filepath.Join(rel, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if sums != "" {
		if err := os.WriteFile(filepath.Join(rel, released.Sums), []byte(sums), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestCheck(t *testing.T) {
	older := strings.Replace(doc, `"1.1.0"`, `"1.0.0"`, 1)
	sums := sum(older) + "  shop-1.0.0.yaml\n" + sum(doc) + "  shop-1.1.0.yaml\n"
	for _, tc := range []struct {
		name     string
		document string
		records  map[string]string
		sums     string
		want     string // "" for success
	}{
		{name: "the document is its record", document: doc,
			records: map[string]string{"shop-1.0.0.yaml": older, "shop-1.1.0.yaml": doc}, sums: sums},
		{name: "a comment changed under a released version", document: strings.Replace(doc, "a comment", "another comment", 1),
			records: map[string]string{"shop-1.0.0.yaml": older, "shop-1.1.0.yaml": doc}, sums: sums,
			want: "catalogue shop 1.1.0 was released as"},
		{name: "the current version has no record", document: doc,
			records: map[string]string{"shop-1.0.0.yaml": older}, sums: sum(older) + "  shop-1.0.0.yaml\n",
			want: "has no released record"},
		{name: "no records at all", document: doc, want: "has no released record"},
		{name: "a record rewritten with the document", document: doc + "# edit\n",
			records: map[string]string{"shop-1.0.0.yaml": older, "shop-1.1.0.yaml": doc + "# edit\n"}, sums: sums,
			want: "was rewritten after release"},
		{name: "a record not pinned", document: doc,
			records: map[string]string{"shop-1.0.0.yaml": older, "shop-1.1.0.yaml": doc}, sums: sum(older) + "  shop-1.0.0.yaml\n",
			want: "is not in"},
		{name: "a record removed", document: doc,
			records: map[string]string{"shop-1.1.0.yaml": doc}, sums: sums,
			want: "which is gone"},
		{name: "a record misnamed", document: doc,
			records: map[string]string{"shop-1.0.0.yaml": older, "shop-1.1.0.yaml": doc, "shop-9.yaml": older},
			sums:    sums + sum(older) + "  shop-9.yaml\n",
			want:    "it should be named shop-1.0.0.yaml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := released.Check(layout(t, tc.document, tc.records, tc.sums))
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("want success, got %v", err)
			case tc.want != "" && err == nil:
				t.Fatalf("want an error containing %q, got none", tc.want)
			case tc.want != "" && !strings.Contains(err.Error(), tc.want):
				t.Fatalf("want an error containing %q, got %v", tc.want, err)
			}
		})
	}
}

// An installation registers the schemas beside the document and compares
// them as well, so they are pinned under the version too.
func TestCheckPinsTheSchemas(t *testing.T) {
	const schema = `{"$id": "https://example.com/schemas/order.json", "type": "object"}`
	pins := sum(doc) + "  shop-1.1.0.yaml\n" + sum(schema) + "  shop-1.1.0/order.json\n"
	for _, tc := range []struct {
		name, schema, sums, want string
	}{
		{name: "pinned", schema: schema, sums: pins},
		{name: "changed under the version", schema: strings.Replace(schema, "object", "array", 1), sums: pins,
			want: "differs from the one released under shop-1.1.0"},
		{name: "not pinned", schema: schema, sums: sum(doc) + "  shop-1.1.0.yaml\n", want: "is not pinned"},
		{name: "removed", sums: pins, want: "which is no longer beside"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := layout(t, doc, map[string]string{"shop-1.1.0.yaml": doc}, tc.sums)
			if tc.schema != "" {
				if err := os.WriteFile(filepath.Join(filepath.Dir(path), "order.json"), []byte(tc.schema), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			err := released.Check(path)
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("want success, got %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("want an error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestFindSkipsRecordsAndExamples(t *testing.T) {
	path := layout(t, doc, map[string]string{"shop-1.1.0.yaml": doc}, sum(doc)+"  shop-1.1.0.yaml\n")
	root := filepath.Dir(filepath.Dir(path))
	for _, extra := range []string{"examples/catalogue/demo.yaml", ".hidden/demo.yaml", "values.yaml"} {
		body := doc
		if extra == "values.yaml" {
			body = "version: 1\nsource: x\n" // no actions: not a catalogue
		}
		p := filepath.Join(root, filepath.FromSlash(extra))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	docs, err := released.CheckTree(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 || docs[0] != path {
		t.Fatalf("want only %s, got %v", path, docs)
	}
}

func TestCheckTreeRefusesAnEmptySweep(t *testing.T) {
	if _, err := released.CheckTree(t.TempDir()); err == nil {
		t.Fatal("a tree without a catalogue passed")
	}
}
