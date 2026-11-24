package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type policyCase struct {
	Name string            `json:"name"`
	OK   bool              `json:"ok"`
	Docs map[string]string `json:"docs"`
	Why  string            `json:"why"`
}

// verifies: REQ-AUDIT-149
//
// The policy of the schema documents of an AuditCatalogue, on the corpus the Rust crate reads too: the closed keywords, the
// references to the shared definitions only, patterns RE2 and Rust read alike, bounded depth and size. A document the policy
// takes also compiles; one it refuses never reaches the library (so a $ref to a file reads nothing).
func TestThePolicyOfTheCatalogueSchemas(t *testing.T) {
	var f struct {
		PolicyVersion int          `json:"policy_version"`
		Cases         []policyCase `json:"cases"`
	}
	readVectors(t, "schema-policy-vectors.json", &f)
	if f.PolicyVersion != PolicyVersion {
		t.Fatalf("the corpus is for policy %d, the code is policy %d: change both together", f.PolicyVersion, PolicyVersion)
	}
	if len(f.Cases) < 50 {
		t.Fatalf("%d cases: the vector file lost its cases", len(f.Cases))
	}
	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			_, err := ParseSchemas(c.Docs)
			if c.OK && err != nil {
				t.Fatalf("refused: %v", err)
			}
			if !c.OK && err == nil {
				t.Fatal("taken")
			}
			if err := CheckPolicy(c.Docs); c.OK != (err == nil) {
				t.Fatalf("CheckPolicy: %v", err)
			}
		})
	}
}

type semverCases struct {
	Accept []string `json:"accept"`
	Reject []string `json:"reject"`
}

// verifies: REQ-AUDIT-149
//
// The releases a catalogue and a record may name, on the corpus the Rust crate reads too.
func TestValidRelease(t *testing.T) {
	var f semverCases
	readVectors(t, "semver-vectors.json", &f)
	if len(f.Accept) < 10 || len(f.Reject) < 20 {
		t.Fatal("the vector file lost its cases")
	}
	for _, s := range f.Accept {
		if !ValidRelease(s) {
			t.Errorf("%q refused", s)
		}
	}
	for _, s := range f.Reject {
		if ValidRelease(s) {
			t.Errorf("%q taken", strings.TrimSpace(s))
		}
	}
}

// verifies: REQ-AUDIT-149
//
// The cost of the real catalogue is a fraction of the limit, so the limit loses nothing: the most a record of sluis costs is
// 885 (key.revoke), against 4000.
func TestTheRealCatalogueCostsAFractionOfTheLimit(t *testing.T) {
	files := map[string]string{}
	ents, err := os.ReadDir("../../conformance/audit/schema")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		b, err := os.ReadFile(filepath.Join("../../conformance/audit/schema", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		files[e.Name()] = string(b)
	}
	worst, err := MaxCost(files)
	if err != nil {
		t.Fatal(err)
	}
	if worst < 100 || worst > catalogueCostLimit/2 {
		t.Fatalf("the worst cost of the real catalogue is %d, the limit %d", worst, catalogueCostLimit)
	}
	t.Logf("the worst cost of the real catalogue: %d of %d", worst, catalogueCostLimit)
}

// catalogueCostLimit is the most a record may cost by audit's policy of the catalogue schemas (record 0004 of truvity/audit).
const catalogueCostLimit = 4000
