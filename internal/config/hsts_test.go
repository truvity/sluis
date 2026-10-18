package config_test

import (
	"testing"
	"time"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/httpsec"
)

func TestHSTSDefaultsAndOverrides(t *testing.T) {
	t.Parallel()

	age := func(d time.Duration) *config.Duration {
		v := config.Duration(d)
		return &v
	}
	off := false

	for name, c := range map[string]struct {
		section *config.HSTS
		want    httpsec.HSTS
		wantErr bool
	}{
		"absent":    {nil, httpsec.HSTS{MaxAge: 8760 * time.Hour, IncludeSubDomains: true, PublicURL: "https://x.example"}, false},
		"empty":     {&config.HSTS{}, httpsec.HSTS{MaxAge: 8760 * time.Hour, IncludeSubDomains: true, PublicURL: "https://x.example"}, false},
		"overrides": {&config.HSTS{MaxAge: age(time.Hour), IncludeSubDomains: &off}, httpsec.HSTS{MaxAge: time.Hour, PublicURL: "https://x.example"}, false},
		"off":       {&config.HSTS{MaxAge: age(0)}, httpsec.HSTS{IncludeSubDomains: true, PublicURL: "https://x.example"}, false},
		"negative":  {&config.HSTS{MaxAge: age(-time.Hour)}, httpsec.HSTS{}, true},
		"fraction":  {&config.HSTS{MaxAge: age(1500 * time.Millisecond)}, httpsec.HSTS{}, true},
	} {
		got, err := c.section.Policy("https://x.example")
		if (err != nil) != c.wantErr {
			t.Errorf("%s: error = %v, want error %v", name, err, c.wantErr)
			continue
		}

		if got != c.want {
			t.Errorf("%s: policy = %+v, want %+v", name, got, c.want)
		}
	}
}
