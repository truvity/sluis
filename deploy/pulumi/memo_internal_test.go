package sluispulumi

import (
	"regexp"
	"testing"
)

var tempPolicyDir = regexp.MustCompile(`sluis-[a-z]+-[0-9]+`)

func errText(err error) string {
	if err == nil {
		return "<nil>"
	}
	return tempPolicyDir.ReplaceAllString(err.Error(), "sluis-tmp")
}

// Remembering what loading a document decoded changes neither the result nor
// the error: the same text answers the same, cold and warm.
func TestRememberedPolicyAnswersAsTheUncachedOne(t *testing.T) {
	for name, policy := range map[string]string{
		"valid":       "apiVersion: sluis.truvity.github.io/policy/v2\n",
		"unknown key": "apiVersion: sluis.truvity.github.io/policy/v2\nnotAKey: true\n",
		"not YAML":    "a: [b\n",
	} {
		want, wantErr := renderPolicyDocument(policy)
		for i := 0; i < 3; i++ {
			got, err := renderPolicy(&LambdaArgs{Policy: policy})
			if got != want || (name == "valid") != (err == nil) || errText(err) != errText(wantErr) {
				t.Errorf("%s, call %d: got %q, %q; uncached %q, %q", name, i, got, errText(err), want, errText(wantErr))
			}
		}
	}
}
