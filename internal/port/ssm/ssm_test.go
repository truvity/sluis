package ssm_test

import (
	"context"
	"testing"

	"github.com/truvity/sluis/internal/port"
	_ "github.com/truvity/sluis/internal/port/ssm"
)

func TestRegistered(t *testing.T) {
	d, ok := port.Default.Lookup(port.ConcernSecrets, "ssm")
	if !ok || d.Status != port.StatusImplemented || !d.SecretStore || !d.Requires.AWS || d.Factory == nil {
		t.Fatalf("descriptor: %+v", d)
	}
	if !d.Works(port.RuntimeKubernetes) || !d.Works(port.RuntimeLambda) || d.Works(port.RuntimeProcess) {
		t.Fatalf("runtimes: %v", d.Runtimes)
	}
	if _, err := d.Factory(context.Background(), port.Settings{"bogus": 1}); err == nil {
		t.Fatal("an unknown setting was accepted")
	}
	if _, err := d.Factory(context.Background(), port.Settings{"root": "/sluis/example"}); err != nil {
		t.Fatalf("a good root: %v", err)
	}
	for _, root := range []string{"", "sluis/x", "/sluis/x/", "/sluis/private", "/sluis/internal", "/sluis/x/external"} {
		if _, err := d.Factory(context.Background(), port.Settings{"root": root}); err == nil {
			t.Errorf("root %q was accepted", root)
		}
	}
}
