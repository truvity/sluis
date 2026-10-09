package signerdial_test

import (
	"context"
	"testing"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/signer"
	"github.com/truvity/sluis/internal/signerdial"
)

func TestDialRefusesWhatItCannotReach(t *testing.T) {
	if _, err := signerdial.Dial(context.Background(), nil, nil); err == nil {
		t.Error("nil remote")
	}
	c, err := signerdial.Dial(context.Background(), &config.SignerRemote{URL: "http://signer.example:8080", TokenFile: "/nonexistent"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Sign(context.Background(), signer.Request{Purpose: signer.PurposeAccess, Payload: []byte(`{}`)}); err == nil {
		t.Error("a call with no token mounted succeeded")
	}
	if _, err = c.PublicKeys(context.Background()); err == nil {
		t.Error("a key set with no token mounted was read")
	}
}
