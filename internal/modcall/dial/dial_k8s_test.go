//go:build !lambda

package dial_test

import (
	"context"
	"strings"
	"testing"

	"github.com/truvity/sluis/internal/modcall"
	"github.com/truvity/sluis/internal/modcall/dial"
)

// The Kubernetes build has no Lambda client: a module named by function is
// refused at Dial, not at its first call.
func TestKubernetesBuildRefusesAFunction(t *testing.T) {
	cfg := modcall.Config{Modules: map[string]modcall.Target{"google": {Function: "sluis-acme-google"}}}
	_, err := dial.Dial(context.Background(), cfg, "issuer", nil)
	if err == nil || !strings.Contains(err.Error(), "no Lambda transport") {
		t.Fatalf("%v", err)
	}
}
