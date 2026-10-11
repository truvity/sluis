package policy_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/truvity/sluis/policy"
)

const (
	nsDMS  = `"k8s.cluster.name"="prod","kubernetes.pod_namespace"="dms"`
	nsKYC  = `"k8s.cluster.name"="prod","kubernetes.pod_namespace"="kyc"`
	wholeC = `"k8s.cluster.name"="prod"`
)

func joinPolicy(as string) string {
	return `
version: 1
groups:
  prod:logs:dms: {}
  prod:logs:kyc: {}
  prod:logs:cluster: {}
  prod:logs:none: {}
claim_merge:
  vm_access.logs_extra_stream_filters:
    join: " or "
    prefix: "_stream:{"
    suffix: "}"
    as: ` + as + `
claims:
  prod:logs:dms:     { vm_access: { logs_extra_stream_filters: ['` + nsDMS + `'] } }
  prod:logs:kyc:     { vm_access: { logs_extra_stream_filters: ['` + nsKYC + `'] } }
  prod:logs:cluster: { vm_access: { logs_extra_stream_filters: ['` + wholeC + `'] } }
`
}

func joinSet(t *testing.T, as string) *policy.Set {
	t.Helper()
	p, err := policy.Parse([]byte(joinPolicy(as)))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	s, err := policy.NewSet(p)
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	return s
}

func filterOf(t *testing.T, claims map[string]any) (any, bool) {
	t.Helper()
	va, ok := claims["vm_access"].(map[string]any)
	if !ok {
		return nil, false
	}
	v, ok := va["logs_extra_stream_filters"]
	return v, ok
}

func TestClaimJoinOrsTwoNamespaceGrants(t *testing.T) {
	t.Parallel()
	got, ok := filterOf(t, joinSet(t, "list").ClaimsFor([]string{"prod:logs:kyc", "prod:logs:dms"}))
	want := []any{`_stream:{` + nsDMS + ` or ` + nsKYC + `}`}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestClaimJoinClusterAndNamespaceGrantGiveOneString(t *testing.T) {
	t.Parallel()
	got, _ := filterOf(t, joinSet(t, "string").ClaimsFor([]string{"prod:logs:dms", "prod:logs:cluster"}))
	want := `_stream:{` + wholeC + ` or ` + nsDMS + `}`
	if got != want {
		t.Fatalf("got %#v, want %q", got, want)
	}
}

func TestClaimJoinNoGrantLeavesTheKeyAbsent(t *testing.T) {
	t.Parallel()
	s := joinSet(t, "list")
	for _, held := range [][]string{nil, {"prod:logs:none"}} {
		claims := s.ClaimsFor(held)
		if _, present := claims["vm_access"]; present {
			t.Errorf("held %v: vm_access present: %#v", held, claims)
		}
	}
}

func TestClaimJoinIsDeterministicAndIgnoresDuplicates(t *testing.T) {
	t.Parallel()
	s := joinSet(t, "list")
	first, _ := filterOf(t, s.ClaimsFor([]string{"prod:logs:dms", "prod:logs:kyc", "prod:logs:cluster"}))
	for range 20 {
		again, _ := filterOf(t, s.ClaimsFor([]string{"prod:logs:cluster", "prod:logs:kyc", "prod:logs:dms", "prod:logs:dms"}))
		if !reflect.DeepEqual(first, again) {
			t.Fatalf("not deterministic: %#v vs %#v", first, again)
		}
	}
}

func TestClaimJoinAppliesToEvaluate(t *testing.T) {
	t.Parallel()
	doc := strings.Replace(joinPolicy("list"), "prod:logs:dms: {}", "prod:logs:dms: { members: [dms@a.example] }", 1)
	doc = strings.Replace(doc, "prod:logs:kyc: {}", "prod:logs:kyc: { members: [dms@a.example] }", 1)
	p, err := policy.Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	s, err := policy.NewSet(p)
	if err != nil {
		t.Fatal(err)
	}
	res := s.Evaluate(policy.Input{Email: "x@a.example", DirectoryGroups: []string{"dms@a.example"}, Authoritative: true})
	got, _ := filterOf(t, res.Claims)
	want := []any{`_stream:{` + nsDMS + ` or ` + nsKYC + `}`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestClaimMergeIsRefusedAtLoad(t *testing.T) {
	t.Parallel()
	base := `
version: 1
groups:
  g: {}
claim_merge:
  vm_access.f: { join: " or " }
`
	cases := map[string]string{
		"non-list":       base + "claims:\n  g: { vm_access: { f: x } }\n",
		"non-string":     base + "claims:\n  g: { vm_access: { f: [1] } }\n",
		"through scalar": base + "claims:\n  g: { vm_access: 3 }\n",
		"empty join":     strings.Replace(base, `join: " or "`, `join: ""`, 1),
		"bad as":         strings.Replace(base, `join: " or "`, `join: " or ", as: map`, 1),
		"bad path":       strings.Replace(base, "vm_access.f", "vm_access..f", 1),
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p, err := policy.Parse([]byte(doc))
			if err == nil {
				_, err = policy.NewSet(p)
			}
			if err == nil {
				t.Fatal("accepted")
			}
		})
	}
	// And a well-formed one loads.
	p, err := policy.Parse([]byte(base + "claims:\n  g: { vm_access: { f: [x] } }\n"))
	if err == nil {
		_, err = policy.NewSet(p)
	}
	if err != nil {
		t.Fatalf("well-formed refused: %v", err)
	}
}
