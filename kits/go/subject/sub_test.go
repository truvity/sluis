package subject_test

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/truvity/sluis/v2/kits/go/subject"
)

// The vector file is shared (kits/conformance/token); no Rust unit forms a sub today.
const subVectorFile = "../../conformance/token/sub-vectors.json"

type subVectors struct {
	Shape []struct {
		Name string `json:"name"`
		Sub  string `json:"sub"`
		Kind string `json:"kind"`
	} `json:"shape"`
	CIJob []struct {
		Name        string `json:"name"`
		TrustDomain string `json:"trust_domain"`
		Repository  string `json:"repository"`
		Sub         string `json:"sub"`
		Refused     string `json:"refused"`
	} `json:"ci_job"`
	ServiceAccount []struct {
		Name        string `json:"name"`
		TrustDomain string `json:"trust_domain"`
		Cluster     string `json:"cluster"`
		Namespace   string `json:"namespace"`
		NameOf      string `json:"name_of"`
		Sub         string `json:"sub"`
		Refused     string `json:"refused"`
	} `json:"service_account"`
	V1 []struct {
		SubjectType string  `json:"subject_type"`
		V1          string  `json:"v1"`
		V2Kind      *string `json:"v2_kind"`
		TrustDomain string  `json:"trust_domain"`
		Repository  string  `json:"repository"`
		Cluster     string  `json:"cluster"`
		Namespace   string  `json:"namespace"`
		NameOf      string  `json:"name_of"`
		V2          *string `json:"v2"`
		Decision    string  `json:"decision"`
	} `json:"v1_spellings"`
}

func loadSubVectors(t *testing.T) subVectors {
	t.Helper()
	raw, err := os.ReadFile(subVectorFile)
	if err != nil {
		t.Fatal(err)
	}
	var v subVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Shape) == 0 || len(v.CIJob) == 0 || len(v.ServiceAccount) == 0 || len(v.V1) == 0 {
		t.Fatal("the vector file lacks a section")
	}
	return v
}

// reason names a refusal as the vector file does.
func reason(t *testing.T, err error) string {
	t.Helper()
	switch {
	case err == nil:
		return ""
	case errors.Is(err, subject.ErrTrustDomain):
		return "trust_domain"
	case errors.Is(err, subject.ErrSegment):
		return "segment"
	case errors.Is(err, subject.ErrLength):
		return "length"
	}
	t.Fatalf("an error that is none of the three: %v", err)
	return ""
}

// verifies: REQ-SUBJ-071, REQ-SUBJ-073
func TestClassifyMatchesTheVectors(t *testing.T) {
	for _, v := range loadSubVectors(t).Shape {
		kind, ok := subject.Classify(v.Sub)
		if string(kind) != v.Kind || ok != (v.Kind != "") {
			t.Errorf("%s: Classify(%q) = %q, %v; want %q", v.Name, v.Sub, kind, ok, v.Kind)
		}
	}
}

// An e-mail address is none of the forms, whatever is put before the at sign: no string with an at sign has a form.
//
// verifies: REQ-SUBJ-071
func TestNoAddressHasAForm(t *testing.T) {
	for _, s := range []string{
		"ada@example.com", strings.Repeat("a", 25) + "@", strings.Repeat("a", 42) + "@",
		"spiffe://prod.example.com/ci/github/example-org@example-app", "@",
	} {
		if kind, ok := subject.Classify(s); ok {
			t.Errorf("Classify(%q) = %q", s, kind)
		}
	}
}

// verifies: REQ-WORKLOAD-008
func TestCIJobIDMatchesTheVectors(t *testing.T) {
	for _, v := range loadSubVectors(t).CIJob {
		got, err := subject.CIJobID(v.TrustDomain, v.Repository)
		if r := reason(t, err); r != v.Refused {
			t.Errorf("%s: refused for %q, want %q", v.Name, r, v.Refused)
		}
		if got != v.Sub {
			t.Errorf("%s: %q, want %q", v.Name, got, v.Sub)
		}
		if kind, ok := subject.Classify(got); v.Sub != "" && (!ok || kind != subject.KindSPIFFE) {
			t.Errorf("%s: the ID %q has no form", v.Name, got)
		}
	}
}

// verifies: REQ-WORKLOAD-008
func TestServiceAccountIDMatchesTheVectors(t *testing.T) {
	for _, v := range loadSubVectors(t).ServiceAccount {
		got, err := subject.ServiceAccountID(v.TrustDomain, v.Cluster, v.Namespace, v.NameOf)
		if r := reason(t, err); r != v.Refused {
			t.Errorf("%s: refused for %q, want %q", v.Name, r, v.Refused)
		}
		if got != v.Sub {
			t.Errorf("%s: %q, want %q", v.Name, got, v.Sub)
		}
		if kind, ok := subject.Classify(got); v.Sub != "" && (!ok || kind != subject.KindSPIFFE) {
			t.Errorf("%s: the ID %q has no form", v.Name, got)
		}
	}
}

// v2 issues no v1 spelling. Each v1 string has no form, and where the vectors give a v2 for it, the inputs make that
// ID and nothing else.
//
// verifies: REQ-SUBJ-071, REQ-WORKLOAD-008
func TestNoV1SpellingIsIssued(t *testing.T) {
	for _, v := range loadSubVectors(t).V1 {
		if kind, ok := subject.Classify(v.V1); ok {
			t.Errorf("%s: the v1 spelling %q has the form %q", v.SubjectType, v.V1, kind)
		}
		if v.V2 == nil {
			if v.Decision == "" {
				t.Errorf("%s: no v2 and no decision", v.SubjectType)
			}
			continue
		}
		var got string
		var err error
		switch {
		case v.Repository != "":
			got, err = subject.CIJobID(v.TrustDomain, v.Repository)
		case v.Cluster != "":
			got, err = subject.ServiceAccountID(v.TrustDomain, v.Cluster, v.Namespace, v.NameOf)
		default:
			t.Fatalf("%s: a v2 that no inputs make", v.SubjectType)
		}
		if err != nil || got != *v.V2 {
			t.Errorf("%s: %q, %v; want %q", v.SubjectType, got, err, *v.V2)
		}
	}
}

// The limit of 2048 bytes, which a vector would have to be as long as.
//
// verifies: REQ-WORKLOAD-008
func TestAnIDOverTheLimitIsRefused(t *testing.T) {
	const td = "prod.example.com"
	// "spiffe://" + td + "/ci/github/" + org + "/" + repo is 2048 bytes when org and repo hold the rest.
	rest := 2048 - len("spiffe://"+td+"/ci/github//")
	repo := strings.Repeat("r", rest-1)
	if id, err := subject.CIJobID(td, "o/"+repo); err != nil || len(id) != 2048 {
		t.Fatalf("an ID of 2048 bytes: %d bytes, %v", len(id), err)
	}
	if _, err := subject.CIJobID(td, "o/"+repo+"r"); !errors.Is(err, subject.ErrLength) {
		t.Errorf("an ID of 2049 bytes: %v", err)
	}
	if _, err := subject.CIJobID(strings.Repeat("a", 256), "o/r"); !errors.Is(err, subject.ErrTrustDomain) {
		t.Errorf("a trust domain of 256 characters: %v", err)
	}
	if kind, ok := subject.Classify("spiffe://" + td + "/" + strings.Repeat("r", 2048)); ok {
		t.Errorf("a string of over 2048 bytes has the form %q", kind)
	}
}
