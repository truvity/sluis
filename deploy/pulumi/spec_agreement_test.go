package sluispulumi

import (
	"testing"

	"github.com/truvity/sluis/deploy/spec"
)

// TestSpecAgreesWithTheLibrary holds deploy/spec, the SDK-free copy of the
// names and checks an installation's tooling uses before anything exists, to
// the rules this library applies.
func TestSpecAgreesWithTheLibrary(t *testing.T) {
	for name, pair := range map[string][2]string{
		"instance":      {instancePattern.String(), spec.InstancePattern.String()},
		"function name": {functionNamePattern.String(), spec.FunctionNamePattern.String()},
		"key alias":     {aliasPattern.String(), spec.AliasPattern.String()},
	} {
		if pair[0] != pair[1] {
			t.Errorf("%s: the library's pattern is %s, deploy/spec's %s", name, pair[0], pair[1])
		}
	}

	for _, instance := range []string{"main", "private", "export", "Main", "a-"} {
		if got, want := spec.CheckInstance(instance) == nil, validInstance(instance); got != want {
			t.Errorf("instance %q: deploy/spec accepts %v, the library %v", instance, got, want)
		}
	}

	if got := spec.KubernetesRoleName("acme"); got != "acme-sluis" {
		t.Errorf("KubernetesRoleName = %q, the library names the pod's role <prefix>-sluis", got)
	}
}
