package audit_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// An audit installation keeps every catalogue version it was sent and refuses
// a different document under a version it already holds, which stops the
// service at start. So a released version is frozen: testdata/released holds
// the document of each version that shipped, and a catalogue carrying one of
// those versions must equal it. Changing an action means bumping the version
// in roster.yaml AND adding roster-<version>.yaml here.
func TestAReleasedCatalogueVersionIsNeverChanged(t *testing.T) {
	read := func(path string) (string, any) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		version, _ := doc["version"].(string)
		if version == "" {
			t.Fatalf("%s: no version", path)
		}
		return version, doc
	}

	current, currentDoc := read(filepath.Join("catalogue", "roster.yaml"))

	fixtures, err := filepath.Glob(filepath.Join("catalogue", "testdata", "released", "roster-*.yaml"))
	if err != nil || len(fixtures) == 0 {
		t.Fatalf("no released fixtures found (err %v)", err)
	}
	seen := false
	for _, fixture := range fixtures {
		version, doc := read(fixture)
		if want := "roster-" + version + ".yaml"; filepath.Base(fixture) != want {
			t.Errorf("%s declares version %s; it should be named %s", fixture, version, want)
		}
		if version != current {
			continue
		}
		seen = true
		if !reflect.DeepEqual(doc, currentDoc) {
			t.Errorf("catalogue version %s was released as %s and roster.yaml now differs from it: "+
				"bump `version` in roster.yaml and add testdata/released/roster-<new version>.yaml", current, fixture)
		}
	}
	_ = seen // a version with no fixture is new: it is not yet released
	if strings.TrimSpace(current) == "" {
		t.Fatal("empty catalogue version")
	}
}

// A released fixture is the record of what an installation holds, so it is
// frozen too. The test above compares roster.yaml with the fixture of its
// version, which proves nothing when both were edited together: a rename that
// rewrote every catalogue document in the repository (v1.57.0) changed
// roster.yaml and all of its fixtures alike, the comparison still passed, and
// every deployment refused the "same" 1.6.0. SHA256SUMS pins each fixture's
// bytes. A new version adds a line; an existing line changes only if a
// deployment's registered document did, which is never.
func TestAReleasedFixtureIsNeverRewritten(t *testing.T) {
	dir := filepath.Join("catalogue", "testdata", "released")
	raw, err := os.ReadFile(filepath.Join(dir, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	pinned := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		sum, name, ok := strings.Cut(line, "  ")
		if !ok {
			t.Fatalf("SHA256SUMS: malformed line %q", line)
		}
		pinned[name] = sum
	}
	fixtures, err := filepath.Glob(filepath.Join(dir, "roster-*.yaml"))
	if err != nil || len(fixtures) == 0 {
		t.Fatalf("no released fixtures found (err %v)", err)
	}
	for _, fixture := range fixtures {
		name := filepath.Base(fixture)
		body, err := os.ReadFile(fixture)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(body)
		want, ok := pinned[name]
		switch {
		case !ok:
			t.Errorf("%s is not in SHA256SUMS: add its line (sha256sum %s) when you release a new version", name, name)
		case hex.EncodeToString(sum[:]) != want:
			t.Errorf("%s was rewritten after release: a released document is never edited, "+
				"an installation holds the old bytes. Restore it, and bump the version for the change", name)
		}
		delete(pinned, name)
	}
	for name := range pinned {
		t.Errorf("SHA256SUMS lists %s, which is gone: a released fixture is never removed", name)
	}
}
