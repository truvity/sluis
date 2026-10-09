package config_test

import (
	"testing"

	"github.com/truvity/sluis/internal/config"
)

func TestSignerRemoteNamesOneTransport(t *testing.T) {
	ok := func(r *config.SignerRemote) *config.Signer { return &config.Signer{Remote: r} }
	for name, s := range map[string]*config.Signer{
		"unset":    nil,
		"local":    {},
		"function": ok(&config.SignerRemote{Function: "fn"}),
		"url":      ok(&config.SignerRemote{URL: "https://signer.example"}),
	} {
		if err := s.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, s := range map[string]*config.Signer{
		"neither": ok(&config.SignerRemote{}),
		"both":    ok(&config.SignerRemote{Function: "fn", URL: "https://signer.example"}),
		"bad url": ok(&config.SignerRemote{URL: "signer"}),
	} {
		if err := s.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
