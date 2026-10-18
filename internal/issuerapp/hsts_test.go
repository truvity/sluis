package issuerapp

import (
	"testing"
	"time"

	"github.com/truvity/sluis/internal/config"
)

// The issuer's Strict-Transport-Security is a year with subdomains unless the
// document says otherwise, and is published at the issuer's URL; a max-age
// that cannot be sent is refused at load.
func TestHSTSComesFromTheDocument(t *testing.T) {
	cfg, err := FromConfig(withPolicy(t, &config.Serve{IssuerURL: "https://access.example/"}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if cfg.hsts.MaxAge != 365*24*time.Hour || !cfg.hsts.IncludeSubDomains || cfg.hsts.PublicURL != "https://access.example" {
		t.Errorf("hsts = %+v", cfg.hsts)
	}

	cfg, err = FromConfig(withPolicy(t, &config.Serve{
		IssuerURL: "https://access.example",
		HSTS:      &config.HSTS{MaxAge: ptr(config.Duration(time.Hour)), IncludeSubDomains: ptr(false)},
	}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if cfg.hsts.MaxAge != time.Hour || cfg.hsts.IncludeSubDomains {
		t.Errorf("hsts = %+v", cfg.hsts)
	}

	if _, err = FromConfig(withPolicy(t, &config.Serve{
		IssuerURL: "https://access.example", HSTS: &config.HSTS{MaxAge: ptr(config.Duration(-time.Hour))},
	})); err == nil {
		t.Error("a negative hsts.maxAge was accepted")
	}
}
