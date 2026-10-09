package audit_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/truvity/sluis/audit/sdk/catalogue"
)

// The writer registers each catalogue version it is given into the archive and
// the readers (audit-observe, audit-query) load every one with their own
// release's validator. A catalogue shape change that a reader of the same
// release rejects is a stalled index (v1.74.0 replaced an action's `profiles`
// with `category`, and a reader of the release before refused the first record).
// So the newest released catalogue of each source must load with this release's
// validator, the one a reader of this release runs.
func TestTheNewestReleasedCatalogueOfEachSourceLoadsWithTheReadersValidator(t *testing.T) {
	fixtures, err := filepath.Glob(filepath.Join("catalogue", "testdata", "released", "*.yaml"))
	if err != nil || len(fixtures) == 0 {
		t.Fatalf("no released catalogues found (err %v)", err)
	}
	schemas, err := filepath.Glob(filepath.Join("catalogue", "*.json"))
	if err != nil || len(schemas) == 0 {
		t.Fatalf("no extension schemas found (err %v)", err)
	}
	var extension [][]byte
	for _, s := range schemas {
		raw, err := os.ReadFile(s)
		if err != nil {
			t.Fatal(err)
		}
		extension = append(extension, raw)
	}

	newest := map[string]struct {
		version string
		path    string
	}{}
	for _, f := range fixtures {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var head struct {
			Source  string `yaml:"source"`
			Version string `yaml:"version"`
		}
		if err := yaml.Unmarshal(raw, &head); err != nil || head.Source == "" || head.Version == "" {
			t.Fatalf("%s: source %q version %q: %v", f, head.Source, head.Version, err)
		}
		if cur, ok := newest[head.Source]; !ok || versionLess(cur.version, head.Version) {
			newest[head.Source] = struct{ version, path string }{head.Version, f}
		}
	}
	if len(newest) == 0 {
		t.Fatal("no source found: a guard must not pass an empty sweep")
	}
	for source, n := range newest {
		raw, err := os.ReadFile(n.path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := catalogue.Load(raw, extension); err != nil {
			t.Errorf("the newest released catalogue of %s (%s) does not load with the readers' validator, so a reader of this release would stall on it: %v",
				source, n.version, err)
		}
	}
}

// versionLess orders dotted numeric versions.
func versionLess(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		x, _ := strconv.Atoi(as[i])
		y, _ := strconv.Atoi(bs[i])
		if x != y {
			return x < y
		}
	}
	return len(as) < len(bs)
}
