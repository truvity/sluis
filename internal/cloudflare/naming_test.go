package cloudflare_test

import (
	"testing"
	"time"

	"github.com/truvity/sluis/internal/cloudflare"
)

func TestSplitNameReadsBackWhatTheNamesSay(t *testing.T) {
	at := time.Date(2026, 10, 9, 8, 30, 0, 0, time.UTC)
	for _, c := range []struct{ name, caller string }{
		{cloudflare.StoredName("example", "dns", at), ""},
		{cloudflare.OnDemandName("example", "dns", "ada@example.com", at), "ada@example.com"},
		{cloudflare.OnDemandName("example", "dns", "github:org/repo:deploy job", at), "github:org_repo:deploy_job"},
	} {
		caller, got := cloudflare.SplitName(c.name, "example", "dns")
		if caller != c.caller || !got.Equal(at) {
			t.Errorf("%q: caller %q at %v, want %q at %v", c.name, caller, got, c.caller, at)
		}
	}
	if caller, got := cloudflare.SplitName("someone else's token", "example", "dns"); caller != "" || !got.IsZero() {
		t.Errorf("a name that is not ours: %q %v", caller, got)
	}
	if caller, _ := cloudflare.SplitName(cloudflare.StoredName("example", "dns-edge", at), "example", "dns"); caller != "" {
		t.Errorf("the prefix of dns must not read dns-edge's tokens: %q", caller)
	}
}
