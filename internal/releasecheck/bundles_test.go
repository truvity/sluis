// Package releasecheck holds the tests of what a release publishes, which
// nothing else reads before a tag does: .goreleaser.yaml is exercised only
// by a tagged run, and a bundle that lacks a file is found by whoever
// downloads it.
package releasecheck

import (
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/truvity/sluis/internal/config/schema"
)

const root = "../.."

type archive struct {
	ID    string `yaml:"id"`
	Meta  bool   `yaml:"meta"`
	Files []file `yaml:"files"`
	Name  string `yaml:"name_template"`
}

// file is an entry of `files`: a bare glob, or a mapping with `src`.
type file struct {
	Src         string `yaml:"src"`
	StripParent bool   `yaml:"strip_parent"`
}

func (f *file) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		f.Src = n.Value
		return nil
	}
	type plain file
	return n.Decode((*plain)(f))
}

// bundle is the file names a meta archive of .goreleaser.yaml holds, with the
// globs expanded against the checkout, as goreleaser expands them (flat:
// strip_parent).
func bundle(t *testing.T, id, namePrefix string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, ".goreleaser.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Archives []archive `yaml:"archives"`
		Checksum struct {
			Disable bool `yaml:"disable"`
		} `yaml:"checksum"`
	}
	if err = yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Checksum.Disable {
		t.Error("checksums are disabled: the bundles must be in checksums.txt")
	}
	for _, a := range cfg.Archives {
		if a.ID != id {
			continue
		}
		if !a.Meta {
			t.Errorf("archive %s is not a meta archive: it would be built per platform", id)
		}
		if !strings.HasPrefix(a.Name, namePrefix+"_{{ .Version }}") {
			t.Errorf("archive %s is named %q, want %s_<version>", id, a.Name, namePrefix)
		}
		var names []string
		for _, f := range a.Files {
			if !f.StripParent {
				t.Errorf("archive %s: %s keeps its directory: the bundle is flat", id, f.Src)
			}
			matches, err := filepath.Glob(filepath.Join(root, f.Src))
			if err != nil || len(matches) == 0 {
				t.Errorf("archive %s: %s matches nothing (%v)", id, f.Src, err)
			}
			for _, m := range matches {
				names = append(names, path.Base(filepath.ToSlash(m)))
			}
		}
		slices.Sort(names)
		return names
	}
	t.Fatalf(".goreleaser.yaml has no archive %q", id)
	return nil
}

// TestTheAuditCatalogueBundleHoldsExactlyWhatTheCatalogueNames: the audit
// writer refuses to start when a schema its catalogue references is not
// beside it, and example's was down for a day because of exactly that. So every
// `data_schema` of roster.yaml is in the bundle, and so is nothing else.
func TestTheAuditCatalogueBundleHoldsExactlyWhatTheCatalogueNames(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(root, "internal", "audit", "catalogue", "roster.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Actions map[string]struct {
			DataSchema string `yaml:"data_schema"`
		} `yaml:"actions"`
	}
	if err = yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	want := []string{"roster.yaml"}
	for name, a := range doc.Actions {
		if a.DataSchema == "" {
			continue
		}
		if !strings.HasSuffix(a.DataSchema, ".json") {
			t.Errorf("action %s: data_schema %q is not a .json file", name, a.DataSchema)
		}
		want = append(want, path.Base(a.DataSchema))
	}
	if len(want) < 10 {
		t.Fatalf("found %d files referenced: the test is not reading the catalogue (actions %d)", len(want), len(doc.Actions))
	}
	slices.Sort(want)
	want = slices.Compact(want)
	got := bundle(t, "audit-catalogue", "sluis-audit-catalogue")
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("the catalogue references %s and the bundle does not hold it", w)
		}
	}
	for _, g := range got {
		if !slices.Contains(want, g) {
			t.Errorf("the bundle holds %s, which the catalogue does not reference", g)
		}
	}
	// Every file the writer needs is also in the directory the bundle is cut from.
	for _, w := range want {
		if _, err := os.Stat(filepath.Join(root, "internal", "audit", "catalogue", w)); err != nil {
			t.Errorf("referenced file %s is missing: %v", w, err)
		}
	}
}

// TestTheConfigSchemasBundleHoldsEveryAuthoredSchema: the bundle is the files
// the generator writes, no fewer and no more.
func TestTheConfigSchemasBundleHoldsEveryAuthoredSchema(t *testing.T) {
	var want []string
	for _, n := range schema.Names {
		want = append(want, n+".schema.json")
	}
	slices.Sort(want)
	got := bundle(t, "config-schemas", "sluis-config-schemas")
	if !slices.Equal(got, want) {
		t.Errorf("sluis-config-schemas holds %v, the generator writes %v", got, want)
	}
}
