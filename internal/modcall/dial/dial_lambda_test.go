//go:build lambda

package dial_test

import (
	"context"
	"testing"

	"github.com/truvity/sluis/internal/modcall"
	"github.com/truvity/sluis/internal/modcall/dial"
)

func TestLambdaBuildDialsAFunction(t *testing.T) {
	t.Setenv("AWS_REGION", "eu-west-1")
	cfg := modcall.Config{Modules: map[string]modcall.Target{"google": {Function: "sluis-acme-google"}}}
	if _, err := dial.Dial(context.Background(), cfg, "issuer", nil); err != nil {
		t.Fatal(err)
	}
}
